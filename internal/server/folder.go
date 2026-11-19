package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"fdsync/internal/fsx"
	"fdsync/internal/index"
	"fdsync/internal/protocol"
	"fdsync/internal/rules"
	"fdsync/internal/scan"
	"fdsync/internal/syncer"
)

const (
	bPending   = "pending"   // path → pending: a client version waiting for its content
	bConflicts = "conflicts" // path → protocol.Conflict
)

// pending is a newer client version the server accepted but whose content
// hasn't arrived yet.
type pending struct {
	Entry  index.Entry `json:"entry"`
	Device string      `json:"device"`
	Source string      `json:"source,omitempty"` // client path to upload from, if not Entry.Path ("keep both")
}

func (f *folder) event(e Event) {
	if f.srv != nil {
		e.FolderID = f.id
		f.srv.ev.add(e)
	}
}

// folder is one sync folder: its Windows root, scope and index.
type folder struct {
	id    string
	side  syncer.Side
	store *index.Store
	log   *slog.Logger
	srv   *Server

	mu        sync.Mutex // serializes scans and index changes
	rulesSrc  []string
	manual    []ManualExclusion // excluded by the admin
	scope     *rules.Scope      // rules minus manual and auto exclusions
	exclPaths []string          // the scope's excluded paths, sorted
	rulesHash string            // hash of the whole scope; clients re-exchange when it changes
	changed   chan struct{}     // closed (and replaced) when the index or the scope changes
	nudge     uint64            // counts notifications, so clients wake for any of them
	winItems  []scan.Item       // the whole Windows folder, from the last scan
	winHash   uint64            // of winItems: the panel's tree only changes when this does
	excluded  []scan.Excluded   // Windows-side auto-exclusions
	reports   map[string]*protocol.TreeReport
	reportAt  map[string]time.Time         // when each report's exclusions were first seen
	winExclAt time.Time                    // when the Windows exclusions last changed
	caseLost  map[string]map[string]string // device → path refused for a case collision → the Windows path
	moves     map[string]*index.Entry      // during a push: content hash → local file the batch deletes
	lastScan  time.Time
	policy    string // conflict policy: "" (ask) or PolicyNewestWins
	paused    bool   // the sync API refuses this folder; scans go on
	name      string // display name; "" = the ID
}

// PolicyNewestWins resolves conflicts automatically in favour of the newer version.
const PolicyNewestWins = "newest-wins"

// bothSides are the auto-exclusion reasons that take a path out of the
// scope on every side (docs/requirements.md §6). Invalid names only exist
// on Linux anyway; unreadable paths are a local, passing problem.
var bothSides = map[string]bool{"symlink": true, "junction": true, "special-file": true}

func (f *folder) setRules(lines []string) error {
	if _, err := rules.Compile(lines); err != nil {
		return err
	}
	f.rulesSrc = append([]string(nil), lines...)
	f.rebuildScopeLocked()
	return nil
}

// rebuildScopeLocked recomputes the scope from rules and exclusions and
// reports whether it changed.
func (f *folder) rebuildScopeLocked() bool {
	set := map[string]bool{}
	for _, m := range f.manual {
		set[m.Path] = true
	}
	for _, x := range f.excluded {
		if bothSides[x.Reason] {
			set[x.Path] = true
		}
	}
	for _, r := range f.reports {
		for _, x := range r.Excluded {
			if bothSides[x.Reason] {
				set[x.Path] = true
			}
		}
	}
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	sc, err := rules.NewScope(f.rulesSrc, paths)
	if err != nil {
		return false // rules were validated by setRules
	}
	h := sha256.Sum256([]byte(strings.Join(f.rulesSrc, "\n") + "\x00" + strings.Join(paths, "\n")))
	hash := hex.EncodeToString(h[:8])
	changed := hash != f.rulesHash
	f.scope, f.exclPaths, f.rulesHash = sc, paths, hash
	return changed
}

