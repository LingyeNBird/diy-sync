// Package syncer applies remote index entries to a local root. The server and
// the client use the same code, so both sides behave identically.
package syncer

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fdsync/internal/fsx"
	"fdsync/internal/index"
)

// Side is one local root and how it records changes.
type Side struct {
	Root      string
	Device    string
	Stage     string // staging folder for incoming files (fsx.Workspace)
	Trash     *fsx.Trash
	TrackMode bool // apply permission bits (Linux clients)
}

// ExpectOf turns the local index entry into what should be on disk.
func ExpectOf(l *index.Entry) fsx.Expect {
	switch {
	case l == nil || l.Deleted:
		return fsx.Expect{Absent: true}
	case l.Dir:
		return fsx.Expect{Dir: true}
	}
	return fsx.Expect{Size: l.Size, ModTime: l.ModTime}
}

// adopt records r as the local entry, with this disk's size and mtime.
func adopt(r *index.Entry, fi fs.FileInfo) *index.Entry {
	e := *r
	e.Seq = 0
	if fi != nil {
		e.ModTime = fi.ModTime().UnixNano()
		if !e.Dir {
			e.Size = fi.Size()
		}
	}
	return &e
}

func (s Side) chmod(p string, mode uint32) error {
	if !s.TrackMode || runtime.GOOS == "windows" || mode == 0 {
		return nil
	}
	return os.Chmod(p, fs.FileMode(mode).Perm())
}

// Merge records that local and remote reached the same content independently.
func (s Side) Merge(tx *index.Tx, l, r *index.Entry) error {
	e := *l
	e.Version = l.Version.Merge(r.Version)
	return tx.Put(&e)
}

// Apply applies a newer remote entry that needs no file content (a delete,
// a folder, or a change to metadata only). When the file content itself must
// be transferred, it returns needContent and what the destination must still
// hold when the content is committed.
//
// fsx.ErrChanged means the local disk changed since the last scan; the caller
// skips the entry, and the rescan turns it into a conflict if it is one.
func (s Side) Apply(tx *index.Tx, l, r *index.Entry) (needContent bool, expect fsx.Expect, err error) {
	p := fsx.Abs(s.Root, r.Path)
	switch {
	case r.Deleted:
		if l == nil || l.Deleted {
			return false, expect, tx.Put(adopt(r, nil))
		}
		if !l.Dir && !fsx.ExactName(s.Root, r.Path) {
			// Gone already, or only another spelling is left (a case-only
			// rename on Windows): nothing to delete.
			return false, expect, tx.Put(adopt(r, nil))
		}
		err := fsx.Delete(s.Root, r.Path, ExpectOf(l), s.Trash)
		if errors.Is(err, fsx.ErrNotEmpty) {
			// The folder still holds files this side keeps (private or not yet
			// synced). Keep the folder and announce it again.
			e := *l
			e.Deleted = false
			e.Version = l.Version.Merge(r.Version).Bump(s.Device)
			return false, expect, tx.Put(&e)
		}
		if err != nil {
			return false, expect, err
		}
		return false, expect, tx.Put(adopt(r, nil))

	case r.Dir:
		if l != nil && !l.Deleted && !l.Dir {
			// A file became a folder.
			if !ExpectOf(l).Matches(p) {
				return false, expect, fsx.ErrChanged
			}
			if err := s.Trash.Put(s.Root, r.Path); err != nil {
				return false, expect, err
			}
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return false, expect, err
		}
		if err := s.chmod(p, r.Mode); err != nil {
			return false, expect, err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return false, expect, err
		}
		return false, expect, tx.Put(adopt(r, fi))
	}

	// A file.
	if l != nil && !l.Deleted && l.Dir {
		// A folder became a file: only if the folder is empty.
		if !ExpectOf(l).Matches(p) {
			return false, expect, fsx.ErrChanged
		}
		if err := os.Remove(p); err != nil {
			return false, expect, fsx.ErrNotEmpty
		}
		return true, fsx.Expect{Absent: true}, nil
	}
	if l != nil && !l.Deleted && l.Hash == r.Hash {
		// Same bytes; only the version or the mode differs.
		if !ExpectOf(l).Matches(p) {
			return false, expect, fsx.ErrChanged
		}
		if err := s.chmod(p, r.Mode); err != nil {
			return false, expect, err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return false, expect, err
		}
		return false, expect, tx.Put(adopt(r, fi))
	}
	return true, ExpectOf(l), nil
}

// Commit puts received content (a temp file from fsx.Receive) in place and
// records r.
func (s Side) Commit(tx *index.Tx, r *index.Entry, tmp string, expect fsx.Expect) error {
	mode := fs.FileMode(0o644)
	if s.TrackMode && r.Mode != 0 {
		mode = fs.FileMode(r.Mode)
	}
	fi, err := fsx.Commit(s.Root, r.Path, tmp, mode, time.Unix(0, r.ModTime), expect, s.Trash)
	if err != nil {
		return err
	}
	return tx.Put(adopt(r, fi))
}

// Move applies r by renaming a local file instead of transferring content:
// from is this side's entry for a file the same batch deletes, with r's
// content (a rename or move on the other side). It returns false when the
// move doesn't apply (the caller fetches the content as usual).
func (s Side) Move(tx *index.Tx, from, r *index.Entry, expect fsx.Expect) (bool, error) {
	if from == nil || from.Deleted || from.Dir || from.Hash != r.Hash || from.Path == r.Path {
		return false, nil
	}
	src, dst := fsx.Abs(s.Root, from.Path), fsx.Abs(s.Root, r.Path)
	if !ExpectOf(from).Matches(src) {
		return false, nil
	}
	// The destination must hold what this side expects (usually nothing). A
	// case-only rename on Windows finds the source itself there.
	if !expect.Matches(dst) {
		si, err1 := os.Lstat(src)
		di, err2 := os.Lstat(dst)
		if err1 != nil || err2 != nil || !strings.EqualFold(from.Path, r.Path) || !os.SameFile(si, di) {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if err := os.Rename(src, dst); err != nil {
		return false, nil // e.g. locked on Windows: transfer instead
	}
	if err := s.chmod(dst, r.Mode); err != nil {
		return true, err
	}
	mt := time.Unix(0, r.ModTime)
	_ = os.Chtimes(dst, mt, mt)
	fi, err := os.Lstat(dst)
	if err != nil {
		return true, err
	}
	return true, tx.Put(adopt(r, fi))
}
