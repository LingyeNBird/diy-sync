package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Event is one line in the panel's activity log. Code names a message the
// panel translates (activity.events.<code>), with Params filled in.
type Event struct {
	ID           string         `json:"id"`
	Time         time.Time      `json:"time"`
	Level        string         `json:"level"` // info | warn | error
	Kind         string         `json:"kind"`  // sync | delete | conflict | resolve | scope | exclude | connect | disconnect | pair
	FolderID     string         `json:"folderId,omitempty"`
	ConnectionID string         `json:"connectionId,omitempty"`
	Path         string         `json:"path,omitempty"`
	Code         string         `json:"code"`
	Params       map[string]any `json:"params,omitempty"`
	Admin        bool           `json:"admin,omitempty"` // an admin action, also written to the audit log
}

const (
	eventRing    = 1000
	auditMaxSize = 5 << 20 / 2 // two files of 2.5 MB: the audit log never exceeds 5 MB
)

// activity keeps recent events in memory and appends admin actions to
// audit.log (rotated to audit.log.1).
type activity struct {
	mu     sync.Mutex
	seq    uint64
	events []Event // oldest first, at most eventRing
	audit  string  // path of audit.log; "" = none
}

func newActivity(dataDir string) *activity {
	a := &activity{}
	if dataDir != "" {
		a.audit = filepath.Join(dataDir, "audit.log")
	}
	return a
}

func (a *activity) add(e Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	e.ID = strconv.FormatUint(a.seq, 10)
	e.Time = time.Now()
	if e.Level == "" {
		e.Level = "info"
	}
	a.events = append(a.events, e)
	if len(a.events) > eventRing {
		a.events = append([]Event(nil), a.events[len(a.events)-eventRing:]...)
	}
	if e.Admin && a.audit != "" {
		a.appendAudit(e)
	}
}

func (a *activity) appendAudit(e Event) {
	if fi, err := os.Stat(a.audit); err == nil && fi.Size() > auditMaxSize {
		_ = os.Rename(a.audit, a.audit+".1")
	}
	f, err := os.OpenFile(a.audit, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, _ = f.Write(append(b, '\n'))
}

// list returns events newest first.
func (a *activity) list() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Event, len(a.events))
	for i, e := range a.events {
		out[len(a.events)-1-i] = e
	}
	return out
}