// notifyLocked wakes long-polling clients. Call with f.mu held.
func (f *folder) notifyLocked() {
	f.nudge++
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *folder) info() protocol.Folder {
	f.mu.Lock()
	defer f.mu.Unlock()
	var seq uint64
	_ = f.store.View(func(tx *index.Tx) error { seq = tx.Seq(); return nil })
	return protocol.Folder{ID: f.id, Rules: f.rulesSrc, Excluded: f.exclPaths, RulesHash: f.rulesHash, Seq: seq}
}

// Scan rescans the Windows root, then retries pending client versions.
func (f *folder) Scan(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scanLocked(ctx)
}

func (f *folder) scanLocked(ctx context.Context) error {
	res, err := scan.Scan(ctx, f.store, scan.Options{Root: f.side.Root, Device: f.side.Device, Scope: f.scope})
	if err != nil {
		return err
	}
	f.noteNewExclusions("Windows", f.excluded, res.Excluded)
	if !sameExcluded(f.excluded, res.Excluded) {
		f.winExclAt = time.Now()
	}
	f.excluded = res.Excluded
	f.winItems = res.All
	f.winHash = itemsHash(res.All)
	f.lastScan = time.Now()
	changed := res.Changed > 0
	if f.rebuildScopeLocked() {
		changed = true
	}
	if c, err := f.retryPendingLocked(); err != nil {
		return err
	} else if c {
		changed = true
	}
	if changed {
		f.log.Info("scan", "folder", f.id, "changed", res.Changed, "hashed", res.Hashed, "excluded", len(res.Excluded))
		f.notifyLocked()
	}
	return nil
}

// receive handles one client entry. It returns whether the index changed,
// whether content is needed, and whether a conflict was recorded.
func (f *folder) receiveLocked(tx *index.Tx, r *index.Entry, device string) (changed, need, conflict bool, err error) {
	l, err := tx.Get(r.Path)
	if err != nil {
		return
	}
	switch protocol.Decide(l, r) {
	case protocol.ActNone:
		return
	case protocol.ActMerge:
		return true, false, false, f.side.Merge(tx, l, r)
	case protocol.ActConflict:
		return false, false, true, f.recordConflict(tx, l, r, device)
	}
	var expect fsx.Expect
	need, expect, err = f.side.Apply(tx, l, r)
	if err == nil && need {
		// Renamed or moved on the client: move the file here too.
		if src := f.moves[r.Hash]; src != nil {
			if moved, merr := f.side.Move(tx, src, r, expect); merr != nil {
				return false, false, false, merr
			} else if moved {
				delete(f.moves, r.Hash)
				f.event(Event{Kind: "sync", ConnectionID: device, Path: r.Path, Code: "moved", Params: map[string]any{"from": src.Path}})
				return true, false, false, f.dropPending(tx, r.Path)
			}
		}
	}
	if err == nil && r.Deleted && l != nil && !l.Deleted {
		f.event(Event{Kind: "delete", ConnectionID: device, Path: r.Path, Code: "deletedWindows"})
	}
	if errors.Is(err, fsx.ErrChanged) {
		// The Windows file changed since the last scan. Keep the client
		// version pending; the next scan decides (usually a conflict).
		return false, false, false, f.putPending(tx, r, device)
	}
	if err != nil {
		return
	}
	if need {
		return false, true, false, f.putPending(tx, r, device)
	}
	return true, false, false, f.dropPending(tx, r.Path)
}

