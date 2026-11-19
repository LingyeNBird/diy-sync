package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fdsync/internal/fsx"
	"fdsync/internal/index"
	"fdsync/internal/protocol"
	"fdsync/internal/server"
	"fdsync/internal/vv"
)

// env is a server with one sync folder ("proj") served over real HTTP, and
// any number of clients with their own local folders.
type env struct {
	t    *testing.T
	srv  *server.Server
	hs   *httptest.Server
	win  string // the "Windows" root
	data string // server data directory
	n    int
}

const token = "test-token"

func newEnv(t *testing.T, rules ...string) *env {
	t.Helper()
	return newEnvWith(t, nil, rules...)
}

// newEnvWith lets a test change the server config before it starts.
func newEnvWith(t *testing.T, edit func(*server.Config), rules ...string) *env {
	t.Helper()
	win := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	data := t.TempDir()
	cfg := server.Config{
		Token:   token,
		DataDir: data,
		Folders: []server.FolderConfig{{ID: "proj", Path: win, Rules: rules}, {ID: "other", Path: t.TempDir()}},
	}
	if edit != nil {
		edit(&cfg)
	}
	srv, err := server.New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hs.Close(); srv.Close() })
	return &env{t: t, srv: srv, hs: hs, win: win, data: data}
}

type cl struct {
	*Session
	root  string
	trash string // this client's trash, outside root
	e     *env
}

func (e *env) client() *cl {
	e.t.Helper()
	e.n++
	root := e.t.TempDir()
	data := e.t.TempDir() // the client's data directory
	trash, err := fsx.OpenTrash(filepath.Join(data, "trash"), fsx.TrashLimit)
	if err != nil {
		e.t.Fatal(err)
	}
	c := &Client{BaseURL: e.hs.URL, Token: token, Device: "c" + string(rune('0'+e.n)), HTTP: e.hs.Client(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Trash: trash}
	s, err := c.Open(Binding{Folder: "proj", Root: root}, filepath.Join(data, "proj"))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { s.Close() })
	return &cl{s, root, trash.Dir, e}
}

