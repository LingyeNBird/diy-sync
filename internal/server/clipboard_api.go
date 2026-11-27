package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"fdsync/internal/protocol"
)

// SetClipboardPaused pauses or resumes automatic Windows clipboard capture and
// persists the preference to config.json.
func (s *Server) SetClipboardPaused(paused bool) {
	s.clip.setPaused(paused)
	s.saveClipboardConfig()
}

func (s *Server) saveClipboardConfig() {
	snap := s.clip.configSnapshot()
	if err := s.updateConfig(func(c *Config) { c.Clipboard = snap }); err != nil {
		s.log.Error("save clipboard config", "err", err)
	}
}

// ---------------------------------------------------------------- sync API (WSL clients)

func (s *Server) handleClipboardList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.clip.list())
}

func (s *Server) handleClipboardImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	item, pngBytes, ok := s.clip.get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errClipNotFound)
		return
	}
	s.clip.markRead(id)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(pngBytes)))
	w.Header().Set(protocol.HeaderHash, item.Hash)
	_, _ = w.Write(pngBytes)
	if dev := r.Header.Get(protocol.HeaderDevice); dev != "" {
		s.reg.add(dev, 0, int64(len(pngBytes)))
	}
}

func (s *Server) handleClipboardPush(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxClipboardImageBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(body) > maxClipboardImageBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, errors.New("image exceeds size limit"))
		return
	}
	if dev := r.Header.Get(protocol.HeaderDevice); dev != "" {
		s.reg.add(dev, int64(len(body)), 0)
	}
	q := r.URL.Query()
	selectIt := q.Get("select") == "1" || q.Get("select") == "true"
	copyToWin := q.Get("copy") != "0" && q.Get("copy") != "false"
	item, _, err := s.clip.addImage(body, "wsl", q.Get("note"), selectIt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, pngBytes, ok := s.clip.get(item.ID); ok && s.clipWin != nil {
		s.clipWin.onWSLImage(item, pngBytes, copyToWin, s.log)
	}
	writeJSON(w, item)
}

func (s *Server) handleClipboardPushText(w http.ResponseWriter, r *http.Request) {
	var req protocol.ClipboardTextRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxClipboardTextBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Text == "" {
		writeErr(w, http.StatusBadRequest, errors.New("empty text"))
		return
	}
	if dev := r.Header.Get(protocol.HeaderDevice); dev != "" {
		s.reg.add(dev, int64(len(req.Text)), 0)
	}
	if s.clipWin != nil {
		if err := s.clipWin.onWSLText(req.Text, s.log); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- panel API (Windows web UI)

func (s *Server) apiClipboardList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.clip.list())
}

func (s *Server) apiClipboardImage(w http.ResponseWriter, r *http.Request) {
	item, pngBytes, ok := s.clip.get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, errClipNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(pngBytes)))
	w.Header().Set("ETag", `"`+item.Hash+`"`)
	_, _ = w.Write(pngBytes)
}

func (s *Server) apiClipboardUpload(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxClipboardImageBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(body) > maxClipboardImageBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, errors.New("image exceeds size limit"))
		return
	}
	q := r.URL.Query()
	selectIt := q.Get("select") == "1" || q.Get("select") == "true"
	item, _, err := s.clip.addImage(body, "upload", q.Get("note"), selectIt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, item)
}

func (s *Server) apiClipboardUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Selected *bool   `json:"selected"`
		Note     *string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	item, err := s.clip.update(r.PathValue("id"), req.Selected, req.Note)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errClipNotFound) {
			code = http.StatusNotFound
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, item)
}

func (s *Server) apiClipboardDelete(w http.ResponseWriter, r *http.Request) {
	if !s.clip.delete(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, errClipNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiClipboardClear(w http.ResponseWriter, r *http.Request) {
	onlyUnselected := r.URL.Query().Get("unselected") == "1" || r.URL.Query().Get("unselected") == "true"
	s.clip.clear(onlyUnselected)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiClipboardUnselect(w http.ResponseWriter, _ *http.Request) {
	s.clip.unselectAll()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiClipboardPause(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paused bool `json:"paused"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.SetClipboardPaused(req.Paused)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiClipboardOptions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Compress     *bool `json:"compress"`
		AutoUnselect *bool `json:"autoUnselect"`
		Max          *int  `json:"max"`
		MaxEdge      *int  `json:"maxEdge"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.clip.setOptions(req.Compress, req.AutoUnselect, req.Max, req.MaxEdge)
	s.saveClipboardConfig()
	w.WriteHeader(http.StatusNoContent)
}
