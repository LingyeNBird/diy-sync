package server

import (
	"time"
)

// allPaused reports whether every folder is paused (Pause all).
func (s *Server) allPaused() bool {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	return s.pauseAll && (s.pauseUntil.IsZero() || time.Now().Before(s.pauseUntil))
}

// pauseState is what the panel shows: paused, and until when (zero = until resumed).
func (s *Server) pauseState() (bool, time.Time) {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	return s.pauseAll, s.pauseUntil
}

// armPause sets the global pause in memory (no event, no save); a past
// until resumes at once.
func (s *Server) armPause(until time.Time) {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if s.pauseTimer != nil {
		s.pauseTimer.Stop()
		s.pauseTimer = nil
	}
	s.pauseAll, s.pauseUntil = true, until
	if !until.IsZero() {
		s.pauseTimer = time.AfterFunc(time.Until(until), func() { s.SetPauseAll(false, time.Time{}, true) })
	}
}

// SetPauseAll pauses every folder (until a time, or until resumed when
// until is zero) or resumes them. Clients wake at once on resume.
func (s *Server) SetPauseAll(on bool, until time.Time, expired bool) {
	if on {
		s.armPause(until)
	} else {
		s.pmu.Lock()
		was := s.pauseAll
		if s.pauseTimer != nil {
			s.pauseTimer.Stop()
			s.pauseTimer = nil
		}
		s.pauseAll, s.pauseUntil = false, time.Time{}
		s.pmu.Unlock()
		if !was {
			return
		}
	}
	if err := s.updateConfig(func(c *Config) { c.PauseAll, c.PausedUntil = on, until }); err != nil {
		s.log.Error("save config", "err", err)
	}
	switch {
	case on && until.IsZero():
		s.ev.add(Event{Kind: "scope", Admin: true, Code: "allPaused"})
	case on:
		mins := int(time.Until(until).Round(time.Minute) / time.Minute)
		s.ev.add(Event{Kind: "scope", Admin: true, Code: "allPausedFor", Params: map[string]any{"minutes": mins}})
	case expired:
		s.ev.add(Event{Kind: "scope", Code: "allResumedAuto"})
	default:
		s.ev.add(Event{Kind: "scope", Admin: true, Code: "allResumed"})
	}
	if !on {
		for _, f := range s.folderList() {
			f.mu.Lock()
			f.notifyLocked()
			f.mu.Unlock()
		}
	}
	s.log.Info("pause all", "paused", on, "until", until)
}

// Autostart turns starting at login on and off (the Windows Run key).
type Autostart interface {
	Enabled() (bool, error)
	Set(on bool) error
}

// Status is a summary for the tray icon.
type Status struct {
	Paused          bool
	PausedUntil     time.Time // zero = until resumed
	ClipboardPaused bool
	Conflicts       int
	Online          int // connections seen lately
	Connections     int
}

// Status returns what the tray icon shows.
func (s *Server) Status() Status {
	var st Status
	st.Paused, st.PausedUntil = s.pauseState()
	if s.clip != nil {
		st.ClipboardPaused = s.clip.isPaused()
	}
	for _, f := range s.folderList() {
		cs, _ := f.conflicts()
		st.Conflicts += len(cs)
	}
	for _, d := range s.reg.list() {
		if d.Revoked {
			continue
		}
		st.Connections++
		if time.Since(d.LastSeen) < onlineWindow {
			st.Online++
		}
	}
	return st
}