func (f *folder) recordConflict(tx *index.Tx, l, r *index.Entry, device string) error {
	b, err := tx.Bucket(bConflicts)
	if err != nil {
		return err
	}
	c := protocol.Conflict{Path: r.Path, Device: device, Kind: protocol.ConflictKind(l, r), Server: *l, Client: *r, Detected: time.Now()}
	if old := b.Get([]byte(r.Path)); old != nil {
		var o protocol.Conflict
		if json.Unmarshal(old, &o) == nil {
			c.Detected = o.Detected
		}
	} else {
		f.log.Warn("conflict", "folder", f.id, "path", r.Path, "client", device, "kind", c.Kind)
		code := "concurrentEdit"
		if c.Kind == "edit-delete" {
			code = "editDelete"
		}
		f.event(Event{Kind: "conflict", Level: "warn", ConnectionID: device, Path: r.Path, Code: code})
	}
	v, _ := json.Marshal(c)
	if err := f.dropPending(tx, r.Path); err != nil {
		return err
	}
	if err := b.Put([]byte(r.Path), v); err != nil {
		return err
	}
	if f.policy == PolicyNewestWins {
		// No admin needed: the newer version wins; an edit beats a delete.
		use := protocol.UseWindows
		switch {
		case l.Deleted && !r.Deleted:
			use = protocol.UseClient
		case r.Deleted && !l.Deleted:
			use = protocol.UseWindows
		case r.ModTime > l.ModTime:
			use = protocol.UseClient
		}
		return f.resolveTx(tx, r.Path, use, false)
	}
	return nil
}

func (f *folder) putPending(tx *index.Tx, r *index.Entry, device string) error {
	return f.putPendingFrom(tx, r, device, "")
}

func (f *folder) putPendingFrom(tx *index.Tx, r *index.Entry, device, source string) error {
	b, err := tx.Bucket(bPending)
	if err != nil {
		return err
	}
	if source == "" {
		// Re-deciding a pending version (after a scan) keeps where it is uploaded from.
		var old pending
		if v := b.Get([]byte(r.Path)); v != nil && json.Unmarshal(v, &old) == nil && old.Device == device {
			source = old.Source
		}
	}
	v, _ := json.Marshal(pending{Entry: *r, Device: device, Source: source})
	return b.Put([]byte(r.Path), v)
}

func (f *folder) dropPending(tx *index.Tx, path string) error {
	b, err := tx.Bucket(bPending)
	if err != nil {
		return err
	}
	return b.Delete([]byte(path))
}

func (f *folder) getPending(tx *index.Tx, path string) (*pending, error) {
	b, err := tx.Bucket(bPending)
	if err != nil || b == nil {
		return nil, err
	}
	v := b.Get([]byte(path))
	if v == nil {
		return nil, nil
	}
	var p pending
	return &p, json.Unmarshal(v, &p)
}

// retryPendingLocked re-decides every pending client version against the
// current index (after a scan, a resolution or a push).
func (f *folder) retryPendingLocked() (changed bool, err error) {
	err = f.store.Update(func(tx *index.Tx) error {
		b, err := tx.Bucket(bPending)
		if err != nil {
			return err
		}
		var all []pending
		if err := b.ForEach(func(_, v []byte) error {
			var p pending
			if err := json.Unmarshal(v, &p); err != nil {
				return err
			}
			all = append(all, p)
			return nil
		}); err != nil {
			return err
		}
		for i := range all {
			p := &all[i]
			if !f.scope.InScope(p.Entry.Path, p.Entry.Dir) {
				if err := f.dropPending(tx, p.Entry.Path); err != nil {
					return err
				}
				continue
			}
			l, err := tx.Get(p.Entry.Path)
			if err != nil {
				return err
			}
			if protocol.Decide(l, &p.Entry) == protocol.ActNone {
				if err := f.dropPending(tx, p.Entry.Path); err != nil {
					return err
				}
				continue
			}
			c, _, _, err := f.receiveLocked(tx, &p.Entry, p.Device)
			if err != nil && !errors.Is(err, fsx.ErrNotEmpty) {
				f.log.Warn("pending", "folder", f.id, "path", p.Entry.Path, "err", err)
				continue
			}
			changed = changed || c
		}
		return nil
	})
	return
}

