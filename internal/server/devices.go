package server

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"fdsync/internal/protocol"
)

// Device is a client the server has seen (a "connection" in the panel).
type Device struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`              // the connection name (chosen by the admin when pairing)
	Hostname    string          `json:"hostname"`          // what the client reports
	NameSet     bool            `json:"nameSet,omitempty"` // the admin named it: hello must not rename it
	Distro      string          `json:"distro"`
	Version     string          `json:"version"`
	NetworkMode string          `json:"networkMode"`
	Address     string          `json:"address"`
	FirstSeen   time.Time       `json:"firstSeen"`
	LastSeen    time.Time       `json:"lastSeen"`
	Bindings    []DeviceBinding `json:"bindings"`
	// Paired clients authenticate with their own tokens; the refresh
	// token is kept as a SHA-256 hash only. Empty = not paired (or revoked).
	RefreshHash string    `json:"refreshHash,omitempty"`
	PairedAt    time.Time `json:"pairedAt,omitempty"`
	Revoked     bool      `json:"revoked,omitempty"` // blocked until it pairs again
	Warnings    []string  `json:"warnings,omitempty"`
}

// DeviceBinding is one of a client's bindings.
type DeviceBinding struct {
	Folder   string    `json:"folder"`
	Root     string    `json:"root"`
	LastSync time.Time `json:"lastSync"`
	Pulled   uint64    `json:"pulled"` // acknowledged server sequence (tombstones up to here may go)
}

// onlineWindow: a client long-polls every 30 s, so it is offline after this.
const onlineWindow = 75 * time.Second

// registry tracks devices (persisted in devices.json) and their transfer rates.
type registry struct {
	mu    sync.Mutex
	path  string
	devs  map[string]*Device
	rates map[string]*rate
	dirty bool
	ev    *activity
}

func newRegistry(dataDir string, ev *activity) *registry {
	r := &registry{devs: map[string]*Device{}, rates: map[string]*rate{}, ev: ev}
	if dataDir != "" {
		r.path = filepath.Join(dataDir, "devices.json")
		if b, err := os.ReadFile(r.path); err == nil {
			var list []*Device
			if json.Unmarshal(b, &list) == nil {
				for _, d := range list {
					r.devs[d.ID] = d
				}
			}
		}
	}
	return r
}

func (r *registry) get(id string) *Device {
	d := r.devs[id]
	if d == nil {
		now := time.Now()
		d = &Device{ID: id, Name: id, FirstSeen: now}
		r.devs[id] = d
		r.dirty = true
	}
	return d
}

func hostOf(remote string) string {
	if h, _, err := net.SplitHostPort(remote); err == nil {
		return h
	}
	return remote
}

// touch records a request from a device.
func (r *registry) touch(id, remote string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.get(id)
	wasOnline := time.Since(d.LastSeen) < onlineWindow
	d.LastSeen = time.Now()
	if addr := hostOf(remote); addr != d.Address {
		d.Address = addr
		r.dirty = true
	}
	if !wasOnline {
		r.ev.add(Event{Kind: "connect", ConnectionID: id, Code: "connected",
			Params: map[string]any{"name": d.Name, "address": d.Address, "mode": d.NetworkMode}})
	}
}

func (r *registry) hello(id, remote string, h protocol.Hello) {
	r.touch(id, remote)
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.get(id)
	d.Hostname, d.Distro, d.Version, d.NetworkMode = h.Name, h.Distro, h.Version, h.NetworkMode
	d.Warnings = h.Warnings
	// A paired connection keeps the name the admin gave it; shared-token
	// clients are named after their host.
	if d.RefreshHash == "" && !d.NameSet && h.Name != "" {
		d.Name = h.Name
	}
	if d.Name == "" {
		d.Name = id
	}
	keep := map[string]DeviceBinding{}
	for _, b := range d.Bindings {
		keep[b.Folder+"\x00"+b.Root] = b
	}
	d.Bindings = d.Bindings[:0]
	for _, b := range h.Bindings {
		old := keep[b.Folder+"\x00"+b.Root]
		d.Bindings = append(d.Bindings, DeviceBinding{Folder: b.Folder, Root: b.Root, LastSync: old.LastSync})
	}
	r.dirty = true
}

// rename gives a connection the name the admin chose.
func (r *registry) rename(id, name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.devs[id]
	if d == nil {
		return false
	}
	d.Name, d.NameSet = name, true
	r.saveLocked()
	return true
}

// pair records an approved client.
func (r *registry) pair(id, name, distro, address, refreshHash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.get(id)
	d.Name, d.Distro, d.Address = name, distro, address
	d.RefreshHash, d.PairedAt, d.Revoked, d.NameSet = refreshHash, time.Now(), false, true
	r.saveLocked()
}

// rotateRefresh swaps a valid refresh token for the next one.
func (r *registry) rotateRefresh(oldHash, newHash string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.devs {
		if d.RefreshHash != "" && subtle.ConstantTimeCompare([]byte(d.RefreshHash), []byte(oldHash)) == 1 {
			d.RefreshHash = newHash
			r.saveLocked()
			return d.ID, true
		}
	}
	return "", false
}

func (r *registry) revoke(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d := r.devs[id]; d != nil {
		d.RefreshHash = ""
		d.Bindings = nil
		d.Revoked = true
		r.saveLocked()
	}
}

// sharedBlocked reports whether a device may not use the shared token:
// it is paired (it has its own token) or the admin revoked it.
func (r *registry) sharedBlocked(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.devs[id]
	return d != nil && (d.RefreshHash != "" || d.Revoked)
}

func (r *registry) synced(id, folder string, pulled uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.get(id)
	for i := range d.Bindings {
		if d.Bindings[i].Folder == folder {
			d.Bindings[i].LastSync = time.Now()
			d.Bindings[i].Pulled = pulled
		}
	}
}

// acknowledged returns the lowest cursor of every live binding of folder,
// and whether there is any.
func (r *registry) acknowledged(folder string) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lowest uint64
	found := false
	for _, d := range r.devs {
		if d.Revoked {
			continue
		}
		for _, b := range d.Bindings {
			if b.Folder == folder && (!found || b.Pulled < lowest) {
				lowest, found = b.Pulled, true
			}
		}
	}
	return lowest, found
}

// checkOffline logs devices that went silent, and saves the registry.
func (r *registry) tick() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.devs {
		quiet := time.Since(d.LastSeen)
		if quiet >= onlineWindow && quiet < onlineWindow+15*time.Second {
			r.ev.add(Event{Kind: "disconnect", Level: "warn", ConnectionID: d.ID, Code: "disconnected", Params: map[string]any{"name": d.Name}})
		}
	}
	if r.dirty || len(r.devs) > 0 {
		r.saveLocked()
	}
}

func (r *registry) saveLocked() {
	if r.path == "" {
		return
	}
	b, _ := json.MarshalIndent(r.listLocked(), "", "  ")
	tmp := r.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, r.path)
		r.dirty = false
	}
}

func (r *registry) listLocked() []Device {
	out := make([]Device, 0, len(r.devs))
	for _, d := range r.devs {
		c := *d
		c.Bindings = append([]DeviceBinding(nil), d.Bindings...)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen.Before(out[j].FirstSeen) })
	return out
}

func (r *registry) list() []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listLocked()
}

func (r *registry) name(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d := r.devs[id]; d != nil && d.Name != "" {
		return d.Name
	}
	return id
}

// ---------------------------------------------------------------- rates

// rate counts bytes per second over the last few seconds.
type rate struct {
	up, down [8]int64 // ring by second
	sec      int64
}

func (r *registry) add(id string, up, down int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rt := r.rates[id]
	if rt == nil {
		rt = &rate{}
		r.rates[id] = rt
	}
	now := time.Now().Unix()
	rt.advance(now)
	rt.up[now%8] += up
	rt.down[now%8] += down
}

func (rt *rate) advance(now int64) {
	if now-rt.sec >= 8 {
		rt.up, rt.down = [8]int64{}, [8]int64{}
	} else {
		for s := rt.sec + 1; s <= now; s++ {
			rt.up[s%8], rt.down[s%8] = 0, 0
		}
	}
	rt.sec = now
}

// bps returns bytes per second up (client → Windows) and down, averaged
// over the last 3 full seconds.
func (r *registry) bps(id string) (up, down int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rt := r.rates[id]
	if rt == nil {
		return 0, 0
	}
	now := time.Now().Unix()
	rt.advance(now)
	for s := now - 3; s < now; s++ {
		up += rt.up[s%8]
		down += rt.down[s%8]
	}
	return up / 3, down / 3
}

// countingReader counts bytes read through it.
type countingReader struct {
	r interface{ Read([]byte) (int, error) }
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
