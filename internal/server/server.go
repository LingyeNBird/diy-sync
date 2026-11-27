// Package server is the Windows side: it owns the sync folders, their rules
// and conflicts, and serves the sync API.
package server

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fdsync/internal/fsx"
	"fdsync/internal/index"
	"fdsync/internal/protocol"
	"fdsync/internal/syncer"
	"fdsync/internal/watch"
)

// FolderConfig is one sync folder in the configuration file.
type FolderConfig struct {
	ID       string            `json:"id"`
	Path     string            `json:"path"` // the Windows folder
	Rules    []string          `json:"rules"`
	Excluded []ManualExclusion `json:"excluded,omitempty"` // excluded by the admin
	// ConflictPolicy: "" or "ask" (the admin decides), or "newest-wins".
	ConflictPolicy string `json:"conflictPolicy,omitempty"`
	Paused         bool   `json:"paused,omitempty"`
	Name           string `json:"name,omitempty"` // display name in the panel; "" = the ID
	// Hidden: paths the panel's file browser doesn't show (a view setting
	// only, sync ignores it), by side: "windows" or "wsl:<connection>".
	Hidden map[string][]string `json:"hidden,omitempty"`
}

// Config is the server configuration (config.json in the data directory).
type Config struct {
	Listen       string `json:"listen"`       // sync API address, e.g. "0.0.0.0:7443"
	PanelListen  string `json:"panelListen"`  // management panel, default "127.0.0.1:7444"
	PasswordHash string `json:"passwordHash"` // panel password (argon2id); empty = set on first visit
	Token        string `json:"token"`        // shared client token (before pairing); see AllowToken
	// AllowToken accepts clients that use the shared token instead of
	// pairing (default true, so existing clients keep working).
	AllowToken *bool `json:"allowToken,omitempty"`
	// PlainHTTP turns TLS off for the sync API (trusted networks, benchmarks).
	PlainHTTP bool           `json:"plainHttp,omitempty"`
	Folders   []FolderConfig `json:"folders"`
	// TrashMB caps the trash for all folders together (default and maximum
	// 50; 0 turns it off).
	TrashMB *int `json:"trashMB,omitempty"`
	// PauseAll pauses every folder, until PausedUntil (zero = until resumed).
	PauseAll    bool      `json:"pauseAll,omitempty"`
	PausedUntil time.Time `json:"pausedUntil,omitzero"`

	// Not stored in the file.
	// Autostart starts the server at Windows login (nil = not supported here).
	Autostart Autostart `json:"-"`
	// Restart stops the server so main starts the (upgraded) exe again;
	// nil = upgrading from the panel isn't possible.
	Restart      func()        `json:"-"`
	DataDir      string        `json:"-"`
	ScanDelay    time.Duration `json:"-"` // watcher batching; default 1 s
	RescanPeriod time.Duration `json:"-"` // full rescan; default 60 min
}

