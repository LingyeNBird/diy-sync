//go:build windows

package main

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// runKey starts the server at login through the current user's Run key
// (no admin rights, no service): the value holds this exe and its flags.
type runKey struct {
	command string // what the value should hold
}

const (
	runPath  = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "fdsync-server"
)

// newRunKey builds the start command: this exe with the flags it runs with now.
func newRunKey(args []string) *runKey {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	parts := []string{quoteArg(exe)}
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	k := &runKey{command: strings.Join(parts, " ")}
	// Moved or updated exe: keep an existing entry pointing at this one.
	if on, err := k.Enabled(); err == nil && on {
		_ = k.Set(true)
	}
	return k
}

func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func (k *runKey) Enabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runPath, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer key.Close()
	_, _, err = key.GetStringValue(runValue)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (k *runKey) Set(on bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if !on {
		if err := key.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	if cur, _, err := key.GetStringValue(runValue); err == nil && cur == k.command {
		return nil
	}
	return key.SetStringValue(runValue, k.command)
}
