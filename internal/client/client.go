// Package client is the WSL side: for each binding it scans the local
// folder, pushes local changes, uploads what the server asks for, and pulls
// the server's changes.
package client

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"fdsync/internal/fsx"
	"fdsync/internal/index"
	"fdsync/internal/protocol"
	"fdsync/internal/rules"
	"fdsync/internal/scan"
	"fdsync/internal/syncer"
	"fdsync/internal/watch"
)

// Client talks to one server.
type Client struct {
	BaseURL string // e.g. http://172.28.0.1:7443
	Token   string
	Device  string
	HTTP    *http.Client
	Log     *slog.Logger
	Trash   *fsx.TrashStore // shared by all bindings; nil = no trash
	// Tokens authenticates a paired client; without it, Token (the shared
	// token) is sent.
	Tokens *TokenSource
	// OnRevoked is called (once) when the server no longer accepts this
	// client's credentials; the daemon stops.
	OnRevoked func()
	revoked   sync.Once
	Info      protocol.Hello // who this client is (bindings are filled in)
	// Root is the only folder the panel may browse and bind under (default $HOME).
	Root string
	// OnBind creates a binding the admin made in the panel (the daemon
	// starts syncing it and remembers it). nil = not allowed.
	OnBind func(b Binding) error
	// RefreshBaseURL re-resolves the server URL (e.g. WSL's default gateway)
	// after a network error, so a daemon recovers if the host IP changes.
	RefreshBaseURL func() string

	urlMu    sync.RWMutex
	mu       sync.Mutex
	sessions []*Session
}

func (c *Client) getBaseURL() string {
	c.urlMu.RLock()
	defer c.urlMu.RUnlock()
	return c.BaseURL
}

func (c *Client) maybeRefreshBaseURL() bool {
	if c.RefreshBaseURL == nil {
		return false
	}
	next := c.RefreshBaseURL()
	if next == "" {
		return false
	}
	c.urlMu.Lock()
	old := c.BaseURL
	changed := next != old
	if changed {
		c.BaseURL = next
	}
	c.urlMu.Unlock()
	if changed {
		if c.Tokens != nil {
			c.Tokens.SetBaseURL(next)
		}
		if c.Log != nil {
			c.Log.Info("Windows host address changed; updated server URL", "from", old, "to", next)
		}
	}
	return changed
}

// Binding links a server folder to a local folder.
type Binding struct {
	Folder string
	Root   string
}

// Session syncs one binding.
type Session struct {
	c        *Client
	b        Binding
	side     syncer.Side
	store    *index.Store
	scope    *rules.Scope
	mu       sync.Mutex // one sync round or admin delete at a time
	treeHash string
	tree     *protocol.TreeReport
	// caseRetry: paths Windows refused for a case collision, offered again every round.
	caseRetry map[string]bool
	moves     map[string]*index.Entry // during a pull page: content hash → local file the page deletes
	moveMu    sync.Mutex
	lastPurge time.Time
}

// Stats counts what one sync round did.
type Stats struct {
	Scanned   scan.Result
	Pushed    int
	Uploaded  int
	Pulled    int // remote entries applied
	Moved     int // applied by renaming a local file instead of downloading
	Conflicts int
	Skipped   int // remote entries that wait for the next round
	// Bytes over the network, and bytes taken from local old versions instead.
	BytesUp, BytesDown, BytesReused int64
}

const (
	metaPushed = "pushed"     // last local seq the server has seen
	metaPulled = "pulled"     // server cursor
	metaRules  = "rules-hash" // scope the cursors belong to
)

// Open opens the binding. workDir holds its index, staging folder and
// trash; it must be outside the bound folder.
func (c *Client) Open(b Binding, workDir string) (*Session, error) {
	fi, err := os.Stat(b.Root)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("binding %s: %s is not a folder", b.Folder, b.Root)
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, err
	}
	var trash *fsx.Trash
	if c.Trash != nil {
		trash = c.Trash.For(filepath.Base(workDir))
	}
	ws, err := fsx.OpenWorkspace(workDir, b.Root, trash)
	if err != nil {
		return nil, err
	}
	if !ws.SameDrive {
		c.Log.Warn("state directory is on another filesystem than the folder: incoming files are received next to their destination, and the trash copies instead of moving",
			"folder", b.Folder, "state", workDir)
	}
	store, err := index.Open(filepath.Join(workDir, "index.db"))
	if err != nil {
		return nil, err
	}
	s := &Session{
		c:     c,
		b:     b,
		side:  syncer.Side{Root: b.Root, Device: c.Device, Stage: ws.Stage, Trash: ws.Trash, TrackMode: true},
		store: store,
	}
	c.mu.Lock()
	c.sessions = append(c.sessions, s)
	c.mu.Unlock()
	return s, nil
}

