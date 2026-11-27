//go:build windows

package server

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
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
	procAddClipboardFormatListener  = user32Clip.NewProc("AddClipboardFormatListener")
	procRemoveClipboardFormatListen = user32Clip.NewProc("RemoveClipboardFormatListener")
	procRegisterClipboardFormatW    = user32Clip.NewProc("RegisterClipboardFormatW")
	procOpenClipboard               = user32Clip.NewProc("OpenClipboard")
	procCloseClipboard              = user32Clip.NewProc("CloseClipboard")
	procGetClipboardData            = user32Clip.NewProc("GetClipboardData")
	procIsClipboardFormatAvailable  = user32Clip.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber  = user32Clip.NewProc("GetClipboardSequenceNumber")

	procGetModuleHandleW = kernel32Clip.NewProc("GetModuleHandleW")
	procGlobalLock       = kernel32Clip.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32Clip.NewProc("GlobalUnlock")
	procGlobalSize       = kernel32Clip.NewProc("GlobalSize")
	procRtlMoveMemory    = kernel32Clip.NewProc("RtlMoveMemory")

	procDragQueryFileW = shell32Clip.NewProc("DragQueryFileW")
)

const (
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmClipboardUpdate = 0x031D

	cfDIB   = 8
	cfHDrop = 15
	cfDIBV5 = 17

	hwndMessage = ^uintptr(2) // (HWND)-3
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

var (
	clipWndMu   sync.Mutex
	clipWndSigs = map[uintptr]chan struct{}{}
	clipWndProc = syscall.NewCallback(clipboardWndProc)
	clipRegOnce sync.Once
	clipRegErr  error
	clipClass   = windows.StringToUTF16Ptr("fdsyncClipboardListener")
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
	case wmClose:
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

// startClipboardWatch listens for Windows clipboard image updates until ctx
// ends. It uses AddClipboardFormatListener (event-driven, zero polling) and
// falls back to lightweight sequence-number polling if the message window
// cannot be registered.
func startClipboardWatch(ctx context.Context, store *clipboardStore, log *slog.Logger) {
	var fmts winClipFormats
	if r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("PNG")))); r != 0 {
		fmts.png = uint32(r)
	}
	if r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("image/png")))); r != 0 {
		fmts.imagePNG = uint32(r)
	}
	sig := make(chan struct{}, 1)

	// Worker debounces rapid WM_CLIPBOARDUPDATE bursts and decodes images off
	// the Win32 message thread.
	go func() {
		var lastSeq uint32
		if r, _, _ := procGetClipboardSequenceNumber.Call(); r != 0 {
			lastSeq = uint32(r)
		}
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
				if seq != 0 && seq == lastSeq {
					continue
				}
				if seq != 0 {
					lastSeq = seq
				}
				captureWindowsClipboard(store, fmts, log)
			}
		}
	}()

	// Dedicated OS thread for the message-only window.
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
			hwndMessage,
			0,
			hInst,
			0,
		)
		if hwnd == 0 {
			log.Warn("clipboard listener fallback to polling (create window failed)", "err", err)
			pollClipboardFallback(ctx, sig)
			return
		}
		clipWndMu.Lock()
		clipWndSigs[hwnd] = sig
		clipWndMu.Unlock()
		defer func() {
			clipWndMu.Lock()
			delete(clipWndSigs, hwnd)
			clipWndMu.Unlock()
		}()

		ok, _, err := procAddClipboardFormatListener.Call(hwnd)
		if ok == 0 {
			log.Warn("clipboard listener fallback to polling (AddClipboardFormatListener failed)", "err", err)
			procDestroyWindow.Call(hwnd)
			pollClipboardFallback(ctx, sig)
			return
		}
		defer procRemoveClipboardFormatListen.Call(hwnd)
		log.Info("clipboard watcher started")

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

	// OpenClipboard may fail briefly while the source app finishes writing.
	opened := false
	for range 8 {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !opened {
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
	// Only copy DIB fallbacks if PNG wasn't present (avoids extra memory copy
	// in the common Snipping Tool / browser case, while still falling back if
	// only DIB exists).
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
