package server

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"fdsync/internal/protocol"
)

// ---------------------------------------------------------------- sync API (WSL clients)

func (s *Server) handleClipboardList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.clip.list())
}

func (s *Server) handleClipboardImage(w http.ResponseWriter, r *http.Request) {
	item, pngBytes, ok := s.clip.get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, errClipNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(pngBytes)))
	w.Header().Set(protocol.HeaderHash, item.Hash)
	_, _ = w.Write(pngBytes)
	if dev := r.Header.Get(protocol.HeaderDevice); dev != "" {
		s.reg.add(dev, 0, int64(len(pngBytes)))
	}
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
	s.clip.setPaused(req.Paused)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiClipboardOptions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Compress *bool `json:"compress"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Compress != nil {
		s.clip.setCompress(*req.Compress)
	}
	w.WriteHeader(http.StatusNoContent)
}
