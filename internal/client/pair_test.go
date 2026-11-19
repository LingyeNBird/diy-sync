package client

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fdsync/internal/protocol"
)

// The whole pairing flow over real TLS: pin, code, approval in the panel,
// tokens, sync with them, refresh rotation, revoke.
func TestPairingOverTLS(t *testing.T) {
	e := newEnv(t, "a/")
	ts := httptest.NewUnstartedServer(e.srv.Handler())
	ts.TLS = e.srv.TLSConfig()
	ts.StartTLS()
	t.Cleanup(ts.Close)
	addr := strings.TrimPrefix(ts.URL, "https://")
	p := newPanel(t, e)
	p.login()
	ctx := context.Background()

	fp, err := FetchFingerprint(ctx, addr)
	if err != nil || fp != e.srv.Fingerprint() || !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("fingerprint %q vs %q: %v", fp, e.srv.Fingerprint(), err)
	}
	// A wrong pin refuses the connection.
	bad := &http.Client{Transport: PinnedTransport("SHA256:wrong")}
	if _, err := bad.Get(ts.URL + protocol.APIPrefix + "/folders"); !errors.Is(err, ErrFingerprint) {
		t.Fatalf("wrong pin: %v", err)
	}

	hc := &http.Client{Transport: PinnedTransport(fp)}
	codes := make(chan string, 1)
	type result struct {
		tok *protocol.Tokens
		err error
	}
	done := make(chan result, 1)
	go func() {
		tok, err := Pair(ctx, ts.URL, hc, protocol.PairStart{Device: "c-paired", Hostname: "ubuntu-box", Distro: "Ubuntu 24.04"},
			func(st protocol.PairStarted) { codes <- st.UserCode })
		done <- result{tok, err}
	}()
	code := <-codes
	// The panel lists the request; the admin approves it under a name.
	var ov struct {
		Pairing []struct{ Code, Hostname string }
	}
	p.call("GET", "/api/overview", nil, &ov)
	if len(ov.Pairing) != 1 || ov.Pairing[0].Code != code || ov.Pairing[0].Hostname != "ubuntu-box" {
		t.Fatalf("pending: %+v", ov.Pairing)
	}
	if c, _, b := p.call("POST", "/api/pair/approve", map[string]string{"code": code, "name": "Ubuntu laptop"}, nil); c != 204 {
		t.Fatalf("approve: %d %s", c, b)
	}
	r := <-done
	if r.err != nil || r.tok.Name != "Ubuntu laptop" || r.tok.Device != "c-paired" {
		t.Fatalf("pair: %+v %v", r.tok, r.err)
	}

	// A client with these credentials syncs (no shared token at all).
	creds := &Credentials{Server: ts.URL, Fingerprint: fp, Device: r.tok.Device, RefreshToken: r.tok.RefreshToken}
	credPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := creds.Save(credPath); err != nil {
		t.Fatal(err)
	}
	cl := &Client{BaseURL: ts.URL, Device: creds.Device, HTTP: hc, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tokens: &TokenSource{Creds: creds, Path: credPath, HTTP: hc, BaseURL: ts.URL}}
	root := t.TempDir()
	put(t, e.win, "a/x.txt", "over tls")
	e.srv.ScanAll(ctx)
	s, err := cl.Open(Binding{Folder: "proj", Root: root}, filepath.Join(t.TempDir(), "w"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	e.want(root, "a/x.txt", "over tls")

	// Refresh tokens rotate: the first one no longer works (it was used above).
	if _, err := (&TokenSource{Creds: &Credentials{RefreshToken: r.tok.RefreshToken}, HTTP: hc, BaseURL: ts.URL}).Token(ctx); !errors.Is(err, ErrRevoked) {
		t.Fatalf("old refresh token still works: %v", err)
	}
	saved, _ := LoadCredentials(credPath)
	if saved.RefreshToken == r.tok.RefreshToken {
		t.Fatal("rotated refresh token was not saved")
	}

	// Revoke in the panel: the client is out, and can't come back with the shared token.
	p.call("POST", "/api/connections/c-paired/revoke", nil, nil)
	if _, err := s.SyncOnce(ctx); err == nil {
		t.Fatal("revoked client still syncs")
	}
	cl.Tokens.Invalidate()
	if _, err := cl.Tokens.Token(ctx); !errors.Is(err, ErrRevoked) {
		t.Fatalf("refresh after revoke: %v", err)
	}
	req, _ := http.NewRequest("GET", ts.URL+protocol.APIPrefix+"/folders", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(protocol.HeaderDevice, "c-paired")
	if resp, _ := hc.Do(req); resp.StatusCode != 401 {
		t.Fatalf("revoked device with the shared token: %d", resp.StatusCode)
	}
}

func TestPairingDeniedAndSharedTokenOff(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	codes := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := Pair(ctx, e.hs.URL, e.hs.Client(), protocol.PairStart{Device: "c-deny"}, func(st protocol.PairStarted) { codes <- st.UserCode })
		done <- err
	}()
	p.call("POST", "/api/pair/deny", map[string]string{"code": <-codes}, nil)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("denied pairing: %v", err)
	}

	// Turning the shared token off locks token clients out.
	if code := e.api("GET", "/folders", nil, nil); code != 200 {
		t.Fatalf("shared token before: %d", code)
	}
	p.call("PUT", "/api/settings", map[string]bool{"allowToken": false}, nil)
	if code := e.api("GET", "/folders", nil, nil); code != 401 {
		t.Fatalf("shared token after turning it off: %d", code)
	}
}

// The name chosen when approving survives the client's hello (which reports its hostname).
func TestPairedNameSticks(t *testing.T) {
	e := newEnv(t, "a/")
	ts := httptest.NewUnstartedServer(e.srv.Handler())
	ts.TLS = e.srv.TLSConfig()
	ts.StartTLS()
	t.Cleanup(ts.Close)
	p := newPanel(t, e)
	p.login()
	ctx := context.Background()
	hc := &http.Client{Transport: PinnedTransport(e.srv.Fingerprint())}
	codes := make(chan string, 1)
	done := make(chan *protocol.Tokens, 1)
	go func() {
		tok, _ := Pair(ctx, ts.URL, hc, protocol.PairStart{Device: "c-named", Hostname: "PC-123"}, func(st protocol.PairStarted) { codes <- st.UserCode })
		done <- tok
	}()
	p.call("POST", "/api/pair/approve", map[string]string{"code": <-codes, "name": "Work laptop"}, nil)
	tok := <-done
	creds := &Credentials{Server: ts.URL, Device: tok.Device, RefreshToken: tok.RefreshToken}
	cl := &Client{BaseURL: ts.URL, Device: tok.Device, HTTP: hc, Info: protocol.Hello{Name: "PC-123"},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Tokens: &TokenSource{Creds: creds, HTTP: hc, BaseURL: ts.URL}}
	s, _ := cl.Open(Binding{Folder: "proj", Root: t.TempDir()}, filepath.Join(t.TempDir(), "w"))
	defer s.Close()
	if _, err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var ov struct {
		Connections []struct {
			Name   string
			Paired bool
		}
	}
	p.call("GET", "/api/overview", nil, &ov)
	if len(ov.Connections) != 1 || ov.Connections[0].Name != "Work laptop" || !ov.Connections[0].Paired {
		t.Fatalf("connections: %+v", ov.Connections)
	}
}
