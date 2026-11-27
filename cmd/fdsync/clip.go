package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fdsync/internal/client"
	"fdsync/internal/protocol"
)

const clipHelpText = `fdsync clip — exchange screenshots & clipboard content between Windows and WSL

USAGE
  fdsync clip [get]  [flags] [ID...]      download Windows screenshots to a local folder
  fdsync clip list   [flags]              list available screenshots and metadata
  fdsync clip push   [flags] <FILE | ->   send an image from WSL to Windows (clipboard + notification)
  fdsync clip copy   [flags] [TEXT...]    copy UTF-8 text (or stdin) from WSL to Windows clipboard
  fdsync clip help                        show this guide (for humans and AI assistants)

GUIDE FOR AI ASSISTANTS
  When the user asks you to inspect Windows screenshots or clipboard images:

  1. Map the user's request to the right command (prefer --json for structured output):
     • "Look at my screenshot" / "read the images I selected" (no explicit count):
         fdsync clip --json
       Behavior: if the user pinned/selected one or more images in the Windows panel,
       downloads those selected images; otherwise downloads the latest 1 screenshot.

     • "Look at my latest N screenshots" (explicit count, e.g. latest 2 or 3):
         fdsync clip --latest N --json
       Behavior: ignores panel selection and always downloads the newest N screenshots.

     • "What screenshots do I have?" / "list my clipboard images":
         fdsync clip list --json

     • User names specific IDs (e.g. clip-0003 clip-0005):
         fdsync clip get --json clip-0003 clip-0005

  2. Read the downloaded PNG files:
     • Inspect every path in files[].path in parallel using your image/file reading tool.
     • Check each file's "note" field (optional label added in the Windows panel,
       such as "before", "after", or "error") and "capturedAt" timestamp.
     • If originalWidth/originalHeight are present and small text is hard to read,
       let the user know the image was downscaled so they can crop closer or turn
       off Auto-resize in the Windows panel.

  3. Handle stale selections gracefully:
     • If mode == "selected" and newerUnselected > 0, there are newer screenshots on
       Windows that were not selected in the panel. Mention this briefly in case the
       user forgot to clear an earlier selection.

  4. Sending an image or text back to Windows (reverse direction):
     • Send a generated PNG/JPEG/GIF image to Windows (copies to Windows clipboard
       and pops a clickable Windows notification that opens the image viewer):
         fdsync clip push /path/to/chart.png --note "benchmark"
     • Copy text from WSL to the Windows clipboard:
         printf '%s' "some text" | fdsync clip copy

  5. Privacy & workspace hygiene:
     • Read images directly from their temporary output directory (/tmp/fdsync-clip by
       default). Do not copy user screenshots into the project repository.

FLAGS (for get / default)
  --latest N, -n N   download the newest N screenshots (ignores panel selection)
  --selected         download only screenshots marked as Selected in the panel
  --all              download all available screenshots
  --out DIR, -o DIR  output directory (default: /tmp/fdsync-clip)
  --keep             keep previous PNG files in the default output directory
  --json             print structured JSON (recommended for scripts and AI tools)

FLAGS (for push)
  --note TEXT        optional short label shown in the notification and panel
  --no-copy          do not overwrite the Windows clipboard (only add to panel + notify)
  --json             print the created item as JSON

FLAGS (common)
  --data-dir DIR     fdsync state directory (reuses daemon.sock if daemon is running)
  --credentials FILE credentials file saved by fdsync connect
`

const clipUsage = `Usage:
  fdsync clip [get]  [flags] [ID...]      download Windows screenshots to a local folder
  fdsync clip list   [flags]              list available screenshots on Windows
  fdsync clip push   [flags] <FILE | ->   send an image from WSL to Windows (notifies & copies)
  fdsync clip copy   [flags] [TEXT...]    copy text (or stdin) from WSL to Windows clipboard
  fdsync clip help                        show detailed usage & AI assistant guide

Flags:
`

type clipSelectOptions struct {
	Latest   int
	Selected bool
	All      bool
	IDs      []string
}

type clipSelection struct {
	Mode            string
	Items           []protocol.ClipboardItem
	NewerUnselected int
}

