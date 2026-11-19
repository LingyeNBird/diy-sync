package index

import (
	"path/filepath"
	"testing"
	"time"

	"fdsync/internal/vv"
)

func TestPutSince(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	err = s.Update(func(tx *Tx) error {
		for _, p := range []string{"a", "a/b.txt", "c.txt"} {
			if err := tx.Put(&Entry{Path: p, Version: vv.Vector{"x": 1}}); err != nil {
				return err
			}
		}
		// Changing a/b.txt moves it to the end of the sequence.
		return tx.Put(&Entry{Path: "a/b.txt", Hash: "h2", Version: vv.Vector{"x": 2}})
	})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	err = s.View(func(tx *Tx) error {
		if tx.Seq() != 4 {
			t.Errorf("seq = %d, want 4", tx.Seq())
		}
		return tx.Since(1, func(e *Entry) (bool, error) {
			got = append(got, e.Path)
			return true, nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "c.txt" || got[1] != "a/b.txt" {
		t.Fatalf("since 1 = %v, want [c.txt a/b.txt]", got)
	}

	// PutLocal keeps the sequence number.
	err = s.Update(func(tx *Tx) error {
		e, _ := tx.Get("c.txt")
		e.ModTime = 42
		if err := tx.PutLocal(e); err != nil {
			return err
		}
		e2, _ := tx.Get("c.txt")
		if e2.Seq != 3 || e2.ModTime != 42 || tx.Seq() != 4 {
			t.Errorf("PutLocal changed seq: %+v, store seq %d", e2, tx.Seq())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPurgeTombstones(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "i.db"))
	defer s.Close()
	old := time.Now().Add(-40 * 24 * time.Hour).UnixNano()
	_ = s.Update(func(tx *Tx) error {
		tx.Put(&Entry{Path: "old-acked", Deleted: true, ModTime: old})   // seq 1
		tx.Put(&Entry{Path: "live", ModTime: old})                       // seq 2
		tx.Put(&Entry{Path: "old-unacked", Deleted: true, ModTime: old}) // seq 3
		tx.Put(&Entry{Path: "new", Deleted: true, ModTime: time.Now().UnixNano()})
		return nil
	})
	var n int
	_ = s.Update(func(tx *Tx) (err error) {
		n, err = tx.PurgeTombstones(2, time.Now().Add(-30*24*time.Hour))
		return
	})
	var left []string
	_ = s.View(func(tx *Tx) error {
		return tx.ForEach(func(e *Entry) error { left = append(left, e.Path); return nil })
	})
	if n != 1 || len(left) != 3 {
		t.Fatalf("purged %d, left %v", n, left)
	}
	_ = s.View(func(tx *Tx) error {
		return tx.Since(0, func(e *Entry) (bool, error) {
			if e.Path == "old-acked" {
				t.Error("purged entry still in the sequence")
			}
			return true, nil
		})
	})
}
