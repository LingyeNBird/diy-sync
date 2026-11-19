package fsx

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TrashLimit is the most the trash may ever hold on one side, for all
// folders together. Settings can lower it (or turn the trash off), never
// raise it.
const TrashLimit = 50 << 20

// TrashStore keeps recent versions of files that sync overwrote or deleted,
// in one folder outside every sync root (the data directory). It is shared
// by all folders on this side and never holds more than its limit: every
// Put evicts the oldest entries right away.
type TrashStore struct {
	Dir        string
	MaxBytes   int64         // total; at most TrashLimit; 0 = trash off
	MaxFileLen int64         // larger files are deleted, not kept
	MaxAge     time.Duration // older entries go at the next Prune

	mu   sync.Mutex
	size int64   // bytes currently in Dir (as allocated on disk)
	list []batch // oldest first; read from disk once, then kept up to date
}

// OpenTrash opens the trash in dir. maxBytes is clamped to TrashLimit;
// 0 turns the trash off (overwritten and deleted files are just deleted).
func OpenTrash(dir string, maxBytes int64) (*TrashStore, error) {
	maxBytes = min(max(maxBytes, 0), TrashLimit)
	t := &TrashStore{Dir: dir, MaxBytes: maxBytes, MaxFileLen: min(16<<20, maxBytes), MaxAge: 7 * 24 * time.Hour}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if maxBytes == 0 {
		return t, os.RemoveAll(dir) // off: don't keep anything from before either
	}
	t.list = t.batches()
	for _, b := range t.list {
		t.size += b.size
	}
	return t, t.evictLocked(time.Time{})
}

// SetLimit changes the size limit (clamped to TrashLimit; 0 = off) and
// evicts what no longer fits.
func (t *TrashStore) SetLimit(maxBytes int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.MaxBytes = min(max(maxBytes, 0), TrashLimit)
	t.MaxFileLen = min(16<<20, t.MaxBytes)
	return t.evictLocked(time.Time{})
}

// For returns the trash of one folder; sub names it inside the store.
func (t *TrashStore) For(sub string) *Trash { return &Trash{store: t, sub: sub} }

// Size returns the bytes currently held.
func (t *TrashStore) Size() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.size
}

// Trash is one folder's view of a TrashStore. A nil *Trash deletes directly.
type Trash struct {
	store *TrashStore
	sub   string
}

const batchLayout = "20060102-150405.000"

// Put moves root/rel into the trash, or deletes it when the trash is off or
// the file is too large to keep. A missing file is not an error.
func (t *Trash) Put(root, rel string) error {
	p := Abs(root, rel)
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if t == nil || t.store == nil || t.store.MaxBytes == 0 || fi.Size() > t.store.MaxFileLen {
		return os.Remove(p)
	}
	s := t.store
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	bdir := filepath.Join(s.Dir, t.sub, now.Format(batchLayout))
	dst := filepath.Join(bdir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		os.Remove(dst) // same file twice within a millisecond: keep the newer
	}
	err = os.Rename(p, dst)
	if crossDevice(err) {
		// The trash is on another drive: copy, then delete the original.
		if err := copyFile(p, dst, fi); err != nil {
			os.Remove(dst)
			return err
		}
		err = os.Remove(p)
	}
	if err != nil {
		return err
	}
	n := onDisk(fi.Size())
	s.size += n
	if k := len(s.list); k > 0 && s.list[k-1].path == bdir {
		s.list[k-1].size += n
	} else {
		s.list = append(s.list, batch{path: bdir, size: n, when: now})
	}
	return s.evictLocked(time.Time{})
}

// Prune drops entries older than MaxAge (and enforces the size limit).
func (t *TrashStore) Prune() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.evictLocked(time.Now().Add(-t.MaxAge))
}

type batch struct {
	path string
	size int64
	when time.Time
}

// batches lists <Dir>/<sub>/<time> folders with their sizes, oldest first.
func (t *TrashStore) batches() []batch {
	var out []batch
	subs, _ := os.ReadDir(t.Dir)
	for _, sub := range subs {
		if !sub.IsDir() {
			continue
		}
		ents, _ := os.ReadDir(filepath.Join(t.Dir, sub.Name()))
		for _, e := range ents {
			when, err := time.Parse(batchLayout, e.Name())
			if !e.IsDir() || err != nil {
				continue
			}
			p := filepath.Join(t.Dir, sub.Name(), e.Name())
			var size int64
			_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if fi, err := d.Info(); err == nil {
						size += onDisk(fi.Size())
					}
				}
				return nil
			})
			out = append(out, batch{p, size, when})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].when.Before(out[j].when) })
	return out
}

