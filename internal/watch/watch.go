// Package watch reports file changes under a root, batched: one signal after
// the tree has been quiet for the delay (and at least every 5× the delay
// while changes keep coming). Callers rescan on each signal; periodic full
// rescans cover anything a watcher misses.
package watch

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"fdsync/internal/fsx"
)

// Limited is set when some folders could not be watched (e.g. the inotify
// limit was reached); changes there are only found by periodic rescans.
var Limited atomic.Bool

// Watch starts watching root recursively. The channel gets a value per batch.
func Watch(ctx context.Context, root string, delay time.Duration, log *slog.Logger) (<-chan struct{}, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	out := make(chan struct{}, 1)
	warned := false
	add := func(dir string) {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if rel != "." && fsx.Ignored(filepath.ToSlash(rel)) {
				return fs.SkipDir
			}
			if err := w.Add(p); err != nil {
				if !warned {
					warned = true
					if errors.Is(err, syscall.ENOSPC) {
						Limited.Store(true)
					}
					msg := "cannot watch every folder; changes there are found by the periodic rescan"
					if errors.Is(err, syscall.ENOSPC) {
						msg += ` (inotify watch limit reached; raise it with: sudo sysctl fs.inotify.max_user_watches=1048576)`
					}
					log.Warn(msg, "dir", p, "err", err)
				}
				return fs.SkipDir
			}
			return nil
		})
	}
	add(root)

	go func() {
		defer w.Close()
		var quiet, maxWait <-chan time.Time
		fire := func() {
			quiet, maxWait = nil, nil
			select {
			case out <- struct{}{}:
			default:
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				rel, err := filepath.Rel(root, ev.Name)
				if err != nil || fsx.Ignored(filepath.ToSlash(rel)) || strings.HasPrefix(filepath.Base(ev.Name), fsx.TempPrefix) {
					continue
				}
				if ev.Has(fsnotify.Create) {
					if fi, err := os.Lstat(ev.Name); err == nil && fi.IsDir() {
						add(ev.Name)
					}
				}
				quiet = time.After(delay)
				if maxWait == nil {
					maxWait = time.After(5 * delay)
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				// Overflow and similar: events were lost, so rescan.
				log.Warn("watcher", "root", root, "err", err)
				fire()
			case <-quiet:
				fire()
			case <-maxWait:
				fire()
			}
		}
	}()
	return out, nil
}