// Server is a running server.
type Server struct {
	cfg     Config
	device  string
	folders map[string]*folder
	order   []string
	log     *slog.Logger
	fmu     sync.RWMutex // guards folders, order, runCtx
	runCtx  context.Context
	cfgMu   sync.Mutex // guards cfg and saving config.json
	trash   *fsx.TrashStore
	ev      *activity
	reg     *registry
	broker  *broker
	panel   *panelAuth
	upd     updates
	pair    *pairing
	clip    *clipboardStore
	cert    tls.Certificate
	fp      string // certificate fingerprint, "" without TLS

	pmu        sync.Mutex // guards the global pause
	pauseAll   bool
	pauseUntil time.Time   // zero = until resumed
	pauseTimer *time.Timer // resumes at pauseUntil
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// New opens the folders' indexes. It doesn't scan or listen yet.
func New(cfg Config, log *slog.Logger) (*Server, error) {
	if cfg.ScanDelay == 0 {
		cfg.ScanDelay = time.Second
	}
	if cfg.RescanPeriod == 0 {
		cfg.RescanPeriod = time.Hour
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "folders"), 0o700); err != nil {
		return nil, err
	}
	device, err := DeviceID(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	trashMB := 50
	if cfg.TrashMB != nil {
		trashMB = *cfg.TrashMB
	}
	trash, err := fsx.OpenTrash(filepath.Join(cfg.DataDir, "trash"), int64(trashMB)<<20)
	if err != nil {
		return nil, err
	}
	ev := newActivity(cfg.DataDir)
	s := &Server{cfg: cfg, device: device, folders: map[string]*folder{}, log: log, trash: trash,
		ev: ev, reg: newRegistry(cfg.DataDir, ev), broker: newBroker()}
	s.panel = newPanelAuth(s)
	s.pair = newPairing()
	s.clip = newClipboardStore()
	if !cfg.PlainHTTP && cfg.DataDir != "" {
		if s.cert, s.fp, err = LoadOrCreateCert(cfg.DataDir); err != nil {
			return nil, fmt.Errorf("certificate: %w", err)
		}
	}
	for _, fc := range cfg.Folders {
		f, err := s.openFolder(fc)
		if err != nil {
			return nil, err
		}
		s.folders[fc.ID] = f
		s.order = append(s.order, fc.ID)
	}
	if cfg.PauseAll {
		s.armPause(cfg.PausedUntil)
	}
	return s, nil
}

// openFolder opens a sync folder's index and workspace.
func (s *Server) openFolder(fc FolderConfig) (*folder, error) {
	if !validID.MatchString(fc.ID) {
		return nil, fmt.Errorf("folder id %q: use letters, digits, '.', '_' or '-'", fc.ID)
	}
	if _, dup := s.folders[fc.ID]; dup {
		return nil, fmt.Errorf("folder id %q already exists", fc.ID)
	}
	root, err := filepath.Abs(fc.Path)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("folder %q: %s is not a folder", fc.ID, root)
	}
	// Everything fdsync keeps for this folder lives here, outside the folder itself.
	work := filepath.Join(s.cfg.DataDir, "folders", fc.ID)
	if err := os.MkdirAll(work, 0o700); err != nil {
		return nil, err
	}
	old := filepath.Join(s.cfg.DataDir, "folders", fc.ID+".db") // before 2026-11-19
	if _, err := os.Stat(old); err == nil {
		_ = os.Rename(old, filepath.Join(work, "index.db"))
	}
	ws, err := fsx.OpenWorkspace(work, root, s.trash.For(fc.ID))
	if err != nil {
		return nil, fmt.Errorf("folder %q: %w", fc.ID, err)
	}
	if !ws.SameDrive {
		s.log.Warn("data directory is on another drive than the folder: incoming files are received next to their destination, and the trash copies instead of moving",
			"folder", fc.ID, "data", s.cfg.DataDir)
	}
	store, err := index.Open(filepath.Join(work, "index.db"))
	if err != nil {
		return nil, err
	}
	f := &folder{
		id:      fc.ID,
		side:    syncer.Side{Root: root, Device: s.device, Stage: ws.Stage, Trash: ws.Trash},
		store:   store,
		log:     s.log,
		srv:     s,
		manual:  append([]ManualExclusion(nil), fc.Excluded...),
		policy:  fc.ConflictPolicy,
		paused:  fc.Paused,
		name:    fc.Name,
		changed: make(chan struct{}),
	}
	if err := f.setRules(fc.Rules); err != nil {
		store.Close()
		return nil, fmt.Errorf("folder %q: %w", fc.ID, err)
	}
	return f, nil
}

// folder returns a sync folder by ID, or nil.
func (s *Server) folder(id string) *folder {
	s.fmu.RLock()
	defer s.fmu.RUnlock()
	return s.folders[id]
}

// folderList returns the sync folders in configuration order.
func (s *Server) folderList() []*folder {
	s.fmu.RLock()
	defer s.fmu.RUnlock()
	out := make([]*folder, len(s.order))
	for i, id := range s.order {
		out[i] = s.folders[id]
	}
	return out
}