// evictLocked deletes the oldest batches until the trash fits MaxBytes and
// nothing is older than cutoff.
func (t *TrashStore) evictLocked(cutoff time.Time) error {
	if t.size <= t.MaxBytes && cutoff.IsZero() {
		return nil
	}
	// The oldest batches go first; the list stays in memory, so a full
	// trash doesn't cost a walk of the whole folder on every file.
	for len(t.list) > 0 {
		b := t.list[0]
		if t.size <= t.MaxBytes && !b.when.Before(cutoff) {
			break
		}
		if err := os.RemoveAll(b.path); err != nil {
			return err
		}
		t.size -= b.size
		t.list = t.list[1:]
	}
	if t.size < 0 {
		t.size = 0
	}
	return nil
}

func copyFile(src, dst string, fi fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

// onDisk is what a file really occupies: file systems allocate whole
// clusters (4 KiB on NTFS and ext4), so 20,000 tiny files are ~80 MB, not
// a few hundred KB. The trash limit counts this.
func onDisk(n int64) int64 {
	const cluster = 4096
	return max(cluster, (n+cluster-1)/cluster*cluster)
}

// TrashItem is one kept version: the file at Path, trashed at When.
type TrashItem struct {
	ID   string    `json:"id"`   // "<batch>/<path>", what Open and Restore take
	Path string    `json:"path"` // relative to the folder root
	When time.Time `json:"when"`
	Size int64     `json:"size"`
}

// List returns this folder's kept versions, newest first.
func (t *Trash) List() []TrashItem {
	out := []TrashItem{}
	if t == nil || t.store == nil {
		return out
	}
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	base := filepath.Join(t.store.Dir, t.sub)
	ents, _ := os.ReadDir(base)
	for _, e := range ents {
		when, err := time.Parse(batchLayout, e.Name())
		if !e.IsDir() || err != nil {
			continue
		}
		bdir := filepath.Join(base, e.Name())
		_ = filepath.WalkDir(bdir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(bdir, p)
			rel = filepath.ToSlash(rel)
			out = append(out, TrashItem{ID: e.Name() + "/" + rel, Path: rel, When: when, Size: fi.Size()})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].When.Equal(out[j].When) {
			return out[i].When.After(out[j].When)
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// path resolves a TrashItem ID to its file, refusing anything else.
func (t *Trash) path(id string) (string, string, error) {
	if t == nil || t.store == nil {
		return "", "", errors.New("the trash is off")
	}
	batchName, rel, ok := strings.Cut(id, "/")
	if _, err := time.Parse(batchLayout, batchName); !ok || err != nil {
		return "", "", fmt.Errorf("bad trash id %q", id)
	}
	if err := CheckRel(rel); err != nil {
		return "", "", err
	}
	return filepath.Join(t.store.Dir, t.sub, batchName, filepath.FromSlash(rel)), rel, nil
}

// Open opens a kept version for reading.
func (t *Trash) Open(id string) (*os.File, error) {
	p, _, err := t.path(id)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, errors.New("this version is no longer in the trash")
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return os.Open(p)
}

// Restore puts a kept version back at its path under root; stage is
// Workspace.Stage ("" when it is on another drive). The file there now, if
// any, goes to the trash first, so a restore can be undone the same way.
// The kept version stays in the trash. The restored file gets the current
// time: it is a new change and syncs like one. Returns the restored path.
func (t *Trash) Restore(id, stage, root string) (string, error) {
	src, err := t.Open(id)
	if err != nil {
		return "", err
	}
	defer src.Close()
	_, rel, _ := t.path(id)
	dst := Abs(root, rel)
	if fi, err := os.Lstat(dst); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is a %s now, not a file", rel, fi.Mode().Type())
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	dir := stage
	if dir == "" {
		dir = filepath.Dir(dst)
	}
	tmp := filepath.Join(dir, randName())
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, src)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := t.Put(root, rel); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return rel, nil
}
