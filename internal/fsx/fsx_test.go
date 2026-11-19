package fsx

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lukechampine.com/blake3"
)

func TestWindowsNameProblem(t *testing.T) {
	for name, want := range map[string]string{
		"ok.txt":    "",
		"what?.txt": "invalid-name",
		"a:b":       "invalid-name",
		"trail.":    "invalid-name",
		"trail ":    "invalid-name",
		"tab\tx":    "invalid-name",
		"aux.txt":   "reserved-name",
		"AUX":       "reserved-name",
		"com1.log":  "reserved-name",
		"com10":     "",
		"auxiliary": "",
	} {
		if got := WindowsNameProblem(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

func TestCheckRel(t *testing.T) {
	for _, bad := range []string{"", "/abs", "../x", "a/../b", "a//b", "a/./b", `a\b`, "C:/x", "a/file:stream", "x/.fdsync-tmp-1"} {
		if CheckRel(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"a", "a/b.txt", ".bashrc", ".fdsync/x"} {
		if err := CheckRel(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
}

func hashOf(s string) string {
	h := blake3.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestWriteFileReplacesIntoTrash(t *testing.T) {
	root := t.TempDir()
	store, err := OpenTrash(t.TempDir(), TrashLimit)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := OpenWorkspace(t.TempDir(), root, store.For("f"))
	if err != nil || !ws.SameDrive {
		t.Fatalf("workspace: %v %+v", err, ws)
	}
	trash, stage := ws.Trash, ws.Stage
	mt := time.Unix(1_700_000_000, 0)
	fi, err := WriteFile(stage, root, "d/a.txt", strings.NewReader("one"), hashOf("one"), 0o644, mt, Expect{Absent: true}, trash)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(mt) {
		t.Fatalf("mtime %v", fi.ModTime())
	}
	// Wrong expectation: the file isn't replaced.
	_, err = WriteFile(stage, root, "d/a.txt", strings.NewReader("two"), hashOf("two"), 0o644, mt, Expect{Absent: true}, trash)
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("want ErrChanged, got %v", err)
	}
	exp := Expect{Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}
	if _, err := WriteFile(stage, root, "d/a.txt", strings.NewReader("two"), hashOf("two"), 0o644, mt, exp, trash); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "d", "a.txt"))
	if string(b) != "two" {
		t.Fatalf("content %q", b)
	}
	// The old version is in the trash (outside root); nothing else is in root.
	var trashed, inRoot int
	_ = filepath.Walk(store.Dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			trashed++
		}
		return nil
	})
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			inRoot++
		}
		return nil
	})
	if trashed != 1 || inRoot != 1 {
		t.Fatalf("trashed=%d files in root=%d", trashed, inRoot)
	}
	// Bad hash is refused.
	if _, err := WriteFile(stage, root, "x", strings.NewReader("abc"), hashOf("abd"), 0, mt, Expect{Absent: true}, nil); err == nil {
		t.Fatal("bad hash accepted")
	}
}

func TestTrashNeverExceedsLimit(t *testing.T) {
	root := t.TempDir()
	store, err := OpenTrash(t.TempDir(), 100<<20) // asks for more than allowed
	if err != nil {
		t.Fatal(err)
	}
	if store.MaxBytes != TrashLimit {
		t.Fatalf("limit %d, want clamped to %d", store.MaxBytes, TrashLimit)
	}
	a, b := store.For("a"), store.For("b")
	blob := bytes.Repeat([]byte("x"), 10<<20) // 10 MiB
	for i := 0; i < 12; i++ {                 // 120 MiB through the trash
		name := fmt.Sprintf("f%02d.bin", i)
		os.WriteFile(filepath.Join(root, name), blob, 0o644)
		tr := a
		if i%2 == 1 {
			tr = b
		}
		if err := tr.Put(root, name); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct batch names
		if store.Size() > TrashLimit {
			t.Fatalf("after %d puts the trash holds %d bytes", i+1, store.Size())
		}
	}
	var onDisk int64
	filepath.Walk(store.Dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			onDisk += info.Size()
		}
		return nil
	})
	if onDisk > TrashLimit || onDisk != store.Size() {
		t.Fatalf("on disk %d, counted %d, limit %d", onDisk, store.Size(), TrashLimit)
	}
	// The newest files are the ones kept.
	if _, err := os.Stat(filepath.Join(store.Dir, "b")); err != nil {
		t.Fatal("newest entries should survive")
	}

	// Too big to keep: deleted, not trashed.
	os.WriteFile(filepath.Join(root, "big.bin"), bytes.Repeat([]byte("y"), 17<<20), 0o644)
	before := store.Size()
	if err := a.Put(root, "big.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "big.bin")); err == nil || store.Size() != before {
		t.Fatal("a file over 16 MiB should be deleted without entering the trash")
	}

	// Reopening counts what is there and keeps the cap.
	again, err := OpenTrash(store.Dir, TrashLimit)
	if err != nil || again.Size() != store.Size() {
		t.Fatalf("reopen: %v size %d want %d", err, again.Size(), store.Size())
	}
	// 0 turns it off and empties it.
	off, err := OpenTrash(store.Dir, 0)
	if err != nil || off.Size() != 0 {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "z"), []byte("z"), 0o644)
	if err := off.For("a").Put(root, "z"); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(store.Dir); len(ents) != 0 {
		t.Fatal("trash off should keep nothing")
	}
}

