package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"fdsync/internal/protocol"
)

// Pairing follows the OAuth 2.0 Device Authorization Grant (RFC 8628):
// the client asks for a user code, the admin approves the code in the
// panel, the client polls and receives an access token (1 h) and a refresh
// token. Refresh tokens are stored as SHA-256 hashes only; access tokens
// live in memory (after a restart, clients refresh).

const (
	pairTTL        = 10 * time.Minute
	pairInterval   = 2 // seconds between polls
	accessTTL      = time.Hour
	maxPending     = 20
	userCodeLetter = "BCDFGHJKLMNPQRSTVWXZ" // no vowels: no words, no 0/O 1/I confusion
)

type pairRequest struct {
	UserCode   string    `json:"code"`
	Hostname   string    `json:"hostname"`
	Distro     string    `json:"distro"`
	Address    string    `json:"address"`
	Requested  time.Time `json:"requestedAt"`
	Expires    time.Time `json:"expiresAt"`
	deviceID   string
	deviceHash string // sha256 of the device code
	approved   string // connection name once approved
	denied     bool
}

type accessToken struct {
	device  string
	expires time.Time
}

type pairing struct {
	mu     sync.Mutex
	reqs   map[string]*pairRequest // user code → request
	access map[string]accessToken  // sha256(token) → device
}

func newPairing() *pairing {
	return &pairing{reqs: map[string]*pairRequest{}, access: map[string]accessToken{}}
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func secret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func userCode() string {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		if i == 4 {
			b.WriteByte('-')
		}
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(userCodeLetter))))
		b.WriteByte(userCodeLetter[n.Int64()])
	}
	return b.String()
}

func (p *pairing) gcLocked() {
	now := time.Now()
	for k, r := range p.reqs {
		if now.After(r.Expires) {
			delete(p.reqs, k)
		}
	}
	for k, a := range p.access {
		if now.After(a.expires) {
			delete(p.access, k)
		}
	}
}

// pending lists open requests for the panel.
func (p *pairing) pending() []pairRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gcLocked()
	out := []pairRequest{}
	for _, r := range p.reqs {
		if r.approved == "" && !r.denied {
			out = append(out, *r)
		}
	}
	return out
}

func (p *pairing) issueAccess(device string) (string, time.Time) {
	tok := secret()
	exp := time.Now().Add(accessTTL)
	p.mu.Lock()
	p.access[hashToken(tok)] = accessToken{device, exp}
	p.mu.Unlock()
	return tok, exp
}

// deviceFor resolves an access token.
func (p *pairing) deviceFor(tok string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.access[hashToken(tok)]
	if !ok || time.Now().After(a.expires) {
		return "", false
	}
	return a.device, true
}

func (p *pairing) revokeDevice(device string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, a := range p.access {
		if a.device == device {
			delete(p.access, k)
		}
	}
}

// ---------------------------------------------------------------- client endpoints (no auth)

func writeOAuthErr(w http.ResponseWriter, code int, e string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": e})
}

func (s *Server) handlePairStart(w http.ResponseWriter, r *http.Request) {
	var req protocol.PairStart
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !validDevice.MatchString(req.Device) || req.Device == s.device {
		writeErr(w, http.StatusBadRequest, errors.New("invalid device id"))
		return
	}
	p := s.pair
	p.mu.Lock()
	p.gcLocked()
	if len(p.reqs) >= maxPending {
		p.mu.Unlock()
		writeErr(w, http.StatusTooManyRequests, errors.New("too many pairing requests waiting; try again later"))
		return
	}
	dc := secret()
	pr := &pairRequest{
		UserCode: userCode(), Hostname: trimTo(req.Hostname, 64), Distro: trimTo(req.Distro, 80), Address: hostOf(r.RemoteAddr),
		Requested: time.Now(), Expires: time.Now().Add(pairTTL), deviceID: req.Device, deviceHash: hashToken(dc),
	}
	p.reqs[pr.UserCode] = pr
	p.mu.Unlock()
	s.ev.add(Event{Kind: "pair", Code: "pairRequest", Params: map[string]any{"host": pr.Hostname, "address": pr.Address, "code": pr.UserCode}})
	writeJSON(w, protocol.PairStarted{
		DeviceCode: dc, UserCode: pr.UserCode, ExpiresIn: int(pairTTL.Seconds()), Interval: pairInterval,
		VerificationURI: "http://" + s.cfg.PanelListen + "/pair?code=" + pr.UserCode,
	})
}