func selectClipboardItems(items []protocol.ClipboardItem, opt clipSelectOptions) (clipSelection, error) {
	if len(opt.IDs) > 0 {
		byID := make(map[string]protocol.ClipboardItem, len(items))
		for _, it := range items {
			byID[it.ID] = it
		}
		out := make([]protocol.ClipboardItem, 0, len(opt.IDs))
		for _, id := range opt.IDs {
			it, ok := byID[id]
			if !ok {
				return clipSelection{}, fmt.Errorf("clipboard image %q not found", id)
			}
			out = append(out, it)
		}
		return clipSelection{Mode: "ids", Items: out}, nil
	}
	if opt.Latest > 0 {
		n := min(opt.Latest, len(items))
		return clipSelection{Mode: "latest", Items: append([]protocol.ClipboardItem(nil), items[:n]...)}, nil
	}
	if opt.All {
		return clipSelection{Mode: "all", Items: append([]protocol.ClipboardItem(nil), items...)}, nil
	}

	var selected []protocol.ClipboardItem
	for _, it := range items {
		if it.Selected {
			selected = append(selected, it)
		}
	}
	if opt.Selected {
		return clipSelection{Mode: "selected", Items: selected}, nil
	}
	if len(selected) > 0 {
		// Items are newest-first, so selected[0] is the newest selected image.
		newer := 0
		for _, it := range items {
			if it.ID == selected[0].ID {
				break
			}
			if !it.Selected {
				newer++
			}
		}
		return clipSelection{Mode: "selected", Items: selected, NewerUnselected: newer}, nil
	}
	if len(items) == 0 {
		return clipSelection{Mode: "latest"}, nil
	}
	return clipSelection{Mode: "latest", Items: []protocol.ClipboardItem{items[0]}}, nil
}

