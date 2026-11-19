//go:build windows

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// The exe is built without a console (-H windowsgui), so a double-click or
// the login start shows only the tray icon. Run from a terminal, it
// attaches to that terminal so subcommands can print.

var hasConsole bool

func init() {
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && h != 0 && h != windows.InvalidHandle {
		hasConsole = true // output is redirected, or this is a console build
		return
	}
	const attachParent = ^uint32(0)
	if kernel32Call("AttachConsole", uintptr(attachParent)) == nil {
		useConsole()
	}
}

func useConsole() {
	hasConsole = true
	if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
		os.Stdin = f
	}
}

// ownConsole opens a console window of our own, for commands that read
// input (the terminal we were started from doesn't wait for a GUI exe).
func ownConsole() {
	_ = kernel32Call("FreeConsole")
	if kernel32Call("AllocConsole") == nil {
		useConsole()
	}
}

func kernel32Call(name string, args ...uintptr) error {
	r, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc(name).Call(args...)
	if r == 0 {
		return err
	}
	return nil
}

// showError tells the user about a fatal error: there may be no console to print it to.
func showError(title, msg string) {
	if hasConsole {
		return
	}
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}
