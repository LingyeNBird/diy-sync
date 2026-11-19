package logfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotationCapsSize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.log")
	w, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	line := append(bytes.Repeat([]byte("x"), 1023), '\n')
	for i := 0; i < 15*1024; i++ { // 15 MB through the log
		w.Write(line)
	}
	w.Close()
	a, _ := os.Stat(p)
	b, _ := os.Stat(p + ".1")
	if a.Size() > MaxSize || b.Size() > MaxSize {
		t.Fatalf("sizes %d + %d exceed the cap", a.Size(), b.Size())
	}
	if _, err := os.Stat(p + ".2"); err == nil {
		t.Fatal("only one old file is kept")
	}
}
