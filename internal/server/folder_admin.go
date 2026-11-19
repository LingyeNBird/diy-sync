package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"fdsync/internal/fsx"
	"fdsync/internal/index"
	"fdsync/internal/protocol"
	"fdsync/internal/scan"
	"fdsync/internal/vv"
)

// ManualExclusion is a path the admin excluded from sync in the panel.
type ManualExclusion struct {
	Path  string    `json:"path"`
	Since time.Time `json:"since"`
}

// noteNewExclusions logs auto-exclusions that weren't there before.
func (f *folder) noteNewExclusions(origin string, before, after []scan.Excluded) {
	old := map[string]bool{}
	for _, x := range before {
		old[x.Path] = true
	}
	codes := map[string]string{"symlink": "exclSymlink", "junction": "exclJunction", "special-file": "exclSpecial",
		"invalid-name": "exclInvalid", "reserved-name": "exclReserved"}
	for _, x := range after {
		code := codes[x.Reason]
		if old[x.Path] || code == "" || f.srv == nil {
			continue
		}
		f.srv.ev.add(Event{Kind: "exclude", FolderID: f.id, Path: x.Path, Code: code, Params: map[string]any{"origin": origin, "name": path.Base(x.Path), "chars": badChars(path.Base(x.Path))}})
	}
}

func badChars(name string) string {
	var out []rune
	for _, r := range name {
		if strings.ContainsRune(`<>:"\|?*`, r) && !strings.ContainsRune(string(out), r) {
			out = append(out, r)
		}
	}
	return string(out)
}

func sameExcluded(a, b []scan.Excluded) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// setTreeReport stores a client's whole-folder metadata.
func (f *folder) setTreeReport(device, name string, rep *protocol.TreeReport) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reports == nil {
		f.reports = map[string]*protocol.TreeReport{}
	}
	var before []scan.Excluded
	if old := f.reports[device]; old != nil {
		before = old.Excluded
	}
	f.noteNewExclusions(name, before, rep.Excluded)
	f.reports[device] = rep
	if f.reportAt == nil {
		f.reportAt = map[string]time.Time{}
	}
	if _, ok := f.reportAt[device]; !ok || len(before) != len(rep.Excluded) {
		f.reportAt[device] = time.Now()
	}
	if f.rebuildScopeLocked() {
		f.notifyLocked() // e.g. a new WSL symlink is now excluded on Windows too
	}
}

func (f *folder) treeHash(device string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.reports[device]; r != nil {
		return r.Hash
	}
	return ""
}

// setManual adds or cancels manual exclusions, then rescans.
func (f *folder) setManual(ctx context.Context, paths []string, add bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := map[string]ManualExclusion{}
	for _, m := range f.manual {
		cur[m.Path] = m
	}
	for _, p := range paths {
		if err := fsx.CheckRel(p); err != nil {
			return err
		}
		if add {
			if _, ok := cur[p]; !ok {
				cur[p] = ManualExclusion{Path: p, Since: time.Now()}
			}
		} else {
			delete(cur, p)
		}
	}
	f.manual = f.manual[:0]
	for _, m := range cur {
		f.manual = append(f.manual, m)
	}
	sort.Slice(f.manual, func(i, j int) bool { return f.manual[i].Path < f.manual[j].Path })
	f.rebuildScopeLocked()
	defer f.notifyLocked()
	return f.scanLocked(ctx)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// keepBothLocked resolves a conflict by keeping the Windows version at the
// path and saving the client's version as name.wsl-<client>.ext.
func (f *folder) keepBothLocked(tx *index.Tx, c *protocol.Conflict, l *index.Entry, clientName string) error {
	if c.Client.Deleted || c.Client.Dir {
		return errors.New("keep both needs a file on the client side")
	}
	// The Windows version wins at the original path.
	e := *l
	e.Version = l.Version.Merge(c.Client.Version).Bump(f.side.Device)
	if err := tx.Put(&e); err != nil {
		return err
	}
	dir, base := path.Split(c.Path)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	tag := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(clientName), "-"), "-")
	if tag == "" {
		tag = "client"
	}
	var target string
	for i := 1; ; i++ {
		suffix := ""
		if i > 1 {
			suffix = fmt.Sprintf("-%d", i)
		}
		target = dir + stem + ".wsl-" + tag + suffix + ext
		existing, err := tx.Get(target)
		if err != nil {
			return err
		}
		_, statErr := os.Lstat(fsx.Abs(f.side.Root, target))
		if (existing == nil || existing.Deleted) && errors.Is(statErr, fs.ErrNotExist) {
			break
		}
	}
	// A new file for everyone: the client uploads it from the original path.
	r := c.Client
	r.Path = target
	r.Version = vv.Vector{}.Bump(f.side.Device)
	return f.putPendingFrom(tx, &r, c.Device, c.Path)
}

