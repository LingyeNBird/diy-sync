// Package scan walks a sync root and brings its index up to date: new and
// changed files get a new version, missing ones get a tombstone.
package scan

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"fdsync/internal/fsx"
	"fdsync/internal/index"
	"fdsync/internal/rules"
	"fdsync/internal/vv"
)

// Options configures a scan.
type Options struct {
	Root   string
	Device string       // this side's device ID, for version vectors
	Scope  *rules.Scope // nil = everything in scope
	// TrackMode records permission bits as part of a version (Linux clients).
	// The Windows server keeps whatever mode the index already has.
	TrackMode bool
	// CheckWindowsNames excludes names Windows can't store (Linux clients).
	CheckWindowsNames bool
}

// Excluded is a path the scan left out, with the reason
// (symlink, special-file, invalid-name, reserved-name, unreadable).
type Excluded struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Other  string `json:"other,omitempty"` // case-collision: the path that won
}

// Item is one path seen on disk, in scope or not, as metadata only. The
// panel's two-sided tree is built from these; content never leaves a side
// unless the path is in scope (or an admin opens it in the panel).
type Item struct {
	Path    string `json:"p"`
	Dir     bool   `json:"d,omitempty"`
	Link    bool   `json:"l,omitempty"` // symlink or junction
	Size    int64  `json:"s,omitempty"`
	ModTime int64  `json:"m,omitempty"` // Unix ns
}

// Result summarizes a scan.
type Result struct {
	Changed  int        // entries with a new version (including tombstones)
	Hashed   int        // files whose content was hashed
	Moved    int        // files that look renamed or moved (same content, new path)
	Excluded []Excluded // sorted by path
	All      []Item     // everything seen, in walk order (parents first)
}

type seen struct {
	dir     bool
	size    int64
	modTime int64
	mode    uint32
}

