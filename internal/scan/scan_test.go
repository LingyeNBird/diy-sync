package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fdsync/internal/index"
	"fdsync/internal/rules"
	"fdsync/internal/vv"
)

func setup(t *testing.T) (string, *index.Store) {
	t.Helper()
	root := t.TempDir()
	s, err := index.Open(filepath.Join(t.TempDir(), "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return root, s
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, s *index.Store, p string) *index.Entry {
	t.Helper()
	var e *index.Entry
	_ = s.View(func(tx *index.Tx) (err error) { e, err = tx.Get(p); return })
	return e
}

func TestScanLifecycle(t *testing.T) {
	root, s := setup(t)
	rs, _ := rules.NewScope([]string{"a/", "!a/private/"}, nil)
	opts := Options{Root: root, Device: "dev1", Scope: rs, TrackMode: true, CheckWindowsNames: true}
	ctx := context.Background()

	write(t, root, "a/x.txt", "one")
	write(t, root, "a/private/p.txt", "p")
	write(t, root, "b/out.txt", "o")
	write(t, root, "a/what?.txt", "bad name")
	write(t, root, ".fdsync/trash/t", "t")
	if err := os.Symlink("x.txt", filepath.Join(root, "a", "link")); err != nil {
		t.Fatal(err)
	}

	r, err := Scan(ctx, s, opts)
	if err != nil {
		t.Fatal(err)
	}
	// a/ and a/x.txt only.
	if r.Changed != 2 || get(t, s, "a/x.txt") == nil || get(t, s, "a/private/p.txt") != nil || get(t, s, "b/out.txt") != nil {
		t.Fatalf("first scan: %+v", r)
	}
	if len(r.Excluded) != 2 || r.Excluded[0].Reason != "symlink" || r.Excluded[1].Reason != "invalid-name" {
		t.Fatalf("excluded: %+v", r.Excluded)
	}
	// All lists everything as metadata, private items and exclusions included.
	seen := map[string]bool{}
	for _, it := range r.All {
		seen[it.Path] = true
	}
	for _, p := range []string{"a", "a/x.txt", "a/private/p.txt", "b/out.txt", "a/what?.txt", "a/link"} {
		if !seen[p] {
			t.Errorf("All misses %s", p)
		}
	}

	// Nothing changed: no new versions, nothing hashed.
	r, _ = Scan(ctx, s, opts)
	if r.Changed != 0 || r.Hashed != 0 {
		t.Fatalf("rescan: %+v", r)
	}

	// Edit → new version that descends from the old one.
	v1 := get(t, s, "a/x.txt").Version
	write(t, root, "a/x.txt", "two")
	os.Chtimes(filepath.Join(root, "a", "x.txt"), time.Now().Add(time.Second), time.Now().Add(time.Second))
	r, _ = Scan(ctx, s, opts)
	e := get(t, s, "a/x.txt")
	if r.Changed != 1 || e.Version.Compare(v1) != vv.Greater {
		t.Fatalf("edit: %+v %v", r, e.Version)
	}

	// Touch only (same content): no new version.
	os.Chtimes(filepath.Join(root, "a", "x.txt"), time.Now().Add(2*time.Second), time.Now().Add(2*time.Second))
	r, _ = Scan(ctx, s, opts)
	if r.Changed != 0 || r.Hashed != 1 || get(t, s, "a/x.txt").Seq != e.Seq {
		t.Fatalf("touch: %+v", r)
	}

	// chmod +x is a change on a client.
	os.Chmod(filepath.Join(root, "a", "x.txt"), 0o755)
	r, _ = Scan(ctx, s, opts)
	if r.Changed != 1 || get(t, s, "a/x.txt").Mode != 0o755 {
		t.Fatalf("chmod: %+v", r)
	}

	// Cancelling synchronization of a/ must not produce tombstones.
	rs2, _ := rules.NewScope([]string{"b/"}, nil)
	r, _ = Scan(ctx, s, Options{Root: root, Device: "dev1", Scope: rs2, TrackMode: true})
	if get(t, s, "a/x.txt").Deleted {
		t.Fatal("cancel sync deleted a/x.txt")
	}

	// Delete → tombstones, children first.
	os.RemoveAll(filepath.Join(root, "a"))
	r, _ = Scan(ctx, s, opts)
	if r.Changed != 2 || !get(t, s, "a/x.txt").Deleted || !get(t, s, "a").Deleted {
		t.Fatalf("delete: %+v", r)
	}
	if get(t, s, "a/x.txt").Seq > get(t, s, "a").Seq {
		t.Fatal("folder tombstone should come after its children")
	}
}

func TestSymlinkReplacingFolderIsNotADelete(t *testing.T) {
	root, s := setup(t)
	opts := Options{Root: root, Device: "d", TrackMode: true}
	write(t, root, "d/f.txt", "x")
	Scan(context.Background(), s, opts)
	os.RemoveAll(filepath.Join(root, "d"))
	os.Symlink("/tmp", filepath.Join(root, "d"))
	Scan(context.Background(), s, opts)
	if get(t, s, "d/f.txt").Deleted {
		t.Fatal("files under a path that became a symlink must not get tombstones")
	}
}

func TestCaseCollisions(t *testing.T) {
	root, s := setup(t)
	opts := Options{Root: root, Device: "d", TrackMode: true, CheckWindowsNames: true}
	ctx := context.Background()
	write(t, root, "README.md", "first")
	Scan(ctx, s, opts) // README.md is now synced (known)
	write(t, root, "Readme.md", "second, newer")
	write(t, root, "src/a.go", "a")
	write(t, root, "Src/b.go", "b")
	os.Chtimes(filepath.Join(root, "Src"), time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)) // older: wins
	r, err := Scan(ctx, s, opts)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range r.Excluded {
		if x.Reason == "case-collision" {
			got[x.Path] = x.Other
		}
	}
	if got["Readme.md"] != "README.md" {
		t.Errorf("the already synced README.md should win: %v", got)
	}
	if got["src"] != "Src" {
		t.Errorf("the older Src/ should win, src/ lose with its contents: %v", got)
	}
	if get(t, s, "Readme.md") != nil || get(t, s, "src/a.go") != nil || get(t, s, "Src/b.go") == nil {
		t.Error("losers must not be indexed, the winner must")
	}
	// The loser stays out on the next round, and the winner keeps its version.
	r, _ = Scan(ctx, s, opts)
	if r.Changed != 0 {
		t.Errorf("stable rescan changed %d", r.Changed)
	}
}
