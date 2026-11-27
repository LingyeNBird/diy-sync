// fdsync is the WSL side: a daemon that keeps bindings in sync with the
// Windows server, plus a one-shot sync for testing.
//
//	fdsync daemon --server URL --bind FOLDER=PATH [--bind ...]
//	fdsync sync   --server URL --bind FOLDER=PATH     (one round, then exit)
//
// Phase 1 authenticates with the server's static token (--token or
// FDSYNC_TOKEN). Pairing (fdsync connect) replaces it later.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"fdsync/internal/client"
	"fdsync/internal/fsx"
	"fdsync/internal/logfile"
	"fdsync/internal/protocol"
	"fdsync/internal/update"
)

type binds []client.Binding

func (b *binds) String() string { return fmt.Sprint(*b) }
func (b *binds) Set(v string) error {
	id, path, ok := strings.Cut(v, "=")
	if !ok || id == "" || path == "" {
		return errors.New("want FOLDER=PATH")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	*b = append(*b, client.Binding{Folder: id, Root: abs})
	return nil
}

const usage = `fdsync: the WSL side of fdsync.

Usage:
  fdsync connect [https://SERVER:7443]   pair with a server (default: the Windows host); approve the code in its panel
  fdsync start  [daemon flags]            run the daemon in the background
  fdsync stop                             stop the background daemon
  fdsync daemon [--bind FOLDER=PATH ...]  keep syncing in this terminal (bindings can also be made in the panel)
  fdsync sync   [--bind FOLDER=PATH ...]  one round, then exit
  fdsync clip   [list|get|help] [flags]   list, fetch, or show AI usage guide for Windows screenshots
  fdsync status                           show the connection and bindings
  fdsync upgrade [--check]                check for and install the newest release (restarts daemon)
  fdsync help   [clip]                    show usage help (use "fdsync help clip" for AI guide)
  fdsync install-service                  write a systemd user unit for the daemon
  fdsync version                          print the version

After connect, daemon and sync need no server flags. Without pairing they
can use the server's shared token: --server URL --token T (or $FDSYNC_TOKEN),
plus --fingerprint for an https server.

Flags:
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	switch cmd {
	case "connect":
		connect(os.Args[2:])
		return
	case "status":
		status(os.Args[2:])
		return
	case "start":
		start(os.Args[2:])
		return
	case "stop":
		stop(os.Args[2:])
		return
	case "clip":
		clip(os.Args[2:])
		return
	case "upgrade", "update":
		upgrade(os.Args[2:])
		return
	case "help", "--help", "-h":
		if len(os.Args) > 2 && os.Args[2] == "clip" {
			_, _ = io.WriteString(os.Stdout, clipHelpText)
			return
		}
		_, _ = io.WriteString(os.Stdout, usage)
		return
	case "install-service":
		installService()
		return
	case "version", "--version", "-v":
		fmt.Println("fdsync", update.Version)
		return
	case "daemon", "sync":
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	serverURL := fs.String("server", "", "server address when using the shared token, e.g. http://172.28.0.1:7443")
	token := fs.String("token", os.Getenv("FDSYNC_TOKEN"), "the server's shared token (default $FDSYNC_TOKEN); not needed after connect")
	pin := fs.String("fingerprint", "", "with --token and an https server: its certificate fingerprint")
	credPath := fs.String("credentials", defaultCredentials(), "credentials saved by connect")
	dataDir := fs.String("data-dir", defaultDataDir(), "state directory (indexes, device id)")
	delay := fs.Duration("delay", time.Second, "batch local changes for this long before syncing")
	rescan := fs.Duration("rescan", time.Hour, "full rescan period")
	verbose := fs.Bool("v", false, "debug logging")
	trashMB := fs.Int("trash-mb", 50, "trash size for all bindings together, in MB (at most 50; 0 = off)")
	browseRoot := fs.String("browse-root", "", "the panel may browse and bind only under this folder (default $HOME)")
	var bs binds
	fs.Var(&bs, "bind", "FOLDER=PATH (repeatable)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	_ = fs.Parse(os.Args[2:])
	var creds *client.Credentials
	if *token == "" {
		var err error
		if creds, err = client.LoadCredentials(*credPath); err != nil {
			fatal(fmt.Errorf("not connected: run `fdsync connect` first (%v)", err))
		}
		*serverURL = creds.ServerURL()
	} else if *serverURL == "" {
		fs.Usage()
		os.Exit(2)
	}
	if strings.HasPrefix(*serverURL, "https://") && creds == nil && *pin == "" {
		fatal(errors.New("an https server needs --fingerprint with --token (or pair with fdsync connect)"))
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	if abs, err := filepath.Abs(*dataDir); err == nil {
		*dataDir = abs
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		fatal(err)
	}
	// One daemon (or sync) per state directory: a second one leaves at once,
	// so starting it from every new shell is safe.
	lock, other, err := lockDaemon(*dataDir)
	if err != nil {
		fatal(err)
	}
	if lock == nil {
		fmt.Fprintf(os.Stderr, "fdsync is already running%s for %s.\n", pidNote(other), *dataDir)
		os.Exit(0)
	}
	defer lock.Close()
	// The console and fdsync.log in the state directory (at most 2 × 5 MB).
	var logOut io.Writer = os.Stderr
	if lf, err := logfile.Open(filepath.Join(*dataDir, "fdsync.log")); err == nil {
		defer lf.Close()
		if os.Getenv(backgroundEnv) != "" {
			logOut = lf // started by fdsync start: no terminal
		} else {
			logOut = io.MultiWriter(lf, os.Stderr) // the file first, so a broken terminal can't lose a line
		}
	}
	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: level}))
	dev, err := deviceID(*dataDir)
	if err != nil {
		fatal(err)
	}
	trash, err := fsx.OpenTrash(filepath.Join(*dataDir, "trash"), int64(*trashMB)<<20)
	if err != nil {
		fatal(err)
	}
	host, _ := os.Hostname()
	transport := client.NewTransport()
	switch {
	case creds != nil:
		transport = client.PinnedTransport(creds.Fingerprint)
	case *pin != "":
		transport = client.PinnedTransport(*pin)
	}
	hc := &http.Client{Transport: transport}
	c := &client.Client{
		Info:    protocol.Hello{Name: host, Distro: distro(), Version: update.Version, NetworkMode: networkMode()},
		Trash:   trash,
		Root:    *browseRoot,
		BaseURL: strings.TrimRight(*serverURL, "/"),
		Token:   *token,
		Device:  dev,
		HTTP:    hc,
		Log:     log,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	revoked := false
	if creds != nil {
		c.Device = creds.Device
		var resolve func() string
		if creds.ViaGateway {
			resolve = func() string { return strings.TrimRight(creds.ServerURL(), "/") }
			c.RefreshBaseURL = resolve
		}
		c.Tokens = &client.TokenSource{Creds: creds, Path: *credPath, HTTP: hc, BaseURL: c.BaseURL, RefreshBaseURL: resolve}
		c.OnRevoked = func() {
			revoked = true
			log.Error("the server revoked this client: sync stopped. Pair again with: fdsync connect")
			stop()
		}
	}
	defer func() {
		if revoked {
			os.Exit(3)
		}
	}()

	// Bindings from --bind, plus the ones created in the panel earlier.
	saved := loadBindings(*dataDir)
	for _, b := range saved {
		if !hasBinding(bs, b) {
			bs = append(bs, b)
		}
	}
	if len(bs) == 0 && cmd == "sync" {
		fatal(errors.New("no bindings: use --bind FOLDER=PATH, or create one in the panel while the daemon runs"))
	}
	open := func(b client.Binding) (*client.Session, error) {
		h := sha256.Sum256([]byte(b.Root))
		return c.Open(b, filepath.Join(*dataDir, fmt.Sprintf("%s-%s", b.Folder, hex.EncodeToString(h[:4]))))
	}
	var sessions []*client.Session
	for _, b := range bs {
		s, err := open(b)
		if err != nil {
			fatal(err)
		}
		defer s.Close()
		sessions = append(sessions, s)
	}

	if cmd == "sync" {
		failed := false
		for i, s := range sessions {
			st, err := s.SyncOnce(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", bs[i].Folder, err)
				failed = true
				continue
			}
			fmt.Printf("%s ↔ %s: %d local change(s), %d uploaded, %d pulled, %d conflict(s), %d excluded\n",
				bs[i].Folder, bs[i].Root, st.Scanned.Changed, st.Uploaded, st.Pulled, st.Conflicts, len(st.Scanned.Excluded))
			for _, x := range st.Scanned.Excluded {
				fmt.Printf("  excluded %s (%s)\n", x.Path, x.Reason)
			}
		}
		if failed {
			os.Exit(1)
		}
		return
	}

	log.Info("daemon starting", "device", dev, "server", c.BaseURL, "bindings", len(sessions))
	var wg sync.WaitGroup
	start := func(s *client.Session) {
		wg.Add(1)
		go func() { defer wg.Done(); s.Run(ctx, *delay, *rescan) }()
	}
	var bindMu sync.Mutex
	c.OnBind = func(b client.Binding) error { // a binding created in the panel
		bindMu.Lock()
		defer bindMu.Unlock()
		s, err := open(b)
		if err != nil {
			return err
		}
		saved = append(saved, b)
		if err := saveBindings(*dataDir, saved); err != nil {
			log.Warn("save bindings", "err", err)
		}
		log.Info("binding created in the panel", "folder", b.Folder, "path", b.Root)
		start(s)
		return nil
	}
	go c.ServeRequests(ctx) // previews, deletes and new bindings asked for in the panel
	sockPath := filepath.Join(*dataDir, "daemon.sock")
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := client.ServeLocalIPC(ctx, sockPath, c, log); err != nil && ctx.Err() == nil {
			log.Warn("local ipc stopped", "err", err)
		}
	}()
	for _, s := range sessions {
		start(s)
	}
	<-ctx.Done()
	wg.Wait()
}

func hasBinding(bs []client.Binding, b client.Binding) bool {
	for _, x := range bs {
		if x.Folder == b.Folder && x.Root == b.Root {
			return true
		}
	}
	return false
}

// bindings.json keeps bindings created in the panel.
func loadBindings(dir string) []client.Binding {
	var bs []client.Binding
	if b, err := os.ReadFile(filepath.Join(dir, "bindings.json")); err == nil {
		_ = json.Unmarshal(b, &bs)
	}
	return bs
}

func saveBindings(dir string, bs []client.Binding) error {
	b, _ := json.MarshalIndent(bs, "", "  ")
	tmp := filepath.Join(dir, "bindings.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "bindings.json"))
}

// distro returns PRETTY_NAME from /etc/os-release.
func distro() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// networkMode asks WSL (wslinfo, WSL 2.0+) how this instance is networked.
func networkMode() string {
	out, err := exec.Command("wslinfo", "--networking-mode").Output()
	if err != nil {
		return "nat"
	}
	return strings.ToLower(strings.TrimSpace(string(out)))
}

// installService writes ~/.config/systemd/user/fdsync.service. It does not
// enable it: it prints the commands, so starting a service stays a choice.
func installService() {
	exe, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		fatal(err)
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	unit := filepath.Join(dir, "systemd", "user", "fdsync.service")
	body := fmt.Sprintf(`[Unit]
Description=fdsync: sync with the Windows fdsync-server
After=network-online.target

[Service]
ExecStart=%s daemon
Restart=on-failure
RestartSec=10
# Exit code 3 means the server revoked this client: don't restart then.
RestartPreventExitStatus=3

[Install]
WantedBy=default.target
`, exe)
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(unit, []byte(body), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("Wrote %s\n\nStart it now and at every login (WSL needs systemd enabled in /etc/wsl.conf):\n  systemctl --user daemon-reload\n  systemctl --user enable --now fdsync\n\nLogs: journalctl --user -u fdsync -f   (also %s)\n",
		unit, filepath.Join(defaultDataDir(), "fdsync.log"))
}

func defaultCredentials() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "fdsync", "credentials.json")
}

// connect pairs this client with a server (OAuth device flow, TLS pinned
// on first use like SSH).
func connect(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "state directory (device id)")
	credPath := fs.String("credentials", defaultCredentials(), "where to save the credentials")
	want := fs.String("fingerprint", "", "expected server fingerprint (skips the question)")
	yes := fs.Bool("yes", false, "trust the fingerprint without asking")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: fdsync connect [flags] [https://SERVER:7443]\n\nWithout a server, connects to the Windows host (WSL's default gateway).\n\nFlags:\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() > 1 {
		fs.Usage()
		os.Exit(2)
	}
	gw, gwErr := client.WindowsHost()
	arg := fs.Arg(0)
	if arg == "" {
		if gwErr != nil {
			fatal(fmt.Errorf("give the server address: %w", gwErr))
		}
		arg = "https://" + gw + ":7443"
	}
	u, err := url.Parse(strings.TrimRight(arg, "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		fatal(fmt.Errorf("give the server as https://HOST:PORT (got %q)", arg))
	}
	host := u.Host
	if u.Port() == "" {
		host += ":7443"
	}
	base := "https://" + host
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fp, err := client.FetchFingerprint(ctx, host)
	if err != nil {
		fatal(fmt.Errorf("cannot reach %s: %w", host, err))
	}
	fmt.Printf("Server certificate fingerprint:\n  %s\n", fp)
	switch {
	case *want != "":
		if *want != fp {
			fatal(errors.New("this is NOT the fingerprint you expected; not connecting"))
		}
	case !*yes:
		fmt.Print("Compare it with the panel (Settings → Encryption). Same? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !strings.EqualFold(strings.TrimSpace(answer), "y") && !strings.EqualFold(strings.TrimSpace(answer), "yes") {
			fatal(errors.New("not connecting"))
		}
	}

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		fatal(err)
	}
	dev, err := deviceID(*dataDir)
	if err != nil {
		fatal(err)
	}
	hostname, _ := os.Hostname()
	hc := &http.Client{Transport: client.PinnedTransport(fp)}
	tok, err := client.Pair(ctx, base, hc, protocol.PairStart{Device: dev, Hostname: hostname, Distro: distro()}, func(st protocol.PairStarted) {
		fmt.Printf("\nApprove this client in the fdsync panel on Windows:\n  %s\n  (or Pairing → enter the code)\n\n  Code: %s\n\nWaiting for approval (expires in %d min)…\n",
			st.VerificationURI, st.UserCode, st.ExpiresIn/60)
	})
	if err != nil {
		fatal(err)
	}
	creds := &client.Credentials{Server: base, Fingerprint: fp, Device: tok.Device, Name: tok.Name, RefreshToken: tok.RefreshToken,
		ViaGateway: gwErr == nil && u.Hostname() == gw}
	if err := creds.Save(*credPath); err != nil {
		fatal(err)
	}
	fmt.Printf("Paired as %q. Credentials saved to %s.\nStart syncing with: fdsync daemon\n", tok.Name, *credPath)
}

// status shows the saved connection, whether the server answers, and the bindings.
func status(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "state directory")
	credPath := fs.String("credentials", defaultCredentials(), "credentials saved by connect")
	_ = fs.Parse(args)
	pid := runningPid(*dataDir)
	if pid != 0 {
		fmt.Printf("Daemon:      running%s\n", pidNote(pid))
	} else {
		fmt.Println("Daemon:      not running (start it with: fdsync start)")
	}
	creds, err := client.LoadCredentials(*credPath)
	if err != nil {
		fmt.Println("Not paired (no credentials). Run: fdsync connect")
	} else {
		where := creds.ServerURL()
		if creds.ViaGateway {
			where += " (the Windows host, followed when its address changes)"
		}
		fmt.Printf("Server:      %s\nConnection:  %s (device %s)\nFingerprint: %s\n", where, creds.Name, creds.Device, creds.Fingerprint)
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		sockPath := filepath.Join(*dataDir, "daemon.sock")
		if ipc, err := client.DialLocalIPC(ctx, sockPath); err == nil {
			if err := ipc.Ping(ctx); err != nil {
				fmt.Printf("Server:      not usable: %v\n", err)
			} else {
				fmt.Println("Server:      reachable, credentials valid (via daemon)")
			}
		} else if pid != 0 {
			fmt.Println("Server:      check skipped (daemon running without local IPC; restart it to check safely)")
		} else if lock, _, lerr := lockDaemon(*dataDir); lerr == nil && lock != nil {
			defer lock.Close()
			hc := &http.Client{Transport: client.PinnedTransport(creds.Fingerprint), Timeout: 10 * time.Second}
			ts := &client.TokenSource{Creds: creds, Path: *credPath, HTTP: hc, BaseURL: creds.ServerURL()}
			if _, err := ts.Token(ctx); err != nil {
				fmt.Printf("Server:      not usable: %v\n", err)
			} else {
				fmt.Println("Server:      reachable, credentials valid")
			}
		} else {
			fmt.Println("Server:      check skipped (daemon is starting)")
		}
	}
	bs := loadBindings(*dataDir)
	if len(bs) == 0 {
		fmt.Println("Bindings:    none saved (create them in the panel, or use --bind)")
	}
	for _, b := range bs {
		fmt.Printf("Binding:     %s ↔ %s\n", b.Folder, b.Root)
	}
}

func defaultDataDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "fdsync")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "fdsync")
}

func deviceID(dir string) (string, error) {
	p := filepath.Join(dir, "device-id")
	if b, err := os.ReadFile(p); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	id := "c-" + hex.EncodeToString(b)
	return id, os.WriteFile(p, []byte(id+"\n"), 0o600)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "fdsync:", err)
	os.Exit(1)
}
