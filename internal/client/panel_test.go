package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fdsync/internal/server"
)

// panelEnv is the panel's HTTP side next to a running sync env.
type panelEnv struct {
	*env
	ps   *httptest.Server
	hc   *http.Client
	csrf string
}

func newPanel(t *testing.T, e *env) *panelEnv {
	ps := httptest.NewServer(e.srv.PanelHandler())
	t.Cleanup(ps.Close)
	jar, _ := cookiejar.New(nil)
	return &panelEnv{env: e, ps: ps, hc: &http.Client{Jar: jar}}
}

func (p *panelEnv) call(method, path string, body any, out any) (int, http.Header, []byte) {
	p.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, p.ps.URL+path, r)
	if p.csrf != "" {
		req.Header.Set("X-CSRF-Token", p.csrf)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			p.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode, resp.Header, b
}

func (p *panelEnv) login() {
	p.t.Helper()
	var res struct{ CSRF string }
	if code, _, b := p.call("POST", "/api/setup", map[string]string{"password": "correct horse"}, &res); code != 200 {
		p.t.Fatalf("setup: %d %s", code, b)
	}
	p.csrf = res.CSRF
}

func TestPanelAuth(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	if code, _, _ := p.call("GET", "/api/overview", nil, nil); code != 401 {
		t.Fatalf("overview without a session: %d", code)
	}
	var sess map[string]any
	p.call("GET", "/api/session", nil, &sess)
	if sess["setup"] != true || sess["setupAllowed"] != true {
		t.Fatalf("session before setup: %v", sess)
	}
	if code, _, _ := p.call("POST", "/api/setup", map[string]string{"password": "short"}, nil); code != 400 {
		t.Fatal("short password accepted")
	}
	p.login()
	if code, _, _ := p.call("POST", "/api/setup", map[string]string{"password": "another one"}, nil); code != 409 {
		t.Fatal("second setup accepted")
	}
	// Writes need the CSRF token.
	csrf := p.csrf
	p.csrf = ""
	if code, _, _ := p.call("PUT", "/api/folders/proj/rules", map[string]any{"rules": []string{"b/"}}, nil); code != 403 {
		t.Fatalf("write without CSRF: %d", code)
	}
	p.csrf = csrf
	// Fresh client: wrong password refused, right one works.
	q := newPanel(t, e)
	if code, _, _ := q.call("POST", "/api/login", map[string]string{"password": "wrong password"}, nil); code != 401 {
		t.Fatal("wrong password accepted")
	}
	if code, _, _ := q.call("POST", "/api/login", map[string]string{"password": "correct horse"}, nil); code != 200 {
		t.Fatal("login failed")
	}
	// DNS rebinding: a foreign hostname is refused.
	req, _ := http.NewRequest("GET", p.ps.URL+"/api/session", nil)
	req.Host = "evil.example:7444"
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("foreign Host: %d", resp.StatusCode)
	}
}

