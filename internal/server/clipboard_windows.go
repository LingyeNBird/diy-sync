//go:build windows

package server

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"fdsync/internal/protocol"
)

var (
	user32Clip   = windows.NewLazySystemDLL("user32.dll")
	kernel32Clip = windows.NewLazySystemDLL("kernel32.dll")
	shell32Clip  = windows.NewLazySystemDLL("shell32.dll")

	procRegisterClassExW            = user32Clip.NewProc("RegisterClassExW")
	procCreateWindowExW             = user32Clip.NewProc("CreateWindowExW")
	procDestroyWindow               = user32Clip.NewProc("DestroyWindow")
	procDefWindowProcW              = user32Clip.NewProc("DefWindowProcW")
	procGetMessageW                 = user32Clip.NewProc("GetMessageW")
	procDispatchMessageW            = user32Clip.NewProc("DispatchMessageW")
	procPostMessageW                = user32Clip.NewProc("PostMessageW")
	procPostQuitMessage             = user32Clip.NewProc("PostQuitMessage")
	procLoadIconW                   = user32Clip.NewProc("LoadIconW")
	procSetTimer                    = user32Clip.NewProc("SetTimer")
	procKillTimer                   = user32Clip.NewProc("KillTimer")
	procAddClipboardFormatListener  = user32Clip.NewProc("AddClipboardFormatListener")
	procRemoveClipboardFormatListen = user32Clip.NewProc("RemoveClipboardFormatListener")
	procRegisterClipboardFormatW    = user32Clip.NewProc("RegisterClipboardFormatW")
	procOpenClipboard               = user32Clip.NewProc("OpenClipboard")
	procCloseClipboard              = user32Clip.NewProc("CloseClipboard")
	procEmptyClipboard              = user32Clip.NewProc("EmptyClipboard")
	procGetClipboardData            = user32Clip.NewProc("GetClipboardData")
	procSetClipboardData            = user32Clip.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable  = user32Clip.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber  = user32Clip.NewProc("GetClipboardSequenceNumber")

	procGetModuleHandleW = kernel32Clip.NewProc("GetModuleHandleW")
	procGlobalAlloc      = kernel32Clip.NewProc("GlobalAlloc")
	procGlobalFree       = kernel32Clip.NewProc("GlobalFree")
	procGlobalLock       = kernel32Clip.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32Clip.NewProc("GlobalUnlock")
	procGlobalSize       = kernel32Clip.NewProc("GlobalSize")
	procRtlMoveMemory    = kernel32Clip.NewProc("RtlMoveMemory")

	procDragQueryFileW  = shell32Clip.NewProc("DragQueryFileW")
	procShellNotifyIcon = shell32Clip.NewProc("Shell_NotifyIconW")
	procShellExecuteW   = shell32Clip.NewProc("ShellExecuteW")
)

const (
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmTimer           = 0x0113
	wmClipboardUpdate = 0x031D
	wmUser            = 0x0400
	wmTrayNotify      = wmUser + 0x20
	wmShowNotify      = wmUser + 0x21

	ninBalloonUserClick = 0x0405

	nimAdd        = 0x00000000
	nimModify     = 0x00000001
	nimDelete     = 0x00000002
	nimSetVersion = 0x00000004

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010

	niifInfo            = 0x00000001
	notifyIconVersion4  = 4
	notifyIconID        = 0xFD5C
	timerClearNotifyID  = 1
	notifyLifetimeMs    = 45_000
	idiApplication      = 32512
	swShowNormal        = 1
	gmemMoveable        = 0x0002
	cfDIB               = 8
	cfUnicodeText       = 13
	cfHDrop             = 15
	cfDIBV5             = 17
)

type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// notifyIconDataW matches Windows SDK NOTIFYICONDATAW (976 bytes on 64-bit).
type notifyIconDataW struct {
	CbSize           uint32
	Hwnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type notifyPayload struct {
	title    string
	body     string
	imageID  string
	pngBytes []byte
}

type winClipboardBridge struct {
	mu          sync.Mutex
	hwnd        uintptr
	cfPNG       uint32
	cfImagePNG  uint32
	lastSeq     atomic.Uint32
	log         *slog.Logger
	pending     *notifyPayload
	activeClick *notifyPayload
	iconAdded   bool
	hIcon       uintptr
}

func newWinClipboardBridge() *winClipboardBridge {
	b := &winClipboardBridge{}
	if r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("PNG")))); r != 0 {
		b.cfPNG = uint32(r)
	}
	if r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("image/png")))); r != 0 {
		b.cfImagePNG = uint32(r)
	}
	return b
}