// Scan updates store from the disk under opts.Root.
func Scan(ctx context.Context, store *index.Store, opts Options) (Result, error) {
	var res Result
	found := map[string]seen{}
	var excluded []Excluded
	var all []Item
	note := func(rel string, d fs.DirEntry, link bool) {
		it := Item{Path: rel, Dir: d.IsDir(), Link: link}
		if fi, err := d.Info(); err == nil && !it.Dir {
			it.Size, it.ModTime = fi.Size(), fi.ModTime().UnixNano()
		}
		all = append(all, it)
	}

	err := filepath.WalkDir(opts.Root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p == opts.Root {
			return err
		}
		relOS, _ := filepath.Rel(opts.Root, p)
		rel := filepath.ToSlash(relOS)
		if err != nil {
			excluded = append(excluded, Excluded{Path: rel, Reason: "unreadable"})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if fsx.Ignored(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if opts.CheckWindowsNames {
			if why := fsx.WindowsNameProblem(d.Name()); why != "" {
				excluded = append(excluded, Excluded{Path: rel, Reason: why})
				note(rel, d, false)
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		t := d.Type()
		switch {
		case t&fs.ModeSymlink != 0:
			excluded = append(excluded, Excluded{Path: rel, Reason: "symlink"})
			note(rel, d, true)
			return nil
		case runtime.GOOS == "windows" && t&fs.ModeIrregular != 0:
			// Junctions and other reparse points (Go reports them as irregular).
			excluded = append(excluded, Excluded{Path: rel, Reason: "junction"})
			note(rel, d, true)
			return nil
		case !t.IsDir() && !t.IsRegular():
			// FIFOs, sockets, devices.
			excluded = append(excluded, Excluded{Path: rel, Reason: "special-file"})
			note(rel, d, false)
			return nil
		}
		note(rel, d, false)
		// Folders outside the scope are still walked: a later rule may
		// include something inside them.
		if !opts.Scope.InScope(rel, d.IsDir()) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			excluded = append(excluded, Excluded{Path: rel, Reason: "unreadable"})
			return nil
		}
		found[rel] = seen{dir: d.IsDir(), size: fi.Size(), modTime: fi.ModTime().UnixNano(), mode: uint32(fi.Mode().Perm())}
		return nil
	})
	if err != nil {
		return res, err
	}
	if opts.CheckWindowsNames {
		// A case-sensitive side syncing with Windows: names that differ only
		// in case can't both exist there.
		lost, err := caseCollisions(store, found)
		if err != nil {
			return res, err
		}
		excluded = append(excluded, lost...)
	}
	sort.Slice(excluded, func(i, j int) bool { return excluded[i].Path < excluded[j].Path })
	res.Excluded = excluded
	res.All = all

	// Work out what changed, without holding a write transaction while hashing.
	type change struct {
		rel  string
		s    seen
		hash string
		hs   fsx.Hashes
	}
	var toHash, dirs []change
	var gone []*index.Entry
	err = store.View(func(tx *index.Tx) error {
		for rel, s := range found {
			e, err := tx.Get(rel)
			if err != nil {
				return err
			}
			if s.dir {
				if e == nil || e.Deleted || !e.Dir || (opts.TrackMode && e.Mode != s.mode) {
					dirs = append(dirs, change{rel: rel, s: s})
				}
				continue
			}
			if e != nil && !e.Deleted && !e.Dir && e.Size == s.size && e.ModTime == s.modTime &&
				(!opts.TrackMode || e.Mode == s.mode) {
				continue // unchanged
			}
			toHash = append(toHash, change{rel: rel, s: s})
		}
		return tx.ForEach(func(e *index.Entry) error {
			if e.Deleted || fsx.Ignored(e.Path) {
				return nil
			}
			if _, ok := found[e.Path]; ok {
				return nil
			}
			// Out of scope now: cancelling synchronization never deletes anything.
			if !opts.Scope.InScope(e.Path, e.Dir) || underExcluded(e.Path, excluded) {
				return nil
			}
			gone = append(gone, e)
			return nil
		})
	})
	if err != nil {
		return res, err
	}

	for i := range toHash {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		c := &toHash[i]
		p := fsx.Abs(opts.Root, c.rel)
		hs, err := fsx.HashFileBlocks(p)
		if err != nil {
			continue // vanished or locked; the next scan sees it
		}
		res.Hashed++
		// Skip files that changed while being hashed; the next scan gets them.
		if fi, err := os.Lstat(p); err != nil || fi.Size() != c.s.size || fi.ModTime().UnixNano() != c.s.modTime {
			continue
		}
		c.hash, c.hs = hs.Hash, hs
	}

	// Tombstones children-first, so a peer deletes a folder's files before the folder.
	sort.Slice(gone, func(i, j int) bool {
		if a, b := gone[i].Depth(), gone[j].Depth(); a != b {
			return a > b
		}
		return gone[i].Path < gone[j].Path
	})
	// New folders parents-first.
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].rel < dirs[j].rel })

	// Renames and moves: a vanished file whose content reappears elsewhere.
	// Its tombstone is written right after the new entry, so both reach
	// the other side in the same batch and it can move its copy instead of
	// transferring the content again.
	goneByHash := map[string][]*index.Entry{}
	for _, e := range gone {
		if !e.Dir && e.Hash != "" {
			goneByHash[e.Hash] = append(goneByHash[e.Hash], e)
		}
	}
	paired := map[string]bool{}
	tombstone := func(tx *index.Tx, e *index.Entry) error {
		// A tombstone's ModTime is when the delete was seen (for purging after 30 days).
		t := &index.Entry{Path: e.Path, Dir: e.Dir, Deleted: true, Mode: e.Mode, ModTime: time.Now().UnixNano(), Version: e.Version.Bump(opts.Device)}
		res.Changed++
		return tx.Put(t)
	}

	err = store.Update(func(tx *index.Tx) error {
		for _, c := range dirs {
			e, err := tx.Get(c.rel)
			if err != nil {
				return err
			}
			n := &index.Entry{Path: c.rel, Dir: true, ModTime: c.s.modTime, Mode: mode(opts, e, c.s, 0o755)}
			n.Version = version(e).Bump(opts.Device)
			if err := tx.Put(n); err != nil {
				return err
			}
			res.Changed++
		}
		for _, c := range toHash {
			if c.hash == "" {
				continue
			}
			e, err := tx.Get(c.rel)
			if err != nil {
				return err
			}
			n := &index.Entry{Path: c.rel, Size: c.s.size, ModTime: c.s.modTime, Mode: mode(opts, e, c.s, 0o644), Hash: c.hash,
				BlockSize: c.hs.BlockSize, Blocks: c.hs.Blocks}
			if e != nil && !e.Deleted && !e.Dir && e.Hash == c.hash && e.Mode == n.Mode {
				// Same content, only the timestamp moved: record it locally.
				n.Version = e.Version
				if err := tx.PutLocal(n); err != nil {
					return err
				}
				continue
			}
			n.Version = version(e).Bump(opts.Device)
			if err := tx.Put(n); err != nil {
				return err
			}
			res.Changed++
			if cands := goneByHash[c.hash]; len(cands) > 0 && (e == nil || e.Deleted) {
				src := cands[0]
				goneByHash[c.hash] = cands[1:]
				paired[src.Path] = true
				res.Moved++
				if err := tombstone(tx, src); err != nil {
					return err
				}
			}
		}
		for _, e := range gone {
			if paired[e.Path] {
				continue
			}
			if err := tombstone(tx, e); err != nil {
				return err
			}
		}
		return nil
	})
	return res, err
}