// push accepts a batch of client entries.
func (f *folder) push(device string, entries []index.Entry) (protocol.PushResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var res protocol.PushResult
	changed := false
	// Paths this batch deletes: a case-only rename deletes the old name.
	going := map[string]bool{}
	for _, e := range entries {
		if e.Deleted {
			going[strings.ToLower(e.Path)] = true
		}
	}
	cc := newCaseCheck(f.side.Root, going)
	f.moves = map[string]*index.Entry{}
	defer func() { f.moves = nil }()
	if f.caseLost == nil {
		f.caseLost = map[string]map[string]string{}
	}
	err := f.store.Update(func(tx *index.Tx) error {
		// Files this batch deletes whose content reappears in it: moves.
		for _, t := range entries {
			if !t.Deleted || t.Dir {
				continue
			}
			if l, _ := tx.Get(t.Path); l != nil && !l.Deleted && !l.Dir && l.Hash != "" && protocol.Decide(l, &t) == protocol.ActApply {
				f.moves[l.Hash] = l
			}
		}
		for i := range entries {
			r := &entries[i]
			r.Seq = 0
			if err := fsx.CheckRel(r.Path); err != nil {
				res.Rejected = append(res.Rejected, protocol.Rejected{Path: r.Path, Reason: err.Error()})
				continue
			}
			if !f.scope.InScope(r.Path, r.Dir) {
				res.Rejected = append(res.Rejected, protocol.Rejected{Path: r.Path, Reason: "out-of-scope"})
				continue
			}
			if !r.Deleted {
				if l, _ := tx.Get(r.Path); l == nil || l.Deleted {
					if other := cc.collides(r.Path); other != "" {
						res.Rejected = append(res.Rejected, protocol.Rejected{Path: r.Path, Reason: protocol.ReasonCaseCollision, Other: other})
						if f.caseLost[device] == nil {
							f.caseLost[device] = map[string]string{}
						}
						f.caseLost[device][r.Path] = other
						continue
					}
				}
			}
			delete(f.caseLost[device], r.Path)
			c, _, conflict, err := f.receiveLocked(tx, r, device)
			if err != nil {
				f.log.Warn("push", "folder", f.id, "path", r.Path, "err", err)
				continue
			}
			changed = changed || c
			if conflict {
				res.Conflicts = append(res.Conflicts, r.Path)
			}
		}
		// Every pending version of this client, from this push or earlier ones.
		b, err := tx.Bucket(bPending)
		if err != nil {
			return err
		}
		return b.ForEach(func(_, v []byte) error {
			var p pending
			if err := json.Unmarshal(v, &p); err != nil {
				return err
			}
			if p.Device == device && !p.Entry.Deleted && !p.Entry.Dir {
				n := protocol.Need{Path: p.Entry.Path, Hash: p.Entry.Hash, Source: p.Source}
				// Large file with an older version here: ask only for the changed blocks.
				if l, _ := tx.Get(p.Entry.Path); p.Source == "" && p.Entry.BlockSize > 0 && l != nil && !l.Deleted && !l.Dir &&
					l.BlockSize == p.Entry.BlockSize && len(l.Blocks) > 0 {
					if m := fsx.Missing(p.Entry.Blocks, fsx.Plan(l.Blocks, l.BlockSize)); len(m) < len(p.Entry.Blocks) {
						n.Delta, n.Missing = true, m
					}
				}
				res.Need = append(res.Need, n)
			}
			return nil
		})
	})
	if changed {
		f.notifyLocked()
	}
	return res, err
}

var errStale = errors.New("not expected: the server no longer needs this version")

// upload commits content the server asked for. The body is received into a
// temp file before taking the lock.
func (f *folder) upload(device, path, hash, tmp string) error {
	errs := f.commitUploads(device, []Received{{Path: path, Hash: hash, Tmp: tmp}})
	if errs[0] == nil {
		f.log.Info("received", "folder", f.id, "path", path, "from", device)
		f.event(Event{Kind: "sync", ConnectionID: device, Path: path, Code: "received"})
	}
	return errs[0]
}

// Received is uploaded content waiting in a temp file to be committed.
type Received struct {
	Path, Hash, Tmp string
}

