package server

import (
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fdsync/internal/fsx"
	"fdsync/internal/index"
	"fdsync/internal/panel"
	"fdsync/internal/protocol"
	"fdsync/internal/scan"
)

// ---------------------------------------------------------------- auth

const (
	sessionCookie = "fdsync_session"
	sessionTTL    = 12 * time.Hour
	csrfHeader    = "X-CSRF-Token"
)

type session struct {
	csrf    string
	expires time.Time
}

type panelAuth struct {
	s        *Server
	mu       sync.Mutex
	sessions map[string]*session
}

func newPanelAuth(s *Server) *panelAuth {
	return &panelAuth{s: s, sessions: map[string]*session{}}
}

func (a *panelAuth) passwordHash() string {
	a.s.cfgMu.Lock()
	defer a.s.cfgMu.Unlock()
	return a.s.cfg.PasswordHash
}

func (a *panelAuth) current(r *http.Request) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ss := a.sessions[c.Value]
	if ss == nil || time.Now().After(ss.expires) {
		delete(a.sessions, c.Value)
		return nil
	}
	ss.expires = time.Now().Add(sessionTTL) // sliding
	return ss
}

func (a *panelAuth) start(w http.ResponseWriter) *session {
	id, csrf := newID()+newID(), newID()
	ss := &session{csrf: csrf, expires: time.Now().Add(sessionTTL)}
	a.mu.Lock()
	for k, v := range a.sessions { // drop expired ones
		if time.Now().After(v.expires) {
			delete(a.sessions, k)
		}
	}
	a.sessions[id] = ss
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return ss
}

func isLoopback(remote string) bool {
	ip := net.ParseIP(hostOf(remote))
	return ip != nil && ip.IsLoopback()
}

// allowedHost blocks DNS rebinding: a web page can't reach the panel
// through a hostname of its own that resolves to 127.0.0.1.
func allowedHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || net.ParseIP(h) != nil
}

// ---------------------------------------------------------------- handler

// PanelHandler serves the management panel and its API.
func (s *Server) PanelHandler() http.Handler {
	mux := http.NewServeMux()
	a := s.panel
	open := func(h http.HandlerFunc) http.HandlerFunc { return h }
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ss := a.current(r)
			if ss == nil {
				writeErr(w, http.StatusUnauthorized, errors.New("sign in first"))
				return
			}
			if r.Method != http.MethodGet && subtle.ConstantTimeCompare([]byte(r.Header.Get(csrfHeader)), []byte(ss.csrf)) != 1 {
				writeErr(w, http.StatusForbidden, errors.New("missing or wrong CSRF token"))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/session", open(s.apiSession))
	mux.HandleFunc("POST /api/setup", open(s.apiSetup))
	mux.HandleFunc("POST /api/login", open(s.apiLogin))
	mux.HandleFunc("POST /api/logout", authed(s.apiLogout))
	mux.HandleFunc("POST /api/password", authed(s.apiPassword))
	mux.HandleFunc("GET /api/overview", authed(s.apiOverview))
	mux.HandleFunc("GET /api/activity", authed(s.apiActivity))
	mux.HandleFunc("GET /api/tree", authed(s.apiTree))
	mux.HandleFunc("GET /api/content", authed(s.apiContent))
	mux.HandleFunc("PUT /api/folders/{id}/rules", authed(s.apiRules))
	mux.HandleFunc("POST /api/folders/{id}/exclusions", authed(s.apiExclusions))
	mux.HandleFunc("PUT /api/folders/{id}/options", authed(s.apiFolderOptions))
	mux.HandleFunc("POST /api/conflicts/resolve", authed(s.apiResolve))
	mux.HandleFunc("POST /api/delete", authed(s.apiDelete))
	mux.HandleFunc("GET /api/connections/{id}/dirs", authed(s.apiDirs))
	mux.HandleFunc("POST /api/pair/approve", authed(s.apiPairApprove))
	mux.HandleFunc("POST /api/pair/deny", authed(s.apiPairDeny))
	mux.HandleFunc("POST /api/connections/{id}/revoke", authed(s.apiRevoke))
	mux.HandleFunc("PUT /api/connections/{id}/name", authed(s.apiRenameConnection))
	mux.HandleFunc("POST /api/folders/{id}/rescan", authed(s.apiRescan))
	mux.HandleFunc("POST /api/reveal", authed(s.apiReveal))
	mux.HandleFunc("POST /api/vscode", authed(s.apiOpenVSCode))
	mux.HandleFunc("GET /api/trash", authed(s.apiTrash))
	mux.HandleFunc("PUT /api/pause", authed(s.apiPause))
	mux.HandleFunc("POST /api/trash/restore", authed(s.apiTrashRestore))
	mux.HandleFunc("POST /api/debug/stacks", authed(s.apiStacks))
	mux.HandleFunc("GET /api/settings", authed(s.apiSettings))
	mux.HandleFunc("PUT /api/settings", authed(s.apiSaveSettings))
	mux.HandleFunc("POST /api/token", authed(s.apiNewToken))
	mux.HandleFunc("GET /api/windirs", authed(s.apiWinDirs))
	mux.HandleFunc("POST /api/folders", authed(s.apiAddFolder))
	mux.HandleFunc("POST /api/connections/{id}/bindings", authed(s.apiBind))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, errors.New("no such API"))
	})
	mux.Handle("/", staticHandler())

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host) {
			http.Error(w, "use the panel through its IP address or localhost", http.StatusMisdirectedRequest)
			return
		}
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; worker-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		gzipped(mux).ServeHTTP(w, r)
	})
}

