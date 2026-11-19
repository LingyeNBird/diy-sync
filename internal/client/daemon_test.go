package client

import (
	"context"
	"testing"
	"time"

	"fdsync/internal/protocol"
)

// The daemon loop: changes on either side arrive without anyone calling
// SyncOnce, through the watchers and the long poll.
func TestDaemonPicksUpChanges(t *testing.T) {
	e := newEnv(t, "a/")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.srv.ScanAll(ctx); err != nil {
		t.Fatal(err)
	}
	e.srv.Watch(ctx)
	c := e.client()
	done := make(chan struct{})
	go func() { c.Run(ctx, 100*time.Millisecond, time.Hour); close(done) }()
	defer func() { cancel(); <-done }()

	eventually := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	put(t, e.win, "a/from-win.txt", "hello wsl")
	eventually("Windows → WSL", func() bool { s, _ := read(c.root, "a/from-win.txt"); return s == "hello wsl" })
	put(t, c.root, "a/deep/from-wsl.txt", "hello windows")
	eventually("WSL → Windows", func() bool { s, _ := read(e.win, "a/deep/from-wsl.txt"); return s == "hello windows" })
}

// Resolving "use WSL" changes nothing in the server's index; the running
// client must still wake up and upload its version.
func TestDaemonUploadsAfterResolve(t *testing.T) {
	e := newEnv(t, "a/")
	put(t, e.win, "a/f.txt", "base")
	c := e.client()
	c.sync()
	put(t, e.win, "a/f.txt", "windows edit")
	put(t, c.root, "a/f.txt", "wsl edit")
	c.sync()
	if len(e.conflicts()) != 1 {
		t.Fatal("expected a conflict")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, 100*time.Millisecond, time.Hour); close(done) }()
	defer func() { cancel(); <-done }()
	time.Sleep(300 * time.Millisecond) // the client is idle in its long poll now

	e.api("POST", "/folders/proj/conflicts/resolve", protocol.ResolveRequest{Path: "a/f.txt", Use: protocol.UseClient}, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if s, _ := read(e.win, "a/f.txt"); s == "wsl edit" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the WSL version never reached Windows")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