var (
	clipWndMu      sync.Mutex
	clipWndBridges = map[uintptr]*winClipboardBridge{}
	clipWndSigs    = map[uintptr]chan struct{}{}
	clipWndProc    = syscall.NewCallback(clipboardWndProc)
	clipRegOnce    sync.Once
	clipRegErr     error
	clipClass      = windows.StringToUTF16Ptr("fdsyncClipboardListener")
)

func clipboardWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmClipboardUpdate:
		clipWndMu.Lock()
		ch := clipWndSigs[hwnd]
		clipWndMu.Unlock()
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		return 0
	case wmShowNotify:
		clipWndMu.Lock()
		b := clipWndBridges[hwnd]
		clipWndMu.Unlock()
		if b != nil {
			b.showNotificationOnUIThread(hwnd)
		}
		return 0
	case wmTrayNotify:
		code := uint32(lParam & 0xFFFF)
		if code == ninBalloonUserClick {
			clipWndMu.Lock()
			b := clipWndBridges[hwnd]
			clipWndMu.Unlock()
			if b != nil {
				b.handleNotificationClickOnUIThread(hwnd)
			}
		}
		return 0
	case wmTimer:
		if wParam == timerClearNotifyID {
			clipWndMu.Lock()
			b := clipWndBridges[hwnd]
			clipWndMu.Unlock()
			if b != nil {
				b.removeNotifyIconOnUIThread(hwnd)
			}
		}
		return 0
	case wmClose:
		clipWndMu.Lock()
		b := clipWndBridges[hwnd]
		clipWndMu.Unlock()
		if b != nil {
			b.removeNotifyIconOnUIThread(hwnd)
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func registerClipClass(hInst uintptr) error {
	clipRegOnce.Do(func() {
		wc := wndClassExW{
			Size:      uint32(unsafe.Sizeof(wndClassExW{})),
			WndProc:   clipWndProc,
			Instance:  hInst,
			ClassName: clipClass,
		}
		atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			clipRegErr = err
		}
	})
	return clipRegErr
}

type winClipFormats struct {
	png      uint32
	imagePNG uint32
}

// start listens for Windows clipboard image updates and handles Windows toast
// notifications until ctx ends.
func (b *winClipboardBridge) start(ctx context.Context, store *clipboardStore, log *slog.Logger) {
	b.mu.Lock()
	b.log = log
	b.mu.Unlock()
	fmts := winClipFormats{png: b.cfPNG, imagePNG: b.cfImagePNG}
	sig := make(chan struct{}, 1)

	if r, _, _ := procGetClipboardSequenceNumber.Call(); r != 0 {
		b.lastSeq.Store(uint32(r))
	}

	// Worker debounces rapid WM_CLIPBOARDUPDATE bursts and decodes images off
	// the Win32 message thread.
	go func() {
		var timer *time.Timer
		var timerCh <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-sig:
				if timer == nil {
					timer = time.NewTimer(80 * time.Millisecond)
				} else {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(80 * time.Millisecond)
				}
				timerCh = timer.C
			case <-timerCh:
				timerCh = nil
				if store.isPaused() {
					continue
				}
				seqRaw, _, _ := procGetClipboardSequenceNumber.Call()
				seq := uint32(seqRaw)
				if seq != 0 && seq == b.lastSeq.Load() {
					continue
				}
				if seq != 0 {
					b.lastSeq.Store(seq)
				}
				captureWindowsClipboard(store, fmts, log)
			}
		}
	}()

	// Dedicated OS thread for the hidden top-level Win32 window (supports both
	// AddClipboardFormatListener and Shell_NotifyIconW balloon click callbacks).
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hInst, _, _ := procGetModuleHandleW.Call(0)
		if err := registerClipClass(hInst); err != nil {
			log.Warn("clipboard listener fallback to polling (register class failed)", "err", err)
			pollClipboardFallback(ctx, sig)
			return
		}
		hwnd, _, err := procCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(clipClass)),
			0,
			0,
			0, 0, 0, 0,
			0, // hidden top-level window (never shown): required for Shell_NotifyIconW
			0,
			hInst,
			0,
		)
		if hwnd == 0 {
			log.Warn("clipboard listener fallback to polling (create window failed)", "err", err)
			pollClipboardFallback(ctx, sig)
			return
		}
		b.mu.Lock()
		b.hwnd = hwnd
		b.hIcon, _, _ = procLoadIconW.Call(0, idiApplication)
		b.mu.Unlock()

		clipWndMu.Lock()
		clipWndBridges[hwnd] = b
		clipWndSigs[hwnd] = sig
		clipWndMu.Unlock()
		defer func() {
			b.mu.Lock()
			b.hwnd = 0
			b.mu.Unlock()
			clipWndMu.Lock()
			delete(clipWndBridges, hwnd)
			delete(clipWndSigs, hwnd)
			clipWndMu.Unlock()
		}()

		ok, _, err := procAddClipboardFormatListener.Call(hwnd)
		if ok == 0 {
			log.Warn("clipboard listener fallback to polling (AddClipboardFormatListener failed)", "err", err)
			go pollClipboardFallback(ctx, sig)
		} else {
			defer procRemoveClipboardFormatListen.Call(hwnd)
			log.Info("clipboard watcher started")
		}

		go func() {
			<-ctx.Done()
			procPostMessageW.Call(hwnd, wmClose, 0, 0)
		}()

		var m winMsg
		for {
			// Pass hwnd=0 so WM_QUIT (whose m.Hwnd is 0) is also delivered cleanly.
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
}

