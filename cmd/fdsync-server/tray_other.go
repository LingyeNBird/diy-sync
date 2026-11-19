//go:build !windows

package main

import (
	"context"

	"fdsync/internal/server"
)

// Elsewhere (development on Linux) there is no tray: run until stopped.

type runKey struct{}

func newRunKey([]string) *runKey { return nil }

func runUI(_ context.Context, _ func(), _ *server.Server, _ string, _ *runKey, done <-chan struct{}, _ bool) {
	<-done
}

func showError(string, string) {}

func ownConsole() {}

func (*runKey) Enabled() (bool, error) { return false, nil }
func (*runKey) Set(bool) error         { return nil }