func TestTrashPruneByAge(t *testing.T) {
	dir := t.TempDir()
	// An old entry from an earlier run: found when the trash opens.
	old := filepath.Join(dir, "a", time.Now().Add(-2*time.Hour).UTC().Format(batchLayout))
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "f"), []byte("x"), 0o644)
	store, _ := OpenTrash(dir, TrashLimit)
	store.MaxAge = time.Hour
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "g"), []byte("y"), 0o644)
	if err := store.For("a").Put(root, "g"); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(store.Dir, "a"))
	if len(ents) != 1 {
		t.Fatalf("want 1 batch left, got %d", len(ents))
	}
}

// A workspace on another filesystem (/dev/shm is tmpfs) falls back to
// receiving next to the file and copying into the trash.
func TestWorkspaceOnAnotherFilesystem(t *testing.T) {
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("no /dev/shm")
	}
	work, err := os.MkdirTemp("/dev/shm", "fdsync-test-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(work)
	root, err := os.MkdirTemp(".", "xdev-root-") // the module's filesystem
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	store, err := OpenTrash(filepath.Join(work, "trash"), TrashLimit)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := OpenWorkspace(filepath.Join(work, "f"), root, store.For("f"))
	if err != nil {
		t.Fatal(err)
	}
	if ws.SameDrive {
		t.Skip("/dev/shm is on the same filesystem here")
	}
	if ws.Stage != "" {
		t.Fatal("stage should fall back to next-to-file")
	}
	fi, err := WriteFile(ws.Stage, root, "f.txt", strings.NewReader("x"), hashOf("x"), 0o644, time.Now(), Expect{Absent: true}, ws.Trash)
	if err != nil {
		t.Fatal(err)
	}
	if err := Delete(root, "f.txt", Expect{Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}, ws.Trash); err != nil {
		t.Fatal(err)
	}
	n := 0
	filepath.Walk(store.Dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n++
		}
		return nil
	})
	ents, _ := os.ReadDir(root)
	if n != 1 || len(ents) != 0 {
		t.Fatalf("trash files %d, left in root %d", n, len(ents))
	}
}

func TestBlockSizes(t *testing.T) {
	for n, want := range map[int64]int{1000: 0, BlockMin - 1: 0, BlockMin: 128 << 10, 250 << 20: 128 << 10, 1 << 30: 1 << 20, 100 << 30: 16 << 20} {
		if got := BlockSize(n); got != want {
			t.Errorf("BlockSize(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestIndexEncoding(t *testing.T) {
	ix := []int{0, 1, 2, 5, 7, 8}
	s := EncodeIndexes(ix)
	if s != "0-2,5,7-8" {
		t.Fatalf("encode: %q", s)
	}
	m, err := DecodeIndexes(s, 9)
	if err != nil || len(m) != 6 || !m[8] || m[3] {
		t.Fatalf("decode: %v %v", m, err)
	}
	for _, bad := range []string{"9", "3-1", "-1", "x", "0-99999999"} {
		if _, err := DecodeIndexes(bad, 9); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Many tiny files count as what they take on disk, so they can't flood the trash.
func TestTrashCountsDiskUsage(t *testing.T) {
	root := t.TempDir()
	store, _ := OpenTrash(t.TempDir(), TrashLimit)
	tr := store.For("f")
	for i := 0; i < 20000; i++ {
		name := fmt.Sprintf("t%05d", i)
		os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644)
		if err := tr.Put(root, name); err != nil {
			t.Fatal(err)
		}
	}
	if store.Size() > TrashLimit {
		t.Fatalf("trash holds %d bytes", store.Size())
	}
	n := 0
	filepath.Walk(store.Dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n++
		}
		return nil
	})
	if n > TrashLimit/4096 {
		t.Fatalf("%d tiny files kept; at most %d fit in %d MB of disk", n, TrashLimit/4096, TrashLimit>>20)
	}
}
func TestTrashListRestore(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	store, err := OpenTrash(filepath.Join(data, "trash"), TrashLimit)
	if err != nil {
		t.Fatal(err)
	}
	tr := store.For("f")
	p := filepath.Join(root, "d", "a.txt")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("v1"), 0o644)
	if err := tr.Put(root, "d/a.txt"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	os.WriteFile(p, []byte("v2"), 0o644)
	tr.Put(root, "d/a.txt")
	os.WriteFile(p, []byte("v3"), 0o644)

	items := tr.List()
	if len(items) != 2 || items[0].Path != "d/a.txt" || !items[0].When.After(items[1].When) {
		t.Fatalf("list = %+v", items)
	}
	f, err := tr.Open(items[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "v1" {
		t.Fatalf("oldest = %q", b)
	}
	if _, err := tr.Restore(items[1].ID, "", root); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "v1" {
		t.Fatalf("restored %q", b)
	}
	// v3 went to the trash; v1 is still there too.
	if n := len(tr.List()); n != 3 {
		t.Fatalf("after restore: %d items", n)
	}
	for _, bad := range []string{"../x", "20260101-000000.000/../../etc/passwd", "nope/a.txt", "20260101-000000.000"} {
		if _, err := tr.Open(bad); err == nil {
			t.Errorf("opened %q", bad)
		}
	}
}
