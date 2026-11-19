// Package index stores one side's file index: an entry per file and folder,
// with a sequence number that grows on every change, so a peer can ask for
// "everything since N". Backed by bbolt, one database file per sync root.
package index

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"fdsync/internal/vv"
)

// Entry describes one file or folder. Paths are slash-separated and relative
// to the sync root.
type Entry struct {
	Path    string `json:"path"`
	Dir     bool   `json:"dir,omitempty"`
	Size    int64  `json:"size,omitempty"`
	ModTime int64  `json:"mtime,omitempty"` // Unix nanoseconds, as read from this side's disk
	Mode    uint32 `json:"mode,omitempty"`  // permission bits (kept on Linux clients)
	Hash    string `json:"hash,omitempty"`  // BLAKE3-256, hex; files only
	// Files from 1 MiB up: fixed-size blocks with 128-bit BLAKE3 hashes, so
	// only changed blocks are transferred.
	BlockSize int       `json:"bs,omitempty"`
	Blocks    []string  `json:"blocks,omitempty"`
	Deleted   bool      `json:"deleted,omitempty"`
	Version   vv.Vector `json:"version"`
	Seq       uint64    `json:"seq"` // this side's sequence number for the entry
}

// SameContent reports whether two entries describe the same content
// (both deleted, both folders, or files with the same hash).
func (e *Entry) SameContent(o *Entry) bool {
	switch {
	case e.Deleted || o.Deleted:
		return e.Deleted && o.Deleted
	case e.Dir || o.Dir:
		return e.Dir && o.Dir
	}
	return e.Hash == o.Hash
}

// Depth is the number of path segments, used to order deletes children-first.
func (e *Entry) Depth() int { return strings.Count(e.Path, "/") + 1 }

var (
	bFiles = []byte("files") // path → Entry (JSON)
	bSeq   = []byte("seq")   // seq (big endian) → path
	bMeta  = []byte("meta")  // string keys → values
	kSeq   = []byte("seq")
)

// Store is an index database.
type Store struct {
	db *bolt.DB
}

// Open opens or creates the index at path.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open index %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bFiles, bSeq, bMeta} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Tx is a read or read-write transaction.
type Tx struct {
	tx *bolt.Tx
}

// View runs fn in a read-only transaction.
func (s *Store) View(fn func(*Tx) error) error {
	return s.db.View(func(tx *bolt.Tx) error { return fn(&Tx{tx}) })
}

// Update runs fn in a read-write transaction.
func (s *Store) Update(fn func(*Tx) error) error {
	return s.db.Update(func(tx *bolt.Tx) error { return fn(&Tx{tx}) })
}

// Get returns the entry for path, or nil.
func (t *Tx) Get(path string) (*Entry, error) {
	v := t.tx.Bucket(bFiles).Get([]byte(path))
	if v == nil {
		return nil, nil
	}
	var e Entry
	if err := json.Unmarshal(v, &e); err != nil {
		return nil, fmt.Errorf("index entry %q: %w", path, err)
	}
	return &e, nil
}

// Seq returns the latest sequence number.
func (t *Tx) Seq() uint64 {
	v := t.tx.Bucket(bMeta).Get(kSeq)
	if len(v) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(v)
}

func seqKey(n uint64) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, n)
	return k
}

// Put stores e as a change: it gets the next sequence number, so peers see it.
func (t *Tx) Put(e *Entry) error {
	if !t.tx.Writable() {
		return errors.New("index: Put in read-only transaction")
	}
	old, err := t.Get(e.Path)
	if err != nil {
		return err
	}
	if old != nil {
		if err := t.tx.Bucket(bSeq).Delete(seqKey(old.Seq)); err != nil {
			return err
		}
	}
	n := t.Seq() + 1
	if err := t.tx.Bucket(bMeta).Put(kSeq, seqKey(n)); err != nil {
		return err
	}
	e.Seq = n
	if err := t.tx.Bucket(bSeq).Put(seqKey(n), []byte(e.Path)); err != nil {
		return err
	}
	return t.write(e)
}

// Remove drops an entry entirely (tombstone garbage collection).
func (t *Tx) Remove(path string) error {
	old, err := t.Get(path)
	if err != nil || old == nil {
		return err
	}
	if err := t.tx.Bucket(bSeq).Delete(seqKey(old.Seq)); err != nil {
		return err
	}
	return t.tx.Bucket(bFiles).Delete([]byte(path))
}

// PurgeTombstones removes tombstones older than before whose sequence is
// at most upTo (every peer has seen them). It returns how many went.
func (t *Tx) PurgeTombstones(upTo uint64, before time.Time) (int, error) {
	var drop []string
	err := t.ForEach(func(e *Entry) error {
		if e.Deleted && e.Seq <= upTo && e.ModTime != 0 && e.ModTime < before.UnixNano() {
			drop = append(drop, e.Path)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, p := range drop {
		if err := t.Remove(p); err != nil {
			return 0, err
		}
	}
	return len(drop), nil
}

// PutLocal stores local metadata (size, mtime) of an unchanged version
// without announcing it to peers. The entry must already exist.
func (t *Tx) PutLocal(e *Entry) error {
	old, err := t.Get(e.Path)
	if err != nil {
		return err
	}
	if old == nil {
		return fmt.Errorf("index: PutLocal of unknown path %q", e.Path)
	}
	e.Seq = old.Seq
	return t.write(e)
}

func (t *Tx) write(e *Entry) error {
	v, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return t.tx.Bucket(bFiles).Put([]byte(e.Path), v)
}

// ForEach calls fn for every entry, in path order.
func (t *Tx) ForEach(fn func(*Entry) error) error {
	return t.tx.Bucket(bFiles).ForEach(func(_, v []byte) error {
		var e Entry
		if err := json.Unmarshal(v, &e); err != nil {
			return err
		}
		return fn(&e)
	})
}

// Since calls fn for entries changed after seq, in sequence order, until fn
// returns more = false or an error.
func (t *Tx) Since(seq uint64, fn func(*Entry) (more bool, err error)) error {
	c := t.tx.Bucket(bSeq).Cursor()
	for k, p := c.Seek(seqKey(seq + 1)); k != nil; k, p = c.Next() {
		e, err := t.Get(string(p))
		if err != nil {
			return err
		}
		if e == nil {
			continue
		}
		more, err := fn(e)
		if err != nil || !more {
			return err
		}
	}
	return nil
}

// Meta returns a stored value, or nil.
func (t *Tx) Meta(key string) []byte {
	v := t.tx.Bucket(bMeta).Get([]byte(key))
	if v == nil {
		return nil
	}
	return append([]byte(nil), v...)
}

// SetMeta stores a value. A nil value deletes the key.
func (t *Tx) SetMeta(key string, v []byte) error {
	if v == nil {
		return t.tx.Bucket(bMeta).Delete([]byte(key))
	}
	return t.tx.Bucket(bMeta).Put([]byte(key), v)
}

// MetaUint / SetMetaUint store counters such as a peer's cursor.
func (t *Tx) MetaUint(key string) uint64 {
	v := t.Meta(key)
	if len(v) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(v)
}

func (t *Tx) SetMetaUint(key string, n uint64) error { return t.SetMeta(key, seqKey(n)) }

// Bucket gives callers (the server's conflict and pending lists) their own
// key/value bucket inside the same database.
func (t *Tx) Bucket(name string) (*bolt.Bucket, error) {
	if t.tx.Writable() {
		return t.tx.CreateBucketIfNotExists([]byte(name))
	}
	b := t.tx.Bucket([]byte(name))
	if b == nil {
		return nil, nil
	}
	return b, nil
}