// onWSLImage handles an image pushed from WSL: optionally writes it to the
// Windows clipboard and pops a clickable Windows notification that opens the
// image with the default viewer when clicked.
func (b *winClipboardBridge) onWSLImage(item protocol.ClipboardItem, pngBytes []byte, copyToWin bool, log *slog.Logger) {
	if copyToWin {
		if err := b.writeClipboardImage(pngBytes); err != nil {
			log.Warn("write wsl image to windows clipboard failed", "id", item.ID, "err", err)
		}
	}
	zh := isWindowsZH()
	title := "fdsync — Image from WSL"
	body := fmt.Sprintf("%dx%d · Click to open with your image viewer", item.Width, item.Height)
	if copyToWin {
		body = fmt.Sprintf("%dx%d · Copied to clipboard · Click to open", item.Width, item.Height)
	}
	if zh {
		title = "fdsync — 收到来自 WSL 的图片"
		body = fmt.Sprintf("%dx%d · 点击用图片工具打开", item.Width, item.Height)
		if copyToWin {
			body = fmt.Sprintf("%dx%d · 已复制到剪贴板 · 点击用图片工具打开", item.Width, item.Height)
		}
	}
	if item.Note != "" {
		body = item.Note + " (" + body + ")"
	}
	b.postNotification(&notifyPayload{
		title:    title,
		body:     body,
		imageID:  item.ID,
		pngBytes: append([]byte(nil), pngBytes...),
	})
}

// onWSLText writes plain text from WSL to the Windows clipboard and displays a
// short Windows notification.
func (b *winClipboardBridge) onWSLText(text string, log *slog.Logger) error {
	if err := b.writeClipboardText(text); err != nil {
		return err
	}
	runes := len([]rune(text))
	preview := strings.Join(strings.Fields(text), " ")
	if pr := []rune(preview); len(pr) > 60 {
		preview = string(pr[:60]) + "…"
	}
	zh := isWindowsZH()
	title := "fdsync — Text copied from WSL"
	body := fmt.Sprintf("%d characters copied", runes)
	if preview != "" {
		body += ": " + preview
	}
	if zh {
		title = "fdsync — 已复制来自 WSL 的文本"
		body = fmt.Sprintf("已复制 %d 个字符", runes)
		if preview != "" {
			body += "：" + preview
		}
	}
	b.postNotification(&notifyPayload{title: title, body: body})
	if log != nil {
		log.Info("copied wsl text to windows clipboard", "chars", runes)
	}
	return nil
}

func (b *winClipboardBridge) postNotification(p *notifyPayload) {
	b.mu.Lock()
	b.pending = p
	hwnd := b.hwnd
	b.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmShowNotify, 0, 0)
	}
}