// AddFolder adds a sync folder while running: it is saved, scanned and watched.
func (s *Server) AddFolder(ctx context.Context, fc FolderConfig) error {
	s.fmu.Lock()
	f, err := s.openFolder(fc)
	if err != nil {
		s.fmu.Unlock()
		return err
	}
	s.folders[fc.ID] = f
	s.order = append(s.order, fc.ID)
	runCtx := s.runCtx
	s.fmu.Unlock()
	fc.Path = f.side.Root
	if err := s.updateConfig(func(c *Config) { c.Folders = append(c.Folders, fc) }); err != nil {
		s.log.Error("save config", "err", err)
	}
	if err := f.Scan(ctx); err != nil {
		return err
	}
	if runCtx != nil {
		go s.watchFolder(runCtx, f)
	}
	s.log.Info("folder added", "folder", fc.ID, "path", f.side.Root)
	return nil
}

// Close closes the indexes.
func (s *Server) Close() error {
	var errs []error
	for _, f := range s.folderList() {
		errs = append(errs, f.store.Close())
	}
	return errors.Join(errs...)
}

// TLSConfig is the sync API's TLS configuration (nil with plain HTTP).
func (s *Server) TLSConfig() *tls.Config {
	if s.fp == "" {
		return nil
	}
	return &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS13}
}

// Fingerprint returns the certificate fingerprint clients pin.
func (s *Server) Fingerprint() string { return s.fp }

// Device returns the server's device ID.
func (s *Server) Device() string { return s.device }

// ScanAll scans every folder once.
func (s *Server) ScanAll(ctx context.Context) error {
	for _, f := range s.folderList() {
		if err := f.Scan(ctx); err != nil {
			return fmt.Errorf("scan %s: %w", f.id, err)
		}
	}
	return nil
}

// Run scans, starts the watchers and serves the API until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	if err := s.ScanAll(ctx); err != nil {
		return err
	}
	s.Watch(ctx)
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	panelAddr := s.cfg.PanelListen
	if panelAddr == "" {
		panelAddr = "127.0.0.1:7444"
	}
	pln, err := net.Listen("tcp", panelAddr)
	if err != nil {
		ln.Close()
		return fmt.Errorf("panel: %w", err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	tlsOn := false
	if c := s.TLSConfig(); c != nil {
		// HTTP/2 over TLS: many transfers share one connection.
		c.NextProtos = []string{"h2", "http/1.1"}
		srv.TLSConfig = c
		tlsOn = true
	}
	psrv := &http.Server{Handler: s.PanelHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = psrv.Shutdown(sctx)
	}()
	s.log.Info("sync API listening", "addr", ln.Addr().String(), "tls", s.fp != "", "fingerprint", s.fp, "folders", len(s.folderList()))
	s.log.Info("panel listening", "url", "http://"+pln.Addr().String()+"/")
	errc := make(chan error, 2)
	go func() { errc <- psrv.Serve(pln) }()
	go func() {
		if tlsOn {
			errc <- srv.ServeTLS(ln, "", "")
		} else {
			errc <- srv.Serve(ln)
		}
	}()
	for range 2 {
		if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

// Watch starts the per-folder watchers and periodic rescans (Run does this).
func (s *Server) Watch(ctx context.Context) {
	s.fmu.Lock()
	s.runCtx = ctx
	s.fmu.Unlock()
	for _, f := range s.folderList() {
		go s.watchFolder(ctx, f)
	}
	startClipboardWatch(ctx, s.clip, s.log)
	go func() { // the size cap holds on every write; this drops week-old entries
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.trash.Prune(); err != nil {
					s.log.Warn("trash prune", "err", err)
				}
				s.purgeTombstones()
			}
		}
	}()
	go func() { // offline detection, devices.json
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				s.reg.tick()
				return
			case <-t.C:
				s.reg.tick()
			}
		}
	}()
}

func (s *Server) watchFolder(ctx context.Context, f *folder) {
	events, err := watch.Watch(ctx, f.side.Root, s.cfg.ScanDelay, s.log)
	if err != nil {
		s.log.Warn("watcher unavailable; relying on periodic rescans", "folder", f.id, "err", err)
	}
	rescan := time.NewTicker(s.cfg.RescanPeriod)
	defer rescan.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
		case <-rescan.C:
		}
		if err := f.Scan(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("scan", "folder", f.id, "err", err)
		}
	}
}

// ---------------------------------------------------------------- HTTP