func clip(args []string) {
	sub := "get"
	if len(args) > 0 {
		switch args[0] {
		case "list", "ls":
			sub = "list"
			args = args[1:]
		case "get":
			sub = "get"
			args = args[1:]
		case "push", "send":
			sub = "push"
			args = args[1:]
		case "copy":
			sub = "copy"
			args = args[1:]
		case "help", "-h", "--help":
			_, _ = io.WriteString(os.Stdout, clipHelpText)
			return
		}
	}

	fs := flag.NewFlagSet("clip "+sub, flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "state directory")
	credPath := fs.String("credentials", defaultCredentials(), "credentials saved by connect")
	serverURL := fs.String("server", "", "server address when using the shared token")
	token := fs.String("token", os.Getenv("FDSYNC_TOKEN"), "shared token (not needed after connect)")
	pin := fs.String("fingerprint", "", "server certificate fingerprint (with --token and https)")
	asJSON := fs.Bool("json", false, "output JSON")

	var latest int
	var onlySelected, all, keep, noCopy bool
	var outDir, note string
	defaultOut := filepath.Join(os.TempDir(), "fdsync-clip")
	switch sub {
	case "get":
		fs.IntVar(&latest, "latest", 0, "download the newest N screenshots (ignoring panel selection)")
		fs.IntVar(&latest, "n", 0, "shorthand for --latest")
		fs.BoolVar(&onlySelected, "selected", false, "download only screenshots selected in the panel")
		fs.BoolVar(&all, "all", false, "download all screenshots")
		fs.StringVar(&outDir, "out", defaultOut, "directory to save downloaded PNG files")
		fs.StringVar(&outDir, "o", defaultOut, "shorthand for --out")
		fs.BoolVar(&keep, "keep", false, "keep previous files in the default output directory")
	case "push":
		fs.StringVar(&note, "note", "", "optional short note shown in Windows notification and panel")
		fs.BoolVar(&noCopy, "no-copy", false, "do not write the image to the Windows clipboard")
	}
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, clipUsage)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	src, release, err := openClipboardSource(ctx, *dataDir, *credPath, *serverURL, *token, *pin)
	if err != nil {
		fatal(err)
	}
	defer release()

	if sub == "push" {
		clipPush(ctx, src, fs.Args(), note, !noCopy, *asJSON)
		return
	}
	if sub == "copy" {
		clipCopyText(ctx, src, fs.Args())
		return
	}

	list, err := src.ClipboardList(ctx)
	if err != nil {
		fatal(err)
	}

	if sub == "list" {
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(list)
			return
		}
		printClipboardList(list)
		return
	}

	if latest < 0 {
		fatal(errors.New("--latest must be >= 1"))
	}
	sel, err := selectClipboardItems(list.Items, clipSelectOptions{
		Latest:   latest,
		Selected: onlySelected,
		All:      all,
		IDs:      fs.Args(),
	})
	if err != nil {
		fatal(err)
	}

	type savedFile struct {
		ID             string    `json:"id"`
		Path           string    `json:"path"`
		CapturedAt     time.Time `json:"capturedAt"`
		Width          int       `json:"width"`
		Height         int       `json:"height"`
		OriginalWidth  int       `json:"originalWidth,omitempty"`
		OriginalHeight int       `json:"originalHeight,omitempty"`
		Size           int64     `json:"size"`
		Selected       bool      `json:"selected"`
		Source         string    `json:"source"`
		Note           string    `json:"note,omitempty"`
	}
	result := struct {
		Mode            string      `json:"mode"`
		Paused          bool        `json:"paused"`
		NewerUnselected int         `json:"newerUnselected,omitempty"`
		Files           []savedFile `json:"files"`
	}{
		Mode:            sel.Mode,
		Paused:          list.Paused,
		NewerUnselected: sel.NewerUnselected,
		Files:           []savedFile{},
	}

	if len(sel.Items) > 0 {
		if err := os.MkdirAll(outDir, 0o700); err != nil {
			fatal(err)
		}
		if !keep && filepath.Clean(outDir) == filepath.Clean(defaultOut) {
			cleanClipDir(outDir)
		}
		for i, item := range sel.Items {
			data, err := src.ClipboardImage(ctx, item.ID)
			if err != nil {
				fatal(fmt.Errorf("download %s: %w", item.ID, err))
			}
			name := fmt.Sprintf("%02d_%s.png", i+1, item.ID)
			dst := filepath.Join(outDir, name)
			if err := os.WriteFile(dst, data, 0o600); err != nil {
				fatal(err)
			}
			result.Files = append(result.Files, savedFile{
				ID:             item.ID,
				Path:           dst,
				CapturedAt:     item.CapturedAt,
				Width:          item.Width,
				Height:         item.Height,
				OriginalWidth:  item.OriginalWidth,
				OriginalHeight: item.OriginalHeight,
				Size:           int64(len(data)),
				Selected:       item.Selected,
				Source:         item.Source,
				Note:           item.Note,
			})
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	if len(result.Files) == 0 {
		if onlySelected {
			fmt.Println("No clipboard screenshots are currently selected in the Windows panel.")
		} else {
			fmt.Println("No clipboard screenshots available on Windows.")
		}
		return
	}
	fmt.Printf("Mode: %s (%d image(s))\n", result.Mode, len(result.Files))
	if result.NewerUnselected > 0 {
		fmt.Printf("Note: %d newer unselected screenshot(s) exist on Windows (use --latest N to fetch recent ones)\n", result.NewerUnselected)
	}
	for _, f := range result.Files {
		tag := "          "
		if f.Selected {
			tag = "[selected]"
		}
		dims := fmt.Sprintf("%dx%d", f.Width, f.Height)
		if f.OriginalWidth > 0 && f.OriginalHeight > 0 {
			dims += fmt.Sprintf(" (from %dx%d)", f.OriginalWidth, f.OriginalHeight)
		}
		line := fmt.Sprintf("%s  %s  %s  %s  %s", f.Path, tag, f.CapturedAt.Local().Format("2006-01-02 15:04:05"), dims, humanBytes(f.Size))
		if f.Note != "" {
			line += fmt.Sprintf("  note=%q", f.Note)
		}
		fmt.Println(line)
	}
}

// openClipboardSource connects through the running daemon's Unix socket when
// available (avoiding Refresh Token rotation conflicts). If no daemon is
// running, it acquires daemon.lock before talking to the server directly.
func openClipboardSource(ctx context.Context, dataDir, credPath, serverURL, token, pin string) (client.ClipboardSource, func(), error) {
	noop := func() {}
	if token != "" && serverURL != "" {
		if strings.HasPrefix(serverURL, "https://") && pin == "" {
			return nil, noop, errors.New("an https server needs --fingerprint with --token (or pair with fdsync connect)")
		}
		tr := client.NewTransport()
		if pin != "" {
			tr = client.PinnedTransport(pin)
		}
		dev, _ := deviceID(dataDir)
		c := &client.Client{
			BaseURL: strings.TrimRight(serverURL, "/"),
			Token:   token,
			Device:  dev,
			HTTP:    &http.Client{Transport: tr, Timeout: 30 * time.Second},
			Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		return c, noop, nil
	}

	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	sockPath := filepath.Join(dataDir, "daemon.sock")
	if ipc, err := client.DialLocalIPC(ctx, sockPath); err == nil {
		return ipc, noop, nil
	}

	// No IPC socket answered: acquire daemon.lock so we never rotate the
	// refresh token concurrently with a running or starting daemon.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, noop, err
	}
	lock, other, err := lockDaemon(dataDir)
	if err != nil {
		return nil, noop, err
	}
	if lock == nil {
		return nil, noop, fmt.Errorf("fdsync daemon%s is running without clipboard IPC; restart it with: fdsync stop && fdsync start", pidNote(other))
	}

	creds, err := client.LoadCredentials(credPath)
	if err != nil {
		lock.Close()
		return nil, noop, fmt.Errorf("not connected: run `fdsync connect` first (%v)", err)
	}
	base := strings.TrimRight(creds.ServerURL(), "/")
	hc := &http.Client{Transport: client.PinnedTransport(creds.Fingerprint), Timeout: 30 * time.Second}
	c := &client.Client{
		BaseURL: base,
		Device:  creds.Device,
		HTTP:    hc,
		Tokens:  &client.TokenSource{Creds: creds, Path: credPath, HTTP: hc, BaseURL: base},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return c, func() { lock.Close() }, nil
}

func clipPush(ctx context.Context, src client.ClipboardSource, args []string, note string, copyToWin, asJSON bool) {
	var (
		data []byte
		err  error
	)
	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "-"):
		if len(args) == 0 {
			if fi, _ := os.Stdin.Stat(); fi != nil && (fi.Mode()&os.ModeCharDevice) != 0 {
				fatal(errors.New("give an image file path (or pipe image bytes via stdin): fdsync clip push <image.png>"))
			}
		}
		data, err = io.ReadAll(io.LimitReader(os.Stdin, (25<<20)+1))
	case len(args) == 1:
		data, err = os.ReadFile(args[0])
		if note == "" {
			note = filepath.Base(args[0])
		}
	default:
		fatal(errors.New("give one image path at a time: fdsync clip push <image.png>"))
	}
	if err != nil {
		fatal(err)
	}
	item, err := src.ClipboardPush(ctx, data, note, copyToWin)
	if err != nil {
		fatal(err)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(item)
		return
	}
	fmt.Printf("Sent to Windows: %s (%dx%d, %s)\n", item.ID, item.Width, item.Height, humanBytes(item.Size))
}