func staticHandler() http.Handler {
	files := panel.Files()
	if !panel.Built() {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>fdsync</title><p style="font:16px system-ui;margin:3em">This build has no panel. Run <code>pnpm build</code> in <code>web/</code>, then rebuild the server.</p>`)
		})
	}
	fsrv := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(files, p); err != nil {
			// Client-side routes: serve the app.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			w.Header().Set("Cache-Control", "no-cache")
			fsrv.ServeHTTP(w, r2)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fsrv.ServeHTTP(w, r)
	})
}

type gzipWriter struct {
	http.ResponseWriter
	zw *gzip.Writer
}

func (g *gzipWriter) Write(b []byte) (int, error) { return g.zw.Write(b) }

// gzipped compresses API JSON (trees can be large).
func gzipped(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/content" || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		zw := gzip.NewWriter(w)
		defer zw.Close()
		h.ServeHTTP(&gzipWriter{ResponseWriter: w, zw: zw}, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

// ---------------------------------------------------------------- session API

func (s *Server) apiSession(w http.ResponseWriter, r *http.Request) {
	ss := s.panel.current(r)
	res := map[string]any{"authenticated": ss != nil, "setup": s.panel.passwordHash() == "", "setupAllowed": isLoopback(r.RemoteAddr)}
	if ss != nil {
		res["csrf"] = ss.csrf
	}
	writeJSON(w, res)
}

type passwordReq struct {
	Password string `json:"password"`
	Current  string `json:"current,omitempty"`
}

func (s *Server) apiSetup(w http.ResponseWriter, r *http.Request) {
	var req passwordReq
	if !decode(w, r, &req) {
		return
	}
	if !isLoopback(r.RemoteAddr) {
		writeErr(w, http.StatusForbidden, errors.New("set the first password on the Windows machine itself (http://127.0.0.1)"))
		return
	}
	if s.panel.passwordHash() != "" {
		writeErr(w, http.StatusConflict, errors.New("a password is already set"))
		return
	}
	if err := s.setPassword(req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ss := s.panel.start(w)
	s.ev.add(Event{Kind: "pair", Admin: true, Code: "passwordSet"})
	writeJSON(w, map[string]any{"csrf": ss.csrf})
}

func (s *Server) setPassword(pw string) error {
	hash, err := HashPassword(pw)
	if err != nil {
		return err
	}
	return s.updateConfig(func(c *Config) { c.PasswordHash = hash })
}

func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	var req passwordReq
	if !decode(w, r, &req) {
		return
	}
	hash := s.panel.passwordHash()
	if hash == "" || !CheckPassword(hash, req.Password) {
		time.Sleep(700 * time.Millisecond) // slows down guessing
		writeErr(w, http.StatusUnauthorized, errors.New("wrong password"))
		return
	}
	ss := s.panel.start(w)
	writeJSON(w, map[string]any{"csrf": ss.csrf})
}

func (s *Server) apiLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.panel.mu.Lock()
		delete(s.panel.sessions, c.Value)
		s.panel.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiPassword(w http.ResponseWriter, r *http.Request) {
	var req passwordReq
	if !decode(w, r, &req) {
		return
	}
	if !CheckPassword(s.panel.passwordHash(), req.Current) {
		time.Sleep(700 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, errors.New("the current password is wrong"))
		return
	}
	if err := s.setPassword(req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.ev.add(Event{Kind: "pair", Admin: true, Code: "passwordChanged"})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- data API

type panelVersion struct {
	Size    int64     `json:"size"`
	MTime   time.Time `json:"mtime"`
	Hash    string    `json:"hash"`
	Deleted bool      `json:"deleted,omitempty"`
}

func versionOf(e *index.Entry) panelVersion {
	v := panelVersion{Size: e.Size, Hash: e.Hash, Deleted: e.Deleted}
	if e.ModTime != 0 {
		v.MTime = time.Unix(0, e.ModTime)
	}
	if e.Hash != "" {
		v.Hash = "b3:" + e.Hash[:min(8, len(e.Hash))]
	}
	return v
}

func (s *Server) apiOverview(w http.ResponseWriter, _ *http.Request) {
	devs := s.reg.list()
	online := func(d Device) bool { return time.Since(d.LastSeen) < onlineWindow }
	byID := map[string]Device{}
	type conn struct {
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		Distro      string    `json:"distro"`
		Address     string    `json:"address"`
		Online      bool      `json:"online"`
		LastSeen    time.Time `json:"lastSeen"`
		Version     string    `json:"version"`
		NetworkMode string    `json:"networkMode"`
		PairedAt    time.Time `json:"pairedAt"`
		UpBps       int64     `json:"upBps"`
		DownBps     int64     `json:"downBps"`
		Paired      bool      `json:"paired"` // false = uses the shared token
		Hostname    string    `json:"hostname"`
		Warnings    []string  `json:"warnings"`
	}
	conns := []conn{}
	live := devs[:0]
	for _, d := range devs {
		if !d.Revoked {
			live = append(live, d)
		}
	}
	devs = live
	for _, d := range devs {
		byID[d.ID] = d
		up, down := s.reg.bps(d.ID)
		mode := map[string]string{"mirrored": "mirrored", "bridged": "bridged"}[d.NetworkMode]
		if mode == "" {
			mode = "NAT"
		}
		pairedAt := d.FirstSeen
		if !d.PairedAt.IsZero() {
			pairedAt = d.PairedAt
		}
		warn := d.Warnings
		if warn == nil {
			warn = []string{}
		}
		conns = append(conns, conn{d.ID, d.Name, d.Distro, d.Address, online(d), d.LastSeen, d.Version, mode, pairedAt, up, down, d.RefreshHash != "", d.Hostname, warn})
	}

	type conflictOut struct {
		ID           string       `json:"id"`
		FolderID     string       `json:"folderId"`
		ConnectionID string       `json:"connectionId"`
		Path         string       `json:"path"`
		Kind         string       `json:"kind"`
		Win          panelVersion `json:"win"`
		WSL          panelVersion `json:"wsl"`
		DetectedAt   time.Time    `json:"detectedAt"`
	}
	type folderOut struct {
		ID             string           `json:"id"`
		Name           string           `json:"name"`
		WinPath        string           `json:"winPath"`
		Rules          []string         `json:"rules"`
		AutoExcluded   []panelExclusion `json:"autoExcluded"`
		FileCount      int              `json:"fileCount"`
		TotalBytes     int64            `json:"totalBytes"`
		State          string           `json:"state"`
		ConflictPolicy string           `json:"conflictPolicy"`
		Paused         bool             `json:"paused"` // this folder itself (state is also "paused" under Pause all)
	}
	type bindingOut struct {
		ID           string    `json:"id"`
		FolderID     string    `json:"folderId"`
		ConnectionID string    `json:"connectionId"`
		LocalPath    string    `json:"localPath"`
		State        string    `json:"state"`
		WinCount     int       `json:"winCount"`
		WSLCount     int       `json:"wslCount"`
		InScopeCount int       `json:"inScopeCount"`
		Conflicts    int       `json:"conflicts"`
		LastSync     time.Time `json:"lastSync"`
	}
	folders := []folderOut{}
	bindings := []bindingOut{}
	conflicts := []conflictOut{}
	for _, f := range s.folderList() {
		id := f.id
		files, bytes, pend := f.stats()
		cs, _ := f.conflicts()
		perDev := map[string]int{}
		for _, c := range cs {
			perDev[c.Device]++
			cur := c.Server
			_ = f.store.View(func(tx *index.Tx) error {
				if e, _ := tx.Get(c.Path); e != nil {
					cur = *e
				}
				return nil
			})
			kind := c.Kind
			if kind == "concurrent-edit" && cur.Version[c.Device] == 0 && c.Client.Version[s.device] == 0 {
				kind = "scope-add" // neither side had seen the other's version
			}
			conflicts = append(conflicts, conflictOut{id + ":" + c.Path, id, c.Device, c.Path, kind, versionOf(&cur), versionOf(&c.Client), c.Detected})
		}
		state := "idle"
		switch {
		case len(cs) > 0:
			state = "conflict"
		case len(pend) > 0:
			state = "syncing"
		}
		f.mu.Lock()
		rulesSrc := append([]string{}, f.rulesSrc...)
		winCount := countFiles(f.winItems)
		wslCounts := map[string]int{}
		for d, r := range f.reports {
			wslCounts[d] = countFiles(r.Items)
		}
		f.mu.Unlock()
		f.mu.Lock()
		policy, paused := f.policy, f.paused
		f.mu.Unlock()
		if policy == "" {
			policy = "ask"
		}
		if paused || s.allPaused() {
			state = "paused"
		}
		name := f.displayName()
		folders = append(folders, folderOut{id, name, f.side.Root, rulesSrc, f.exclusions(s.reg.name), files, bytes, state, policy, paused})
		for _, d := range devs {
			for _, b := range d.Bindings {
				if b.Folder != id {
					continue
				}
				st := "idle"
				switch {
				case !online(d):
					st = "offline"
				case perDev[d.ID] > 0:
					st = "conflict"
				case pend[d.ID] > 0:
					st = "syncing"
				}
				bindings = append(bindings, bindingOut{id + "@" + d.ID, id, d.ID, b.Root, st, winCount, wslCounts[d.ID], files, perDev[d.ID], b.LastSync})
			}
		}
	}
	host, _ := os.Hostname()
	pauseOn, pauseUntil := s.pauseState()
	pause := map[string]any{"paused": pauseOn} // "until" only for a pause with an end
	if pauseOn && !pauseUntil.IsZero() {
		pause["until"] = pauseUntil
	}
	writeJSON(w, map[string]any{
		"pause":       pause,
		"server":      map[string]any{"device": s.device, "host": host, "listen": s.cfg.Listen, "panelListen": s.cfg.PanelListen, "version": Version},
		"folders":     folders,
		"connections": conns,
		"bindings":    bindings,
		"conflicts":   conflicts,
		"pairing":     s.pair.pending(),
	})
}

// Version is the server's version, set at build time.
var Version = "0.1.0-dev"

func countFiles(items []scan.Item) int {
	n := 0
	for _, it := range items {
		if !it.Dir {
			n++
		}
	}
	return n
}

func (s *Server) apiActivity(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.ev.list())
}

func (s *Server) folderParam(w http.ResponseWriter, id string) *folder {
	f := s.folder(id)
	if f == nil {
		writeErr(w, http.StatusNotFound, errors.New("no such folder"))
	}
	return f
}

func (s *Server) apiTree(w http.ResponseWriter, r *http.Request) {
	f := s.folderParam(w, r.URL.Query().Get("folder"))
	if f == nil {
		return
	}
	dev := r.URL.Query().Get("connection")
	v := f.treeVersion(dev)
	w.Header().Set("X-Fdsync-Tree-Version", v)
	if r.URL.Query().Get("v") == v {
		w.WriteHeader(http.StatusNoContent) // unchanged: the panel keeps what it has
		return
	}
	writeJSON(w, f.tree(dev))
}

func (s *Server) apiRules(w http.ResponseWriter, r *http.Request) {
	f := s.folderParam(w, r.PathValue("id"))
	if f == nil {
		return
	}
	var req protocol.RulesRequest
	if !decode(w, r, &req) {
		return
	}
	if err := f.updateRules(r.Context(), req.Rules); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.saveRules(f.id, req.Rules); err != nil {
		s.log.Error("save config", "err", err)
	}
	s.ev.add(Event{Kind: "scope", Admin: true, FolderID: f.id, Code: "rulesUpdated"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiExclusions(w http.ResponseWriter, r *http.Request) {
	f := s.folderParam(w, r.PathValue("id"))
	if f == nil {
		return
	}
	var req struct {
		Paths  []string `json:"paths"`
		Cancel bool     `json:"cancel"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := f.setManual(r.Context(), req.Paths, !req.Cancel); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	f.mu.Lock()
	manual := append([]ManualExclusion(nil), f.manual...)
	f.mu.Unlock()
	if err := s.updateConfig(func(c *Config) {
		for i := range c.Folders {
			if c.Folders[i].ID == f.id {
				c.Folders[i].Excluded = manual
			}
		}
	}); err != nil {
		s.log.Error("save config", "err", err)
	}
	code := "exclManual"
	if req.Cancel {
		code = "exclCancelled"
	}
	for _, p := range req.Paths {
		s.ev.add(Event{Kind: "exclude", Admin: true, FolderID: f.id, Path: p, Code: code})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiResolve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
		How string   `json:"how"` // windows | wsl | both
	}
	if !decode(w, r, &req) {
		return
	}
	use := map[string]string{"windows": protocol.UseWindows, "wsl": protocol.UseClient, "both": protocol.KeepBoth}[req.How]
	if use == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown choice %q", req.How))
		return
	}
	var errs []error
	for _, id := range req.IDs {
		fid, p, ok := strings.Cut(id, ":")
		f := s.folder(fid)
		if !ok || f == nil {
			errs = append(errs, fmt.Errorf("unknown conflict %q", id))
			continue
		}
		if err := f.resolve(p, use); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiContent streams one side's copy of a file, cut at limit bytes.
// The WSL copy is read by the client on request; nothing is stored.
func (s *Server) apiContent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := s.folderParam(w, q.Get("folder"))
	if f == nil {
		return
	}
	p, trash := q.Get("path"), q.Get("trash") // trash: a kept version instead of the file
	if err := fsx.CheckRel(p); err != nil && trash == "" {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	limit, _ := strconv.ParseInt(q.Get("limit"), 10, 64)
	offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
	offset = max(offset, 0)
	if limit <= 0 || limit > maxAnswer {
		limit = maxAnswer
	}
	var body []byte
	var size int64
	switch q.Get("side") {
	case "windows":
		var file *os.File
		var err error
		if trash != "" {
			file, err = f.side.Trash.Open(trash)
		} else {
			file, err = openRegular(fsx.Abs(f.side.Root, p))
		}
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		defer file.Close()
		fi, _ := file.Stat()
		size = fi.Size()
		if offset > 0 {
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		body, err = io.ReadAll(io.LimitReader(file, limit))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	case "wsl":
		a, err := s.broker.ask(r.Context(), q.Get("connection"), protocol.Request{Kind: protocol.ReqRead, Folder: f.id, Path: p, Limit: limit, Offset: offset, Trash: trash})
		if err != nil {
			code := http.StatusBadGateway
			if errors.Is(err, errNoAnswer) {
				code = http.StatusGatewayTimeout
			}
			writeErr(w, code, err)
			return
		}
		body, size = a.body, a.size
	default:
		writeErr(w, http.StatusBadRequest, errors.New("side must be windows or wsl"))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set(protocol.HeaderSize, strconv.FormatInt(max(size, int64(len(body))), 10))
	_, _ = w.Write(body)
}

// openRegular opens a regular file without following a final symlink.
func openRegular(p string) (*os.File, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return os.Open(p)
}

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder     string   `json:"folder"`
		Connection string   `json:"connection"`
		Side       string   `json:"side"`
		Paths      []string `json:"paths"`
	}
	if !decode(w, r, &req) {
		return
	}
	f := s.folderParam(w, req.Folder)
	if f == nil {
		return
	}
	// In-scope items: cancel synchronization first, so the other side keeps its copy.
	f.mu.Lock()
	rulesSrc := append([]string{}, f.rulesSrc...)
	var add []string
	dirs := map[string]bool{}
	for _, it := range f.winItems {
		dirs[it.Path] = it.Dir
	}
	if rep := f.reports[req.Connection]; rep != nil {
		for _, it := range rep.Items {
			dirs[it.Path] = dirs[it.Path] || it.Dir
		}
	}
	for _, p := range req.Paths {
		if f.scope.InScope(p, dirs[p]) {
			rule := "!" + p
			if dirs[p] {
				rule += "/"
			}
			add = append(add, rule)
		}
	}
	f.mu.Unlock()
	if len(add) > 0 {
		lines := append(rulesSrc, add...)
		if err := f.updateRules(r.Context(), lines); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		_ = s.saveRules(f.id, lines)
		for _, p := range req.Paths {
			s.ev.add(Event{Kind: "scope", Admin: true, FolderID: f.id, Path: p, Code: "syncCancelled", Params: map[string]any{"path": p}})
		}
	}
	var errs []error
	switch req.Side {
	case "windows":
		errs = append(errs, f.deleteHere(req.Paths))
	case "wsl":
		for _, p := range req.Paths {
			if _, err := s.broker.ask(r.Context(), req.Connection, protocol.Request{Kind: protocol.ReqDelete, Folder: f.id, Path: p}); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p, err))
			}
		}
	default:
		writeErr(w, http.StatusBadRequest, errors.New("side must be windows or wsl"))
		return
	}
	sideName := "Windows"
	if req.Side == "wsl" {
		sideName = s.reg.name(req.Connection)
	}
	for _, p := range req.Paths {
		s.ev.add(Event{Kind: "delete", Admin: true, FolderID: f.id, ConnectionID: req.Connection, Path: p, Code: "adminDeleted", Params: map[string]any{"side": sideName}})
	}
	if err := errors.Join(errs...); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if req.Side == "windows" {
		go f.Scan(context.Background()) //nolint:errcheck
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiDirs lists folders on a client, to pick a binding's local folder.
func (s *Server) apiDirs(w http.ResponseWriter, r *http.Request) {
	a, err := s.broker.ask(r.Context(), r.PathValue("id"), protocol.Request{Kind: protocol.ReqList, Path: r.URL.Query().Get("path")})
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, errNoAnswer) {
			code = http.StatusGatewayTimeout
		}
		writeErr(w, code, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(a.body)
}

// apiBind asks a client to bind a local folder to a sync folder.
func (s *Server) apiBind(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string `json:"folder"`
		Path   string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	if s.folder(req.Folder) == nil {
		writeErr(w, http.StatusNotFound, errors.New("no such folder"))
		return
	}
	dev := r.PathValue("id")
	if _, err := s.broker.ask(r.Context(), dev, protocol.Request{Kind: protocol.ReqBind, Folder: req.Folder, Path: req.Path}); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.ev.add(Event{Kind: "scope", Admin: true, FolderID: req.Folder, ConnectionID: dev, Path: req.Path, Code: "bindingCreated",
		Params: map[string]any{"name": s.reg.name(dev), "path": req.Path}})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- settings

type settingsOut struct {
	Listen      string `json:"listen"`
	PanelListen string `json:"panelListen"`
	Token       string `json:"token"`
	TrashMB     int    `json:"trashMB"`
	TrashUsed   int64  `json:"trashUsed"`
	TrashMax    int    `json:"trashMax"` // the hard limit, MB
	// Autostart: start at Windows login; absent where that isn't supported.
	Autostart   *autostartOut `json:"autostart,omitempty"`
	DataDir     string        `json:"dataDir"`
	ScanDelayMs int64         `json:"scanDelayMs"`
	RescanMin   int64         `json:"rescanMin"`
	Fingerprint string        `json:"fingerprint"` // "" = plain HTTP
	AllowToken  bool          `json:"allowToken"`
}

type autostartOut struct {
	Enabled bool   `json:"enabled"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) apiSettings(w http.ResponseWriter, _ *http.Request) {
	s.cfgMu.Lock()
	out := settingsOut{
		Listen: s.cfg.Listen, PanelListen: s.cfg.PanelListen, Token: s.cfg.Token, DataDir: s.cfg.DataDir,
		ScanDelayMs: s.cfg.ScanDelay.Milliseconds(), RescanMin: int64(s.cfg.RescanPeriod.Minutes()),
	}
	s.cfgMu.Unlock()
	out.Fingerprint = s.fp
	out.AllowToken = s.allowToken()
	out.TrashMB = int(s.trash.MaxBytes >> 20)
	out.TrashUsed = s.trash.Size()
	out.TrashMax = fsx.TrashLimit >> 20
	if a := s.cfg.Autostart; a != nil {
		on, err := a.Enabled()
		out.Autostart = &autostartOut{Enabled: on}
		if err != nil {
			out.Autostart.Error = err.Error()
		}
	}
	writeJSON(w, out)
}

func (s *Server) apiSaveSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrashMB    *int  `json:"trashMB"`
		AllowToken *bool `json:"allowToken"`
		Autostart  *bool `json:"autostart"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Autostart != nil {
		if s.cfg.Autostart == nil {
			writeErr(w, http.StatusBadRequest, errors.New("starting at login is only available on Windows"))
			return
		}
		if err := s.cfg.Autostart.Set(*req.Autostart); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.AllowToken != nil {
		v := *req.AllowToken
		if err := s.updateConfig(func(c *Config) { c.AllowToken = &v }); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.TrashMB != nil {
		mb := min(max(*req.TrashMB, 0), fsx.TrashLimit>>20)
		if err := s.trash.SetLimit(int64(mb) << 20); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := s.updateConfig(func(c *Config) { c.TrashMB = &mb }); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiNewToken replaces the client token. Running clients must be restarted
// with the new one (pairing replaces this later).
func (s *Server) apiNewToken(w http.ResponseWriter, _ *http.Request) {
	tok := RandomToken()
	if err := s.updateConfig(func(c *Config) { c.Token = tok }); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.ev.add(Event{Kind: "pair", Admin: true, Level: "warn", Code: "tokenRegenerated"})
	writeJSON(w, map[string]string{"token": tok})
}

// apiWinDirs lists folders on this (Windows) machine, to pick a new sync
// folder. An empty path lists the drives.
func (s *Server) apiWinDirs(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	res := protocol.ListResult{Path: p, Dirs: []string{}}
	if p == "" {
		if runtime.GOOS == "windows" {
			for c := 'A'; c <= 'Z'; c++ {
				if _, err := os.Stat(string(c) + `:\`); err == nil {
					res.Dirs = append(res.Dirs, string(c)+`:\`)
				}
			}
			writeJSON(w, res)
			return
		}
		p = "/"
	}
	if !filepath.IsAbs(p) {
		writeErr(w, http.StatusBadRequest, errors.New("not an absolute path"))
		return
	}
	res.Path = filepath.Clean(p)
	ents, err := os.ReadDir(res.Path)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), "$") { // skip $Recycle.Bin and similar
			res.Dirs = append(res.Dirs, e.Name())
		} else {
			res.Files++
		}
	}
	sort.Slice(res.Dirs, func(i, j int) bool { return strings.ToLower(res.Dirs[i]) < strings.ToLower(res.Dirs[j]) })
	writeJSON(w, res)
}

func (s *Server) apiAddFolder(w http.ResponseWriter, r *http.Request) {
	var req FolderConfig
	if !decode(w, r, &req) {
		return
	}
	if req.Rules == nil {
		req.Rules = []string{}
	}
	if err := s.AddFolder(r.Context(), req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.ev.add(Event{Kind: "scope", Admin: true, FolderID: req.ID, Code: "folderAdded", Params: map[string]any{"path": req.Path}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiFolderOptions(w http.ResponseWriter, r *http.Request) {
	f := s.folderParam(w, r.PathValue("id"))
	if f == nil {
		return
	}
	var req struct {
		ConflictPolicy *string `json:"conflictPolicy"`
		Paused         *bool   `json:"paused"`
		Name           *string `json:"name"` // "" = show the ID
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if len([]rune(n)) > 64 {
			writeErr(w, http.StatusBadRequest, errors.New("the name is longer than 64 characters"))
			return
		}
		if n == f.id {
			n = ""
		}
		req.Name = &n
	}
	if req.ConflictPolicy != nil {
		switch *req.ConflictPolicy {
		case "ask":
			*req.ConflictPolicy = ""
		case PolicyNewestWins:
		default:
			writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown policy %q", *req.ConflictPolicy))
			return
		}
	}
	from := f.displayName()
	f.setOptions(req.ConflictPolicy, req.Paused, req.Name)
	if err := s.updateConfig(func(c *Config) {
		for i := range c.Folders {
			if c.Folders[i].ID == f.id {
				if req.ConflictPolicy != nil {
					c.Folders[i].ConflictPolicy = *req.ConflictPolicy
				}
				if req.Paused != nil {
					c.Folders[i].Paused = *req.Paused
				}
				if req.Name != nil {
					c.Folders[i].Name = *req.Name
				}
			}
		}
	}); err != nil {
		s.log.Error("save config", "err", err)
	}
	if req.Paused != nil {
		code := "folderResumed"
		if *req.Paused {
			code = "folderPaused"
		}
		s.ev.add(Event{Kind: "scope", Admin: true, FolderID: f.id, Code: code})
	}
	if req.ConflictPolicy != nil {
		s.ev.add(Event{Kind: "scope", Admin: true, FolderID: f.id, Code: "policyChanged"})
	}
	if req.Name != nil && f.displayName() != from {
		s.ev.add(Event{Kind: "scope", Admin: true, FolderID: f.id, Code: "folderRenamed", Params: map[string]any{"from": from, "name": f.displayName()}})
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiStacks writes every goroutine's stack to the log: for diagnosing a
// server that seems stuck (Windows has no SIGQUIT).
func (s *Server) apiStacks(w http.ResponseWriter, _ *http.Request) {
	buf := make([]byte, 8<<20)
	n := runtime.Stack(buf, true)
	s.log.Warn("goroutine dump requested from the panel", "bytes", n)
	s.log.Warn("goroutines\n" + string(buf[:n]))
	writeJSON(w, map[string]int{"bytes": n})
}

// apiRenameConnection sets a connection's display name (an alias for the
// client's hostname).
func (s *Server) apiRenameConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 64 {
		writeErr(w, http.StatusBadRequest, errors.New("a name has 1 to 64 characters"))
		return
	}
	id := r.PathValue("id")
	old := s.reg.name(id)
	if !s.reg.rename(id, name) {
		writeErr(w, http.StatusNotFound, errors.New("no such connection"))
		return
	}
	s.ev.add(Event{Kind: "connect", Admin: true, ConnectionID: id, Code: "connRenamed", Params: map[string]any{"from": old, "name": name}})
	w.WriteHeader(http.StatusNoContent)
}

// apiRescan rescans the Windows folder now and wakes the clients, which
// rescan their side as part of the round.
func (s *Server) apiRescan(w http.ResponseWriter, r *http.Request) {
	f := s.folderParam(w, r.PathValue("id"))
	if f == nil {
		return
	}
	if err := f.Scan(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	f.mu.Lock()
	f.notifyLocked()
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// apiReveal opens Windows Explorer at a file or folder of a sync folder.
func (s *Server) apiReveal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string `json:"folder"`
		Path   string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	f := s.folderParam(w, req.Folder)
	if f == nil {
		return
	}
	abs := f.side.Root
	if req.Path != "" {
		if err := fsx.CheckRel(req.Path); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		abs = fsx.Abs(f.side.Root, req.Path)
	}
	if runtime.GOOS != "windows" {
		writeErr(w, http.StatusNotImplemented, errors.New("only on Windows"))
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var cmd *exec.Cmd
	if fi.IsDir() {
		cmd = exec.Command("explorer.exe", abs)
	} else {
		cmd = exec.Command("explorer.exe", "/select,"+abs)
	}
	_ = cmd.Start() // explorer.exe exits with 1 even when it worked
	w.WriteHeader(http.StatusNoContent)
}

// apiPause pauses every folder: minutes > 0 for that long, -1 until
// resumed; 0 resumes.
func (s *Server) apiPause(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Minutes int `json:"minutes"`
	}
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.Minutes == 0:
		s.SetPauseAll(false, time.Time{}, false)
	case req.Minutes == -1:
		s.SetPauseAll(true, time.Time{}, false)
	case req.Minutes > 0 && req.Minutes <= 7*24*60:
		s.SetPauseAll(true, time.Now().Add(time.Duration(req.Minutes)*time.Minute), false)
	default:
		writeErr(w, http.StatusBadRequest, errors.New("minutes must be 1 to 10080, -1 (until resumed) or 0 (resume)"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