// Handler returns the API handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	p := protocol.APIPrefix
	mux.HandleFunc("GET "+p+"/folders", s.handleFolders)
	mux.HandleFunc("GET "+p+"/folders/{id}", s.withFolder(s.handleFolder))
	mux.HandleFunc("GET "+p+"/folders/{id}/index", s.withFolder(s.handleIndex))
	mux.HandleFunc("GET "+p+"/folders/{id}/wait", s.withFolder(s.handleWait))
	mux.HandleFunc("POST "+p+"/folders/{id}/push", s.withFolder(s.handlePush))
	mux.HandleFunc("PUT "+p+"/folders/{id}/content", s.withFolder(s.handleUpload))
	mux.HandleFunc("GET "+p+"/folders/{id}/content", s.withFolder(s.handleDownload))
	mux.HandleFunc("GET "+p+"/folders/{id}/conflicts", s.withFolder(s.handleConflicts))
	mux.HandleFunc("POST "+p+"/folders/{id}/conflicts/resolve", s.withFolder(s.handleResolve))
	mux.HandleFunc("PUT "+p+"/folders/{id}/rules", s.withFolder(s.handleRules))
	mux.HandleFunc("POST "+p+"/folders/{id}/tree", s.withFolder(s.handleTree))
	mux.HandleFunc("POST "+p+"/folders/{id}/bundle/get", s.withFolder(s.handleBundleGet))
	mux.HandleFunc("POST "+p+"/folders/{id}/bundle/put", s.withFolder(s.handleBundlePut))
	mux.HandleFunc("POST "+p+"/hello", s.handleHello)
	mux.HandleFunc("GET "+p+"/requests", s.handleRequests)
	mux.HandleFunc("PUT "+p+"/requests/{rid}", s.handleAnswer)
	mux.HandleFunc("GET "+p+"/clipboard", s.handleClipboardList)
	mux.HandleFunc("GET "+p+"/clipboard/{id}", s.handleClipboardImage)
	authed := s.auth(mux)
	// Pairing and token refresh are how a client gets credentials: no auth.
	open := http.NewServeMux()
	open.HandleFunc("POST "+p+"/pair/start", s.handlePairStart)
	open.HandleFunc("POST "+p+"/pair/token", s.handlePairToken)
	open.HandleFunc("POST "+p+"/token", s.handleRefresh)
	open.Handle("/", authed)
	return open
}