func TestPanelData(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	put(t, e.win, "a/shared.txt", "both")
	put(t, e.win, "b/win-private.txt", "windows only")
	c := e.client()
	c.c.Info.Name = "ubuntu-test"
	put(t, c.root, "d/wsl-private.txt", "wsl only")
	os.MkdirAll(filepath.Join(c.root, "a"), 0o755)
	os.Symlink("shared.txt", filepath.Join(c.root, "a", "link"))
	c.sync()
	c.sync() // the second round sends the tree report the server asked for

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.c.ServeRequests(ctx)

	var ov struct {
		Folders []struct {
			ID           string
			AutoExcluded []struct{ Path, Reason, Origin string }
		}
		Connections []struct {
			ID, Name string
			Online   bool
		}
		Bindings []struct {
			ID, LocalPath               string
			WinCount, WslCount, InScope int
		}
	}
	p.call("GET", "/api/overview", nil, &ov)
	if len(ov.Connections) != 1 || ov.Connections[0].Name != "ubuntu-test" || !ov.Connections[0].Online {
		t.Fatalf("connections: %+v", ov.Connections)
	}
	if len(ov.Bindings) != 1 || ov.Bindings[0].LocalPath != c.root {
		t.Fatalf("bindings: %+v", ov.Bindings)
	}
	if ex := ov.Folders[0].AutoExcluded; len(ex) != 1 || ex[0].Path != "a/link" || ex[0].Reason != "symlink" || ex[0].Origin != "ubuntu-test" {
		t.Fatalf("auto exclusions: %+v", ex)
	}
	dev := ov.Connections[0].ID

	// The merged tree shows private items of both sides.
	_, _, tree := p.call("GET", "/api/tree?folder=proj&connection="+dev, nil, nil)
	for _, want := range []string{`"b/win-private.txt"`, `"d/wsl-private.txt"`, `"a/link","kind":"symlink"`} {
		if !strings.Contains(string(tree), want) {
			t.Errorf("tree lacks %s", want)
		}
	}

	// Content from both sides; the WSL one comes through the client.
	if code, h, b := p.call("GET", "/api/content?folder=proj&side=windows&path=b/win-private.txt", nil, nil); code != 200 || string(b) != "windows only" || h.Get("X-Fdsync-Size") != "12" {
		t.Fatalf("windows content: %d %q", code, b)
	}
	if code, _, b := p.call("GET", "/api/content?folder=proj&side=wsl&connection="+dev+"&path=d/wsl-private.txt", nil, nil); code != 200 || string(b) != "wsl only" {
		t.Fatalf("wsl content: %d %q", code, b)
	}
	// Reading from an offset (hex view paging), on both sides.
	if code, _, b := p.call("GET", "/api/content?folder=proj&side=windows&path=b/win-private.txt&offset=8&limit=4", nil, nil); code != 200 || string(b) != "only" {
		t.Fatalf("windows offset read: %d %q", code, b)
	}
	if code, _, b := p.call("GET", "/api/content?folder=proj&side=wsl&connection="+dev+"&path=d/wsl-private.txt&offset=4&limit=4", nil, nil); code != 200 || string(b) != "only" {
		t.Fatalf("wsl offset read: %d %q", code, b)
	}
	if code, _, _ := p.call("GET", "/api/content?folder=proj&side=windows&path=../../etc/passwd", nil, nil); code != 400 {
		t.Fatal("path escape accepted")
	}

	// Delete a private item on the WSL side.
	if code, _, b := p.call("POST", "/api/delete", map[string]any{"folder": "proj", "connection": dev, "side": "wsl", "paths": []string{"d/wsl-private.txt"}}, nil); code != 204 {
		t.Fatalf("delete on wsl: %d %s", code, b)
	}
	e.absent(c.root, "d/wsl-private.txt")

	// Delete an in-scope item on Windows: sync is cancelled first, the WSL copy stays.
	if code, _, b := p.call("POST", "/api/delete", map[string]any{"folder": "proj", "connection": dev, "side": "windows", "paths": []string{"a/shared.txt"}}, nil); code != 204 {
		t.Fatalf("delete on windows: %d %s", code, b)
	}
	time.Sleep(100 * time.Millisecond)
	c.sync()
	e.absent(e.win, "a/shared.txt")
	e.want(c.root, "a/shared.txt", "both")

	// Manual exclusion: the path stops syncing.
	p.call("POST", "/api/folders/proj/exclusions", map[string]any{"paths": []string{"a/skip"}}, nil)
	put(t, c.root, "a/skip/x.txt", "x")
	put(t, c.root, "a/keep.txt", "k")
	c.sync()
	e.absent(e.win, "a/skip")
	e.want(e.win, "a/keep.txt", "k")
	p.call("POST", "/api/folders/proj/exclusions", map[string]any{"paths": []string{"a/skip"}, "cancel": true}, nil)
	c.sync()
	e.want(e.win, "a/skip/x.txt", "x")

	var act []struct{ Code string }
	p.call("GET", "/api/activity", nil, &act)
	codes := map[string]bool{}
	for _, a := range act {
		codes[a.Code] = true
	}
	if !codes["received"] && !codes["receivedMany"] {
		t.Errorf("activity lacks received files (have %v)", codes)
	}
	for _, want := range []string{"connected", "exclSymlink", "adminDeleted", "syncCancelled", "exclManual", "exclCancelled"} {
		if !codes[want] {
			t.Errorf("activity lacks %s (have %v)", want, codes)
		}
	}
}

