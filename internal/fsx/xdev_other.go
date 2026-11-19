//go:build !windows

package fsx

import "syscall"

const errXDev = syscall.EXDEV
