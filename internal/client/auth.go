package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fdsync/internal/protocol"
)

// Credentials are what `fdsync connect` stores (~/.config/fdsync/credentials.json, mode 0600).
type Credentials struct {
	Server       string `json:"server"`      // https://host:port
	Fingerprint  string `json:"fingerprint"` // the server certificate, pinned on first connect
	Device       string `json:"device"`
	Name         string `json:"name"` // connection name chosen by the admin
	RefreshToken string `json:"refreshToken"`
	// ViaGateway: Server is the Windows host's address as WSL sees it, which
	// can change; ServerURL follows it.
	ViaGateway bool `json:"viaGateway,omitempty"`
}

func LoadCredentials(path string) (*Credentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Credentials
	return &c, json.Unmarshal(b, &c)
}

func (c *Credentials) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrFingerprint means the server's certificate is not the pinned one.
var ErrFingerprint = errors.New("server certificate does not match the pinned fingerprint")

// NewTransport is the client's HTTP transport: nothing may hang forever.
// A dead connection is found by HTTP/2 pings, and a server that doesn't
// answer within 2 minutes fails the request (it is retried).
func NewTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute, // the long polls answer within 30 s
		TLSHandshakeTimeout:   15 * time.Second,
		HTTP2:                 &http.HTTP2Config{SendPingTimeout: 20 * time.Second, PingTimeout: 15 * time.Second},
	}
}

// PinnedTransport trusts exactly one certificate (by fingerprint), like SSH.
// Names and CAs are not checked: the pin is the trust.
func PinnedTransport(fingerprint string) *http.Transport {
	t := NewTransport()
	t.ForceAttemptHTTP2 = true // a custom TLS config turns HTTP/2 off unless asked
	t.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // replaced by the pin check below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || protocol.Fingerprint(cs.PeerCertificates[0].Raw) != fingerprint {
				return ErrFingerprint
			}
			return nil
		},
	}
	return t
}

// FetchFingerprint connects once without trusting anything, to show the
// fingerprint to the user before pinning it.
func FetchFingerprint(ctx context.Context, addr string) (string, error) {
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("the server sent no certificate")
	}
	return protocol.Fingerprint(certs[0].Raw), nil
}

// TokenSource hands out access tokens, refreshing them with the refresh
// token (which rotates; the new one is saved at once).
type TokenSource struct {
	Creds   *Credentials
	Path    string // where Creds are saved after a rotation
	HTTP    *http.Client
	BaseURL string

	mu      sync.Mutex
	access  string
	expires time.Time
}

// Token returns a valid access token.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.access != "" && time.Until(t.expires) > time.Minute {
		return t.access, nil
	}
	body, _ := json.Marshal(protocol.RefreshRequest{RefreshToken: t.Creds.RefreshToken})
	req, err := http.NewRequestWithContext(ctx, "POST", t.BaseURL+protocol.APIPrefix+"/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", ErrRevoked
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token refresh: %s", resp.Status)
	}
	var tok protocol.Tokens
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	t.Creds.RefreshToken = tok.RefreshToken
	if t.Path != "" {
		if err := t.Creds.Save(t.Path); err != nil {
			return "", fmt.Errorf("save rotated refresh token: %w", err)
		}
	}
	t.access, t.expires = tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second)
	return t.access, nil
}

// Invalidate drops the cached access token (after a 401).
func (t *TokenSource) Invalidate() {
	t.mu.Lock()
	t.access = ""
	t.mu.Unlock()
}

// ErrRevoked: the server no longer accepts this client's refresh token.
var ErrRevoked = errors.New("the server revoked this client (or forgot it); pair again with fdsync connect")

// Pair runs the device flow: it asks for a user code, calls show (so the
// user can approve it in the panel), and polls until approved, denied or
// expired.
func Pair(ctx context.Context, baseURL string, hc *http.Client, start protocol.PairStart, show func(protocol.PairStarted)) (*protocol.Tokens, error) {
	post := func(path string, in, out any) (int, error) {
		b, _ := json.Marshal(in)
		req, err := http.NewRequestWithContext(ctx, "POST", baseURL+protocol.APIPrefix+path, bytes.NewReader(b))
		if err != nil {
			return 0, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	var st protocol.PairStarted
	if code, err := post("/pair/start", start, &st); err != nil || code != http.StatusOK {
		if err == nil {
			err = fmt.Errorf("server answered %d", code)
		}
		return nil, fmt.Errorf("start pairing: %w", err)
	}
	show(st)
	interval := time.Duration(max(st.Interval, 1)) * time.Second
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		var res struct {
			protocol.Tokens
			Error string `json:"error"`
		}
		code, err := post("/pair/token", protocol.PairPoll{DeviceCode: st.DeviceCode}, &res)
		if err != nil {
			return nil, err
		}
		switch {
		case code == http.StatusOK:
			return &res.Tokens, nil
		case res.Error == "authorization_pending":
			continue
		case res.Error == "access_denied":
			return nil, errors.New("the request was denied in the panel")
		case res.Error == "expired_token":
			return nil, errors.New("the code expired before it was approved; run fdsync connect again")
		default:
			return nil, fmt.Errorf("pairing failed: %d %s", code, res.Error)
		}
	}
}
