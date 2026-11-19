package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Workspace is fdsync's working space for one sync root. It lives in the
// data directory, never inside the sync root, so the synced folder holds
// only the user's own files (no .gitignore entries needed).
type Workspace struct {
	Stage string // staging folder for incoming files; "" = next to the destination
	Trash *Trash
	// SameDrive is false when the data directory is on another drive than
	// the root: incoming files are then received next to their destination
	// (briefly), and trashed files are copied instead of moved.
	SameDrive bool
}

// OpenWorkspace prepares dir/stage for root; trash is the folder's share of
// the side-wide trash (nil = no trash).
func OpenWorkspace(dir, root string, trash *Trash) (*Workspace, error) {
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return nil, err
	}
	// Leftovers from an interrupted transfer.
	if ents, err := os.ReadDir(stage); err == nil {
		for _, e := range ents {
			os.Remove(filepath.Join(stage, e.Name()))
		}
	}
	os.RemoveAll(filepath.Join(dir, "trash")) // per-folder trash of earlier builds
	w := &Workspace{Stage: stage, Trash: trash, SameDrive: true}
	// A rename from the staging folder into the root works only on the same
	// drive (volume / filesystem). Try it once.
	probe := filepath.Join(stage, randName())
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return nil, err
	}
	target := filepath.Join(root, randName())
	err := os.Rename(probe, target)
	os.Remove(probe)
	os.Remove(target)
	if err != nil {
		if !crossDevice(err) {
			return nil, err
		}
		w.Stage, w.SameDrive = "", false
	}
	return w, nil
}

func crossDevice(err error) bool {
	var errno syscall.Errno
	return err != nil && errors.As(err, &errno) && errno == errXDev
}
