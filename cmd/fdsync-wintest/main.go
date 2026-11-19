// fdsync-wintest checks the Windows-specific behaviour the sync code relies
// on, in a throwaway folder under %TEMP%. It touches nothing else and needs
// no network. Run it, then send back fdsync-wintest-report.txt.
//
// Checks: atomic receive/replace, mtime precision, trash, replacing a file
// another program holds open, read-only files, long paths (>260 chars),
// junctions and symlinks in the scanner, case-insensitivity, and the
// ReadDirectoryChangesW watcher.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"lukechampine.com/blake3"

	"fdsync/internal/fsx"
	"fdsync/internal/index"
	"fdsync/internal/scan"
	"fdsync/internal/watch"
)

var report bytes.Buffer
var failures int

func logf(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	fmt.Println(s)
	report.WriteString(s + "\n")
}

func check(name string, fn func() (string, error)) {
	start := time.Now()
	detail, err := func() (d string, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return fn()
	}()
	ms := time.Since(start).Milliseconds()
	switch {
	case errors.Is(err, errSkip):
		logf("SKIP  %-34s %s", name, detail)
	case err != nil:
		failures++
		logf("FAIL  %-34s %v %s", name, err, detail)
	default:
		logf("ok    %-34s %s (%d ms)", name, detail, ms)
	}
}

var errSkip = errors.New("skip")