func TestPanelKeepBoth(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	put(t, e.win, "a/notes.md", "base")
	c := e.client()
	c.c.Info.Name = "Ubuntu"
	c.sync()
	put(t, e.win, "a/notes.md", "windows edit")
	put(t, c.root, "a/notes.md", "wsl edit")
	c.sync()
	var ov struct{ Conflicts []struct{ ID, Kind string } }
	p.call("GET", "/api/overview", nil, &ov)
	if len(ov.Conflicts) != 1 {
		t.Fatalf("conflicts: %+v", ov.Conflicts)
	}
	if code, _, b := p.call("POST", "/api/conflicts/resolve", map[string]any{"ids": []string{ov.Conflicts[0].ID}, "how": "both"}, nil); code != 204 {
		t.Fatalf("resolve: %d %s", code, b)
	}
	c.sync()
	c.sync()
	e.want(e.win, "a/notes.md", "windows edit")
	e.want(e.win, "a/notes.wsl-ubuntu.md", "wsl edit")
	e.want(c.root, "a/notes.md", "windows edit")
	e.want(c.root, "a/notes.wsl-ubuntu.md", "wsl edit")
}

var _ = server.Version

func TestPanelBrowseAndBind(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	c := e.client()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "code", "proj"), 0o755)
	os.WriteFile(filepath.Join(home, "notes.txt"), []byte("n"), 0o644)
	c.c.Root = home
	var bound []Binding
	c.c.OnBind = func(b Binding) error { bound = append(bound, b); return nil }
	c.sync()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.c.ServeRequests(ctx)
	dev := c.c.Device

	var ls struct {
		Path  string
		Dirs  []string
		Files int
	}
	if code, _, b := p.call("GET", "/api/connections/"+dev+"/dirs?path=", nil, &ls); code != 200 || ls.Path != home || len(ls.Dirs) != 1 || ls.Dirs[0] != "code" || ls.Files != 1 {
		t.Fatalf("list home: %d %+v %s", code, ls, b)
	}
	if code, _, _ := p.call("GET", "/api/connections/"+dev+"/dirs?path=/etc", nil, nil); code != 502 {
		t.Fatalf("listing outside the root should fail, got %d", code)
	}
	target := filepath.Join(home, "code", "proj")
	if code, _, b := p.call("POST", "/api/connections/"+dev+"/bindings", map[string]string{"folder": "other", "path": target}, nil); code != 204 {
		t.Fatalf("bind: %d %s", code, b)
	}
	if len(bound) != 1 || bound[0].Root != target {
		t.Fatalf("bound: %+v", bound)
	}
	// Already bound: the existing session holds folder "proj".
	if code, _, _ := p.call("POST", "/api/connections/"+dev+"/bindings", map[string]string{"folder": "proj", "path": target}, nil); code != 502 {
		t.Fatal("a second binding of the same folder should be refused")
	}
}

