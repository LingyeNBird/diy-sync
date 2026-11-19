// fdsync-server is the Windows side: a plain exe (not a service) that serves
// the sync API for the folders in its config.json. On Windows it runs in
// the background with a tray icon (no console window).
//
//	fdsync-server                         run (creates config.json on first start)
//	fdsync-server add-folder ID PATH [RULE...]
//	fdsync-server set-rules ID [RULE...]
//	fdsync-server show-config
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/term"

	"fdsync/internal/logfile"
	"fdsync/internal/server"
)

const usage = `fdsync-server: the Windows side of fdsync.

Usage:
  fdsync-server [flags]                         run the server
  fdsync-server [flags] add-folder ID PATH [RULE...]
                                                add a sync folder (RULE e.g. "a/" "c/" "!c/**/node_modules/")
  fdsync-server [flags] set-rules ID [RULE...]  replace a folder's scope rules
  fdsync-server [flags] show-config             print the config file location and contents
  fdsync-server [flags] set-password            set the panel password (asks for it)

Flags:
`

func main() {
	defaultData := filepath.Join(must(os.UserCacheDir()), "fdsync") // %LOCALAPPDATA%\fdsync on Windows
	dataDir := flag.String("data-dir", defaultData, "data directory (config, indexes)")
	listen := flag.String("listen", "", `override the sync API address from config.json, e.g. "0.0.0.0:7443"`)
	panelListen := flag.String("panel", "", `override the panel address from config.json (default "127.0.0.1:7444")`)
	verbose := flag.Bool("v", false, "debug logging")
	openPanel := flag.Bool("open", false, "open the panel in the browser after starting")
	noTray := flag.Bool("no-tray", false, "no tray icon (Windows): run until stopped with Ctrl+C")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage); flag.PrintDefaults() }
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	if abs, err := filepath.Abs(*dataDir); err == nil {
		*dataDir = abs
	}
	// server.log in the data directory (at most 2 × 5 MB), and the console if any.
	var logOut io.Writer = os.Stderr
	if err := os.MkdirAll(*dataDir, 0o700); err == nil {
		if lf, err := logfile.Open(filepath.Join(*dataDir, "server.log")); err == nil {
			defer lf.Close()
			logOut = io.MultiWriter(lf, ignoreErrors{os.Stderr}) // the file first: a missing console must not stop it
		}
	}
	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: level}))
	cfgPath := filepath.Join(*dataDir, "config.json")
	cfg, created, err := server.LoadConfig(cfgPath)
	if err != nil {
		fatal(err)
	}
	if created {
		fmt.Printf("Created %s\nClient token: %s\n\n", cfgPath, cfg.Token)
	}

	switch args := flag.Args(); {
	case len(args) == 0:
	case args[0] == "add-folder" && len(args) >= 3:
		abs, err := filepath.Abs(args[2])
		if err != nil {
			fatal(err)
		}
		for _, f := range cfg.Folders {
			if f.ID == args[1] {
				fatal(fmt.Errorf("folder %q already exists; use set-rules to change its rules", args[1]))
			}
		}
		cfg.Folders = append(cfg.Folders, server.FolderConfig{ID: args[1], Path: abs, Rules: args[3:]})
		if err := server.SaveConfig(cfgPath, cfg); err != nil {
			fatal(err)
		}
		fmt.Printf("Added folder %q → %s with %d rule(s).\n", args[1], abs, len(args[3:]))
		return
	case args[0] == "set-rules" && len(args) >= 2:
		found := false
		for i := range cfg.Folders {
			if cfg.Folders[i].ID == args[1] {
				cfg.Folders[i].Rules = args[2:]
				found = true
			}
		}
		if !found {
			fatal(fmt.Errorf("no folder %q", args[1]))
		}
		if err := server.SaveConfig(cfgPath, cfg); err != nil {
			fatal(err)
		}
		fmt.Printf("Folder %q now has %d rule(s). Restart the server to apply.\n", args[1], len(args[2:]))
		return
	case args[0] == "set-password":
		ownConsole() // a window of its own on Windows: the terminal doesn't wait for this exe
		if runtime.GOOS == "windows" {
			defer time.Sleep(2 * time.Second) // let the result be read before the window closes
		}
		fmt.Print("New panel password: ")
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			fatal(err)
		}
		fmt.Print("Again: ")
		pw2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			fatal(err)
		}
		if string(pw) != string(pw2) {
			fatal(fmt.Errorf("the passwords differ"))
		}
		hash, err := server.HashPassword(string(pw))
		if err != nil {
			fatal(err)
		}
		cfg.PasswordHash = hash
		if err := server.SaveConfig(cfgPath, cfg); err != nil {
			fatal(err)
		}
		fmt.Println("Panel password set.")
		return
	case args[0] == "show-config":
		b, _ := os.ReadFile(cfgPath)
		fmt.Printf("%s\n\n%s", cfgPath, b)
		return
	default:
		flag.Usage()
		os.Exit(2)
	}

	if *listen != "" {
		cfg.Listen = *listen
	}
	if *panelListen != "" {
		cfg.PanelListen = *panelListen
	}
	if cfg.PanelListen == "" {
		cfg.PanelListen = "127.0.0.1:7444"
	}
	fmt.Printf("Panel: http://%s/\n", cfg.PanelListen)
	if cfg.PasswordHash == "" {
		fmt.Println("No panel password yet: open the panel on this machine to set one (or run set-password).")
	}
	cfg.DataDir = *dataDir
	if len(cfg.Folders) == 0 {
		fmt.Fprintf(os.Stderr, "No sync folders yet. Add one, for example:\n  fdsync-server add-folder projects C:\\Users\\me\\projects \"a/\" \"c/\"\n")
	}
	panelURL := "http://" + cfg.PanelListen + "/"
	// Started again while running (a second double-click): show the panel.
	if alreadyRunning(cfg.PanelListen) {
		fmt.Println("fdsync-server is already running; opening the panel.")
		openBrowser(panelURL)
		return
	}
	// The login start runs this exe with the same flags.
	startArgs := []string{"--data-dir", *dataDir}
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "listen" || f.Name == "panel" || f.Name == "v" {
			startArgs = append(startArgs, "--"+f.Name+"="+f.Value.String())
		}
	})
	auto := newRunKey(startArgs)
	if auto != nil {
		cfg.Autostart = auto
	}
	srv, err := server.New(cfg, log)
	if err != nil {
		fatal(err)
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log.Info("starting", "device", srv.Device(), "data", *dataDir)
	if *openPanel || cfg.PasswordHash == "" { // first start: the panel asks for a password
		go func() {
			time.Sleep(time.Second) // let the listener come up
			openBrowser(panelURL)
		}()
	}
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = srv.Run(ctx)
		close(done)
	}()
	runUI(ctx, stop, srv, panelURL, auto, done, *noTray)
	<-done
	if runErr != nil {
		fatal(runErr)
	}
}

// alreadyRunning reports whether an fdsync panel answers at addr.
func alreadyRunning(addr string) bool {
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get("http://" + addr + "/api/session")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type ignoreErrors struct{ w io.Writer }

func (i ignoreErrors) Write(p []byte) (int, error) {
	_, _ = i.w.Write(p)
	return len(p), nil
}

// openBrowser opens url in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func must[T any](v T, err error) T {
	if err != nil {
		fatal(err)
	}
	return v
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "fdsync-server:", err)
	showError("fdsync-server", err.Error())
	os.Exit(1)
}