// deleteHere deletes paths on the Windows side (an admin action). Files go
// to the trash; folders are emptied file by file, then deleted.
func (f *folder) deleteHere(paths []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var errs []error
	for _, p := range paths {
		if err := fsx.CheckRel(p); err != nil {
			errs = append(errs, err)
			continue
		}
		if f.scope.InScope(p, false) || f.scope.InScope(p, true) {
			errs = append(errs, fmt.Errorf("%s is in scope; cancel synchronization first", p))
			continue
		}
		abs := fsx.Abs(f.side.Root, p)
		_ = filepath.WalkDir(abs, func(q string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				rel, _ := filepath.Rel(f.side.Root, q)
				_ = f.side.Trash.Put(f.side.Root, filepath.ToSlash(rel))
			}
			return nil
		})
		if err := os.RemoveAll(abs); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---------------------------------------------------------------- panel tree

// TreeNode is one item of a binding's merged tree, as the panel expects it
// (web/src/lib/types.ts).
type TreeNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	Kind     string      `json:"kind"` // dir | file | symlink
	Win      *NodeMeta   `json:"win,omitempty"`
	WSL      *NodeMeta   `json:"wsl,omitempty"`
	Children []*TreeNode `json:"children,omitempty"`
}

type NodeMeta struct {
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
}

// itemsHash fingerprints a folder listing (paths, kinds, sizes, times).
func itemsHash(items []scan.Item) uint64 {
	h := fnv.New64a()
	var b [8]byte
	for _, it := range items {
		h.Write([]byte(it.Path))
		binary.LittleEndian.PutUint64(b[:], uint64(it.Size)^uint64(it.ModTime)<<1)
		h.Write(b[:])
		if it.Dir {
			h.Write([]byte{1})
		}
		if it.Link {
			h.Write([]byte{2})
		}
	}
	return h.Sum64()
}

// treeVersion changes whenever tree(device) would return something else.
func (f *folder) treeVersion(device string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	rep := ""
	if r := f.reports[device]; r != nil {
		rep = r.Hash
	}
	return fmt.Sprintf("%x-%s", f.winHash, rep)
}

// tree merges the Windows folder with a client's report.
func (f *folder) tree(device string) []*TreeNode {
	f.mu.Lock()
	win := f.winItems
	var wsl []scan.Item
	if r := f.reports[device]; r != nil {
		wsl = r.Items
	}
	f.mu.Unlock()

	nodes := map[string]*TreeNode{}
	var roots []*TreeNode
	var node func(p string, dir, link bool) *TreeNode
	node = func(p string, dir, link bool) *TreeNode {
		if n := nodes[p]; n != nil {
			if dir {
				n.Kind = "dir"
			}
			return n
		}
		kind := "file"
		switch {
		case link:
			kind = "symlink"
		case dir:
			kind = "dir"
		}
		n := &TreeNode{Name: path.Base(p), Path: p, Kind: kind}
		nodes[p] = n
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			parent := node(p[:i], true, false)
			parent.Children = append(parent.Children, n)
		} else {
			roots = append(roots, n)
		}
		return n
	}
	meta := func(it scan.Item) *NodeMeta {
		m := &NodeMeta{Size: it.Size}
		if it.ModTime != 0 {
			m.MTime = time.Unix(0, it.ModTime)
		}
		return m
	}
	for _, it := range win {
		node(it.Path, it.Dir, it.Link).Win = meta(it)
	}
	for _, it := range wsl {
		node(it.Path, it.Dir, it.Link).WSL = meta(it)
	}
	var sortAll func([]*TreeNode)
	sortAll = func(ns []*TreeNode) {
		sort.Slice(ns, func(i, j int) bool {
			if (ns[i].Kind == "dir") != (ns[j].Kind == "dir") {
				return ns[i].Kind == "dir"
			}
			return strings.ToLower(ns[i].Name) < strings.ToLower(ns[j].Name)
		})
		for _, n := range ns {
			if n.Children != nil {
				sortAll(n.Children)
			}
		}
	}
	sortAll(roots)
	if roots == nil {
		roots = []*TreeNode{}
	}
	return roots
}