func TestPanelSettingsAndAddFolder(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	var st struct {
		Token             string
		TrashMB, TrashMax int
	}
	p.call("GET", "/api/settings", nil, &st)
	if st.Token != token || st.TrashMB != 50 || st.TrashMax != 50 {
		t.Fatalf("settings: %+v", st)
	}
	p.call("PUT", "/api/settings", map[string]int{"trashMB": 500}, nil) // more than allowed
	p.call("GET", "/api/settings", nil, &st)
	if st.TrashMB != 50 {
		t.Fatalf("trash limit raised to %d MB", st.TrashMB)
	}
	p.call("PUT", "/api/settings", map[string]int{"trashMB": 10}, nil)
	p.call("GET", "/api/settings", nil, &st)
	if st.TrashMB != 10 {
		t.Fatalf("trash limit %d, want 10", st.TrashMB)
	}

	// A new token takes effect at once.
	var nt struct{ Token string }
	p.call("POST", "/api/token", nil, &nt)
	if code := e.api("GET", "/folders", nil, nil); code != 401 {
		t.Fatalf("old token still accepted: %d", code)
	}

	// Add a sync folder while running.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644)
	if code, _, b := p.call("POST", "/api/folders", map[string]any{"id": "fresh", "path": dir, "rules": []string{"*.txt"}}, nil); code != 204 {
		t.Fatalf("add folder: %d %s", code, b)
	}
	var ov struct {
		Folders []struct {
			ID        string
			FileCount int
		}
	}
	p.call("GET", "/api/overview", nil, &ov)
	if len(ov.Folders) != 3 || ov.Folders[2].ID != "fresh" || ov.Folders[2].FileCount != 1 {
		t.Fatalf("folders: %+v", ov.Folders)
	}
	if code, _, _ := p.call("POST", "/api/folders", map[string]any{"id": "fresh", "path": dir}, nil); code != 400 {
		t.Fatal("duplicate folder id accepted")
	}
	var ls struct{ Dirs []string }
	if code, _, _ := p.call("GET", "/api/windirs?path="+filepath.Dir(dir), nil, &ls); code != 200 || len(ls.Dirs) == 0 {
		t.Fatalf("windirs: %d %+v", code, ls)
	}
}

// A plain folder (no exclusions, no conflicts, no clients) must not send
// null where the panel expects a list; that crashed the folder pages.
func TestPanelOverviewHasNoNullLists(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	put(t, e.win, "a/x.txt", "x")
	e.srv.ScanAll(context.Background())
	_, _, body := p.call("GET", "/api/overview", nil, nil)
	if strings.Contains(string(body), "null") {
		t.Fatalf("overview contains null: %s", body)
	}
	_, _, tree := p.call("GET", "/api/tree?folder=proj&connection=nobody", nil, nil)
	if strings.Contains(string(tree), "null") {
		t.Fatalf("tree contains null: %s", tree)
	}
}

func TestNewestWinsAndPause(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	if c, _, b := p.call("PUT", "/api/folders/proj/options", map[string]string{"conflictPolicy": "newest-wins"}, nil); c != 204 {
		t.Fatalf("set policy: %d %s", c, b)
	}
	put(t, e.win, "a/f.txt", "base")
	put(t, e.win, "a/g.txt", "base")
	c := e.client()
	c.sync()
	// f: WSL edits last → WSL wins. g: Windows edits last → Windows wins.
	put(t, e.win, "a/f.txt", "windows, older")
	put(t, c.root, "a/f.txt", "wsl, newer")
	put(t, c.root, "a/g.txt", "wsl, older")
	put(t, e.win, "a/g.txt", "windows, newer")
	c.sync()
	c.sync()
	e.want(e.win, "a/f.txt", "wsl, newer")
	e.want(c.root, "a/f.txt", "wsl, newer")
	e.want(e.win, "a/g.txt", "windows, newer")
	e.want(c.root, "a/g.txt", "windows, newer")
	if len(e.conflicts()) != 0 {
		t.Fatal("newest wins should leave nothing for the admin")
	}

	// Paused: nothing syncs; resumed: it catches up.
	p.call("PUT", "/api/folders/proj/options", map[string]bool{"paused": true}, nil)
	put(t, c.root, "a/while-paused.txt", "p")
	e.srv.ScanAll(context.Background())
	if _, err := c.SyncOnce(context.Background()); err == nil {
		t.Fatal("a paused folder should refuse to sync")
	}
	e.absent(e.win, "a/while-paused.txt")
	var ov struct {
		Folders []struct{ State, ConflictPolicy string }
	}
	p.call("GET", "/api/overview", nil, &ov)
	if ov.Folders[0].State != "paused" || ov.Folders[0].ConflictPolicy != "newest-wins" {
		t.Fatalf("overview: %+v", ov.Folders[0])
	}
	p.call("PUT", "/api/folders/proj/options", map[string]bool{"paused": false}, nil)
	c.sync()
	e.want(e.win, "a/while-paused.txt", "p")
}