var validDevice = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// allowToken reports whether clients may use the shared token.
func (s *Server) allowToken() bool {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.cfg.AllowToken == nil || *s.cfg.AllowToken
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.cfgMu.Lock()
		shared := s.cfg.Token // can be regenerated in the panel
		s.cfgMu.Unlock()
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case bearer == "":
			writeErr(w, http.StatusUnauthorized, errors.New("missing token"))
			return
		case shared != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(shared)) == 1:
			if !s.allowToken() {
				writeErr(w, http.StatusUnauthorized, errors.New("the shared token is turned off on this server; pair with fdsync connect"))
				return
			}
			// A paired or revoked device can't come back with the shared token.
			if d := r.Header.Get(protocol.HeaderDevice); d != "" && s.reg.sharedBlocked(d) {
				writeErr(w, http.StatusUnauthorized, errors.New("this device is paired or revoked; pair with fdsync connect"))
				return
			}
		default:
			dev, ok := s.pair.deviceFor(bearer)
			if !ok {
				writeErr(w, http.StatusUnauthorized, errors.New("invalid or expired token"))
				return
			}
			r.Header.Set(protocol.HeaderDevice, dev) // the token decides who this is
		}
		if d := r.Header.Get(protocol.HeaderDevice); d != "" {
			if !validDevice.MatchString(d) || d == s.device {
				writeErr(w, http.StatusBadRequest, errors.New("invalid device id"))
				return
			}
			s.reg.touch(d, r.RemoteAddr)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withFolder(h func(http.ResponseWriter, *http.Request, *folder)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f := s.folder(r.PathValue("id"))
		if f == nil {
			writeErr(w, http.StatusNotFound, errors.New("no such folder"))
			return
		}
		if (f.isPaused() || s.allPaused()) && !strings.HasSuffix(r.URL.Path, "/wait") && r.URL.Path != protocol.APIPrefix+"/folders/"+f.id {
			writeErr(w, http.StatusServiceUnavailable, errors.New("this folder is paused on the server"))
			return
		}
		h(w, r, f)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(protocol.Error{Error: err.Error()})
}

func device(w http.ResponseWriter, r *http.Request) (string, bool) {
	d := r.Header.Get(protocol.HeaderDevice)
	if d == "" {
		writeErr(w, http.StatusBadRequest, errors.New("missing "+protocol.HeaderDevice))
		return "", false
	}
	return d, true
}

func (s *Server) handleFolders(w http.ResponseWriter, _ *http.Request) {
	out := []protocol.Folder{}
	for _, f := range s.folderList() {
		out = append(out, f.info())
	}
	writeJSON(w, out)
}

func (s *Server) handleFolder(w http.ResponseWriter, _ *http.Request, f *folder) {
	writeJSON(w, f.info())
}

func uintParam(r *http.Request, name string) uint64 {
	n, _ := strconv.ParseUint(r.URL.Query().Get(name), 10, 64)
	return n
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request, f *folder) {
	pg, err := f.page(uintParam(r, "since"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, pg)
}

func (s *Server) handleWait(w http.ResponseWriter, r *http.Request, f *folder) {
	timeout := 30 * time.Second
	if t := uintParam(r, "timeout"); t > 0 && t <= 120 {
		timeout = time.Duration(t) * time.Second
	}
	writeJSON(w, f.wait(r.Context(), uintParam(r, "since"), r.URL.Query().Get("rules"), uintParam(r, "nudge"), timeout))
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request, f *folder) {
	dev, ok := device(w, r)
	if !ok {
		return
	}
	var req protocol.PushRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := f.push(dev, req.Entries)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	res.WantTree = req.TreeHash != "" && req.TreeHash != f.treeHash(dev)
	s.reg.synced(dev, f.id, req.Pulled)
	writeJSON(w, res)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, f *folder) {
	dev, ok := device(w, r)
	if !ok {
		return
	}
	path, hash := r.URL.Query().Get("path"), r.URL.Query().Get("hash")
	if err := fsx.CheckRel(path); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Only receive what was asked for; check before reading the body.
	if !f.isNeeded(dev, path, hash) {
		writeErr(w, http.StatusConflict, errStale)
		return
	}
	body := &countingReader{r: r.Body}
	var tmp string
	var err error
	if list, delta := r.Header[protocol.HeaderBlocks]; delta {
		tmp, err = s.receiveDelta(f, dev, path, hash, strings.Join(list, ","), body)
	} else {
		tmp, err = fsx.Receive(f.side.Stage, f.side.Root, path, body, hash)
	}
	s.reg.add(dev, body.n, 0)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errStale) || errors.Is(err, fsx.ErrChanged) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	if err := f.upload(dev, path, hash, tmp); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errStale) || errors.Is(err, fsx.ErrChanged) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request, f *folder) {
	path, hash := r.URL.Query().Get("path"), r.URL.Query().Get("hash")
	if err := fsx.CheckRel(path); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	e, err := f.entryForDownload(path, hash)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errStale) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	file, err := os.Open(fsx.Abs(f.side.Root, path))
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set(protocol.HeaderHash, e.Hash)
	// Range requests fetch only the blocks a client lacks. The client checks
	// the whole hash; a file changed mid-transfer is refused there.
	cw := &countingWriter{ResponseWriter: w}
	http.ServeContent(cw, r, "", time.Time{}, file)
	if dev := r.Header.Get(protocol.HeaderDevice); dev != "" {
		s.reg.add(dev, 0, cw.n)
	}
}

type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

// receiveDelta builds the new version from the old file here and the
// blocks in the body (the ones listed in X-Fdsync-Blocks, in order).
func (s *Server) receiveDelta(f *folder, dev, path, hash, list string, body io.Reader) (string, error) {
	r, l, old, err := f.deltaBase(dev, path, hash)
	if err != nil {
		return "", err
	}
	defer old.Close()
	sent, err := fsx.DecodeIndexes(list, len(r.Blocks))
	if err != nil {
		return "", err
	}
	have := fsx.Plan(l.Blocks, l.BlockSize)
	local := func(i int, bh string) (int64, bool) {
		if sent[i] {
			return 0, false
		}
		off, ok := have[bh]
		return off, ok
	}
	fetch := func(i int, n int64) (io.Reader, error) {
		if !sent[i] {
			return nil, fmt.Errorf("block %d is neither here nor in the upload", i)
		}
		return io.LimitReader(body, n), nil
	}
	tmp, st, err := fsx.Assemble(f.side.Stage, f.side.Root, path, r.Size, r.BlockSize, r.Blocks, hash, old, local, fetch)
	if err == nil {
		s.log.Debug("delta upload", "folder", f.id, "path", path, "reused", st.Reused, "received", st.Fetched)
	}
	return tmp, err
}