// files lists the files under dir (slash paths), for "nothing extra" checks.
func files(dir string) []string {
	var out []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

// sync runs one client round after the server has scanned its disk.
func (c *cl) sync() Stats {
	c.e.t.Helper()
	if err := c.e.srv.ScanAll(context.Background()); err != nil {
		c.e.t.Fatal(err)
	}
	st, err := c.SyncOnce(context.Background())
	if err != nil {
		c.e.t.Fatal(err)
	}
	return st
}

var later = time.Now()

func put(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Distinct mtimes even on coarse clocks.
	later = later.Add(time.Second)
	os.Chtimes(p, later, later)
}

func read(root, rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	return string(b), err == nil
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func (e *env) want(root, rel, content string) {
	e.t.Helper()
	got, ok := read(root, rel)
	if !ok {
		e.t.Fatalf("%s missing in %s", rel, root)
	}
	if got != content {
		e.t.Fatalf("%s = %q, want %q", rel, got, content)
	}
}

func (e *env) absent(root, rel string) {
	e.t.Helper()
	if exists(root, rel) {
		e.t.Fatalf("%s should not exist in %s", rel, root)
	}
}

func (e *env) api(method, path string, body any, out any) int {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.hs.URL+protocol.APIPrefix+path, r)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.hs.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (e *env) conflicts() []protocol.Conflict {
	var cs []protocol.Conflict
	e.api("GET", "/folders/proj/conflicts", nil, &cs)
	return cs
}

// ---------------------------------------------------------------- tests

func TestScopeAndPrivateItems(t *testing.T) {
	e := newEnv(t, "a/", "c/", "!c/**/node_modules/")
	put(t, e.win, "a/x.txt", "from windows")
	put(t, e.win, "b/private-win.txt", "never leaves windows")
	c := e.client()
	put(t, c.root, "d/private-wsl.txt", "never reaches windows")
	put(t, c.root, "c/lib/index.ts", "export {}")
	put(t, c.root, "c/node_modules/react/index.js", "big")

	c.sync()
	e.want(c.root, "a/x.txt", "from windows")
	e.absent(c.root, "b")
	e.want(e.win, "c/lib/index.ts", "export {}")
	e.absent(e.win, "d")
	e.absent(e.win, "c/node_modules")
}

func TestEditsAndDeletesBothWays(t *testing.T) {
	e := newEnv(t, "a/")
	put(t, e.win, "a/w.txt", "w1")
	c := e.client()
	c.sync()

	// Windows edit → WSL.
	put(t, e.win, "a/w.txt", "w2")
	c.sync()
	e.want(c.root, "a/w.txt", "w2")

	// WSL new file, new folder and edit → Windows.
	put(t, c.root, "a/new/n.txt", "n1")
	if err := os.Mkdir(filepath.Join(c.root, "a", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, c.root, "a/w.txt", "w3")
	c.sync()
	e.want(e.win, "a/new/n.txt", "n1")
	e.want(e.win, "a/w.txt", "w3")
	if !exists(e.win, "a/empty") {
		t.Fatal("empty folder not synced")
	}

	// Another round changes nothing (no echo).
	if st := c.sync(); st.Scanned.Changed != 0 || st.Pulled != 0 || st.Uploaded != 0 {
		t.Fatalf("idle round did work: %+v", st)
	}

	// WSL deletes a folder tree → Windows deletes it (into the trash).
	os.RemoveAll(filepath.Join(c.root, "a", "new"))
	c.sync()
	e.absent(e.win, "a/new")
	inTrash := false
	for _, f := range files(filepath.Join(e.data, "trash", "proj")) {
		inTrash = inTrash || strings.HasSuffix(f, "/a/new/n.txt")
	}
	if !inTrash {
		t.Fatal("deleted file should be in the server's trash")
	}

	// Windows deletes → WSL deletes.
	os.Remove(filepath.Join(e.win, "a", "w.txt"))
	c.sync()
	e.absent(c.root, "a/w.txt")

	// fdsync leaves nothing of its own in either folder.
	for _, root := range []string{e.win, c.root} {
		for _, f := range files(root) {
			if f != "a/empty" && !strings.HasPrefix(f, "a/") {
				t.Errorf("unexpected file %s in %s", f, root)
			}
		}
		if exists(root, ".fdsync") {
			t.Errorf(".fdsync created in %s", root)
		}
	}
}

func TestConflictResolveUseClient(t *testing.T) {
	e := newEnv(t, "a/")
	put(t, e.win, "a/f.txt", "base")
	c := e.client()
	c.sync()

	put(t, e.win, "a/f.txt", "windows edit")
	put(t, c.root, "a/f.txt", "wsl edit, longer")
	st := c.sync()
	if st.Conflicts == 0 {
		t.Fatal("expected a conflict")
	}
	// Each side keeps its own version; nothing is copied.
	e.want(e.win, "a/f.txt", "windows edit")
	e.want(c.root, "a/f.txt", "wsl edit, longer")
	cs := e.conflicts()
	if len(cs) != 1 || cs[0].Path != "a/f.txt" || cs[0].Kind != "concurrent-edit" {
		t.Fatalf("conflicts: %+v", cs)
	}

	// Other files keep syncing meanwhile.
	put(t, e.win, "a/other.txt", "o")
	c.sync()
	e.want(c.root, "a/other.txt", "o")

	if code := e.api("POST", "/folders/proj/conflicts/resolve", protocol.ResolveRequest{Path: "a/f.txt", Use: protocol.UseClient}, nil); code != 204 {
		t.Fatalf("resolve: %d", code)
	}
	c.sync()
	e.want(e.win, "a/f.txt", "wsl edit, longer")
	if len(e.conflicts()) != 0 {
		t.Fatal("conflict should be gone")
	}
	// Both sides settled: an idle round does nothing.
	if st := c.sync(); st.Scanned.Changed != 0 || st.Pulled != 0 || st.Uploaded != 0 {
		t.Fatalf("not settled: %+v", st)
	}
}

func TestConflictResolveUseWindows(t *testing.T) {
	e := newEnv(t, "a/")
	put(t, e.win, "a/f.txt", "base")
	c := e.client()
	c.sync()
	put(t, e.win, "a/f.txt", "windows edit")
	put(t, c.root, "a/f.txt", "wsl edit")
	c.sync()
	e.api("POST", "/folders/proj/conflicts/resolve", protocol.ResolveRequest{Path: "a/f.txt", Use: protocol.UseWindows}, nil)
	c.sync()
	e.want(c.root, "a/f.txt", "windows edit")
	if len(files(c.trash)) != 1 {
		t.Fatal("the replaced WSL version should be in the client's trash")
	}
}

func TestEditDeleteConflict(t *testing.T) {
	e := newEnv(t, "a/")
	put(t, e.win, "a/f.txt", "base")
	c := e.client()
	c.sync()
	os.Remove(filepath.Join(e.win, "a", "f.txt"))
	put(t, c.root, "a/f.txt", "edited on wsl")
	c.sync()
	cs := e.conflicts()
	if len(cs) != 1 || cs[0].Kind != "edit-delete" {
		t.Fatalf("conflicts: %+v", cs)
	}
	e.want(c.root, "a/f.txt", "edited on wsl")
	e.api("POST", "/folders/proj/conflicts/resolve", protocol.ResolveRequest{Path: "a/f.txt", Use: protocol.UseClient}, nil)
	c.sync()
	e.want(e.win, "a/f.txt", "edited on wsl")
}

func TestSameEditOnBothSidesIsNoConflict(t *testing.T) {
	e := newEnv(t, "a/")
	put(t, e.win, "a/f.txt", "base")
	c := e.client()
	c.sync()
	put(t, e.win, "a/f.txt", "same")
	put(t, c.root, "a/f.txt", "same")
	if st := c.sync(); st.Conflicts != 0 || len(e.conflicts()) != 0 {
		t.Fatalf("identical edits must merge: %+v", st)
	}
}

func TestTwoClientsAndModeBits(t *testing.T) {
	e := newEnv(t, "a/")
	c1, c2 := e.client(), e.client()
	put(t, c1.root, "a/run.sh", "#!/bin/sh\necho hi\n")
	os.Chmod(filepath.Join(c1.root, "a", "run.sh"), 0o755)
	c1.sync()
	c2.sync()
	e.want(c2.root, "a/run.sh", "#!/bin/sh\necho hi\n")
	fi, _ := os.Stat(filepath.Join(c2.root, "a", "run.sh"))
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v, want 0755 carried through the server", fi.Mode().Perm())
	}
	// c2's edit reaches c1 through the server.
	put(t, c2.root, "a/run.sh", "#!/bin/sh\necho bye\n")
	os.Chmod(filepath.Join(c2.root, "a", "run.sh"), 0o755)
	c2.sync()
	c1.sync()
	e.want(c1.root, "a/run.sh", "#!/bin/sh\necho bye\n")
}

func TestSpecialFilesStayLocal(t *testing.T) {
	e := newEnv(t, "a/")
	c := e.client()
	put(t, c.root, "a/ok.txt", "ok")
	put(t, c.root, "a/what?.txt", "bad name")
	put(t, c.root, "a/aux.txt", "reserved")
	os.Symlink("ok.txt", filepath.Join(c.root, "a", "link"))
	st := c.sync()
	e.want(e.win, "a/ok.txt", "ok")
	for _, p := range []string{"a/what?.txt", "a/aux.txt", "a/link"} {
		e.absent(e.win, p)
	}
	if len(st.Scanned.Excluded) != 3 {
		t.Fatalf("excluded: %+v", st.Scanned.Excluded)
	}
}

func TestAddToScopeAndCancelSynchronization(t *testing.T) {
	e := newEnv(t, "a/")
	c := e.client()
	put(t, c.root, "d/notes.md", "wsl only")
	put(t, e.win, "a/x.txt", "x")
	c.sync()
	e.absent(e.win, "d")

	// Add d/ to the scope from the panel: it syncs to Windows.
	if code := e.api("PUT", "/folders/proj/rules", protocol.RulesRequest{Rules: []string{"a/", "d/"}}, nil); code != 204 {
		t.Fatalf("rules: %d", code)
	}
	c.sync()
	e.want(e.win, "d/notes.md", "wsl only")

	// Cancel synchronization of a/: files stay on both sides, edits stop syncing.
	e.api("PUT", "/folders/proj/rules", protocol.RulesRequest{Rules: []string{"d/"}}, nil)
	c.sync()
	e.want(e.win, "a/x.txt", "x")
	e.want(c.root, "a/x.txt", "x")
	put(t, c.root, "a/x.txt", "local only now")
	c.sync()
	e.want(e.win, "a/x.txt", "x")
}

func TestScopeAddConflict(t *testing.T) {
	e := newEnv(t, "a/")
	c := e.client()
	put(t, e.win, "d/f.txt", "windows copy")
	put(t, c.root, "d/f.txt", "wsl copy")
	c.sync()
	e.api("PUT", "/folders/proj/rules", protocol.RulesRequest{Rules: []string{"a/", "d/"}}, nil)
	c.sync()
	cs := e.conflicts()
	if len(cs) != 1 || cs[0].Path != "d/f.txt" {
		t.Fatalf("want a scope-add conflict, got %+v", cs)
	}
	e.want(e.win, "d/f.txt", "windows copy")
	e.want(c.root, "d/f.txt", "wsl copy")
}

func TestRejectsUnsafePaths(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"../escape.txt", "a/../../x", "C:/Windows/x", "a/f.txt:ads", ".fdsync/trash/x"} {
		var res protocol.PushResult
		body := protocol.PushRequest{}
		body.Entries = append(body.Entries, jsonEntry(p))
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", e.hs.URL+protocol.APIPrefix+"/folders/proj/push", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(protocol.HeaderDevice, "evil")
		resp, err := e.hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if len(res.Rejected) != 1 || len(res.Need) != 0 {
			t.Errorf("%q not rejected: %+v", p, res)
		}
	}
	// Wrong token.
	req, _ := http.NewRequest("GET", e.hs.URL+protocol.APIPrefix+"/folders", nil)
	req.Header.Set("Authorization", "Bearer nope")
	resp, _ := e.hs.Client().Do(req)
	if resp.StatusCode != 401 {
		t.Fatalf("wrong token: %d", resp.StatusCode)
	}
}

func jsonEntry(p string) index.Entry {
	return index.Entry{Path: p, Hash: "00", Size: 1, Version: vv.Vector{"evil": 1}}
}

// Windows already has Readme.md; WSL creates README.md. The server refuses
// it (it would land on Readme.md), the panel shows why, and once Readme.md
// is gone on Windows the WSL file syncs. A case-only rename works too.
func TestCaseCollisionWithWindows(t *testing.T) {
	e := newEnv(t, "a/")
	put(t, e.win, "a/Readme.md", "windows readme")
	c := e.client()
	put(t, c.root, "a/README.md", "wsl readme")
	st := c.sync()
	_ = st
	// The client got Readme.md from Windows and keeps its own README.md:
	// on the client both exist; its own scan excludes the newer one.
	e.want(e.win, "a/Readme.md", "windows readme")
	if exists(e.win, "a/README.md") {
		t.Fatal("README.md must not be created next to Readme.md on Windows")
	}
	// Windows deletes Readme.md: the client's refused README.md now syncs.
	os.Remove(filepath.Join(e.win, "a", "Readme.md"))
	c.sync()
	c.sync()
	e.want(e.win, "a/README.md", "wsl readme")
}

func TestCaseOnlyRenameFromWSL(t *testing.T) {
	e := newEnv(t, "a/")
	c := e.client()
	put(t, c.root, "a/notes.md", "n")
	c.sync()
	e.want(e.win, "a/notes.md", "n")
	os.Rename(filepath.Join(c.root, "a", "notes.md"), filepath.Join(c.root, "a", "NOTES.md"))
	c.sync()
	c.sync()
	e.want(e.win, "a/NOTES.md", "n")
	if exists(e.win, "a/notes.md") {
		t.Fatal("the old name should be gone on Windows")
	}
}