// authorize adds credentials: a paired client's access token, or the
// shared token.
func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	tok := c.Token
	if c.Tokens != nil {
		var err error
		if tok, err = c.Tokens.Token(ctx); err != nil {
			if errors.Is(err, ErrRevoked) && c.OnRevoked != nil {
				c.revoked.Do(c.OnRevoked)
			}
			return err
		}
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set(protocol.HeaderDevice, c.Device)
	return nil
}

// hello tells the server who we are and which bindings we have.
func (c *Client) hello(ctx context.Context) error {
	h := c.Info
	if watch.Limited.Load() {
		h.Warnings = append(h.Warnings, "watch-limit")
	}
	c.mu.Lock()
	h.Bindings = nil
	for _, s := range c.sessions {
		h.Bindings = append(h.Bindings, protocol.HelloBinding{Folder: s.b.Folder, Root: s.b.Root})
	}
	c.mu.Unlock()
	body, _ := json.Marshal(h)
	return c.do(ctx, "POST", "/hello", nil, bytes.NewReader(body), nil)
}

func (s *Session) Close() error { return s.store.Close() }

// ---------------------------------------------------------------- HTTP

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return fmt.Sprintf("server: %d %s", e.code, e.msg) }

func isConflict(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.code == http.StatusConflict
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, out any) error {
	return c.doHeader(ctx, method, path, q, body, nil, out)
}

func (c *Client) doHeader(ctx context.Context, method, path string, q url.Values, body io.Reader, hdr map[string]string, out any) error {
	u := c.getBaseURL() + protocol.APIPrefix + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if err := c.authorize(ctx, req); err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.maybeRefreshBaseURL()
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && c.Tokens != nil {
		c.Tokens.Invalidate() // e.g. the server restarted: refresh next time
	}
	if resp.StatusCode >= 300 {
		var e protocol.Error
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		return &httpError{resp.StatusCode, e.Error}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (s *Session) folderPath(p string) string { return "/folders/" + url.PathEscape(s.b.Folder) + p }

// ---------------------------------------------------------------- sync

// SyncOnce runs one full round: scope, scan, push, upload, pull.
func (s *Session) SyncOnce(ctx context.Context) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st Stats
	if err := s.c.hello(ctx); err != nil {
		return st, err
	}
	info, err := s.refreshScope(ctx)
	if err != nil {
		return st, err
	}
	rs := s.scope
	// A new scope means paths may have entered it: exchange everything again.
	// Unchanged entries cost a comparison, not a transfer.
	if err := s.store.Update(func(tx *index.Tx) error {
		if string(tx.Meta(metaRules)) != info.RulesHash {
			if err := tx.SetMetaUint(metaPushed, 0); err != nil {
				return err
			}
			if err := tx.SetMetaUint(metaPulled, 0); err != nil {
				return err
			}
			return tx.SetMeta(metaRules, []byte(info.RulesHash))
		}
		return nil
	}); err != nil {
		return st, err
	}

	st.Scanned, err = scan.Scan(ctx, s.store, scan.Options{Root: s.b.Root, Device: s.c.Device, Scope: rs, TrackMode: true, CheckWindowsNames: true})
	if err != nil {
		return st, fmt.Errorf("scan: %w", err)
	}
	s.setTree(st.Scanned)
	if err := s.push(ctx, &st); err != nil {
		return st, fmt.Errorf("push: %w", err)
	}
	if err := s.pull(ctx, &st); err != nil {
		return st, fmt.Errorf("pull: %w", err)
	}
	// Once a day: forget our own deletes that the server has and that are old.
	if time.Since(s.lastPurge) > 24*time.Hour {
		s.lastPurge = time.Now()
		_ = s.store.Update(func(tx *index.Tx) error {
			_, err := tx.PurgeTombstones(tx.MetaUint(metaPushed), time.Now().Add(-30*24*time.Hour))
			return err
		})
	}
	return st, nil
}

// refreshScope fetches the folder's current scope (rules and exclusions).
func (s *Session) refreshScope(ctx context.Context) (protocol.Folder, error) {
	var info protocol.Folder
	if err := s.c.do(ctx, "GET", s.folderPath(""), nil, nil, &info); err != nil {
		return info, err
	}
	sc, err := rules.NewScope(info.Rules, info.Excluded)
	if err != nil {
		return info, err
	}
	s.scope = sc
	return info, nil
}

// setTree keeps the whole-folder metadata for the server (sent when it asks).
func (s *Session) setTree(r scan.Result) {
	rep := &protocol.TreeReport{Items: r.All, Excluded: r.Excluded}
	if rep.Items == nil {
		rep.Items = []scan.Item{}
	}
	b, _ := json.Marshal(rep)
	h := sha256.Sum256(b)
	rep.Hash = hex.EncodeToString(h[:12])
	s.tree, s.treeHash = rep, rep.Hash
}

func (s *Session) sendTree(ctx context.Context) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(s.tree); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.c.getBaseURL()+protocol.APIPrefix+s.folderPath("/tree"), &buf)
	if err != nil {
		return err
	}
	if err := s.c.authorize(ctx, req); err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := s.c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &httpError{resp.StatusCode, "tree report refused"}
	}
	return nil
}