func (b *winClipboardBridge) showNotificationOnUIThread(hwnd uintptr) {
	b.mu.Lock()
	p := b.pending
	b.pending = nil
	if p == nil {
		b.mu.Unlock()
		return
	}
	b.activeClick = p
	hIcon := b.hIcon
	iconAdded := b.iconAdded
	b.mu.Unlock()

	var nid notifyIconDataW
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.Hwnd = hwnd
	nid.UID = notifyIconID
	nid.UCallbackMessage = wmTrayNotify
	nid.HIcon = hIcon
	copyUTF16(nid.SzTip[:], "fdsync")

	if !iconAdded {
		nid.UFlags = nifMessage | nifIcon | nifTip
		if r, _, _ := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
			return
		}
		nid.UVersion = notifyIconVersion4
		procShellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&nid)))
		b.mu.Lock()
		b.iconAdded = true
		b.mu.Unlock()
	}

	nid.UFlags = nifInfo | nifTip | nifIcon
	nid.DwInfoFlags = niifInfo
	copyUTF16(nid.SzInfoTitle[:], p.title)
	copyUTF16(nid.SzInfo[:], p.body)
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
	procSetTimer.Call(hwnd, timerClearNotifyID, notifyLifetimeMs, 0)
}

func (b *winClipboardBridge) handleNotificationClickOnUIThread(hwnd uintptr) {
	b.mu.Lock()
	p := b.activeClick
	b.activeClick = nil
	log := b.log
	b.mu.Unlock()
	b.removeNotifyIconOnUIThread(hwnd)
	if p == nil || p.imageID == "" || len(p.pngBytes) == 0 {
		return
	}
	go openIncomingImage(p.imageID, p.pngBytes, log)
}

func (b *winClipboardBridge) removeNotifyIconOnUIThread(hwnd uintptr) {
	procKillTimer.Call(hwnd, timerClearNotifyID)
	b.mu.Lock()
	added := b.iconAdded
	b.iconAdded = false
	b.mu.Unlock()
	if !added {
		return
	}
	var nid notifyIconDataW
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.Hwnd = hwnd
	nid.UID = notifyIconID
	procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

func openIncomingImage(id string, pngBytes []byte, log *slog.Logger) {
	dir := filepath.Join(os.TempDir(), "fdsync-incoming")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		if log != nil {
			log.Warn("create incoming dir", "err", err)
		}
		return
	}
	dst := filepath.Join(dir, id+".png")
	if err := os.WriteFile(dst, pngBytes, 0o600); err != nil {
		if log != nil {
			log.Warn("write incoming image", "err", err)
		}
		return
	}
	pruneIncomingDir(dir, 20)
	verb := windows.StringToUTF16Ptr("open")
	filePtr := windows.StringToUTF16Ptr(dst)
	r, _, err := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(filePtr)), 0, 0, swShowNormal)
	if r <= 32 && log != nil {
		log.Warn("ShellExecuteW open image failed", "path", dst, "ret", r, "err", err)
	}
}

func pruneIncomingDir(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= keep {
		return
	}
	type fi struct {
		name string
		mod  time.Time
	}
	files := make([]fi, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, fi{e.Name(), info.ModTime()})
		}
	}
	if len(files) <= keep {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, f := range files[keep:] {
		_ = os.Remove(filepath.Join(dir, f.name))
	}
}

func (b *winClipboardBridge) writeClipboardImage(pngBytes []byte) error {
	var dibBytes []byte
	if img, err := png.Decode(bytes.NewReader(pngBytes)); err == nil {
		dibBytes = encodeDIB(img)
	}
	if !openClipboardWithRetry() {
		return fmt.Errorf("could not open Windows clipboard")
	}
	defer func() {
		procCloseClipboard.Call()
		if seq, _, _ := procGetClipboardSequenceNumber.Call(); seq != 0 {
			b.lastSeq.Store(uint32(seq))
		}
	}()
	procEmptyClipboard.Call()
	wroteAny := false
	if b.cfPNG != 0 && setClipboardBytes(b.cfPNG, pngBytes) {
		wroteAny = true
	}
	if len(dibBytes) > 0 && setClipboardBytes(cfDIB, dibBytes) {
		wroteAny = true
	}
	if !wroteAny {
		return fmt.Errorf("SetClipboardData failed for image")
	}
	return nil
}