// An alias the admin gives survives the client's hello (which reports its hostname).
func TestRenameConnection(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	c := e.client()
	c.c.Info.Name = "PC-202606231029"
	c.sync()
	dev := c.c.Device
	if code, _, b := p.call("PUT", "/api/connections/"+dev+"/name", map[string]string{"name": "Ubuntu (work)"}, nil); code != 204 {
		t.Fatalf("rename: %d %s", code, b)
	}
	c.sync() // hello again, still saying PC-202606231029
	var ov struct {
		Connections []struct{ Name, Hostname string }
	}
	p.call("GET", "/api/overview", nil, &ov)
	if len(ov.Connections) != 1 || ov.Connections[0].Name != "Ubuntu (work)" || ov.Connections[0].Hostname != "PC-202606231029" {
		t.Fatalf("connections: %+v", ov.Connections)
	}
	if code, _, _ := p.call("PUT", "/api/connections/"+dev+"/name", map[string]string{"name": "  "}, nil); code != 400 {
		t.Fatal("an empty name was accepted")
	}
	if code, _, _ := p.call("POST", "/api/folders/proj/rescan", nil, nil); code != 204 {
		t.Fatalf("rescan: %d", code)
	}
}

func TestPanelTrash(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	c := e.client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.c.ServeRequests(ctx)

	put(t, c.root, "a/f.txt", "v1")
	c.sync()
	put(t, c.root, "a/f.txt", "v22") // Windows keeps v1
	c.sync()
	os.Remove(filepath.Join(e.win, "a", "f.txt")) // WSL keeps v22
	c.sync()
	e.absent(c.root, "a/f.txt")

	var tr struct {
		Items []struct {
			Side, ConnectionID, FolderID, ID, Path string
			Size                                   int64
		}
		Unavailable []struct{ ConnectionID string }
	}
	if code, _, b := p.call("GET", "/api/trash", nil, &tr); code != 200 {
		t.Fatalf("trash: %d %s", code, b)
	}
	if len(tr.Items) != 2 || len(tr.Unavailable) != 0 {
		t.Fatalf("trash = %+v", tr)
	}
	got := map[string]string{}
	for _, it := range tr.Items {
		if it.Path != "a/f.txt" || it.FolderID != "proj" {
			t.Fatalf("item %+v", it)
		}
		q := "/api/content?folder=proj&side=" + it.Side + "&connection=" + it.ConnectionID + "&trash=" + it.ID
		code, _, b := p.call("GET", q, nil, nil)
		if code != 200 {
			t.Fatalf("read %s: %d %s", it.Side, code, b)
		}
		got[it.Side] = string(b)
	}
	if got["windows"] != "v1" || got["wsl"] != "v22" {
		t.Fatalf("kept versions = %v", got)
	}
	if code, _, _ := p.call("GET", "/api/content?folder=proj&side=windows&trash=20260101-000000.000/../../x", nil, nil); code != 404 {
		t.Fatalf("bad trash id: %d", code)
	}

	// Restoring the WSL version brings it back on both sides.
	for _, it := range tr.Items {
		if it.Side == "wsl" {
			if code, _, b := p.call("POST", "/api/trash/restore", map[string]any{"folder": "proj", "side": "wsl", "connection": it.ConnectionID, "id": it.ID, "path": it.Path}, nil); code != 204 {
				t.Fatalf("restore: %d %s", code, b)
			}
		}
	}
	e.want(c.root, "a/f.txt", "v22")
	c.sync()
	e.want(e.win, "a/f.txt", "v22")

	// And the Windows one, over it (v22 goes to the Windows trash).
	for _, it := range tr.Items {
		if it.Side == "windows" {
			if code, _, b := p.call("POST", "/api/trash/restore", map[string]any{"folder": "proj", "side": "windows", "id": it.ID, "path": it.Path}, nil); code != 204 {
				t.Fatalf("restore: %d %s", code, b)
			}
		}
	}
	c.sync()
	e.want(c.root, "a/f.txt", "v1")
	p.call("GET", "/api/trash", nil, &tr)
	if len(tr.Items) != 4 {
		t.Fatalf("after restores: %d items, want 4 (v1, v22 twice, v22 overwritten on WSL)", len(tr.Items))
	}
}