func (s *Session) push(ctx context.Context, st *Stats) error {
	first := true
	for {
		var batch []index.Entry
		var last uint64
		err := s.store.View(func(tx *index.Tx) error {
			inBatch := map[string]bool{}
			err := tx.Since(tx.MetaUint(metaPushed), func(e *index.Entry) (bool, error) {
				last = e.Seq
				if s.scope.InScope(e.Path, e.Dir) {
					batch = append(batch, *e)
					inBatch[e.Path] = true
				}
				return len(batch) < protocol.PageSize, nil
			})
			if err != nil || !first {
				return err
			}
			// Entries Windows refused for a case collision: offer them again,
			// they go through once the other name is gone there.
			for p := range s.caseRetry {
				if e, _ := tx.Get(p); e != nil && !e.Deleted && !inBatch[p] && s.scope.InScope(p, e.Dir) {
					batch = append(batch, *e)
				} else if e == nil || e.Deleted {
					delete(s.caseRetry, p)
				}
			}
			return nil
		})
		first = false
		if err != nil {
			return err
		}
		var res protocol.PushResult
		var pulled uint64
		_ = s.store.View(func(tx *index.Tx) error { pulled = tx.MetaUint(metaPulled); return nil })
		body, _ := json.Marshal(protocol.PushRequest{Entries: batch, TreeHash: s.treeHash, Pulled: pulled})
		if err := s.c.do(ctx, "POST", s.folderPath("/push"), nil, bytes.NewReader(body), &res); err != nil {
			return err
		}
		if res.WantTree && s.tree != nil {
			if err := s.sendTree(ctx); err != nil {
				s.c.Log.Warn("tree report", "folder", s.b.Folder, "err", err)
			}
		}
		st.Pushed += len(batch)
		st.Conflicts += len(res.Conflicts)
		for _, p := range res.Conflicts {
			s.c.Log.Warn("conflict: waiting for the admin", "folder", s.b.Folder, "path", p)
		}
		refused := map[string]bool{}
		for _, r := range res.Rejected {
			s.c.Log.Debug("rejected by server", "path", r.Path, "reason", r.Reason)
			if r.Reason == protocol.ReasonCaseCollision {
				if s.caseRetry == nil {
					s.caseRetry = map[string]bool{}
				}
				if !s.caseRetry[r.Path] {
					s.c.Log.Warn("not synced: Windows has this name in another case", "folder", s.b.Folder, "path", r.Path, "windows", r.Other)
				}
				s.caseRetry[r.Path] = true
				refused[r.Path] = true
			}
		}
		for _, e := range batch {
			if s.caseRetry[e.Path] && !refused[e.Path] {
				delete(s.caseRetry, e.Path) // accepted now
			}
		}
		if err := s.uploadAll(ctx, res.Need, st); err != nil {
			return err
		}
		if last > 0 {
			if err := s.store.Update(func(tx *index.Tx) error { return tx.SetMetaUint(metaPushed, last) }); err != nil {
				return err
			}
		}
		if len(batch) < protocol.PageSize {
			return nil
		}
	}
}