func version(e *index.Entry) vv.Vector {
	if e == nil {
		return nil
	}
	return e.Version
}

func mode(opts Options, e *index.Entry, s seen, def uint32) uint32 {
	if opts.TrackMode {
		return s.mode
	}
	if e != nil && e.Mode != 0 {
		return e.Mode
	}
	return def
}

// underExcluded reports whether p is an excluded path or lies under one, so
// a path that turned into a symlink (or became unreadable) isn't treated as
// deleted.
func underExcluded(p string, ex []Excluded) bool {
	for d := p; d != "." && d != "/" && d != ""; d = path.Dir(d) {
		i := sort.Search(len(ex), func(i int) bool { return ex[i].Path >= d })
		if i < len(ex) && ex[i].Path == d {
			return true
		}
		if !strings.Contains(d, "/") {
			break
		}
	}
	return false
}

// caseCollisions finds in-scope paths that differ only in case and keeps one
// of each group (docs/requirements.md §6): the one already synced wins, then
// the older one, then the first in byte order. The others, and everything
// under them, are removed from found and returned as exclusions.
func caseCollisions(store *index.Store, found map[string]seen) ([]Excluded, error) {
	groups := map[string][]string{}
	for p := range found {
		k := strings.ToLower(p)
		groups[k] = append(groups[k], p)
	}
	var keys []string
	for k, g := range groups {
		if len(g) > 1 {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	// Parents before children, so a lost folder takes its contents along.
	sort.Slice(keys, func(i, j int) bool {
		if a, b := strings.Count(keys[i], "/"), strings.Count(keys[j], "/"); a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	var out []Excluded
	lostDirs := map[string]bool{}
	under := func(p string) bool {
		for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
			if lostDirs[d] {
				return true
			}
		}
		return false
	}
	err := store.View(func(tx *index.Tx) error {
		for _, k := range keys {
			var g []string
			for _, p := range groups[k] {
				if !under(p) {
					g = append(g, p)
				}
			}
			if len(g) < 2 {
				continue
			}
			known := func(p string) bool {
				e, _ := tx.Get(p)
				return e != nil && !e.Deleted
			}
			sort.Slice(g, func(i, j int) bool {
				ki, kj := known(g[i]), known(g[j])
				if ki != kj {
					return ki
				}
				if mi, mj := found[g[i]].modTime, found[g[j]].modTime; mi != mj {
					return mi < mj
				}
				return g[i] < g[j]
			})
			for _, p := range g[1:] {
				out = append(out, Excluded{Path: p, Reason: "case-collision", Other: g[0]})
				if found[p].dir {
					lostDirs[p] = true
				}
			}
		}
		return nil
	})
	for p := range found {
		for _, x := range out {
			if p == x.Path || strings.HasPrefix(p, x.Path+"/") {
				delete(found, p)
				break
			}
		}
	}
	return out, err
}