func (s *Server) handleConflicts(w http.ResponseWriter, _ *http.Request, f *folder) {
	cs, err := f.conflicts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, cs)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request, f *folder) {
	var req protocol.ResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := f.resolve(req.Path, req.Use); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request, f *folder) {
	var req protocol.RulesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := f.updateRules(r.Context(), req.Rules); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.saveRules(f.id, req.Rules); err != nil {
		s.log.Error("save config", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// saveRules writes changed rules back to config.json, if there is one.
func (s *Server) saveRules(id string, rules []string) error {
	return s.updateConfig(func(c *Config) {
		for i := range c.Folders {
			if c.Folders[i].ID == id {
				c.Folders[i].Rules = rules
			}
		}
	})
}

// updateConfig changes the configuration and saves config.json, if there is one.
func (s *Server) updateConfig(fn func(*Config)) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	fn(&s.cfg)
	if s.cfg.DataDir == "" {
		return nil
	}
	p := filepath.Join(s.cfg.DataDir, "config.json")
	if _, err := os.Stat(p); err != nil {
		return nil
	}
	return SaveConfig(p, s.cfg)
}

func (s *Server) handleHello(w http.ResponseWriter, r *http.Request) {
	dev, ok := device(w, r)
	if !ok {
		return
	}
	var h protocol.Hello
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&h); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.reg.hello(dev, r.RemoteAddr, h)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request, f *folder) {
	dev, ok := device(w, r)
	if !ok {
		return
	}
	var body io.Reader = io.LimitReader(r.Body, 256<<20)
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		defer zr.Close()
		body = zr
	}
	var rep protocol.TreeReport
	if err := json.NewDecoder(body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	f.setTreeReport(dev, s.reg.name(dev), &rep)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	dev, ok := device(w, r)
	if !ok {
		return
	}
	wait := 30 * time.Second
	if t := uintParam(r, "wait"); t > 0 && t <= 60 {
		wait = time.Duration(t) * time.Second
	}
	reqs := s.broker.take(r.Context(), dev, wait)
	if reqs == nil {
		reqs = []protocol.Request{}
	}
	writeJSON(w, reqs)
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	dev, ok := device(w, r)
	if !ok {
		return
	}
	body := &countingReader{r: io.LimitReader(r.Body, maxAnswer+1)}
	b, err := io.ReadAll(body)
	s.reg.add(dev, body.n, 0)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(b) > maxAnswer {
		writeErr(w, http.StatusRequestEntityTooLarge, errors.New("answer too large"))
		return
	}
	size, _ := strconv.ParseInt(r.Header.Get(protocol.HeaderSize), 10, 64)
	if !s.broker.deliver(dev, r.PathValue("rid"), answer{body: b, size: size, err: r.Header.Get(protocol.HeaderError)}) {
		writeErr(w, http.StatusGone, errors.New("nobody is waiting for this answer any more"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TombstoneAge is how long deletes are remembered (docs/requirements.md §5.3).
const TombstoneAge = 30 * 24 * time.Hour

// purgeTombstones forgets deletes older than TombstoneAge that every bound
// client has received. A client that stays away holds them back.
func (s *Server) purgeTombstones() {
	for _, f := range s.folderList() {
		upTo, ok := s.reg.acknowledged(f.id)
		if !ok {
			continue // nobody bound: keep them (a client may still come)
		}
		f.mu.Lock()
		var n int
		err := f.store.Update(func(tx *index.Tx) (err error) {
			n, err = tx.PurgeTombstones(upTo, time.Now().Add(-TombstoneAge))
			return
		})
		f.mu.Unlock()
		if err != nil {
			s.log.Warn("purge tombstones", "folder", f.id, "err", err)
		} else if n > 0 {
			s.log.Info("purged old tombstones", "folder", f.id, "count", n)
		}
	}
}

// handleBundleGet streams many small files in one response.
func (s *Server) handleBundleGet(w http.ResponseWriter, r *http.Request, f *folder) {
	var req protocol.BundleRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Items) > protocol.BundleFilesMax {
		writeErr(w, http.StatusBadRequest, errors.New("too many files in one bundle"))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	cw := &countingWriter{ResponseWriter: w}
	bw := bufio.NewWriterSize(cw, 256<<10)
	for _, it := range req.Items {
		out := protocol.BundleItem{Path: it.Path, Hash: it.Hash}
		var file *os.File
		var size int64
		err := fsx.CheckRel(it.Path)
		if err == nil {
			var e *index.Entry
			if e, err = f.entryForDownload(it.Path, it.Hash); err == nil {
				if file, err = openRegular(fsx.Abs(f.side.Root, it.Path)); err == nil {
					if fi, serr := file.Stat(); serr != nil || fi.Size() != e.Size {
						file.Close()
						file, err = nil, errStale
					} else {
						size = fi.Size()
					}
				}
			}
		}
		if err != nil {
			out.Error = err.Error()
			if protocol.WriteItem(bw, out) != nil {
				return
			}
			continue
		}
		out.Size = size
		werr := protocol.WriteItem(bw, out)
		if werr == nil {
			_, werr = io.CopyN(bw, file, size) // a file that shrinks now breaks the stream; the client retries
		}
		file.Close()
		if werr != nil {
			return
		}
	}
	_ = bw.Flush()
	if dev := r.Header.Get(protocol.HeaderDevice); dev != "" {
		s.reg.add(dev, 0, cw.n)
	}
}

// handleBundlePut receives many small files the server asked for and
// commits them in one index transaction.
func (s *Server) handleBundlePut(w http.ResponseWriter, r *http.Request, f *folder) {
	dev, ok := device(w, r)
	if !ok {
		return
	}
	body := &countingReader{r: io.LimitReader(r.Body, protocol.BundleBytesMax+protocol.BundleFilesMax*1024)}
	br := bufio.NewReaderSize(body, 64<<10)
	var items []Received
	var results []protocol.BundleResult
	defer func() {
		for _, it := range items {
			fsx.Discard(it.Tmp) // left over if we bail out early; Commit removes the others
		}
	}()
	for len(results) < protocol.BundleFilesMax {
		it, err := protocol.ReadItem(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		res := protocol.BundleResult{Path: it.Path}
		if err := fsx.CheckRel(it.Path); err != nil || !f.isNeeded(dev, it.Path, it.Hash) {
			if _, err := io.CopyN(io.Discard, br, it.Size); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			res.Error = errStale.Error()
			results = append(results, res)
			continue
		}
		tmp, err := fsx.ReceiveUnsynced(f.side.Stage, f.side.Root, it.Path, io.LimitReader(br, it.Size), it.Hash)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		items = append(items, Received{Path: it.Path, Hash: it.Hash, Tmp: tmp})
		results = append(results, res)
	}
	s.reg.add(dev, body.n, 0)
	// Everything on disk before anything is renamed into place.
	tmps := make([]string, len(items))
	for i, it := range items {
		tmps[i] = it.Tmp
	}
	if err := fsx.SyncAll(tmps); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	errs := f.commitUploads(dev, items)
	items = nil // committed or discarded by commitUploads
	n := 0
	k := 0
	last := ""
	for i := range results {
		if results[i].Error != "" {
			continue
		}
		if errs[k] != nil {
			results[i].Error = errs[k].Error()
		} else {
			n++
			last = results[i].Path
		}
		k++
	}
	if n > 0 {
		f.log.Info("received", "folder", f.id, "files", n, "from", dev)
		if n == 1 {
			f.event(Event{Kind: "sync", ConnectionID: dev, Path: last, Code: "received"})
		} else {
			f.event(Event{Kind: "sync", ConnectionID: dev, Code: "receivedMany", Params: map[string]any{"count": n}})
		}
	}
	writeJSON(w, results)
}