// uploadAll sends what the server asked for: small files in bundles, the
// others one by one (changed blocks only when possible), a few at a time.
func (s *Session) uploadAll(ctx context.Context, needs []protocol.Need, st *Stats) error {
	if len(needs) == 0 {
		return nil
	}
	type small struct {
		n protocol.Need
		e *index.Entry
	}
	var smalls []small
	var singles []protocol.Need
	_ = s.store.View(func(tx *index.Tx) error {
		for _, n := range needs {
			src := n.Path
			if n.Source != "" {
				src = n.Source
			}
			if e, _ := tx.Get(src); !n.Delta && e != nil && !e.Deleted && !e.Dir && e.Hash == n.Hash && e.Size <= protocol.BundleFileMax {
				smalls = append(smalls, small{n, e})
			} else {
				singles = append(singles, n)
			}
		}
		return nil
	})
	var mu sync.Mutex
	var fatal error
	setFatal := func(err error) {
		mu.Lock()
		if fatal == nil {
			fatal = err
		}
		mu.Unlock()
	}
	var tasks []func(*Stats)
	for i := 0; i < len(smalls); {
		j, size := i, int64(0)
		for j < len(smalls) && j-i < protocol.BundleFilesMax && size+smalls[j].e.Size <= protocol.BundleBytesMax {
			size += smalls[j].e.Size
			j++
		}
		if j == i {
			j++
		}
		group := smalls[i:j]
		tasks = append(tasks, func(ls *Stats) {
			ns := make([]protocol.Need, len(group))
			es := make([]*index.Entry, len(group))
			for k, g := range group {
				ns[k], es[k] = g.n, g.e
			}
			if err := s.uploadBundle(ctx, ns, es, ls); err != nil {
				var he *httpError
				if errors.As(err, &he) {
					s.c.Log.Warn("bundle upload failed; retrying next round", "folder", s.b.Folder, "files", len(group), "err", err)
					ls.Skipped += len(group)
					return
				}
				setFatal(fmt.Errorf("bundle upload: %w", err))
			}
		})
		i = j
	}
	for _, n := range singles {
		tasks = append(tasks, func(ls *Stats) {
			err := s.upload(ctx, n, ls)
			var he *httpError
			switch {
			case err == nil:
				ls.Uploaded++
			case isConflict(err) || errors.Is(err, fsx.ErrChanged):
				// stale: a newer local version follows
			case errors.As(err, &he) || errors.Is(err, fs.ErrNotExist):
				// One file failed (e.g. open in a Windows program, so it
				// can't be replaced). The server keeps asking for it; the
				// rest of the round goes on.
				s.c.Log.Warn("upload failed; retrying next round", "folder", s.b.Folder, "path", n.Path, "err", err)
				ls.Skipped++
			default:
				setFatal(fmt.Errorf("upload %s: %w", n.Path, err))
			}
		})
	}
	runTasks(tasks, st)
	return fatal
}

// uploadBundle sends many small files in one request; the server commits
// them in one go. Files that changed since the scan are left out (their
// new version follows).
func (s *Session) uploadBundle(ctx context.Context, ns []protocol.Need, es []*index.Entry, st *Stats) error {
	pr, pw := io.Pipe()
	go func() {
		bw := bufio.NewWriterSize(pw, 256<<10)
		for k, n := range ns {
			e := es[k]
			p := fsx.Abs(s.b.Root, e.Path)
			if !syncer.ExpectOf(e).Matches(p) {
				continue
			}
			f, err := os.Open(p)
			if err != nil {
				continue
			}
			err = protocol.WriteItem(bw, protocol.BundleItem{Path: n.Path, Hash: n.Hash, Size: e.Size})
			if err == nil {
				_, err = io.CopyN(bw, f, e.Size)
			}
			f.Close()
			if err != nil {
				pw.CloseWithError(err) // the file shrank meanwhile: retry next round
				return
			}
			st.BytesUp += e.Size
		}
		pw.CloseWithError(bw.Flush())
	}()
	var res []protocol.BundleResult
	if err := s.c.do(ctx, "POST", s.folderPath("/bundle/put"), nil, pr, &res); err != nil {
		pr.CloseWithError(err)
		return err
	}
	for _, r := range res {
		if r.Error == "" {
			st.Uploaded++
		}
	}
	return nil
}