func hashOf(b []byte) string {
	h := blake3.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ws is the workspace (staging + trash), outside the test root as in the
// real programs (which keep it in the data directory).
var ws *fsx.Workspace
var trashStore *fsx.TrashStore

func write(root, rel string, data []byte) (os.FileInfo, error) {
	return fsx.WriteFile(ws.Stage, root, rel, bytes.NewReader(data), hashOf(data), 0o644, time.Unix(1_700_000_000, 123_456_789), fsx.Expect{Absent: true}, ws.Trash)
}

func expectOf(fi os.FileInfo) fsx.Expect {
	return fsx.Expect{Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}
}

func main() {
	logf("fdsync-wintest  %s  %s/%s  %s", time.Now().Format(time.RFC3339), runtime.GOOS, runtime.GOARCH, runtime.Version())
	root, err := os.MkdirTemp("", "fdsync-wintest-")
	if err != nil {
		logf("cannot create temp folder: %v", err)
		finish()
		return
	}
	defer os.RemoveAll(root)
	work, err := os.MkdirTemp("", "fdsync-wintest-data-")
	if err != nil {
		logf("cannot create temp folder: %v", err)
		finish()
		return
	}
	defer os.RemoveAll(work)
	trashStore, err = fsx.OpenTrash(filepath.Join(work, "trash"), fsx.TrashLimit)
	if err != nil {
		logf("trash: %v", err)
		finish()
		return
	}
	ws, err = fsx.OpenWorkspace(filepath.Join(work, "f"), root, trashStore.For("f"))
	if err != nil {
		logf("workspace: %v", err)
		finish()
		return
	}
	logf("test folder: %s\nworkspace:   %s (same drive: %v)\n", root, work, ws.SameDrive)

	check("atomic write + mtime", func() (string, error) {
		fi, err := write(root, "a/new.txt", []byte("hello"))
		if err != nil {
			return "", err
		}
		want := time.Unix(1_700_000_000, 123_456_789)
		got := fi.ModTime()
		d := fmt.Sprintf("mtime set %d, read back %d (diff %d ns)", want.UnixNano(), got.UnixNano(), want.UnixNano()-got.UnixNano())
		if got.Sub(want).Abs() >= 100*time.Nanosecond {
			return d, errors.New("mtime not kept to 100 ns")
		}
		if !expectOf(fi).Matches(filepath.Join(root, "a", "new.txt")) {
			return d, errors.New("index check fails right after writing")
		}
		return d, leftovers(root)
	})

	check("replace keeps old in trash", func() (string, error) {
		fi, err := write(root, "b/f.txt", []byte("v1"))
		if err != nil {
			return "", err
		}
		data := []byte("v2 longer")
		if _, err := fsx.WriteFile(ws.Stage, root, "b/f.txt", bytes.NewReader(data), hashOf(data), 0o644, time.Now(), expectOf(fi), ws.Trash); err != nil {
			return "", err
		}
		b, _ := os.ReadFile(filepath.Join(root, "b", "f.txt"))
		if string(b) != "v2 longer" {
			return "", fmt.Errorf("content %q", b)
		}
		n := countFiles(trashStore.Dir)
		if n != 1 {
			return "", fmt.Errorf("%d files in trash, want 1", n)
		}
		return "old version in the workspace trash", leftovers(root)
	})

	check("replace file held open (read)", func() (string, error) {
		fi, err := write(root, "c/open.txt", []byte("original"))
		if err != nil {
			return "", err
		}
		h, err := os.Open(filepath.Join(root, "c", "open.txt")) // like an editor or viewer holding it
		if err != nil {
			return "", err
		}
		defer h.Close()
		data := []byte("new")
		_, werr := fsx.WriteFile(ws.Stage, root, "c/open.txt", bytes.NewReader(data), hashOf(data), 0o644, time.Now(), expectOf(fi), ws.Trash)
		b, _ := os.ReadFile(filepath.Join(root, "c", "open.txt"))
		d := fmt.Sprintf("write result: %v; file now %q", werr, b)
		if werr == nil && string(b) != "new" {
			return d, errors.New("reported success but content is old")
		}
		if werr != nil && string(b) != "original" {
			return d, errors.New("failed AND damaged the original")
		}
		return d, leftovers(root)
	})

	check("read-only file: replace + delete", func() (string, error) {
		fi, err := write(root, "d/ro.txt", []byte("ro"))
		if err != nil {
			return "", err
		}
		p := filepath.Join(root, "d", "ro.txt")
		if err := os.Chmod(p, 0o444); err != nil { // sets FILE_ATTRIBUTE_READONLY
			return "", err
		}
		fi, _ = os.Lstat(p)
		data := []byte("rw")
		_, werr := fsx.WriteFile(ws.Stage, root, "d/ro.txt", bytes.NewReader(data), hashOf(data), 0o644, time.Now(), expectOf(fi), ws.Trash)
		fi2, _ := os.Lstat(p)
		derr := error(nil)
		if fi2 != nil {
			derr = fsx.Delete(root, "d/ro.txt", expectOf(fi2), ws.Trash)
		}
		_, gone := os.Lstat(p)
		d := fmt.Sprintf("replace: %v; delete: %v; gone after delete: %v", werr, derr, gone != nil)
		if werr != nil || derr != nil || gone == nil {
			return d, errors.New("read-only file not handled")
		}
		return d, nil
	})

	check("long path (>260 chars)", func() (string, error) {
		parts := []string{}
		for i := 0; i < 6; i++ {
			parts = append(parts, strings.Repeat(string(rune('k'+i)), 50))
		}
		rel := strings.Join(parts, "/") + "/long-file.txt"
		full := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := write(root, rel, []byte("deep")); err != nil {
			return fmt.Sprintf("path length %d", len(full)), err
		}
		got, err := fsx.HashFile(full)
		if err != nil || got != hashOf([]byte("deep")) {
			return "", fmt.Errorf("read back: %v", err)
		}
		return fmt.Sprintf("path length %d", len(full)), nil
	})

	check("case-insensitive folder", func() (string, error) {
		if _, err := write(root, "e/Case.txt", []byte("x")); err != nil {
			return "", err
		}
		_, err := os.Lstat(filepath.Join(root, "e", "case.txt"))
		if err == nil {
			return "case.txt finds Case.txt (normal NTFS; case collisions matter)", nil
		}
		return "case-sensitive folder (unusual)", nil
	})

	check("scanner: junction + symlink", func() (string, error) {
		sroot := filepath.Join(root, "scan")
		os.MkdirAll(filepath.Join(sroot, "real", "inner"), 0o755)
		os.WriteFile(filepath.Join(sroot, "real", "inner", "f.txt"), []byte("f"), 0o644)
		var notes []string
		if runtime.GOOS == "windows" {
			out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(sroot, "junction"), filepath.Join(sroot, "real")).CombinedOutput()
			if err != nil {
				notes = append(notes, fmt.Sprintf("mklink /J failed: %v %s", err, strings.TrimSpace(string(out))))
			}
		}
		if err := os.Symlink(filepath.Join(sroot, "real", "inner", "f.txt"), filepath.Join(sroot, "symlink.txt")); err != nil {
			notes = append(notes, "file symlink not created (needs Developer Mode or admin)")
		}
		db, err := index.Open(filepath.Join(root, "scan.db"))
		if err != nil {
			return "", err
		}
		defer db.Close()
		res, err := scan.Scan(context.Background(), db, scan.Options{Root: sroot, Device: "w"})
		if err != nil {
			return "", err
		}
		var ex []string
		for _, x := range res.Excluded {
			ex = append(ex, x.Path+"="+x.Reason)
		}
		var indexed []string
		_ = db.View(func(tx *index.Tx) error {
			return tx.ForEach(func(e *index.Entry) error { indexed = append(indexed, e.Path); return nil })
		})
		d := fmt.Sprintf("excluded [%s]; indexed [%s] %s", strings.Join(ex, " "), strings.Join(indexed, " "), strings.Join(notes, "; "))
		for _, p := range indexed {
			if strings.HasPrefix(p, "junction") || strings.HasPrefix(p, "symlink") {
				return d, errors.New("followed a junction or symlink")
			}
		}
		if len(indexed) != 3 { // real, real/inner, real/inner/f.txt
			return d, fmt.Errorf("indexed %d entries, want 3", len(indexed))
		}
		return d, nil
	})

	check("watcher (ReadDirectoryChangesW)", func() (string, error) {
		wroot := filepath.Join(root, "watch")
		os.MkdirAll(filepath.Join(wroot, "existing"), 0o755)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ev, err := watch.Watch(ctx, wroot, 200*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			return "", err
		}
		time.Sleep(200 * time.Millisecond)
		steps := []struct {
			name string
			do   func() error
		}{
			{"create file", func() error { return os.WriteFile(filepath.Join(wroot, "a.txt"), []byte("1"), 0o644) }},
			{"modify file", func() error { return os.WriteFile(filepath.Join(wroot, "a.txt"), []byte("22"), 0o644) }},
			{"file in existing subfolder", func() error {
				return os.WriteFile(filepath.Join(wroot, "existing", "b.txt"), []byte("b"), 0o644)
			}},
			{"new subfolder, then file in it", func() error {
				if err := os.Mkdir(filepath.Join(wroot, "fresh"), 0o755); err != nil {
					return err
				}
				// Drain the folder's own event, then write inside it.
				select {
				case <-ev:
				case <-time.After(3 * time.Second):
				}
				return os.WriteFile(filepath.Join(wroot, "fresh", "c.txt"), []byte("c"), 0o644)
			}},
			{"rename", func() error { return os.Rename(filepath.Join(wroot, "a.txt"), filepath.Join(wroot, "renamed.txt")) }},
			{"delete", func() error { return os.Remove(filepath.Join(wroot, "renamed.txt")) }},
			{"temp file only (must be ignored)", func() error {
				return os.WriteFile(filepath.Join(wroot, fsx.TempPrefix+"x"), []byte("t"), 0o644)
			}},
		}
		var got []string
		for _, s := range steps {
			if err := s.do(); err != nil {
				return strings.Join(got, ", "), fmt.Errorf("%s: %v", s.name, err)
			}
			t0 := time.Now()
			select {
			case <-ev:
				got = append(got, fmt.Sprintf("%s %dms", s.name, time.Since(t0).Milliseconds()))
				if strings.HasPrefix(s.name, "temp") {
					return strings.Join(got, ", "), errors.New("temp file triggered a rescan")
				}
			case <-time.After(3 * time.Second):
				if !strings.HasPrefix(s.name, "temp") {
					return strings.Join(got, ", "), fmt.Errorf("no signal for %q", s.name)
				}
				got = append(got, "temp ignored")
			}
		}
		os.Remove(filepath.Join(wroot, fsx.TempPrefix+"x")) // this check's own file
		return strings.Join(got, ", "), nil
	})

	check("nothing of fdsync's in the folder", func() (string, error) {
		var bad []string
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil && (info.Name() == ".fdsync" || strings.HasPrefix(info.Name(), fsx.TempPrefix)) {
				bad = append(bad, p)
			}
			return nil
		})
		if len(bad) > 0 {
			return "", fmt.Errorf("found %v", bad)
		}
		return "no .fdsync folder, no temp files", nil
	})

	check("folder on another drive", func() (string, error) {
		if runtime.GOOS != "windows" {
			return "not Windows", errSkip
		}
		sysDrive := strings.ToUpper(filepath.VolumeName(work))
		var other string
		for c := 'C'; c <= 'Z'; c++ {
			d := string(c) + ":"
			if d == sysDrive {
				continue
			}
			if dir, err := os.MkdirTemp(d+`\`, "fdsync-wintest-"); err == nil {
				other = dir
				break
			}
		}
		if other == "" {
			return "no second writable drive", errSkip
		}
		defer os.RemoveAll(other)
		xws, err := fsx.OpenWorkspace(filepath.Join(work, "x"), other, trashStore.For("x"))
		if err != nil {
			return other, err
		}
		if xws.SameDrive {
			return other, errors.New("cross-drive not detected")
		}
		data := []byte("cross")
		fi, err := fsx.WriteFile(xws.Stage, other, "f.txt", bytes.NewReader(data), hashOf(data), 0o644, time.Now(), fsx.Expect{Absent: true}, xws.Trash)
		if err != nil {
			return other, err
		}
		if err := fsx.Delete(other, "f.txt", expectOf(fi), xws.Trash); err != nil {
			return other, fmt.Errorf("delete into trash on %s: %v", sysDrive, err)
		}
		if countFiles(filepath.Join(trashStore.Dir, "x")) != 1 {
			return other, errors.New("trash copy missing")
		}
		return fmt.Sprintf("%s: detected; received next to the file; trash copied to %s", other, sysDrive), leftovers(other)
	})

	finish()
}

func countFiles(dir string) int {
	n := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// leftovers fails if a temp file was left behind.
func leftovers(root string) error {
	var found []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && strings.HasPrefix(info.Name(), fsx.TempPrefix) {
			found = append(found, p)
		}
		return nil
	})
	if len(found) > 0 {
		return fmt.Errorf("temp files left: %v", found)
	}
	return nil
}

func finish() {
	logf("\n%d failure(s)", failures)
	exe, _ := os.Executable()
	out := filepath.Join(filepath.Dir(exe), "fdsync-wintest-report.txt")
	if err := os.WriteFile(out, report.Bytes(), 0o644); err != nil {
		out = filepath.Join(os.TempDir(), "fdsync-wintest-report.txt")
		_ = os.WriteFile(out, report.Bytes(), 0o644)
	}
	fmt.Printf("Report saved to %s\nPress Enter to close.", out)
	fmt.Scanln()
}