func (b *winClipboardBridge) writeClipboardText(text string) error {
	// Normalize LF to CRLF for Windows clipboard compatibility.
	norm := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	u16 := utf16.Encode([]rune(norm))
	u16 = append(u16, 0)
	raw := make([]byte, len(u16)*2)
	for i, c := range u16 {
		raw[i*2] = byte(c)
		raw[i*2+1] = byte(c >> 8)
	}
	if !openClipboardWithRetry() {
		return fmt.Errorf("could not open Windows clipboard")
	}
	defer func() {
		procCloseClipboard.Call()
		if seq, _, _ := procGetClipboardSequenceNumber.Call(); seq != 0 {
			b.lastSeq.Store(uint32(seq))
		}
	}()
	procEmptyClipboard.Call()
	if !setClipboardBytes(cfUnicodeText, raw) {
		return fmt.Errorf("SetClipboardData failed for text")
	}
	return nil
}

func openClipboardWithRetry() bool {
	for range 8 {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return true
		}
		time.Sleep(30 * time.Millisecond)
	}
	return false
}

func setClipboardBytes(fmtID uint32, data []byte) bool {
	if len(data) == 0 {
		return false
	}
	hMem, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if hMem == 0 {
		return false
	}
	ptr, _, _ := procGlobalLock.Call(hMem)
	if ptr == 0 {
		procGlobalFree.Call(hMem)
		return false
	}
	procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	procGlobalUnlock.Call(hMem)
	if r, _, _ := procSetClipboardData.Call(uintptr(fmtID), hMem); r == 0 {
		procGlobalFree.Call(hMem)
		return false
	}
	return true
}

func copyUTF16(dst []uint16, s string) {
	if len(dst) == 0 {
		return
	}
	u := utf16.Encode([]rune(s))
	n := min(len(u), len(dst)-1)
	copy(dst[:n], u[:n])
	dst[n] = 0
}

func isWindowsZH() bool {
	langs, _ := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	return len(langs) > 0 && strings.HasPrefix(strings.ToLower(langs[0]), "zh")
}

// pollClipboardFallback watches GetClipboardSequenceNumber (a cheap kernel
// counter read that never locks the clipboard) if AddClipboardFormatListener
// could not be attached.
func pollClipboardFallback(ctx context.Context, sig chan<- struct{}) {
	var prev uint32
	if r, _, _ := procGetClipboardSequenceNumber.Call(); r != 0 {
		prev = uint32(r)
	}
	t := time.NewTicker(400 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r, _, _ := procGetClipboardSequenceNumber.Call()
			seq := uint32(r)
			if seq != 0 && seq != prev {
				prev = seq
				select {
				case sig <- struct{}{}:
				default:
				}
			}
		}
	}
}

type winClipSnapshot struct {
	pngData   []byte
	dibV5Data []byte
	dibData   []byte
	filePaths []string
}

func captureWindowsClipboard(store *clipboardStore, fmts winClipFormats, log *slog.Logger) {
	snap, ok := readWindowsClipboardImage(fmts)
	if !ok {
		return
	}

	if len(snap.pngData) > 0 {
		item, added, err := store.addImage(snap.pngData, "clipboard", "", false)
		if err == nil {
			if added {
				log.Info("captured clipboard png", "id", item.ID, "w", item.Width, "h", item.Height, "bytes", item.Size)
			}
			return
		}
		log.Warn("clipboard png decode failed; trying dib fallback", "err", err)
	}
	if len(snap.dibV5Data) > 0 {
		item, added, err := store.addDIB(snap.dibV5Data)
		if err == nil {
			if added {
				log.Info("captured clipboard dibv5", "id", item.ID, "w", item.Width, "h", item.Height, "bytes", item.Size)
			}
			return
		}
		log.Warn("clipboard dibv5 decode failed; trying dib fallback", "err", err)
	}
	if len(snap.dibData) > 0 {
		item, added, err := store.addDIB(snap.dibData)
		if err == nil {
			if added {
				log.Info("captured clipboard dib", "id", item.ID, "w", item.Width, "h", item.Height, "bytes", item.Size)
			}
			return
		}
		log.Warn("clipboard dib decode failed", "err", err)
	}
	for _, p := range snap.filePaths {
		data, err := readImageFileForClipboard(p)
		if err != nil {
			log.Warn("clipboard file read failed", "path", p, "err", err)
			continue
		}
		if len(data) == 0 {
			continue
		}
		var (
			item  protocol.ClipboardItem
			added bool
		)
		if strings.EqualFold(filepath.Ext(p), ".bmp") && len(data) > 14 && bytes.Equal(data[:2], []byte("BM")) {
			item, added, err = store.addDIB(data[14:])
		} else {
			item, added, err = store.addImage(data, "clipboard", "", false)
		}
		if err != nil {
			log.Warn("clipboard file decode failed", "path", p, "err", err)
			continue
		}
		if added {
			log.Info("captured clipboard image file", "file", filepath.Base(p), "id", item.ID, "w", item.Width, "h", item.Height, "bytes", item.Size)
		}
	}
}

