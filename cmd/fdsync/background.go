package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The daemon holds an exclusive lock on <state dir>/daemon.lock while it
// runs, with its pid in the file. start and stop use that, not a guess.

// backgroundEnv tells a daemon that `fdsync start` runs it: no terminal to log to.
const backgroundEnv = "FDSYNC_BACKGROUND"

// lockDaemon takes the lock for this state directory, or reports who has it.
func lockDaemon(dir string) (*os.File, int, error) {
	f, err := os.OpenFile(filepath.Join(dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, 0, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		b, _ := os.ReadFile(f.Name())
		f.Close()
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 1 {
			pid = -1 // running, but its pid isn't known (an older build)
		}
		return nil, pid, nil
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return f, 0, nil
}

// runningPid is the pid of the daemon for dir, 0 if none runs, -1 if one
// runs but didn't record its pid.
func runningPid(dir string) int {
	f, pid, err := lockDaemon(dir)
	if err != nil {
		return 0
	}
	if f != nil {
		_ = f.Truncate(0)
		f.Close() // nobody had it: release at once
	}
	return pid
}

// dataDirArg finds --data-dir in daemon flags (start and stop take the same ones).
func dataDirArg(args []string) string {
	for i, a := range args {
		a = strings.TrimLeft(a, "-")
		if v, ok := strings.CutPrefix(a, "data-dir="); ok {
			return v
		}
		if a == "data-dir" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return defaultDataDir()
}

// start runs `fdsync daemon ARGS` in the background, detached from the
// terminal, and checks that it stays up.
func start(args []string) {
	dir, _ := filepath.Abs(dataDirArg(args))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fatal(err)
	}
	if pid := runningPid(dir); pid != 0 {
		fmt.Println("fdsync is already running" + pidNote(pid) + ".")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		fatal(err)
	}
	// The daemon's stderr is a small file, never a pipe: a pipe would die
	// with this process, and the daemon with it (SIGPIPE) at its next write.
	// In the background the log goes only to fdsync.log, so this file just
	// catches a failure to start or a crash.
	errPath := filepath.Join(dir, "daemon.err")
	errFile, err := os.OpenFile(errPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		fatal(err)
	}
	cmd := exec.Command(exe, append([]string{"daemon"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, errFile
	cmd.Env = append(os.Environ(), backgroundEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	log := filepath.Join(dir, "fdsync.log")
	select {
	case err := <-exited:
		b, _ := os.ReadFile(errPath)
		msg := strings.TrimSpace(string(b))
		if msg == "" && err != nil {
			msg = err.Error()
		}
		fatal(fmt.Errorf("the daemon stopped right away: %s\n(log: %s)", msg, log))
	case <-time.After(2 * time.Second):
	}
	fmt.Printf("fdsync started in the background (pid %d).\nLog: %s\nStop it with: fdsync stop\n", cmd.Process.Pid, log)
	os.Exit(0) // don't wait for the child
}

// stop ends the running daemon and waits until it has finished.
func stop(args []string) {
	dir, _ := filepath.Abs(dataDirArg(args))
	pid := runningPid(dir)
	if pid == 0 {
		fmt.Println("fdsync is not running.")
		return
	}
	if pid < 0 {
		fatal(errors.New("a daemon runs but didn't record its pid (started by an older build); stop it with: pkill -x fdsync"))
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fatal(fmt.Errorf("stop pid %d: %w", pid, err))
	}
	for range 150 {
		if runningPid(dir) == 0 {
			fmt.Printf("fdsync stopped (pid %d).\n", pid)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	fatal(fmt.Errorf("pid %d is still running after 15 s", pid))
}

func pidNote(pid int) string {
	if pid > 0 {
		return fmt.Sprintf(" (pid %d)", pid)
	}
	return ""
}
