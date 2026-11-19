// Package fsx holds the file operations both sides share: safe relative
// paths, Windows name rules, hashing, atomic writes and the trash.
package fsx

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"lukechampine.com/blake3"
)

// TempPrefix marks a file being received. Normally files are received in
// the staging folder outside the sync root; only when that is on another
// drive do they appear, briefly, next to their destination. Scanners and
// watchers skip them.
const TempPrefix = ".fdsync-tmp-"

// ErrChanged means the file on disk no longer matches what the index says,
// so it was not overwritten or deleted. A rescan will pick up the change.
var ErrChanged = errors.New("file changed on disk since the last scan")

// Ignored reports whether a slash path is an fdsync temp file.
func Ignored(rel string) bool {
	return strings.HasPrefix(path.Base(rel), TempPrefix)
}

var reserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

func init() {
	for i := 1; i <= 9; i++ {
		reserved[fmt.Sprintf("COM%d", i)] = true
		reserved[fmt.Sprintf("LPT%d", i)] = true
	}
}

// WindowsNameProblem returns why a single path segment can't exist on
// Windows: "invalid-name", "reserved-name", or "" when it is fine.
func WindowsNameProblem(name string) string {
	if name == "" {
		return "invalid-name"
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"\|?*`, r) {
			return "invalid-name"
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "invalid-name"
	}
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if reserved[strings.ToUpper(strings.TrimRight(stem, " "))] {
		return "reserved-name"
	}
	return ""
}

// CheckRel validates a slash path received from a peer: relative, clean, no
// "..", not an fdsync temp file, and every segment valid on Windows (this also rules
// out drive letters and NTFS alternate data streams via ':').
func CheckRel(rel string) error {
	if rel == "" || strings.HasPrefix(rel, "/") || path.Clean(rel) != rel || strings.Contains(rel, `\`) {
		return fmt.Errorf("invalid path %q", rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." || seg == "." {
			return fmt.Errorf("invalid path %q", rel)
		}
		if p := WindowsNameProblem(seg); p != "" {
			return fmt.Errorf("path %q: %s", rel, p)
		}
	}
	if Ignored(rel) {
		return fmt.Errorf("path %q is reserved for fdsync", rel)
	}
	return nil
}

// Abs joins a checked relative slash path onto root.
func Abs(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// HashFile returns the BLAKE3-256 hash of a file, hex encoded.
func HashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := blake3.New(32, nil)
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func randName() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return TempPrefix + hex.EncodeToString(b)
}

// Expect describes what should be at a path before it is replaced or deleted,
// as the index last saw it. Absent means no file or folder should be there.
type Expect struct {
	Absent  bool
	Dir     bool
	Size    int64
	ModTime int64
}

// Matches reports whether the disk still holds what e describes.
func (e Expect) Matches(p string) bool {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return e.Absent
	}
	if err != nil || e.Absent {
		return false
	}
	if e.Dir {
		return fi.IsDir()
	}
	return fi.Mode().IsRegular() && fi.Size() == e.Size && fi.ModTime().UnixNano() == e.ModTime
}

// WriteFile receives content into root/rel atomically (Receive + Commit).
// It returns the final file's info, which the caller records in its index.
func WriteFile(stage, root, rel string, r io.Reader, wantHash string, mode fs.FileMode, mtime time.Time, expect Expect, trash *Trash) (fs.FileInfo, error) {
	tmp, err := Receive(stage, root, rel, r, wantHash)
	if err != nil {
		return nil, err
	}
	return Commit(root, rel, tmp, mode, mtime, expect, trash)
}

// Receive writes r to a temp file in the staging folder (or, when stage is
// "", next to root/rel), checks the hash and fsyncs it. The caller must
// Commit or Discard the temp file.
func Receive(stage, root, rel string, r io.Reader, wantHash string) (string, error) {
	return receive(stage, root, rel, r, wantHash, true)
}

// ReceiveUnsynced is Receive without the fsync: for batches, which call
// SyncAll before committing (many flushes at once are far cheaper than
// one after another).
func ReceiveUnsynced(stage, root, rel string, r io.Reader, wantHash string) (string, error) {
	return receive(stage, root, rel, r, wantHash, false)
}

// SyncAll flushes files to disk, many at once (the file system groups
// concurrent flushes into one journal commit).
func SyncAll(paths []string) error {
	work := make(chan string)
	errs := make(chan error, len(paths))
	var wg sync.WaitGroup
	for range min(64, len(paths)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				f, err := os.OpenFile(p, os.O_RDWR, 0)
				if err == nil {
					err = f.Sync()
					f.Close()
				}
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	for _, p := range paths {
		work <- p
	}
	close(work)
	wg.Wait()
	close(errs)
	return <-errs // nil when none failed
}

func receive(stage, root, rel string, r io.Reader, wantHash string, doSync bool) (string, error) {
	dst := Abs(root, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	dir := stage
	if dir == "" {
		dir = filepath.Dir(dst)
	}
	tmp := filepath.Join(dir, randName())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	h := blake3.New(32, nil)
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		return fail(err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantHash {
		return fail(fmt.Errorf("%s: content hash %s, want %s", rel, short(got), short(wantHash)))
	}
	if doSync {
		if err := f.Sync(); err != nil {
			return fail(err)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// Discard deletes a received temp file that won't be committed.
func Discard(tmp string) { os.Remove(tmp) }

// Commit sets mode and mtime on a received temp file, checks that the
// destination still holds what the index expects, moves an existing file to
// the trash and renames the temp file into place. The temp file is gone
// afterwards either way.
func Commit(root, rel, tmp string, mode fs.FileMode, mtime time.Time, expect Expect, trash *Trash) (fs.FileInfo, error) {
	dst := Abs(root, rel)
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if runtime.GOOS != "windows" && mode != 0 {
		if err := os.Chmod(tmp, mode.Perm()); err != nil {
			return nil, err
		}
	}
	if err := os.Chtimes(tmp, mtime, mtime); err != nil {
		return nil, err
	}
	// Last check right before replacing: a local edit since the scan wins.
	if !expect.Matches(dst) {
		return nil, ErrChanged
	}
	if !expect.Absent {
		if err := trash.Put(root, rel); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		return nil, err
	}
	ok = true
	return os.Lstat(dst)
}

// Delete deletes root/rel (a file, or an empty folder) if it still matches
// expect. Files go to the trash. A folder that still has content is left in
// place and reported with ErrNotEmpty.
func Delete(root, rel string, expect Expect, trash *Trash) error {
	p := Abs(root, rel)
	if !expect.Matches(p) {
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			return nil // already gone
		}
		return ErrChanged
	}
	if expect.Absent {
		return nil
	}
	if expect.Dir {
		ents, err := os.ReadDir(p)
		if err != nil {
			return err
		}
		if len(ents) > 0 {
			return ErrNotEmpty
		}
		return os.Remove(p)
	}
	return trash.Put(root, rel)
}

// ErrNotEmpty: a folder deleted on the other side still holds files here.
var ErrNotEmpty = errors.New("folder is not empty")

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// ExactName reports whether root/rel exists with exactly this spelling.
// On case-insensitive file systems (Windows) a lookup of notes.md also
// finds NOTES.md; deleting "notes.md" must not delete that one.
func ExactName(root, rel string) bool {
	ents, err := os.ReadDir(filepath.Dir(Abs(root, rel)))
	if err != nil {
		return false
	}
	name := path.Base(rel)
	for _, e := range ents {
		if e.Name() == name {
			return true
		}
	}
	return false
}