func clipCopyText(ctx context.Context, src client.ClipboardSource, args []string) {
	var text string
	if len(args) > 0 && !(len(args) == 1 && args[0] == "-") {
		text = strings.Join(args, " ")
	} else {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, (4<<20)+1))
		if err != nil {
			fatal(err)
		}
		text = string(b)
	}
	if text == "" {
		fatal(errors.New("nothing to copy: pass text as arguments or pipe via stdin"))
	}
	if err := src.ClipboardPushText(ctx, text); err != nil {
		fatal(err)
	}
	fmt.Printf("Copied %d character(s) to Windows clipboard.\n", len([]rune(text)))
}

func cleanClipDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".png") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func printClipboardList(list *protocol.ClipboardList) {
	state := "active"
	if list.Paused {
		state = "paused"
	}
	selected := 0
	for _, it := range list.Items {
		if it.Selected {
			selected++
		}
	}
	fmt.Printf("Clipboard: %s (%d image(s), %d selected, limit %d unselected)\n", state, len(list.Items), selected, list.Max)
	if len(list.Items) == 0 {
		return
	}
	fmt.Printf("%-10s  %-8s  %-19s  %-10s  %-9s  %-9s  %s\n", "ID", "SELECTED", "TIME", "DIMENSIONS", "SIZE", "SOURCE", "NOTE")
	for _, it := range list.Items {
		sel := ""
		if it.Selected {
			sel = "*"
		}
		fmt.Printf("%-10s  %-8s  %-19s  %-10s  %-9s  %-9s  %s\n",
			it.ID,
			sel,
			it.CapturedAt.Local().Format("2006-01-02 15:04:05"),
			fmt.Sprintf("%dx%d", it.Width, it.Height),
			humanBytes(it.Size),
			it.Source,
			it.Note,
		)
	}
}

func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n) / 1024
	if v < 1024 {
		return fmt.Sprintf("%.1f KB", v)
	}
	return fmt.Sprintf("%.1f MB", v/1024)
}
