package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"fdsync/internal/update"
)

// updateCheckTTL is how long a check's answer is reused before the panel
// asks GitHub again (unauthenticated API calls are limited to 60 an hour).
const updateCheckTTL = time.Hour

// updates remembers the last release check and whether an upgrade runs.
type updates struct {
	mu         sync.Mutex
	checked    time.Time
	latest     *update.Release
	err        string
	installing bool
}

type updateOut struct {
	Current    string    `json:"current"`
	Latest     string    `json:"latest,omitempty"`
	Newer      bool      `json:"newer"`
	URL        string    `json:"url,omitempty"`
	Notes      string    `json:"notes,omitempty"`
	Published  time.Time `json:"published,omitzero"`
	CheckedAt  time.Time `json:"checkedAt,omitzero"`
	Error      string    `json:"error,omitempty"`
	CanUpgrade bool      `json:"canUpgrade"` // the release has this platform's file and the exe can restart itself
	Installing bool      `json:"installing"`
}

func (s *Server) updateState() updateOut {
	u := &s.upd
	u.mu.Lock()
	defer u.mu.Unlock()
	out := updateOut{Current: update.Version, CheckedAt: u.checked, Error: u.err, Installing: u.installing}
	if r := u.latest; r != nil {
		out.Latest, out.URL, out.Notes, out.Published = r.Version, r.URL, r.Notes, r.Published
		out.Newer = update.Newer(r.Version, update.Version)
		out.CanUpgrade = out.Newer && r.HasAsset() && s.cfg.Restart != nil
	}
	return out
}

// apiUpdate reports the newest release, asking GitHub when the last check
// is older than an hour (or ?refresh=1).
func (s *Server) apiUpdate(w http.ResponseWriter, r *http.Request) {
	u := &s.upd
	u.mu.Lock()
	stale := time.Since(u.checked) > updateCheckTTL || r.URL.Query().Get("refresh") == "1"
	u.mu.Unlock()
	if stale {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		rel, err := update.Latest(ctx, "fdsync-server")
		cancel()
		u.mu.Lock()
		u.checked = time.Now()
		if err != nil {
			u.err = err.Error()
			s.log.Warn("update check", "err", err)
		} else {
			u.err, u.latest = "", rel
		}
		u.mu.Unlock()
	}
	writeJSON(w, s.updateState())
}

// apiUpgrade installs the newest release over this exe, then restarts:
// the server stops, and the new version starts with the same flags.
func (s *Server) apiUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Restart == nil {
		writeErr(w, http.StatusBadRequest, errors.New("this server cannot restart itself"))
		return
	}
	u := &s.upd
	u.mu.Lock()
	rel := u.latest
	if u.installing {
		u.mu.Unlock()
		writeErr(w, http.StatusConflict, errors.New("an upgrade is already running"))
		return
	}
	if rel == nil || !update.Newer(rel.Version, update.Version) {
		u.mu.Unlock()
		writeErr(w, http.StatusBadRequest, errors.New("no newer version; check for updates first"))
		return
	}
	u.installing = true
	u.mu.Unlock()

	s.log.Info("upgrading", "from", update.Version, "to", rel.Version)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	err := update.Install(ctx, rel)
	cancel()
	if err != nil {
		u.mu.Lock()
		u.installing = false
		u.mu.Unlock()
		s.log.Error("upgrade", "err", err)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.ev.add(Event{Kind: "pair", Admin: true, Code: "upgraded", Params: map[string]any{"from": update.Version, "to": rel.Version}})
	writeJSON(w, map[string]string{"version": rel.Version})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(500 * time.Millisecond) // let the answer reach the panel
		s.cfg.Restart()
	}()
}
