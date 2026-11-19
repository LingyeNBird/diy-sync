// Opt-in scale test (100,000 files): FDSYNC_PERF=1 go test -v ./internal/perf

package perf

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"fdsync/internal/client"
	"fdsync/internal/index"
	"fdsync/internal/scan"
	"fdsync/internal/server"
)

func mkTree(t *testing.T, root string, dirs, per int) {
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(root, "a", fmt.Sprintf("pkg%04d", d), "lib")
		os.MkdirAll(dir, 0o755)
		for f := 0; f < per; f++ {
			os.WriteFile(filepath.Join(dir, fmt.Sprintf("m%03d.js", f)), []byte(fmt.Sprintf("module.exports = %d // %d\n", f, d)), 0o644)
		}
	}
}

func mem() string {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return fmt.Sprintf("heap %d MB", m.HeapAlloc>>20)
}

func TestPerf100k(t *testing.T) {
	if os.Getenv("FDSYNC_PERF") == "" {
		t.Skip("set FDSYNC_PERF=1")
	}
	win, wsl := t.TempDir(), t.TempDir()
	t0 := time.Now()
	mkTree(t, wsl, 1000, 100) // 100k files, 2k dirs on the client
	t.Logf("create 100k files: %v", time.Since(t0))

	st, _ := index.Open(filepath.Join(t.TempDir(), "i.db"))
	t0 = time.Now()
	r, err := scan.Scan(context.Background(), st, scan.Options{Root: wsl, Device: "c", TrackMode: true, CheckWindowsNames: true})
	t.Logf("first scan: %v (%d changed, %d hashed) %s", time.Since(t0), r.Changed, r.Hashed, mem())
	t0 = time.Now()
	r, err = scan.Scan(context.Background(), st, scan.Options{Root: wsl, Device: "c", TrackMode: true, CheckWindowsNames: true})
	t.Logf("rescan, nothing changed: %v (%d changed) err=%v", time.Since(t0), r.Changed, err)
	st.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := server.New(server.Config{Token: "t", DataDir: t.TempDir(), Folders: []server.FolderConfig{{ID: "p", Path: win, Rules: []string{"a/"}}}}, log)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	c := &client.Client{BaseURL: hs.URL, Token: "t", Device: "c1", HTTP: hs.Client(), Log: log}
	s, err := c.Open(client.Binding{Folder: "p", Root: wsl}, filepath.Join(t.TempDir(), "w"))
	if err != nil {
		t.Fatal(err)
	}
	srv.ScanAll(context.Background())
	t0 = time.Now()
	stats, err := s.SyncOnce(context.Background())
	t.Logf("first sync WSL→Windows: %v (uploaded %d) err=%v %s", time.Since(t0), stats.Uploaded, err, mem())
	t0 = time.Now()
	srv.ScanAll(context.Background())
	stats, err = s.SyncOnce(context.Background())
	t.Logf("idle round (server rescan + client round): %v (changed %d)", time.Since(t0), stats.Scanned.Changed)
	t0 = time.Now()
	os.WriteFile(filepath.Join(wsl, "a", "pkg0500", "lib", "m050.js"), []byte("changed\n"), 0o644)
	stats, err = s.SyncOnce(context.Background())
	t.Logf("one changed file: %v (uploaded %d)", time.Since(t0), stats.Uploaded)
	// A second, empty client downloads all 100k files.
	c2 := &client.Client{BaseURL: hs.URL, Token: "t", Device: "c2", HTTP: hs.Client(), Log: log}
	wsl2 := t.TempDir()
	s2, err := c2.Open(client.Binding{Folder: "p", Root: wsl2}, filepath.Join(t.TempDir(), "w2"))
	if err != nil {
		t.Fatal(err)
	}
	t0 = time.Now()
	stats, err = s2.SyncOnce(context.Background())
	t.Logf("first sync Windows→new WSL client: %v (pulled %d, %d MB down) err=%v %s", time.Since(t0), stats.Pulled, stats.BytesDown>>20, err, mem())
}
