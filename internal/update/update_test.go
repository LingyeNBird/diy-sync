package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.0.1", "1.0.0", true},
		{"1.10.0", "1.9.9", true},
		{"2.0.0", "10.0.0", false},
		{"1.0.0", "1.0.0", false},
		{"0.9.0", "1.0.0", false},
		{"v1.2.3", "1.2.2", true},
		{"1.0.0", "dev", true}, // a development build is older than any release
		{"1.0.0", "0.1.0-dev", true},
		{"dev", "1.0.0", false},
		{"1.0", "0.1.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestInstall(t *testing.T) {
	newExe := []byte("new version")
	sum := sha256.Sum256(newExe)
	name := AssetName("fdsync-server")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.8.7","html_url":"x","assets":[{"name":%q,"browser_download_url":%q},{"name":"SHA256SUMS","browser_download_url":%q}]}`,
				name, srv.URL+"/dl/"+name, srv.URL+"/dl/SHA256SUMS")
		case "/dl/" + name:
			w.Write(newExe)
		case "/dl/SHA256SUMS":
			fmt.Fprintf(w, "%s  other\n%s  %s\n", strings.Repeat("0", 64), hex.EncodeToString(sum[:]), name)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	apiBase = srv.URL
	defer func() { apiBase = "https://api.github.com" }()

	r, err := Latest(context.Background(), "fdsync-server")
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "9.8.7" || !r.HasAsset() {
		t.Fatalf("release = %+v", r)
	}
	exe := filepath.Join(t.TempDir(), "fdsync-server.exe")
	os.WriteFile(exe, []byte("old version"), 0o755)
	if err := install(context.Background(), r, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new version" {
		t.Fatalf("exe = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old version" {
		t.Fatalf("exe.old = %q", b)
	}

	// A wrong checksum leaves the exe alone.
	newExe = []byte("tampered")
	if err := install(context.Background(), r, exe); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered download: err = %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new version" {
		t.Fatalf("exe after a failed install = %q", b)
	}
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Fatal("the failed download was left behind")
	}
}