// commitUploads commits uploaded files in one index transaction (one disk
// flush for the whole batch). Each file succeeds or fails on its own.
func (f *folder) commitUploads(device string, items []Received) []error {
	f.mu.Lock()
	defer f.mu.Unlock()
	errs := make([]error, len(items))
	ok := 0
	err := f.store.Update(func(tx *index.Tx) error {
		for i, it := range items {
			errs[i] = f.commitOne(tx, device, it)
			if errs[i] == nil {
				ok++
			}
		}
		return nil
	})
	if err != nil {
		for i := range errs {
			if errs[i] == nil {
				errs[i] = err
			}
		}
	}
	rescan := false
	for _, e := range errs {
		rescan = rescan || errors.Is(e, fsx.ErrChanged)
	}
	if rescan {
		go f.Scan(context.Background()) //nolint:errcheck // the next push retries
	}
	if ok > 0 {
		f.notifyLocked()
	}
	return errs
}

func (f *folder) commitOne(tx *index.Tx, device string, it Received) error {
	committed := false
	defer func() {
		if !committed {
			fsx.Discard(it.Tmp)
		}
	}()
	p, err := f.getPending(tx, it.Path)
	if err != nil {
		return err
	}
	if p == nil || p.Device != device || p.Entry.Hash != it.Hash {
		return errStale
	}
	l, err := tx.Get(it.Path)
	if err != nil {
		return err
	}
	if protocol.Decide(l, &p.Entry) != protocol.ActApply {
		if err := f.dropPending(tx, it.Path); err != nil {
			return err
		}
		return errStale
	}
	need, expect, err := f.side.Apply(tx, l, &p.Entry)
	if err != nil {
		return err
	}
	if need {
		committed = true // Commit owns the temp file now
		if err := f.side.Commit(tx, &p.Entry, it.Tmp, expect); err != nil {
			return err
		}
	}
	return f.dropPending(tx, it.Path)
}

// page returns in-scope entries changed after since.
func (f *folder) page(since uint64) (protocol.IndexPage, error) {
	f.mu.Lock()
	rs := f.scope
	f.mu.Unlock()
	pg := protocol.IndexPage{Entries: []index.Entry{}}
	err := f.store.View(func(tx *index.Tx) error {
		pg.Next = tx.Seq()
		n := 0
		return tx.Since(since, func(e *index.Entry) (bool, error) {
			if n >= protocol.PageSize {
				pg.More = true
				return false, nil
			}
			n++
			pg.Next = e.Seq
			if rs.InScope(e.Path, e.Dir) {
				pg.Entries = append(pg.Entries, *e)
			}
			return true, nil
		})
	})
	if !pg.More {
		_ = f.store.View(func(tx *index.Tx) error { pg.Next = tx.Seq(); return nil })
	}
	return pg, err
}

// wait blocks until the index passes since or the scope differs from
// rulesHash, the timeout ends, or ctx is done.
func (f *folder) wait(ctx context.Context, since uint64, rulesHash string, nudge uint64, timeout time.Duration) protocol.WaitResult {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		f.mu.Lock()
		ch := f.changed
		res := protocol.WaitResult{RulesHash: f.rulesHash, Nudge: f.nudge}
		_ = f.store.View(func(tx *index.Tx) error { res.Seq = tx.Seq(); return nil })
		f.mu.Unlock()
		if res.Seq > since || (rulesHash != "" && res.RulesHash != rulesHash) || (nudge != 0 && res.Nudge != nudge) {
			return res
		}
		select {
		case <-ch:
		case <-t.C:
			return res
		case <-ctx.Done():
			return res
		}
	}
}

// entryForDownload returns the current entry if it still has the hash the
// client asked for and the disk still matches it.
func (f *folder) entryForDownload(path, hash string) (*index.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var e *index.Entry
	err := f.store.View(func(tx *index.Tx) (err error) { e, err = tx.Get(path); return })
	if err != nil {
		return nil, err
	}
	if e == nil || e.Deleted || e.Dir || e.Hash != hash || !f.scope.InScope(path, false) {
		return nil, errStale
	}
	if !syncer.ExpectOf(e).Matches(fsx.Abs(f.side.Root, path)) {
		go f.Scan(context.Background()) //nolint:errcheck
		return nil, errStale
	}
	return e, nil
}