func readImageFileForClipboard(path string) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp":
	default:
		return nil, nil
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > maxClipboardImageBytes {
		return nil, err
	}
	return os.ReadFile(path)
}

func readWindowsClipboardImage(fmts winClipFormats) (winClipSnapshot, bool) {
	// Quick lock-free check: ignore plain text clipboard updates completely.
	hasPNG := (fmts.png != 0 && formatAvailable(fmts.png)) || (fmts.imagePNG != 0 && formatAvailable(fmts.imagePNG))
	hasDIBV5 := formatAvailable(cfDIBV5)
	hasDIB := formatAvailable(cfDIB)
	hasHDrop := formatAvailable(cfHDrop)
	if !hasPNG && !hasDIBV5 && !hasDIB && !hasHDrop {
		return winClipSnapshot{}, false
	}

	if !openClipboardWithRetry() {
		return winClipSnapshot{}, false
	}
	defer procCloseClipboard.Call()

	var snap winClipSnapshot
	if fmts.png != 0 {
		snap.pngData = readClipboardBytes(fmts.png, maxClipboardImageBytes)
	}
	if len(snap.pngData) == 0 && fmts.imagePNG != 0 {
		snap.pngData = readClipboardBytes(fmts.imagePNG, maxClipboardImageBytes)
	}
	if len(snap.pngData) < 8 || !bytes.Equal(snap.pngData[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		snap.pngData = nil
		snap.dibV5Data = readClipboardBytes(cfDIBV5, maxClipboardRawDIBBytes)
		snap.dibData = readClipboardBytes(cfDIB, maxClipboardRawDIBBytes)
	}
	if len(snap.pngData) == 0 && len(snap.dibV5Data) == 0 && len(snap.dibData) == 0 {
		snap.filePaths = readClipboardHDrop()
	}
	ok := len(snap.pngData) > 0 || len(snap.dibV5Data) > 0 || len(snap.dibData) > 0 || len(snap.filePaths) > 0
	return snap, ok
}

func formatAvailable(fmtID uint32) bool {
	r, _, _ := procIsClipboardFormatAvailable.Call(uintptr(fmtID))
	return r != 0
}

func readClipboardBytes(fmtID uint32, limit uintptr) []byte {
	h, _, _ := procGetClipboardData.Call(uintptr(fmtID))
	if h == 0 {
		return nil
	}
	sz, _, _ := procGlobalSize.Call(h)
	if sz == 0 || sz > limit {
		return nil
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return nil
	}
	defer procGlobalUnlock.Call(h)
	out := make([]byte, int(sz))
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&out[0])), ptr, sz)
	return out
}

// readClipboardHDrop reads up to 10 copied file paths when the user presses
// Ctrl+C on image files in Windows Explorer.
func readClipboardHDrop() []string {
	h, _, _ := procGetClipboardData.Call(cfHDrop)
	if h == 0 {
		return nil
	}
	n, _, _ := procDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
	if n == 0 || n > 10 {
		return nil
	}
	out := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		charLen, _, _ := procDragQueryFileW.Call(h, i, 0, 0)
		if charLen == 0 || charLen > 4096 {
			continue
		}
		buf := make([]uint16, charLen+1)
		procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), charLen+1)
		p := windows.UTF16ToString(buf)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