// panelExclusion is an auto or manual exclusion as the panel shows it.
type panelExclusion struct {
	Path   string            `json:"path"`
	Reason string            `json:"reason"`
	Params map[string]string `json:"params,omitempty"`
	Origin string            `json:"origin"`
	Since  time.Time         `json:"since"`
}

func (f *folder) exclusions(nameOf func(string) string) []panelExclusion {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []panelExclusion{}
	seen := map[string]bool{}
	addAuto := func(origin string, xs []scan.Excluded, since time.Time) {
		for _, x := range xs {
			if x.Reason == "unreadable" || seen[x.Path] {
				continue
			}
			seen[x.Path] = true
			params := map[string]string{"name": path.Base(x.Path), "chars": badChars(path.Base(x.Path)), "target": "", "other": x.Other}
			out = append(out, panelExclusion{Path: x.Path, Reason: x.Reason, Params: params, Origin: origin, Since: since})
		}
	}
	for _, m := range f.manual {
		seen[m.Path] = true
		out = append(out, panelExclusion{Path: m.Path, Reason: "manual", Origin: "admin", Since: m.Since})
	}
	addAuto("Windows", f.excluded, f.winExclAt)
	devs := make([]string, 0, len(f.reports))
	for d := range f.reports {
		devs = append(devs, d)
	}
	sort.Strings(devs)
	for _, d := range devs {
		addAuto(nameOf(d), f.reports[d].Excluded, f.reportAt[d])
	}
	for d, lost := range f.caseLost {
		for p, other := range lost {
			if !seen[p] {
				seen[p] = true
				out = append(out, panelExclusion{Path: p, Reason: "case-collision", Params: map[string]string{"other": other}, Origin: nameOf(d), Since: f.reportAt[d]})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// stats counts in-scope files and bytes on the Windows side.
func (f *folder) stats() (files int, bytes int64, pendingByDevice map[string]int) {
	pendingByDevice = map[string]int{}
	_ = f.store.View(func(tx *index.Tx) error {
		_ = tx.ForEach(func(e *index.Entry) error {
			if !e.Deleted && !e.Dir {
				files++
				bytes += e.Size
			}
			return nil
		})
		b, _ := tx.Bucket(bPending)
		if b != nil {
			_ = b.ForEach(func(_, v []byte) error {
				var p pending
				if json.Unmarshal(v, &p) == nil {
					pendingByDevice[p.Device]++
				}
				return nil
			})
		}
		return nil
	})
	return
}

// caseCheck finds paths that Windows already has in another case. Windows
// folders are case-insensitive: README.md would land on an existing
// Readme.md. Directory listings are cached for one push.
type caseCheck struct {
	root  string
	going map[string]bool // lower-case paths deleted in the same batch
	dirs  map[string]map[string]string
}

func newCaseCheck(root string, going map[string]bool) *caseCheck {
	return &caseCheck{root: root, going: going, dirs: map[string]map[string]string{}}
}

// collides returns the existing differently-cased path, or "".
func (c *caseCheck) collides(rel string) string {
	segs := strings.Split(rel, "/")
	actual := "" // the path as it is on disk, so far
	for _, seg := range segs {
		names := c.list(actual)
		name, ok := names[strings.ToLower(seg)]
		if !ok {
			return "" // nothing there yet: no collision below either
		}
		next := name
		if actual != "" {
			next = actual + "/" + name
		}
		if name != seg && !c.going[strings.ToLower(next)] {
			return next
		}
		actual = next
	}
	return ""
}

func (c *caseCheck) list(dir string) map[string]string {
	if m, ok := c.dirs[dir]; ok {
		return m
	}
	m := map[string]string{}
	ents, _ := os.ReadDir(fsx.Abs(c.root, dir))
	for _, e := range ents {
		m[strings.ToLower(e.Name())] = e.Name()
	}
	c.dirs[dir] = m
	return m
}

func (f *folder) isPaused() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused
}

// displayName is the folder's name in the panel.
func (f *folder) displayName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.name != "" {
		return f.name
	}
	return f.id
}

// setOptions changes the conflict policy, pause state and display name.
func (f *folder) setOptions(policy *string, paused *bool, name *string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != nil {
		f.name = *name
	}
	if policy != nil {
		f.policy = *policy
	}
	if paused != nil {
		f.paused = *paused
	}
	f.notifyLocked() // a resumed folder: clients sync at once
}
