package server

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"fdsync/internal/fsx"
)

// findVSCode finds VS Code's Code.exe on Windows: the usual install
// folders (user and system, stable before Insiders), then `code` on PATH.
func findVSCode() (string, error) {
	var cands []string
	for _, base := range []string{os.Getenv("LOCALAPPDATA") + `\Programs`, os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		if strings.HasPrefix(base, `\`) || base == "" {
			continue
		}
		cands = append(cands, filepath.Join(base, "Microsoft VS Code", "Code.exe"))
	}
	for _, name := range []string{"code.cmd", "code"} {
		if p, err := exec.LookPath(name); err == nil {
			// <install>\bin\code.cmd → <install>\Code.exe
			cands = append(cands, filepath.Join(filepath.Dir(filepath.Dir(p)), "Code.exe"))
		}
	}
	for _, base := range []string{os.Getenv("LOCALAPPDATA") + `\Programs`, os.Getenv("ProgramFiles")} {
		if base != "" && !strings.HasPrefix(base, `\`) {
			cands = append(cands, filepath.Join(base, "Microsoft VS Code Insiders", "Code - Insiders.exe"))
		}
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
			return c, nil
		}
	}
	return "", errors.New("VS Code was not found (looked in the usual install folders and for `code` on PATH)")
}

// apiOpenVSCode opens a folder or file of a sync folder in VS Code on
// Windows; an empty path opens the whole folder.
func (s *Server) apiOpenVSCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string `json:"folder"`
		Path   string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	f := s.folderParam(w, req.Folder)
	if f == nil {
		return
	}
	abs := f.side.Root
	if req.Path != "" {
		if err := fsx.CheckRel(req.Path); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		abs = fsx.Abs(f.side.Root, req.Path)
	}
	if runtime.GOOS != "windows" {
		writeErr(w, http.StatusNotImplemented, errors.New("only on Windows"))
		return
	}
	if _, err := os.Stat(abs); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	exe, err := findVSCode()
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	cmd := exec.Command(exe, abs)
	cmd.Dir = filepath.Dir(exe)
	// Not as Node: that variable makes Code.exe run a script instead of the editor.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "ELECTRON_RUN_AS_NODE=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if err := cmd.Start(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = cmd.Process.Release()
	s.log.Info("opened in VS Code", "folder", f.id, "path", req.Path)
	w.WriteHeader(http.StatusNoContent)
}