func trimTo(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// handlePairToken is the client's poll (RFC 8628 §3.4/3.5 error codes).
func (s *Server) handlePairToken(w http.ResponseWriter, r *http.Request) {
	var req protocol.PairPoll
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	h := hashToken(req.DeviceCode)
	p := s.pair
	p.mu.Lock()
	p.gcLocked()
	var pr *pairRequest
	for _, x := range p.reqs {
		if x.deviceHash == h {
			pr = x
		}
	}
	switch {
	case pr == nil:
		p.mu.Unlock()
		writeOAuthErr(w, http.StatusBadRequest, "expired_token")
		return
	case pr.denied:
		delete(p.reqs, pr.UserCode)
		p.mu.Unlock()
		writeOAuthErr(w, http.StatusBadRequest, "access_denied")
		return
	case pr.approved == "":
		p.mu.Unlock()
		writeOAuthErr(w, http.StatusBadRequest, "authorization_pending")
		return
	}
	delete(p.reqs, pr.UserCode) // a device code works once
	p.mu.Unlock()

	refresh := secret()
	s.reg.pair(pr.deviceID, pr.approved, pr.Distro, pr.Address, hashToken(refresh))
	access, exp := s.pair.issueAccess(pr.deviceID)
	writeJSON(w, protocol.Tokens{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(time.Until(exp).Seconds()), Device: pr.deviceID, Name: pr.approved})
}

// handleRefresh trades a refresh token for a new access token; the refresh
// token rotates.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req protocol.RefreshRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	next := secret()
	dev, ok := s.reg.rotateRefresh(hashToken(req.RefreshToken), hashToken(next))
	if !ok {
		time.Sleep(300 * time.Millisecond)
		writeOAuthErr(w, http.StatusUnauthorized, "invalid_grant")
		return
	}
	access, exp := s.pair.issueAccess(dev)
	writeJSON(w, protocol.Tokens{AccessToken: access, RefreshToken: next, ExpiresIn: int(time.Until(exp).Seconds()), Device: dev, Name: s.reg.name(dev)})
}

// ---------------------------------------------------------------- panel endpoints

func (s *Server) apiPairApprove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 64 {
		writeErr(w, http.StatusBadRequest, errors.New("give the connection a name (up to 64 characters)"))
		return
	}
	p := s.pair
	p.mu.Lock()
	pr := p.reqs[strings.ToUpper(strings.TrimSpace(req.Code))]
	if pr != nil && time.Now().Before(pr.Expires) && !pr.denied {
		pr.approved = name
	}
	p.mu.Unlock()
	if pr == nil || pr.approved != name {
		writeErr(w, http.StatusNotFound, errors.New("no pending request with this code (it may have expired)"))
		return
	}
	s.ev.add(Event{Kind: "pair", Admin: true, ConnectionID: pr.deviceID, Code: "paired", Params: map[string]any{"name": name, "address": pr.Address}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiPairDeny(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	p := s.pair
	p.mu.Lock()
	if pr := p.reqs[req.Code]; pr != nil {
		pr.denied = true
	}
	p.mu.Unlock()
	s.ev.add(Event{Kind: "pair", Admin: true, Level: "warn", Code: "pairDenied", Params: map[string]any{"code": req.Code}})
	w.WriteHeader(http.StatusNoContent)
}

// apiRevoke ends a connection: its tokens stop working at once.
func (s *Server) apiRevoke(w http.ResponseWriter, r *http.Request) {
	dev := r.PathValue("id")
	name := s.reg.name(dev)
	s.reg.revoke(dev)
	s.pair.revokeDevice(dev)
	s.ev.add(Event{Kind: "pair", Admin: true, Level: "warn", ConnectionID: dev, Code: "revoked", Params: map[string]any{"name": name}})
	w.WriteHeader(http.StatusNoContent)
}
