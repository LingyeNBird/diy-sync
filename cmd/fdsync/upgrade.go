package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fdsync/internal/update"
)

const upgradeUsage = `Usage: fdsync upgrade [flags]

Checks GitHub releases for a newer fdsync binary, verifies its SHA-256 checksum,
replaces this executable in place, and restarts the daemon if it was running.

Flags:
`

func upgrade(args []string) {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	checkOnly := fs.Bool("check", false, "only check for a newer release without installing")
	targetVer := fs.String("version", "", "install a specific version, e.g. 1.1.0")
	force := fs.Bool("force", false, "install even if already on the same version")
	noRestart := fs.Bool("no-restart", false, "do not restart a running daemon after upgrading")
	dataDir := fs.String("data-dir", defaultDataDir(), "state directory (to detect and restart a running daemon)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, upgradeUsage)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var (
		rel *update.Release
		err error
	)
	if v := strings.TrimSpace(*targetVer); v != "" {
		rel, err = update.ByVersion(ctx, "fdsync", v)
	} else {
		rel, err = update.Latest(ctx, "fdsync")
	}
	if err != nil {
		fatal(fmt.Errorf("check for updates: %w", err))
	}
	if !rel.HasAsset() {
		fatal(fmt.Errorf("release %s has no binary for this platform (%s)", rel.Version, update.AssetName("fdsync")))
	}

	cur := update.Version
	isNewer := update.Newer(rel.Version, cur)
	if *checkOnly {
		fmt.Printf("Current version: %s\nLatest release:  %s\n", cur, rel.Version)
		if isNewer {
			fmt.Printf("Update available: %s\nRun `fdsync upgrade` to install.\n", rel.URL)
		} else if cur == rel.Version {
			fmt.Println("Already up to date.")
		} else {
			fmt.Printf("Current build (%s) is not older than %s.\n", cur, rel.Version)
		}
		return
	}

	if *targetVer == "" && !*force && !isNewer {
		fmt.Printf("fdsync is already up to date (%s).\n", cur)
		return
	}

	dir, _ := filepath.Abs(*dataDir)
	systemdRunning := systemdServiceActive()
	bgRunning := !systemdRunning && runningPid(dir) != 0

	fmt.Printf("Downloading fdsync %s (%s)…\n", rel.Version, update.AssetName("fdsync"))
	if err := update.Install(ctx, rel); err != nil {
		fatal(fmt.Errorf("upgrade failed: %w", err))
	}
	exe, err := update.Executable()
	if err != nil {
		fatal(err)
	}
	// On Linux, unlinking the previous binary while an old process is still
	// running is safe and avoids leaving fdsync.old in ~/.local/bin.
	_ = os.Remove(exe + ".old")
	fmt.Printf("Upgraded fdsync: %s → %s (%s)\n", cur, rel.Version, exe)

	if !systemdRunning && !bgRunning {
		return
	}
	if *noRestart {
		if systemdRunning {
			fmt.Println("The systemd service is still running the previous version: restart with `systemctl --user restart fdsync`.")
		} else {
			fmt.Println("The daemon is still running the previous version: restart with `fdsync stop && fdsync start`.")
		}
		return
	}

	if systemdRunning {
		fmt.Println("Restarting systemd user service fdsync…")
		cmd := exec.Command("systemctl", "--user", "restart", "fdsync")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fatal(fmt.Errorf("restart systemd service: %w", err))
		}
		fmt.Println("Restarted fdsync service.")
		return
	}

	fmt.Println("Restarting background daemon…")
	if _, err := stopDaemon(dir); err != nil {
		fatal(fmt.Errorf("stop previous daemon: %w", err))
	}
	startArgs := []string{"start"}
	if dir != defaultDataDir() {
		startArgs = append(startArgs, "--data-dir", dir)
	}
	cmd := exec.Command(exe, startArgs...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fatal(fmt.Errorf("start upgraded daemon: %w", err))
	}
}

func systemdServiceActive() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "--user", "is-active", "--quiet", "fdsync").Run() == nil
}
