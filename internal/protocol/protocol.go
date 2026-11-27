// Package protocol defines the sync API between server and client, and the
// reconcile rule both sides apply to an incoming index entry.
//
// Endpoints (all under /api/v1, Bearer token, client device ID in X-Fdsync-Device):
//
//	GET  /folders                          → []Folder
//	GET  /folders/{id}                     → Folder
//	GET  /folders/{id}/index?since=N       → IndexPage (in-scope entries changed after N)
//	GET  /folders/{id}/wait?since=N&rules=H → WaitResult (long poll until the index passes N or the rules change)
//	POST /folders/{id}/push                → PushResult (client's changed entries)
//	PUT  /folders/{id}/content?path=&hash= ← file body the server asked for (PushResult.Need)
//	GET  /folders/{id}/content?path=&hash= → file body
//	GET  /folders/{id}/conflicts           → []Conflict
//	POST /folders/{id}/conflicts/resolve   ← ResolveRequest
//	PUT  /folders/{id}/rules               ← RulesRequest
//	POST /folders/{id}/tree                ← TreeReport (gzip): the client's whole folder, metadata only
//	POST /hello                            ← Hello (who the client is, its bindings)
//	GET  /requests?wait=30                 → []Request (long poll: things the server needs from this client)
//	PUT  /requests/{rid}                   ← the answer (file content, or empty with X-Fdsync-Error)
//	GET  /clipboard                        → ClipboardList (captured Windows clipboard images, newest first)
//	GET  /clipboard/{id}                   → image/png body
package protocol

import (
	"crypto/sha256"
	"encoding/base64"
	"time"

	"fdsync/internal/fsx"
	"fdsync/internal/index"
	"fdsync/internal/scan"
	"fdsync/internal/vv"
)

const (
	APIPrefix    = "/api/v1"
	HeaderDevice = "X-Fdsync-Device"
	HeaderHash   = "X-Fdsync-Hash"
	PageSize     = 2000
)

type Folder struct {
	ID        string   `json:"id"`
	Rules     []string `json:"rules"`
	Excluded  []string `json:"excluded"`  // out of scope whatever the rules say (manual and auto exclusions)
	RulesHash string   `json:"rulesHash"` // changes whenever the scope (rules or exclusions) changes
	Seq       uint64   `json:"seq"`
}

type IndexPage struct {
	Entries []index.Entry `json:"entries"`
	Next    uint64        `json:"next"` // cursor for the next request
	More    bool          `json:"more"`
}

type WaitResult struct {
	Seq       uint64 `json:"seq"`
	RulesHash string `json:"rulesHash"`
	// Nudge changes whenever the server wants clients to run a round even
	// though the index didn't move (e.g. a resolved conflict needs an upload).
	Nudge uint64 `json:"nudge"`
}

type PushRequest struct {
	Entries  []index.Entry `json:"entries"`
	TreeHash string        `json:"treeHash"` // of the client's latest TreeReport
	Pulled   uint64        `json:"pulled"`   // the client's cursor: it has every server change up to here
}

type Need struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	// Source is the client's path to read from when it differs from Path
	// ("keep both": the client's version is saved under a new name).
	Source string `json:"source,omitempty"`
	// Delta: the server has an old version with blocks; send only these
	// blocks (PUT with X-Fdsync-Blocks), the rest it copies locally.
	Delta   bool  `json:"delta,omitempty"`
	Missing []int `json:"missing,omitempty"`
}

// HeaderBlocks lists the blocks in a delta upload ("0-4,9").
const HeaderBlocks = "X-Fdsync-Blocks"

type Rejected struct {
	Path   string `json:"path"`
	Reason string `json:"reason"` // out-of-scope | case-collision | why the path is invalid
	Other  string `json:"other,omitempty"`
}

// ReasonCaseCollision: Windows already has this path in another case. The
// client keeps offering the entry, so it syncs once the other one is gone.
const ReasonCaseCollision = "case-collision"

type PushResult struct {
	Need      []Need     `json:"need,omitempty"`      // upload these files' content
	Conflicts []string   `json:"conflicts,omitempty"` // paths now waiting for the admin
	Rejected  []Rejected `json:"rejected,omitempty"`  // ignored: out of scope or invalid
	WantTree  bool       `json:"wantTree,omitempty"`  // send a TreeReport (server lacks the one with TreeHash)
}

// TreeReport is a client's whole bound folder as metadata, so the panel can
// show items outside the scope. No content.
type TreeReport struct {
	Hash     string          `json:"hash"`
	Items    []scan.Item     `json:"items"`
	Excluded []scan.Excluded `json:"excluded"`
}

// Hello tells the server who the client is. Sent at start and every round.
type Hello struct {
	Name        string         `json:"name"`   // hostname
	Distro      string         `json:"distro"` // e.g. "Ubuntu 24.04.1 LTS"
	Version     string         `json:"version"`
	NetworkMode string         `json:"networkMode"`        // WSL networking: nat | mirrored | bridged
	Warnings    []string       `json:"warnings,omitempty"` // e.g. "watch-limit"
	Bindings    []HelloBinding `json:"bindings"`
}

type HelloBinding struct {
	Folder string `json:"folder"`
	Root   string `json:"root"`
}

