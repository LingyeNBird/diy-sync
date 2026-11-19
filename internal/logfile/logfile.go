// Package logfile is a size-capped log file: when it reaches MaxSize it is
// renamed to <name>.1 (replacing the previous one), so a log never takes
// more than twice MaxSize on disk (docs/requirements.md §8: 2 × 5 MB).
package logfile

import (
	"os"
	"sync"
)

// MaxSize is the size at which the log rotates.
const MaxSize = 5 << 20

// Writer appends to a log file and rotates it.
type Writer struct {
	path string
	mu   sync.Mutex
	f    *os.File
	size int64
}

// Open opens (or creates) the log file at path.
func Open(path string) (*Writer, error) {
	w := &Writer{path: path}
	return w, w.open()
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, fi.Size()
	return nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return len(p), nil
	}
	if w.size+int64(len(p)) > MaxSize {
		w.f.Close()
		_ = os.Rename(w.path, w.path+".1") // on Windows this replaces the old .1 too
		if err := w.open(); err != nil {
			w.f = nil
			return len(p), nil // logging must never stop the program
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	return w.f.Close()
}
