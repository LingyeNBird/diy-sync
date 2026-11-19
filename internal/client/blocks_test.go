package client

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A 4 MiB file with one changed region moves only a block or two, in both
// directions, and the result is byte-identical.
func TestBlockTransfer(t *testing.T) {
	e := newEnv(t, "a/")
	data := make([]byte, 4<<20)
	rand.Read(data)
	write := func(root string, b []byte) {
		p := filepath.Join(root, "a", "big.bin")
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		later = later.Add(1e9)
		os.Chtimes(p, later, later)
	}
	write(e.win, data)
	c := e.client()
	st := c.sync()
	if st.BytesDown != 4<<20 || st.BytesReused != 0 {
		t.Fatalf("first transfer should be whole: %+v", st)
	}

	// Windows changes 100 bytes in the middle → the client fetches one block.
	edited := append([]byte(nil), data...)
	copy(edited[2<<20:], make([]byte, 100))
	write(e.win, edited)
	st = c.sync()
	got, _ := os.ReadFile(filepath.Join(c.root, "a", "big.bin"))
	if string(got) != string(edited) {
		t.Fatal("client copy differs after a block download")
	}
	if st.BytesDown > 256<<10 || st.BytesReused < 3<<20 {
		t.Fatalf("download moved %d bytes, reused %d: want one block", st.BytesDown, st.BytesReused)
	}

	// The client appends and changes the start → it uploads only those blocks.
	edited2 := append(append([]byte(nil), edited...), []byte("appended in WSL")...)
	copy(edited2[:10], "WSL edit!!")
	write(c.root, edited2)
	st = c.sync()
	got, _ = os.ReadFile(filepath.Join(e.win, "a", "big.bin"))
	if string(got) != string(edited2) {
		t.Fatal("Windows copy differs after a delta upload")
	}
	if st.BytesUp > 512<<10 || st.BytesReused < 3<<20 {
		t.Fatalf("upload sent %d bytes, reused %d: want two blocks", st.BytesUp, st.BytesReused)
	}
}

// Renames and moves travel as moves: no content is sent either way.
func TestRenamesMoveInsteadOfTransfer(t *testing.T) {
	e := newEnv(t, "a/")
	c := e.client()
	blob := make([]byte, 2<<20)
	rand.Read(blob)
	put(t, c.root, "a/x/big.bin", string(blob))
	put(t, c.root, "a/x/notes.txt", "notes")
	c.sync()
	e.want(e.win, "a/x/big.bin", string(blob))

	// WSL renames a file and moves a folder: the server moves its copies.
	os.Rename(filepath.Join(c.root, "a", "x", "big.bin"), filepath.Join(c.root, "a", "x", "renamed.bin"))
	os.Rename(filepath.Join(c.root, "a", "x"), filepath.Join(c.root, "a", "y"))
	st := c.sync()
	if st.BytesUp != 0 || st.Uploaded != 0 {
		t.Fatalf("a move uploaded content: %+v", st)
	}
	e.want(e.win, "a/y/renamed.bin", string(blob))
	e.want(e.win, "a/y/notes.txt", "notes")
	e.absent(e.win, "a/x")

	// Windows renames: the client moves its copy instead of downloading.
	os.Rename(filepath.Join(e.win, "a", "y", "renamed.bin"), filepath.Join(e.win, "a", "y", "back.bin"))
	st = c.sync()
	if st.BytesDown != 0 || st.Moved != 1 {
		t.Fatalf("a Windows rename downloaded content: %+v", st)
	}
	e.want(c.root, "a/y/back.bin", string(blob))
	e.absent(c.root, "a/y/renamed.bin")
	// Settled: nothing more to do.
	if st := c.sync(); st.Scanned.Changed != 0 || st.Pulled != 0 || st.Uploaded != 0 {
		t.Fatalf("not settled: %+v", st)
	}
}

// Many small files both ways, transferred several at a time.
func TestManyFilesInParallel(t *testing.T) {
	e := newEnv(t, "a/")
	c := e.client()
	for i := 0; i < 60; i++ {
		put(t, e.win, fmt.Sprintf("a/w/f%02d.txt", i), fmt.Sprintf("windows %d", i))
		put(t, c.root, fmt.Sprintf("a/l/f%02d.txt", i), fmt.Sprintf("wsl %d", i))
	}
	st := c.sync()
	if st.Uploaded != 60 || st.Pulled < 60 {
		t.Fatalf("stats: %+v", st)
	}
	for i := 0; i < 60; i++ {
		e.want(c.root, fmt.Sprintf("a/w/f%02d.txt", i), fmt.Sprintf("windows %d", i))
		e.want(e.win, fmt.Sprintf("a/l/f%02d.txt", i), fmt.Sprintf("wsl %d", i))
	}
}