func (f *folder) conflicts() ([]protocol.Conflict, error) {
	out := []protocol.Conflict{}
	err := f.store.View(func(tx *index.Tx) error {
		b, err := tx.Bucket(bConflicts)
		if err != nil || b == nil {
			return err
		}
		return b.ForEach(func(_, v []byte) error {
			var c protocol.Conflict
			if err := json.Unmarshal(v, &c); err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// resolve applies the admin's decision. The chosen version gets a version
// vector that descends from both sides, so it syncs right away.
func (f *folder) resolve(path, use string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := f.store.Update(func(tx *index.Tx) error { return f.resolveTx(tx, path, use, true) })
	if err == nil {
		f.notifyLocked()
	}
	return err
}

// resolveTx applies a decision for the conflict at path; admin says whether
// it was the admin (or the folder's "newest wins" policy).
func (f *folder) resolveTx(tx *index.Tx, path, use string, admin bool) error {
	{
		b, err := tx.Bucket(bConflicts)
		if err != nil {
			return err
		}
		v := b.Get([]byte(path))
		if v == nil {
			return fmt.Errorf("no conflict at %q", path)
		}
		var c protocol.Conflict
		if err := json.Unmarshal(v, &c); err != nil {
			return err
		}
		l, err := tx.Get(path)
		if err != nil {
			return err
		}
		if l == nil {
			return fmt.Errorf("no index entry for %q", path)
		}
		merged := l.Version.Merge(c.Client.Version).Bump(f.side.Device)
		switch use {
		case protocol.UseWindows:
			e := *l
			e.Version = merged
			if err := tx.Put(&e); err != nil {
				return err
			}
		case protocol.UseClient:
			r := c.Client
			r.Version = merged
			if _, _, _, err := f.receiveLocked(tx, &r, c.Device); err != nil {
				return err
			}
		case protocol.KeepBoth:
			name := c.Device
			if f.srv != nil {
				name = f.srv.reg.name(c.Device)
			}
			if err := f.keepBothLocked(tx, &c, l, name); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown choice %q", use)
		}
		f.log.Info("resolved", "folder", f.id, "path", path, "use", use, "admin", admin)
		code := map[string]string{protocol.UseWindows: "resolvedWindows", protocol.UseClient: "resolvedWsl", protocol.KeepBoth: "resolvedBoth"}[use]
		if !admin {
			code = map[string]string{protocol.UseWindows: "autoWindows", protocol.UseClient: "autoWsl"}[use]
		}
		f.event(Event{Kind: "resolve", Admin: admin, ConnectionID: c.Device, Path: path, Code: code})
		return b.Delete([]byte(path))
	}
}

func (f *folder) updateRules(ctx context.Context, lines []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.setRules(lines); err != nil {
		return err
	}
	f.log.Info("rules changed", "folder", f.id, "rules", len(lines))
	defer f.notifyLocked() // clients long-polling with the old rules hash wake up
	return f.scanLocked(ctx)
}

// deltaBase returns what a delta upload of path builds on: the pending
// version and the local old file (still as the index says).
func (f *folder) deltaBase(device, path, hash string) (*index.Entry, *index.Entry, *os.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p *pending
	var l *index.Entry
	err := f.store.View(func(tx *index.Tx) (err error) {
		if p, err = f.getPending(tx, path); err != nil {
			return err
		}
		l, err = tx.Get(path)
		return err
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if p == nil || p.Device != device || p.Entry.Hash != hash || l == nil || l.Deleted || l.Dir || l.BlockSize != p.Entry.BlockSize {
		return nil, nil, nil, errStale
	}
	abs := fsx.Abs(f.side.Root, path)
	if !syncer.ExpectOf(l).Matches(abs) {
		return nil, nil, nil, fsx.ErrChanged
	}
	old, err := os.Open(abs)
	if err != nil {
		return nil, nil, nil, err
	}
	return &p.Entry, l, old, nil
}