func TestPauseAllAndFolderName(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	c := e.client()
	c.sync()

	if code, _, b := p.call("PUT", "/api/pause", map[string]int{"minutes": 10}, nil); code != 204 {
		t.Fatalf("pause: %d %s", code, b)
	}
	put(t, c.root, "a/x.txt", "x")
	if _, err := c.SyncOnce(context.Background()); err == nil {
		t.Fatal("paused: sync should be refused")
	}
	var ov struct {
		Pause struct {
			Paused bool
			Until  *time.Time
		}
		Folders []struct{ ID, Name, State string }
	}
	p.call("GET", "/api/overview", nil, &ov)
	if !ov.Pause.Paused || ov.Pause.Until == nil || time.Until(*ov.Pause.Until) < 9*time.Minute || ov.Folders[0].State != "paused" {
		t.Fatalf("overview while paused: %+v", ov)
	}
	// It ends by itself at the set time.
	e.srv.SetPauseAll(true, time.Now().Add(50*time.Millisecond), false)
	time.Sleep(200 * time.Millisecond)
	c.sync()
	e.want(e.win, "a/x.txt", "x")
	p.call("GET", "/api/overview", nil, &ov)
	if ov.Pause.Paused {
		t.Fatal("the pause should have ended")
	}
	// Until resumed, and resume.
	p.call("PUT", "/api/pause", map[string]int{"minutes": -1}, nil)
	if _, err := c.SyncOnce(context.Background()); err == nil {
		t.Fatal("paused: sync should be refused")
	}
	p.call("PUT", "/api/pause", map[string]int{"minutes": 0}, nil)
	c.sync()

	// A display name; the ID stays what clients use.
	if code, _, b := p.call("PUT", "/api/folders/proj/options", map[string]string{"name": "  Work projects "}, nil); code != 204 {
		t.Fatalf("name: %d %s", code, b)
	}
	p.call("GET", "/api/overview", nil, &ov)
	if ov.Folders[0].ID != "proj" || ov.Folders[0].Name != "Work projects" {
		t.Fatalf("folders: %+v", ov.Folders)
	}
	p.call("PUT", "/api/folders/proj/options", map[string]string{"name": ""}, nil)
	p.call("GET", "/api/overview", nil, &ov)
	if ov.Folders[0].Name != "proj" {
		t.Fatalf("cleared name: %+v", ov.Folders[0])
	}
	var act []struct{ Code string }
	p.call("GET", "/api/activity", nil, &act)
	codes := map[string]bool{}
	for _, a := range act {
		codes[a.Code] = true
	}
	for _, want := range []string{"allPausedFor", "allResumedAuto", "allPaused", "allResumed", "folderRenamed"} {
		if !codes[want] {
			t.Errorf("activity lacks %s", want)
		}
	}
}

type fakeAutostart struct{ on bool }

func (f *fakeAutostart) Enabled() (bool, error) { return f.on, nil }
func (f *fakeAutostart) Set(on bool) error      { f.on = on; return nil }

func TestAutostartSetting(t *testing.T) {
	fa := &fakeAutostart{}
	e := newEnvWith(t, func(c *server.Config) { c.Autostart = fa }, "a/")
	p := newPanel(t, e)
	p.login()
	var st struct{ Autostart *struct{ Enabled bool } }
	p.call("GET", "/api/settings", nil, &st)
	if st.Autostart == nil || st.Autostart.Enabled {
		t.Fatalf("settings: %+v", st.Autostart)
	}
	if code, _, b := p.call("PUT", "/api/settings", map[string]bool{"autostart": true}, nil); code != 204 || !fa.on {
		t.Fatalf("enable: %d %s", code, b)
	}
	// Without Windows support the setting is absent and refused.
	e2 := newEnv(t, "a/")
	p2 := newPanel(t, e2)
	p2.login()
	st.Autostart = nil
	p2.call("GET", "/api/settings", nil, &st)
	if st.Autostart != nil {
		t.Fatal("autostart offered without support")
	}
	if code, _, _ := p2.call("PUT", "/api/settings", map[string]bool{"autostart": true}, nil); code != 400 {
		t.Fatalf("unsupported autostart: %d", code)
	}
}

