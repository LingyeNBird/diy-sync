package client

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fdsync/internal/protocol"
)

func solidPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestClipboardPanelAPIAndDaemonIPC(t *testing.T) {
	e := newEnv(t, "a/")
	p := newPanel(t, e)
	p.login()

	upload := func(pngBytes []byte, query string) protocol.ClipboardItem {
		t.Helper()
		req, _ := http.NewRequest("POST", p.ps.URL+"/api/clipboard/upload"+query, bytes.NewReader(pngBytes))
		req.Header.Set("Content-Type", "image/png")
		req.Header.Set("X-CSRF-Token", p.csrf)
		resp, err := p.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("upload: %d %s", resp.StatusCode, b)
		}
		var item protocol.ClipboardItem
		if err := json.Unmarshal(b, &item); err != nil {
			t.Fatal(err)
		}
		return item
	}

	img1 := solidPNG(t, 12, 8, color.NRGBA{R: 200, G: 40, B: 40, A: 255})
	img2 := solidPNG(t, 16, 10, color.NRGBA{R: 40, G: 200, B: 40, A: 255})

	it1 := upload(img1, "?select=1&note=first")
	it2 := upload(img2, "?note=second")
	if !it1.Selected || it1.Note != "first" || it2.Selected {
		t.Fatalf("unexpected uploaded items: %+v %+v", it1, it2)
	}

	// Overview reports clipboard summary without nulls.
	var ov struct {
		Clipboard struct {
			Count, Selected int
			Paused          bool
		}
	}
	p.call("GET", "/api/overview", nil, &ov)
	if ov.Clipboard.Count != 2 || ov.Clipboard.Selected != 1 || ov.Clipboard.Paused {
		t.Fatalf("overview clipboard = %+v", ov.Clipboard)
	}

	// Panel image download and PATCH update.
	if code, h, body := p.call("GET", "/api/clipboard/"+it1.ID, nil, nil); code != 200 || h.Get("Content-Type") != "image/png" || !bytes.Equal(body, img1) {
		t.Fatalf("panel get image: code=%d ct=%q len=%d", code, h.Get("Content-Type"), len(body))
	}
	var updated protocol.ClipboardItem
	if code, _, b := p.call("PATCH", "/api/clipboard/"+it2.ID, map[string]any{"selected": true, "note": "updated note"}, &updated); code != 200 || !updated.Selected || updated.Note != "updated note" {
		t.Fatalf("patch: %d %s", code, b)
	}

	// Pause toggle via panel.
	if code, _, _ := p.call("PUT", "/api/clipboard/pause", map[string]bool{"paused": true}, nil); code != 204 {
		t.Fatalf("pause: %d", code)
	}

	// Direct WSL Client access via Sync API (/api/v1/clipboard).
	cl := e.client()
	ctx := context.Background()
	list, err := cl.c.ClipboardList(ctx)
	if err != nil {
		t.Fatalf("client ClipboardList: %v", err)
	}
	if !list.Paused || len(list.Items) != 2 || list.Items[0].ID != it2.ID || list.Items[1].ID != it1.ID {
		t.Fatalf("sync list = %+v", list)
	}
	got1, err := cl.c.ClipboardImage(ctx, it1.ID)
	if err != nil || !bytes.Equal(got1, img1) {
		t.Fatalf("client ClipboardImage: err=%v len=%d", err, len(got1))
	}

	// Daemon Unix Socket IPC proxy test.
	sockPath := filepath.Join(t.TempDir(), "daemon.sock")
	ipcCtx, ipcCancel := context.WithCancel(context.Background())
	ipcDone := make(chan error, 1)
	go func() {
		ipcDone <- ServeLocalIPC(ipcCtx, sockPath, cl.c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	var ipc *LocalIPCClient
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := DialLocalIPC(ctx, sockPath); err == nil {
			ipc = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ipc == nil {
		t.Fatal("daemon IPC socket never became ready")
	}
	if err := ipc.Ping(ctx); err != nil {
		t.Fatalf("ipc ping: %v", err)
	}

	ipcList, err := ipc.ClipboardList(ctx)
	if err != nil || len(ipcList.Items) != 2 || !ipcList.Paused {
		t.Fatalf("ipc list: %+v err=%v", ipcList, err)
	}
	ipcImg, err := ipc.ClipboardImage(ctx, it2.ID)
	if err != nil || !bytes.Equal(ipcImg, img2) {
		t.Fatalf("ipc image: err=%v len=%d", err, len(ipcImg))
	}
	if _, err := ipc.ClipboardImage(ctx, "clip-9999"); err == nil {
		t.Fatal("expected error for missing clipboard id over IPC")
	}

	// Clean shutdown removes the socket.
	ipcCancel()
	if err := <-ipcDone; err != nil {
		t.Fatalf("ServeLocalIPC exit: %v", err)
	}
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatalf("socket file still exists after shutdown: %v", err)
	}

	// Unselect all, delete one, clear all.
	if code, _, _ := p.call("POST", "/api/clipboard/unselect", nil, nil); code != 204 {
		t.Fatalf("unselect: %d", code)
	}
	if code, _, _ := p.call("DELETE", "/api/clipboard/"+it1.ID, nil, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	listAfter, err := cl.c.ClipboardList(ctx)
	if err != nil || len(listAfter.Items) != 1 || listAfter.Items[0].Selected {
		t.Fatalf("after unselect+delete: %+v err=%v", listAfter, err)
	}
	if code, _, _ := p.call("DELETE", "/api/clipboard", nil, nil); code != 204 {
		t.Fatalf("clear: %d", code)
	}
	listEmpty, err := cl.c.ClipboardList(ctx)
	if err != nil || len(listEmpty.Items) != 0 {
		t.Fatalf("after clear: %+v err=%v", listEmpty, err)
	}
}