func (s *Session) upload(ctx context.Context, n protocol.Need, st *Stats) error {
	src := n.Path
	if n.Source != "" {
		src = n.Source // "keep both": our version, saved under a new name on the server
	}
	if err := fsx.CheckRel(src); err != nil {
		return err
	}
	var e *index.Entry
	if err := s.store.View(func(tx *index.Tx) (err error) { e, err = tx.Get(src); return }); err != nil {
		return err
	}
	if e == nil || e.Deleted || e.Dir || e.Hash != n.Hash {
		return fsx.ErrChanged
	}
	p := fsx.Abs(s.b.Root, src)
	if !syncer.ExpectOf(e).Matches(p) {
		return fsx.ErrChanged
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	q := url.Values{"path": {n.Path}, "hash": {n.Hash}}
	if !n.Delta || e.BlockSize == 0 {
		st.BytesUp += e.Size
		return s.c.do(ctx, "PUT", s.folderPath("/content"), q, f, nil)
	}
	// Only the blocks the server lacks, in order; it copies the rest.
	var parts []io.Reader
	var sent int64
	for _, i := range n.Missing {
		if i < 0 || i >= len(e.Blocks) {
			return fmt.Errorf("server asked for block %d of %d", i, len(e.Blocks))
		}
		ln := fsx.BlockLen(e.Size, e.BlockSize, i)
		parts = append(parts, io.NewSectionReader(f, int64(i)*int64(e.BlockSize), ln))
		sent += ln
	}
	st.BytesUp += sent
	st.BytesReused += e.Size - sent
	return s.c.doHeader(ctx, "PUT", s.folderPath("/content"), q, io.MultiReader(parts...),
		map[string]string{protocol.HeaderBlocks: fsx.EncodeIndexes(n.Missing)}, nil)
}

func (s *Session) pull(ctx context.Context, st *Stats) error {
	for {
		var since uint64
		_ = s.store.View(func(tx *index.Tx) error { since = tx.MetaUint(metaPulled); return nil })
		var pg protocol.IndexPage
		if err := s.c.do(ctx, "GET", s.folderPath("/index"), url.Values{"since": {strconv.FormatUint(since, 10)}}, nil, &pg); err != nil {
			return err
		}
		// Creations in server order (parents first); deletions children-first.
		sort.SliceStable(pg.Entries, func(i, j int) bool {
			a, b := &pg.Entries[i], &pg.Entries[j]
			if a.Deleted != b.Deleted {
				return !a.Deleted
			}
			if a.Deleted {
				return a.Depth() > b.Depth()
			}
			return false
		})
		// Files this page deletes whose content reappears in it: moves.
		s.moves = map[string]*index.Entry{}
		_ = s.store.View(func(tx *index.Tx) error {
			for i := range pg.Entries {
				t := &pg.Entries[i]
				if !t.Deleted || t.Dir {
					continue
				}
				if l, _ := tx.Get(t.Path); l != nil && !l.Deleted && !l.Dir && l.Hash != "" && protocol.Decide(l, t) == protocol.ActApply {
					s.moves[l.Hash] = l
				}
			}
			return nil
		})
		// Folders first, then files (several at once), then deletes
		// (children first). Moves happen with the files, before the
		// deletes of their old paths.
		var dirs, files, dels []*index.Entry
		for i := range pg.Entries {
			r := &pg.Entries[i]
			if err := fsx.CheckRel(r.Path); err != nil || !s.scope.InScope(r.Path, r.Dir) {
				continue
			}
			switch {
			case r.Deleted:
				dels = append(dels, r)
			case r.Dir:
				dirs = append(dirs, r)
			default:
				files = append(files, r)
			}
		}
		all := append(append(dirs, files...), dels...)
		failed := s.applyPage(ctx, all, st)
		if failed {
			return nil // keep the cursor; the next round retries
		}
		if err := s.store.Update(func(tx *index.Tx) error { return tx.SetMetaUint(metaPulled, pg.Next) }); err != nil {
			return err
		}
		if !pg.More {
			return nil
		}
	}
}

// transfers is how many files move at once (they share one HTTP/2 connection).
const transfers = 4

func (st *Stats) add(o *Stats) {
	st.Pulled += o.Pulled
	st.Moved += o.Moved
	st.Conflicts += o.Conflicts
	st.Skipped += o.Skipped
	st.Uploaded += o.Uploaded
	st.BytesUp += o.BytesUp
	st.BytesDown += o.BytesDown
	st.BytesReused += o.BytesReused
}

// takeMove claims the local file a page deletes that has this content.
func (s *Session) takeMove(hash string) *index.Entry {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	src := s.moves[hash]
	delete(s.moves, hash)
	return src
}

// errServerChanged: the server's file changed after it was listed; the
// page is fetched again.
var errServerChanged = &httpError{http.StatusConflict, "changed on the server"}

// want is a remote file whose content must be fetched.
type want struct {
	r      *index.Entry
	old    *index.Entry // this side's version (blocks to reuse), or nil
	lseq   uint64       // its sequence, to detect a local change meanwhile
	expect fsx.Expect
	tmp    string
	err    error
}

// applyPage applies a page of server entries with two index transactions:
// one for everything that needs no content (folders, moves, metadata,
// deletes), then the downloads (small files in bundles, large ones by
// block, several at once), then one that commits what was downloaded.
// It reports whether the page must be fetched again.
func (s *Session) applyPage(ctx context.Context, entries []*index.Entry, st *Stats) (failed bool) {
	note := func(r *index.Entry, err error) {
		switch {
		case errors.Is(err, fsx.ErrChanged), errors.Is(err, fsx.ErrNotEmpty):
			// Local change since the scan: the next round pushes it, and
			// the server decides.
			st.Skipped++
		case isConflict(err):
			st.Skipped++
			failed = true
		default:
			s.c.Log.Warn("apply", "folder", s.b.Folder, "path", r.Path, "err", err)
			failed = true
		}
	}
	var wants []*want
	if err := s.store.Update(func(tx *index.Tx) error {
		for _, r := range entries {
			l, err := tx.Get(r.Path)
			if err != nil {
				return err
			}
			switch protocol.Decide(l, r) {
			case protocol.ActNone:
				continue
			case protocol.ActMerge:
				if err := s.side.Merge(tx, l, r); err != nil {
					note(r, err)
				} else {
					st.Pulled++
				}
				continue
			case protocol.ActConflict:
				st.Conflicts++
				continue
			}
			need, expect, err := s.side.Apply(tx, l, r)
			if err != nil {
				note(r, err)
				continue
			}
			if !need {
				st.Pulled++
				continue
			}
			// Renamed or moved on Windows: move the local copy instead of downloading.
			if src := s.takeMove(r.Hash); src != nil {
				moved, err := s.side.Move(tx, src, r, expect)
				if err != nil {
					note(r, err)
					continue
				}
				if moved {
					st.Pulled++
					st.Moved++
					st.BytesReused += r.Size
					continue
				}
			}
			w := &want{r: r, expect: expect}
			if l != nil {
				w.old, w.lseq = l, l.Seq
			}
			wants = append(wants, w)
		}
		return nil
	}); err != nil {
		s.c.Log.Warn("apply page", "folder", s.b.Folder, "err", err)
		return true
	}
	if len(wants) == 0 {
		return failed
	}

	s.fetchAll(ctx, wants, st)

	if err := s.store.Update(func(tx *index.Tx) error {
		for _, w := range wants {
			if w.err != nil {
				note(w.r, w.err)
				continue
			}
			// Re-check: the local entry may have changed while downloading.
			l, err := tx.Get(w.r.Path)
			if err != nil {
				fsx.Discard(w.tmp)
				return err
			}
			cur := uint64(0)
			if l != nil {
				cur = l.Seq
			}
			if cur != w.lseq || protocol.Decide(l, w.r) != protocol.ActApply {
				fsx.Discard(w.tmp)
				note(w.r, fsx.ErrChanged)
				continue
			}
			if err := s.side.Commit(tx, w.r, w.tmp, w.expect); err != nil {
				note(w.r, err)
				continue
			}
			st.Pulled++
		}
		return nil
	}); err != nil {
		s.c.Log.Warn("commit page", "folder", s.b.Folder, "err", err)
		return true
	}
	return failed
}

// fetchAll downloads into temp files: small files in bundles, the others
// one by one (by block when possible), up to `transfers` at a time.
func (s *Session) fetchAll(ctx context.Context, wants []*want, st *Stats) {
	var tasks []func(*Stats)
	var bundle []*want
	var bytes int64
	flush := func() {
		if len(bundle) > 0 {
			b := bundle
			tasks = append(tasks, func(ls *Stats) { s.fetchBundle(ctx, b, ls) })
			bundle, bytes = nil, 0
		}
	}
	for _, w := range wants {
		if w.r.BlockSize == 0 && w.r.Size <= protocol.BundleFileMax {
			if len(bundle) == protocol.BundleFilesMax || bytes+w.r.Size > protocol.BundleBytesMax {
				flush()
			}
			bundle = append(bundle, w)
			bytes += w.r.Size
			continue
		}
		w := w
		tasks = append(tasks, func(ls *Stats) { w.tmp, w.err = s.fetch(ctx, w.r, w.old, ls) })
	}
	flush()
	runTasks(tasks, st)
}

// runTasks runs tasks `transfers` at a time, each with its own Stats.
func runTasks(tasks []func(*Stats), st *Stats) {
	work := make(chan func(*Stats))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for range min(transfers, len(tasks)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local Stats
			for t := range work {
				t(&local)
			}
			mu.Lock()
			st.add(&local)
			mu.Unlock()
		}()
	}
	for _, t := range tasks {
		work <- t
	}
	close(work)
	wg.Wait()
}