func TestOpenInVSCodeChecks(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	if code, _, _ := p.call("POST", "/api/vscode", map[string]string{"folder": "proj", "path": "../x"}, nil); code != 400 {
		t.Fatalf("path escape: %d", code)
	}
	if code, _, _ := p.call("POST", "/api/vscode", map[string]string{"folder": "nope"}, nil); code != 404 {
		t.Fatalf("unknown folder: %d", code)
	}
	if runtime.GOOS != "windows" {
		if code, _, _ := p.call("POST", "/api/vscode", map[string]string{"folder": "proj"}, nil); code != 501 {
			t.Fatalf("not windows: %d", code)
		}
	}
}

func TestHiddenInBrowser(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	put(t, e.win, "a/x.txt", "x")
	hide := func(side, conn string, paths []string, on bool) int {
		code, _, _ := p.call("PUT", "/api/folders/proj/hidden", map[string]any{"side": side, "connection": conn, "paths": paths, "hidden": on}, nil)
		return code
	}
	if c := hide("windows", "", []string{"a/x.txt", "b"}, true); c != 204 {
		t.Fatalf("hide: %d", c)
	}
	if c := hide("wsl", "c1", []string{"d"}, true); c != 204 {
		t.Fatalf("hide wsl: %d", c)
	}
	hide("windows", "", []string{"b"}, false)
	if hide("windows", "", []string{"../x"}, true) != 400 || hide("wsl", "", []string{"d"}, true) != 400 {
		t.Fatal("bad requests accepted")
	}
	var ov struct {
		Folders []struct {
			ID     string
			Hidden map[string][]string
		}
	}
	p.call("GET", "/api/overview", nil, &ov)
	h := ov.Folders[0].Hidden
	if len(h["windows"]) != 1 || h["windows"][0] != "a/x.txt" || len(h["wsl:c1"]) != 1 {
		t.Fatalf("hidden = %v", h)
	}
	// Only a view: the file still syncs.
	c := e.client()
	c.sync()
	e.want(c.root, "a/x.txt", "x")
}

// A conflict on a path that stops syncing (excluded, or sync cancelled) is dropped.
func TestConflictLeavesWithTheScope(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	c := e.client()
	for _, f := range []string{"a/x.txt", "a/y.txt"} {
		put(t, e.win, f, "base")
	}
	c.sync()
	for _, f := range []string{"a/x.txt", "a/y.txt"} {
		put(t, e.win, f, "windows edit")
		put(t, c.root, f, "wsl edit")
	}
	c.sync()
	if n := len(e.conflicts()); n != 2 {
		t.Fatalf("%d conflicts, want 2", n)
	}
	p.call("POST", "/api/folders/proj/exclusions", map[string]any{"paths": []string{"a/x.txt"}}, nil)
	if cs := e.conflicts(); len(cs) != 1 || cs[0].Path != "a/y.txt" {
		t.Fatalf("after excluding x: %+v", cs)
	}
	p.call("PUT", "/api/folders/proj/rules", map[string]any{"rules": []string{"a/", "!a/y.txt"}}, nil)
	if n := len(e.conflicts()); n != 0 {
		t.Fatalf("after cancelling y: %d conflicts", n)
	}
	// Both sides kept their own versions.
	e.want(e.win, "a/x.txt", "windows edit")
	e.want(c.root, "a/x.txt", "wsl edit")
	// Back in scope with different content: the conflict comes back.
	p.call("PUT", "/api/folders/proj/rules", map[string]any{"rules": []string{"a/"}}, nil)
	c.sync()
	if cs := e.conflicts(); len(cs) != 1 || cs[0].Path != "a/y.txt" {
		t.Fatalf("y back in scope: %+v", cs)
	}
}
