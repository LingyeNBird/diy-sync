package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
)

// Bundles carry many small files in one request (each also costs one index
// commit on the receiver instead of one per file):
//
//	POST /folders/{id}/bundle/get  ← BundleRequest          → bundle stream
//	POST /folders/{id}/bundle/put  ← bundle stream          → []BundleResult
//
// A bundle stream is, per file, one JSON BundleItem line, then Size bytes.

const (
	BundleFileMax  = 1 << 20  // files up to this size go in bundles
	BundleBytesMax = 16 << 20 // per bundle
	BundleFilesMax = 500      // per bundle
)

type BundleItem struct {
	Path  string `json:"path"`
	Hash  string `json:"hash"`
	Size  int64  `json:"size"`
	Error string `json:"error,omitempty"` // get: this file can't be sent now (Size is 0)
}

type BundleRequest struct {
	Items []BundleItem `json:"items"` // Path and Hash
}

type BundleResult struct {
	Path  string `json:"path"`
	Error string `json:"error,omitempty"`
}

// WriteItem writes one header line.
func WriteItem(w io.Writer, it BundleItem) error {
	b, err := json.Marshal(it)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadItem reads one header line; io.EOF at a clean end of the stream.
func ReadItem(r *bufio.Reader) (BundleItem, error) {
	var it BundleItem
	line, err := r.ReadSlice('\n')
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return it, io.EOF
	}
	if err != nil {
		return it, err // includes lines over the buffer size
	}
	if err := json.Unmarshal(line, &it); err != nil {
		return it, err
	}
	if it.Size < 0 || it.Size > BundleBytesMax {
		return it, errors.New("bundle item size out of range")
	}
	return it, nil
}