// Request is something the server (on behalf of the panel) asks a client to do.
type Request struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // read | delete | list | bind | trash | restore
	Folder string `json:"folder,omitempty"`
	Path   string `json:"path"`             // read/delete: relative to the binding; list/bind: absolute local path
	Limit  int64  `json:"limit,omitempty"`  // read: at most this many bytes
	Offset int64  `json:"offset,omitempty"` // read: starting here
	Trash  string `json:"trash,omitempty"`  // read/restore: a kept version (fsx.TrashItem.ID) instead of the file
}

// TrashList answers a trash request: one folder's kept versions on the client.
type TrashList struct {
	Folder string          `json:"folder"`
	Items  []fsx.TrashItem `json:"items"`
}

// ListResult answers a list request: the folders inside one local folder.
type ListResult struct {
	Path  string   `json:"path"`
	Home  string   `json:"home"`  // where browsing starts (the allowed root)
	Dirs  []string `json:"dirs"`  // sub-folder names
	Files int      `json:"files"` // how many other entries there are
}

const (
	ReqRead    = "read"
	ReqDelete  = "delete"
	ReqList    = "list" // the panel browses the client's folders to create a binding
	ReqBind    = "bind"
	ReqTrash   = "trash"   // list the kept versions of every binding
	ReqRestore = "restore" // put a kept version back
	// HeaderError carries a client's failure to answer a Request.
	HeaderError = "X-Fdsync-Error"
	// HeaderSize is a file's full size when a read was cut at Limit.
	HeaderSize = "X-Fdsync-Size"
)

// Conflict is a path both sides changed independently. Each side keeps its
// own version on disk until the admin decides.
type Conflict struct {
	Path     string      `json:"path"`
	Device   string      `json:"device"` // the client involved
	Kind     string      `json:"kind"`   // concurrent-edit | edit-delete
	Server   index.Entry `json:"server"`
	Client   index.Entry `json:"client"`
	Detected time.Time   `json:"detected"`
}

// Resolution choices.
const (
	UseWindows = "windows"
	UseClient  = "client"
	KeepBoth   = "both" // Windows version stays; the client's is saved as name.wsl-<client>.ext
)

type ResolveRequest struct {
	Path string `json:"path"`
	Use  string `json:"use"`
}

type RulesRequest struct {
	Rules []string `json:"rules"`
}

type Error struct {
	Error string `json:"error"`
}

// Action is what to do with a remote entry.
type Action int

const (
	ActNone     Action = iota // local is the same or newer
	ActApply                  // remote is newer: take it
	ActMerge                  // changed on both sides, but to the same content: merge versions
	ActConflict               // changed on both sides differently: ask the admin
)

// Decide compares the local entry (nil if unknown) with a remote one.
func Decide(local, remote *index.Entry) Action {
	if local == nil {
		return ActApply
	}
	switch local.Version.Compare(remote.Version) {
	case vv.Equal, vv.Greater:
		return ActNone
	case vv.Lesser:
		return ActApply
	}
	if local.SameContent(remote) {
		return ActMerge
	}
	return ActConflict
}

// ConflictKind names a conflict for the panel.
func ConflictKind(local, remote *index.Entry) string {
	if local.Deleted != remote.Deleted {
		return "edit-delete"
	}
	return "concurrent-edit"
}

// ---------------------------------------------------------------- pairing (RFC 8628)
//
//	POST /pair/start   ← PairStart       → PairStarted
//	POST /pair/token   ← PairPoll        → Tokens, or {"error": "authorization_pending" | "access_denied" | "expired_token"}
//	POST /token        ← RefreshRequest  → Tokens (the refresh token rotates)

type PairStart struct {
	Device   string `json:"device"`
	Hostname string `json:"hostname"`
	Distro   string `json:"distro"`
}

type PairStarted struct {
	DeviceCode      string `json:"deviceCode"` // secret; the client polls with it
	UserCode        string `json:"userCode"`   // the admin types or checks this in the panel
	VerificationURI string `json:"verificationUri"`
	ExpiresIn       int    `json:"expiresIn"` // seconds
	Interval        int    `json:"interval"`  // seconds between polls
}

type PairPoll struct {
	DeviceCode string `json:"deviceCode"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type Tokens struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	Device       string `json:"device"`
	Name         string `json:"name"` // the connection name the admin chose
}

// Fingerprint is how clients pin the server's certificate (like SSH):
// "SHA256:" + base64 of the SHA-256 of the certificate (DER).
func Fingerprint(der []byte) string {
	h := sha256.Sum256(der)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}

// ---------------------------------------------------------------- clipboard

// ClipboardItem is metadata for one PNG image held in the server's in-memory
// clipboard pool.
type ClipboardItem struct {
	ID             string    `json:"id"`
	CapturedAt     time.Time `json:"capturedAt"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	OriginalWidth  int       `json:"originalWidth,omitempty"`
	OriginalHeight int       `json:"originalHeight,omitempty"`
	Size           int64     `json:"size"`
	Hash           string    `json:"hash"`
	Selected       bool      `json:"selected"`
	Source         string    `json:"source"` // clipboard | upload
	Note           string    `json:"note,omitempty"`
}

// ClipboardList is the server's current clipboard image pool, newest first.
type ClipboardList struct {
	Paused   bool            `json:"paused"`
	Compress bool            `json:"compress"`
	MaxEdge  int             `json:"maxEdge"`
	Max      int             `json:"max"`
	Items    []ClipboardItem `json:"items"`
}
