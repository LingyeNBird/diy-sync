package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"fdsync/internal/index"
)

// LoadConfig reads config.json. When it doesn't exist, it writes a starter
// file with a random token and reports created = true.
func LoadConfig(path string) (cfg Config, created bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg = Config{Listen: "0.0.0.0:7443", Token: RandomToken(), Folders: []FolderConfig{}}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return cfg, false, err
		}
		return cfg, true, SaveConfig(path, cfg)
	}
	if err != nil {
		return cfg, false, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, false, err
	}
	return cfg, false, nil
}

// SaveConfig writes the configuration atomically, readable by the owner only.
func SaveConfig(path string, cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RandomToken returns a new 32-byte token, hex encoded.
func RandomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// DeviceID returns this installation's device ID (created on first use).
func DeviceID(dataDir string) (string, error) {
	p := filepath.Join(dataDir, "device-id")
	if b, err := os.ReadFile(p); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	id := "s-" + hex.EncodeToString(b)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	return id, os.WriteFile(p, []byte(id+"\n"), 0o600)
}

// isNeeded reports whether the server is waiting for this content.
func (f *folder) isNeeded(device, path, hash string) bool {
	var ok bool
	_ = f.store.View(func(tx *index.Tx) error {
		p, err := f.getPending(tx, path)
		ok = err == nil && p != nil && p.Device == device && p.Entry.Hash == hash
		return nil
	})
	return ok
}
