package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"fdsync/internal/protocol"
)

// panelTrashItem is one kept version, on Windows or on one connection.
type panelTrashItem struct {
	Side         string    `json:"side"` // windows | wsl
	ConnectionID string    `json:"connectionId,omitempty"`
	FolderID     string    `json:"folderId"`
	ID           string    `json:"id"`
	Path         string    `json:"path"`
	When         time.Time `json:"when"`
	Size         int64     `json:"size"`
}

// apiTrash lists the kept versions of every side: the Windows trash and
// the trash of each online connection (asked at once, in parallel).
func (s *Server) apiTrash(w http.ResponseWriter, r *http.Request) {
	type unavailable struct {
		ConnectionID string `json:"connectionId"`
		Error        string `json:"error"`
	}
	out := struct {
		Items       []panelTrashItem `json:"items"`
		Unavailable []unavailable    `json:"unavailable"`
	}{Items: []panelTrashItem{}, Unavailable: []unavailable{}}
	for _, f := range s.folderList() {
		for _, it := range f.side.Trash.List() {
			out.Items = append(out.Items, panelTrashItem{"windows", "", f.id, it.ID, it.Path, it.When, it.Size})
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	for _, d := range s.reg.list() {
		if d.Revoked {
			continue
		}
		if time.Since(d.LastSeen) >= onlineWindow {
			out.Unavailable = append(out.Unavailable, unavailable{d.ID, "offline"})
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			var lists []protocol.TrashList
			a, err := s.broker.ask(ctx, d.ID, protocol.Request{Kind: protocol.ReqTrash})
			if err == nil {
				err = json.Unmarshal(a.body, &lists)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.Unavailable = append(out.Unavailable, unavailable{d.ID, err.Error()})
				return
			}
			for _, l := range lists {
				for _, it := range l.Items {
					out.Items = append(out.Items, panelTrashItem{"wsl", d.ID, l.Folder, it.ID, it.Path, it.When, it.Size})
				}
			}
		}()
	}
	wg.Wait()
	writeJSON(w, out)
}

// apiTrashRestore puts a kept version back on the side it was kept on;
// from there it syncs like any change. What is there now goes to the trash.
func (s *Server) apiTrashRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder     string `json:"folder"`
		Side       string `json:"side"`
		Connection string `json:"connection"`
		ID         string `json:"id"`
		Path       string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	f := s.folderParam(w, req.Folder)
	if f == nil {
		return
	}
	switch req.Side {
	case "windows":
		f.mu.Lock()
		_, err := f.side.Trash.Restore(req.ID, f.side.Stage, f.side.Root)
		f.mu.Unlock()
		if err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		if err := f.Scan(r.Context()); err == nil {
			f.mu.Lock()
			f.notifyLocked()
			f.mu.Unlock()
		}
	case "wsl":
		if _, err := s.broker.ask(r.Context(), req.Connection, protocol.Request{Kind: protocol.ReqRestore, Folder: f.id, Trash: req.ID}); err != nil {
			code := http.StatusBadGateway
			if errors.Is(err, errNoAnswer) {
				code = http.StatusGatewayTimeout
			}
			writeErr(w, code, err)
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, errors.New("side must be windows or wsl"))
		return
	}
	where := "Windows"
	if req.Side == "wsl" {
		where = s.reg.name(req.Connection)
	}
	s.ev.add(Event{Kind: "restore", Admin: true, FolderID: f.id, ConnectionID: req.Connection, Path: req.Path,
		Code: "trashRestored", Params: map[string]any{"where": where}})
	w.WriteHeader(http.StatusNoContent)
}