// fetchBundle downloads many small files in one request.
func (s *Session) fetchBundle(ctx context.Context, ws []*want, st *Stats) {
	byPath := map[string]*want{}
	req := protocol.BundleRequest{}
	for _, w := range ws {
		byPath[w.r.Path] = w
		req.Items = append(req.Items, protocol.BundleItem{Path: w.r.Path, Hash: w.r.Hash})
	}
	fail := func(err error) {
		for _, w := range ws {
			if w.tmp == "" && w.err == nil {
				w.err = err
			}
		}
	}
	// On every way out: what arrived goes to disk before it can be committed.
	defer func() {
		var tmps []string
		for _, w := range ws {
			if w.err == nil && w.tmp != "" {
				tmps = append(tmps, w.tmp)
			}
		}
		if err := fsx.SyncAll(tmps); err != nil {
			for _, w := range ws {
				if w.err == nil && w.tmp != "" {
					fsx.Discard(w.tmp)
					w.tmp, w.err = "", err
				}
			}
		}
	}()
	body, _ := json.Marshal(req)
	hreq, err := http.NewRequestWithContext(ctx, "POST", s.c.getBaseURL()+protocol.APIPrefix+s.folderPath("/bundle/get"), bytes.NewReader(body))
	if err != nil {
		fail(err)
		return
	}
	if err := s.c.authorize(ctx, hreq); err != nil {
		fail(err)
		return
	}
	resp, err := s.c.HTTP.Do(hreq)
	if err != nil {
		fail(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fail(&httpError{resp.StatusCode, "bundle refused"})
		return
	}
	br := bufio.NewReaderSize(resp.Body, 256<<10)
	for {
		it, err := protocol.ReadItem(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail(err)
			return
		}
		w := byPath[it.Path]
		if w == nil || it.Hash != w.r.Hash {
			fail(errors.New("bundle: unexpected file"))
			return
		}
		if it.Error != "" {
			w.err = errServerChanged
			continue
		}
		w.tmp, w.err = fsx.ReceiveUnsynced(s.side.Stage, s.b.Root, w.r.Path, io.LimitReader(br, it.Size), w.r.Hash)
		st.BytesDown += it.Size
	}
	fail(errors.New("bundle: file missing from the answer"))
}

// fetch gets a remote version into a temp file: only the changed blocks
// when this side has an older version with blocks, else the whole file.
func (s *Session) fetch(ctx context.Context, r, old *index.Entry, st *Stats) (string, error) {
	if r.BlockSize == 0 || old == nil || old.Deleted || old.Dir || old.BlockSize != r.BlockSize || len(old.Blocks) == 0 {
		st.BytesDown += r.Size
		return s.download(ctx, r)
	}
	have := fsx.Plan(old.Blocks, old.BlockSize)
	missing := fsx.Missing(r.Blocks, have)
	p := fsx.Abs(s.b.Root, old.Path)
	if len(missing) == len(r.Blocks) || !syncer.ExpectOf(old).Matches(p) {
		st.BytesDown += r.Size
		return s.download(ctx, r)
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	miss := map[int]bool{}
	for _, i := range missing {
		miss[i] = true
	}
	// One Range request per run of consecutive missing blocks.
	var body io.ReadCloser
	runEnd := -1
	defer func() {
		if body != nil {
			body.Close()
		}
	}()
	fetchBlock := func(i int, n int64) (io.Reader, error) {
		if body != nil && i < runEnd {
			return body, nil
		}
		if body != nil {
			body.Close()
			body = nil
		}
		runEnd = i + 1
		for miss[runEnd] {
			runEnd++
		}
		from := int64(i) * int64(r.BlockSize)
		to := min(int64(runEnd)*int64(r.BlockSize), r.Size) - 1
		b, err := s.rangeGet(ctx, r, from, to)
		if err != nil {
			return nil, err
		}
		body = b
		return body, nil
	}
	local := func(_ int, bh string) (int64, bool) {
		off, ok := have[bh]
		return off, ok
	}
	tmp, ast, err := fsx.Assemble(s.side.Stage, s.b.Root, r.Path, r.Size, r.BlockSize, r.Blocks, r.Hash, f, local, fetchBlock)
	st.BytesDown += ast.Fetched
	st.BytesReused += ast.Reused
	return tmp, err
}

// rangeGet fetches bytes from..to (inclusive) of the server's version.
func (s *Session) rangeGet(ctx context.Context, r *index.Entry, from, to int64) (io.ReadCloser, error) {
	q := url.Values{"path": {r.Path}, "hash": {r.Hash}}
	req, err := http.NewRequestWithContext(ctx, "GET", s.c.getBaseURL()+protocol.APIPrefix+s.folderPath("/content")+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if err := s.c.authorize(ctx, req); err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
	resp, err := s.c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, &httpError{resp.StatusCode, "range request refused"}
	}
	return resp.Body, nil
}

func (s *Session) download(ctx context.Context, r *index.Entry) (string, error) {
	q := url.Values{"path": {r.Path}, "hash": {r.Hash}}
	req, err := http.NewRequestWithContext(ctx, "GET", s.c.getBaseURL()+protocol.APIPrefix+s.folderPath("/content")+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if err := s.c.authorize(ctx, req); err != nil {
		return "", err
	}
	resp, err := s.c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e protocol.Error
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		return "", &httpError{resp.StatusCode, e.Error}
	}
	return fsx.Receive(s.side.Stage, s.b.Root, r.Path, resp.Body, r.Hash)
}

// ---------------------------------------------------------------- daemon

// Run syncs until ctx ends: on local changes (watcher), on server changes
// (long poll), every rescan period, and with backoff after errors.
func (s *Session) Run(ctx context.Context, scanDelay, rescan time.Duration) {
	trigger := make(chan struct{}, 1)
	poke := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
	if events, err := watch.Watch(ctx, s.b.Root, scanDelay, s.c.Log); err != nil {
		s.c.Log.Warn("watcher unavailable; relying on periodic rescans", "root", s.b.Root, "err", err)
	} else {
		go func() {
			for range events {
				poke()
			}
		}()
	}
	go s.longPoll(ctx, poke)

	backoff := time.Second
	paused := false
	tick := time.NewTicker(rescan)
	defer tick.Stop()
	poke()
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
		case <-tick.C:
		}
		st, err := s.SyncOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		var he *httpError
		if errors.As(err, &he) && he.code == http.StatusServiceUnavailable {
			// Paused on the server: wait for the long poll to say it resumed.
			if !paused {
				s.c.Log.Info("sync is paused on the server; waiting", "folder", s.b.Folder)
			}
			paused = true
			continue
		}
		if paused && err == nil {
			s.c.Log.Info("sync resumed", "folder", s.b.Folder)
		}
		paused = false
		if err != nil {
			s.c.Log.Warn("sync failed; retrying", "folder", s.b.Folder, "in", backoff, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			poke()
			continue
		}
		backoff = time.Second
		if st.Scanned.Changed+st.Pulled+st.Uploaded+st.Conflicts > 0 {
			s.c.Log.Info("synced", "folder", s.b.Folder, "local", st.Scanned.Changed, "uploaded", st.Uploaded,
				"pulled", st.Pulled, "conflicts", st.Conflicts, "skipped", st.Skipped,
				"sent", st.BytesUp, "received", st.BytesDown, "reused", st.BytesReused)
		}
	}
}

func (s *Session) longPoll(ctx context.Context, poke func()) {
	var since, nudge uint64
	var hash string
	backoff := time.Second
	for ctx.Err() == nil {
		var res protocol.WaitResult
		q := url.Values{"since": {strconv.FormatUint(since, 10)}, "rules": {hash}, "nudge": {strconv.FormatUint(nudge, 10)}, "timeout": {"30"}}
		if err := s.c.do(ctx, "GET", s.folderPath("/wait"), q, nil, &res); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		if res.Seq != since || res.RulesHash != hash || res.Nudge != nudge {
			since, hash, nudge = res.Seq, res.RulesHash, res.Nudge
			poke()
		}
	}
}
