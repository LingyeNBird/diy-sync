// Package update finds the newest fdsync release on GitHub and installs it
// over the running executable.
//
// Releases are made by .github/workflows/release.yml: tag vX.Y.Z, one file
// per program and platform (see AssetName) and SHA256SUMS listing them.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases come from, "owner/name".
const Repo = "LingyeNBird/diy-sync"

// Version is this build's version, set at build time:
//
//	go build -ldflags "-X fdsync/internal/update.Version=1.2.3"
var Version = "dev"

// maxAsset bounds a download (the executables are a few tens of MB).
const maxAsset = 200 << 20

// Release is the newest published release.
type Release struct {
	Version   string    `json:"version"` // without the "v"
	URL       string    `json:"url"`     // its GitHub page
	Notes     string    `json:"notes"`
	Published time.Time `json:"published"`
	asset     string    // download URL of this platform's file ("" = none)
	assetName string
	sums      string // download URL of SHA256SUMS
}

// HasAsset reports whether the release has a file for this platform.
func (r *Release) HasAsset() bool { return r.asset != "" && r.sums != "" }

// AssetName is the release file of program (e.g. "fdsync-server") for
// this platform: fdsync-server-windows-amd64.exe, fdsync-linux-arm64.
func AssetName(program string) string {
	name := program + "-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

var (
	client  = &http.Client{Timeout: 5 * time.Minute}
	apiBase = "https://api.github.com" // tests point it elsewhere
)

// Latest asks GitHub for the newest release (drafts and pre-releases are
// not "latest").
func Latest(ctx context.Context, program string) (*Release, error) {
	if strings.HasPrefix(Repo, "__") && apiBase == "https://api.github.com" {
		return nil, errors.New("this build has no release repository configured")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "fdsync-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("no release has been published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var gh struct {
		Tag       string    `json:"tag_name"`
		URL       string    `json:"html_url"`
		Body      string    `json:"body"`
		Published time.Time `json:"published_at"`
		Assets    []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&gh); err != nil {
		return nil, err
	}
	r := &Release{Version: strings.TrimPrefix(gh.Tag, "v"), URL: gh.URL, Notes: gh.Body, Published: gh.Published, assetName: AssetName(program)}
	for _, a := range gh.Assets {
		switch a.Name {
		case r.assetName:
			r.asset = a.URL
		case "SHA256SUMS":
			r.sums = a.URL
		}
	}
	return r, nil
}

// Newer reports whether version a is newer than b. Versions are X.Y.Z;
// anything else (a development build) counts as older than every release.
func Newer(a, b string) bool {
	pa, okA := parse(a)
	pb, okB := parse(b)
	if !okA {
		return false
	}
	if !okB {
		return true
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Install downloads the release's file for this platform, checks it
// against SHA256SUMS and puts it in place of the running executable. The
// old one is kept as <exe>.old until Cleanup (it may still be running).
// The new version runs from the next start.
func Install(ctx context.Context, r *Release) error {
	exe, err := Executable()
	if err != nil {
		return err
	}
	return install(ctx, r, exe)
}

// exePath is the running executable, resolved when the program starts:
// after Install renames the file, os.Executable may name the .old one.
var exePath, exeErr = func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}()

// Executable is where this program was started from (and where Install
// puts the new version).
func Executable() (string, error) { return exePath, exeErr }

func install(ctx context.Context, r *Release, exe string) error {
	if !r.HasAsset() {
		return fmt.Errorf("release %s has no %s", r.Version, r.assetName)
	}
	want, err := expectedSum(ctx, r.sums, r.assetName)
	if err != nil {
		return err
	}
	// Next to the exe: the final rename stays on one volume.
	staged := exe + ".new"
	if err := download(ctx, r.asset, staged, want); err != nil {
		os.Remove(staged)
		return err
	}
	old := exe + ".old"
	os.Remove(old)
	// Windows lets a running exe be renamed, not overwritten.
	if err := os.Rename(exe, old); err != nil {
		os.Remove(staged)
		return fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	if err := os.Rename(staged, exe); err != nil {
		_ = os.Rename(old, exe) // put the running version back
		os.Remove(staged)
		return err
	}
	return nil
}

// Cleanup deletes what an earlier Install left behind (call at start).
// Right after an upgrade the old process may still be exiting, and Windows
// keeps a running exe from being deleted, so it tries for a while.
func Cleanup() {
	exe, err := Executable()
	if err != nil {
		return
	}
	os.Remove(exe + ".new")
	for range 30 {
		if err := os.Remove(exe + ".old"); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(time.Second)
	}
}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "fdsync-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return resp, nil
}

func expectedSum(ctx context.Context, url, name string) (string, error) {
	resp, err := get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		// sha256sum format: "<hex>  <name>" (or " *<name>" in binary mode)
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("SHA256SUMS has no line for %s", name)
}

func download(ctx context.Context, url, dst, wantSum string) error {
	resp, err := get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxAsset+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxAsset {
		return errors.New("download is larger than expected")
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSum {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, wantSum)
	}
	return nil
}
