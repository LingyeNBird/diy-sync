//go:build windows

package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	winreg "golang.org/x/sys/windows/registry"
)

var (
	combaseToast = windows.NewLazySystemDLL("combase.dll")
	shell32Toast = windows.NewLazySystemDLL("shell32.dll")

	procRoInitialize           = combaseToast.NewProc("RoInitialize")
	procRoUninitialize         = combaseToast.NewProc("RoUninitialize")
	procWindowsCreateString    = combaseToast.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combaseToast.NewProc("WindowsDeleteString")
	procRoActivateInstance     = combaseToast.NewProc("RoActivateInstance")
	procRoGetActivationFactory = combaseToast.NewProc("RoGetActivationFactory")
	procCoRegisterClassObject  = combaseToast.NewProc("CoRegisterClassObject")
	procCoRevokeClassObject    = combaseToast.NewProc("CoRevokeClassObject")

	procSetAppUserModelID = shell32Toast.NewProc("SetCurrentProcessExplicitAppUserModelID")
)

const (
	roInitMultithreaded = 1
	clsctxLocalServer   = 0x4
	regclsMultipleUse   = 1

	hresultOK            = 0x00000000
	hresultNoInterface   = 0x80004002
	hresultNoAggregation = 0x80040110
	hresultChangedMode   = 0x80010106
)

var (
	iidIUnknown                        = mustGUID("{00000000-0000-0000-C000-000000000046}")
	iidIClassFactory                   = mustGUID("{00000001-0000-0000-C000-000000000046}")
	iidINotificationActivationCallback = mustGUID("{53E31837-6600-4A81-9395-75CFFE746F94}")
	iidIXmlDocument                    = mustGUID("{F7F3A506-1E87-42D6-BCFB-B8C809FA5494}")
	iidIXmlDocumentIO                  = mustGUID("{6CD0E74E-EE65-4489-9EBF-CA43E87BA637}")
	iidIToastNotificationFactory       = mustGUID("{04124B20-82C6-4229-B109-FD9ED4662B53}")
	iidIToastNotificationManagerStatic = mustGUID("{50AC103F-D235-4598-BBEF-98FE4D1A3AD4}")
	iidIToastNotifier                  = mustGUID("{75927B93-03F3-41EC-91D3-6E5BAC1B38E7}")
	clsidFdsyncToastActivator          = mustGUID(toastActivatorCLSID)
)

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

func hresultFailed(hr uintptr) bool {
	return int32(uint32(hr)) < 0
}

type comVtbl [16]uintptr

type comObj struct {
	vtbl *comVtbl
}

func (o *comObj) release() {
	if o != nil && o.vtbl != nil {
		syscall.SyscallN(o.vtbl[2], uintptr(unsafe.Pointer(o)))
	}
}

func (o *comObj) queryInterface(iid *windows.GUID) (*comObj, error) {
	var out *comObj
	hr, _, _ := syscall.SyscallN(
		o.vtbl[0],
		uintptr(unsafe.Pointer(o)),
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&out)),
	)
	if hresultFailed(hr) || out == nil {
		return nil, fmt.Errorf("QueryInterface: HRESULT 0x%08x", uint32(hr))
	}
	return out, nil
}

// Pure-Go COM IClassFactory and INotificationActivationCallback objects.
type toastClassFactoryVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	CreateInstance uintptr
	LockServer     uintptr
}

type toastClassFactory struct {
	vtbl *toastClassFactoryVtbl
}

type toastActivatorVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Activate       uintptr
}

type toastActivator struct {
	vtbl *toastActivatorVtbl
}

var (
	toastPinner runtime.Pinner

	toastActivateMu sync.RWMutex
	toastActivateFn func(args string)

	cbFactoryQI = syscall.NewCallback(func(this *toastClassFactory, riid *windows.GUID, out **toastClassFactory) uintptr {
		if out == nil {
			return hresultNoInterface
		}
		if riid != nil && (*riid == iidIClassFactory || *riid == iidIUnknown) {
			*out = this
			return hresultOK
		}
		*out = nil
		return hresultNoInterface
	})
	cbFactoryAddRef = syscall.NewCallback(func(this *toastClassFactory) uintptr {
		return 1
	})
	cbFactoryRelease = syscall.NewCallback(func(this *toastClassFactory) uintptr {
		return 1
	})
	cbFactoryCreateInstance = syscall.NewCallback(func(this *toastClassFactory, punkOuter unsafe.Pointer, riid *windows.GUID, out **toastActivator) uintptr {
		if out == nil {
			return hresultNoInterface
		}
		if punkOuter != nil {
			*out = nil
			return hresultNoAggregation
		}
		if riid != nil && (*riid == iidINotificationActivationCallback || *riid == iidIUnknown) {
			*out = globalToastActivator
			return hresultOK
		}
		*out = nil
		return hresultNoInterface
	})
	cbFactoryLockServer = syscall.NewCallback(func(this *toastClassFactory, flock uintptr) uintptr {
		return hresultOK
	})

	cbActivatorQI = syscall.NewCallback(func(this *toastActivator, riid *windows.GUID, out **toastActivator) uintptr {
		if out == nil {
			return hresultNoInterface
		}
		if riid != nil && (*riid == iidINotificationActivationCallback || *riid == iidIUnknown) {
			*out = this
			return hresultOK
		}
		*out = nil
		return hresultNoInterface
	})
	cbActivatorAddRef = syscall.NewCallback(func(this *toastActivator) uintptr {
		return 1
	})
	cbActivatorRelease = syscall.NewCallback(func(this *toastActivator) uintptr {
		return 1
	})
	cbActivatorActivate = syscall.NewCallback(func(this *toastActivator, appUserModelID *uint16, invokedArgs *uint16, data unsafe.Pointer, count uintptr) uintptr {
		var args string
		if invokedArgs != nil {
			args = windows.UTF16PtrToString(invokedArgs)
		}
		toastActivateMu.RLock()
		fn := toastActivateFn
		toastActivateMu.RUnlock()
		if fn != nil {
			go fn(args)
		}
		return hresultOK
	})

	globalToastClassFactory = &toastClassFactory{
		vtbl: &toastClassFactoryVtbl{
			QueryInterface: cbFactoryQI,
			AddRef:         cbFactoryAddRef,
			Release:        cbFactoryRelease,
			CreateInstance: cbFactoryCreateInstance,
			LockServer:     cbFactoryLockServer,
		},
	}
	globalToastActivator = &toastActivator{
		vtbl: &toastActivatorVtbl{
			QueryInterface: cbActivatorQI,
			AddRef:         cbActivatorAddRef,
			Release:        cbActivatorRelease,
			Activate:       cbActivatorActivate,
		},
	}
)

func init() {
	toastPinner.Pin(globalToastClassFactory)
	toastPinner.Pin(globalToastClassFactory.vtbl)
	toastPinner.Pin(globalToastActivator)
	toastPinner.Pin(globalToastActivator.vtbl)
}

func setToastActivateHandler(fn func(args string)) {
	toastActivateMu.Lock()
	toastActivateFn = fn
	toastActivateMu.Unlock()
}

type winToastReq struct {
	xml   string
	errCh chan error
}

// winToastHost owns a dedicated WinRT MTA thread that registers the fdsync
// toast activator once and dispatches native Windows 10/11 toast notifications.
type winToastHost struct {
	mu      sync.Mutex
	started bool
	reqCh   chan winToastReq
	log     *slog.Logger
}

func newWinToastHost() *winToastHost {
	return &winToastHost{
		reqCh: make(chan winToastReq, 8),
	}
}

func (h *winToastHost) start(ctx context.Context, log *slog.Logger, onActivate func(args string)) {
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		return
	}
	h.started = true
	h.log = log
	h.mu.Unlock()

	setToastActivateHandler(onActivate)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := ensureToastRegistry(); err != nil && log != nil {
			log.Warn("register windows toast metadata failed", "err", err)
		}
		if appPtr, err := windows.UTF16PtrFromString(toastAppID); err == nil {
			procSetAppUserModelID.Call(uintptr(unsafe.Pointer(appPtr)))
		}

		uninit, err := roInitMTA()
		if err != nil {
			if log != nil {
				log.Warn("WinRT RoInitialize failed; toast notifications will use balloon fallback", "err", err)
			}
			for {
				select {
				case <-ctx.Done():
					return
				case req := <-h.reqCh:
					req.errCh <- err
				}
			}
		}
		defer uninit()

		var cookie uint32
		hr, _, _ := procCoRegisterClassObject.Call(
			uintptr(unsafe.Pointer(&clsidFdsyncToastActivator)),
			uintptr(unsafe.Pointer(globalToastClassFactory)),
			clsctxLocalServer,
			regclsMultipleUse,
			uintptr(unsafe.Pointer(&cookie)),
		)
		if hresultFailed(hr) {
			if log != nil {
				log.Warn("CoRegisterClassObject for toast activator failed", "hr", fmt.Sprintf("0x%08x", uint32(hr)))
			}
		} else {
			defer procCoRevokeClassObject.Call(uintptr(cookie))
		}

		for {
			select {
			case <-ctx.Done():
				return
			case req := <-h.reqCh:
				req.errCh <- showWinRTToastXML(req.xml)
			}
		}
	}()
}

func (h *winToastHost) show(xmlPayload string) error {
	h.mu.Lock()
	started := h.started
	h.mu.Unlock()
	if !started {
		return fmt.Errorf("winrt toast host not started")
	}
	req := winToastReq{xml: xmlPayload, errCh: make(chan error, 1)}
	select {
	case h.reqCh <- req:
	case <-time.After(2 * time.Second):
		return fmt.Errorf("winrt toast queue timeout")
	}
	select {
	case err := <-req.errCh:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("winrt toast dispatch timeout")
	}
}

// HandleToastActivation handles Windows COM cold-launch activation when the
// user clicks an fdsync notification in Action Center while fdsync-server is
// not running (invoked via LocalServer32 with "-toast-activated").
func HandleToastActivation(ctx context.Context, log *slog.Logger) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	uninit, err := roInitMTA()
	if err != nil {
		return
	}
	defer uninit()

	done := make(chan struct{}, 1)
	setToastActivateHandler(func(args string) {
		if id := parseToastOpenID(args); id != "" {
			openIncomingImageByID(id, log)
		}
		select {
		case done <- struct{}{}:
		default:
		}
	})

	var cookie uint32
	hr, _, _ := procCoRegisterClassObject.Call(
		uintptr(unsafe.Pointer(&clsidFdsyncToastActivator)),
		uintptr(unsafe.Pointer(globalToastClassFactory)),
		clsctxLocalServer,
		regclsMultipleUse,
		uintptr(unsafe.Pointer(&cookie)),
	)
	if hresultFailed(hr) {
		return
	}
	defer procCoRevokeClassObject.Call(uintptr(cookie))

	select {
	case <-done:
		// Give ShellExecuteW a brief moment to spawn the viewer before exiting.
		time.Sleep(400 * time.Millisecond)
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
	}
}

func roInitMTA() (func(), error) {
	if err := combaseToast.Load(); err != nil {
		return nil, err
	}
	hr, _, _ := procRoInitialize.Call(roInitMultithreaded)
	if uint32(hr) == hresultChangedMode {
		return func() {}, nil
	}
	if hresultFailed(hr) {
		return nil, fmt.Errorf("RoInitialize: HRESULT 0x%08x", uint32(hr))
	}
	return func() { procRoUninitialize.Call() }, nil
}

func ensureToastRegistry() error {
	appKeyPath := filepath.Join("SOFTWARE", "Classes", "AppUserModelId", toastAppID)
	appKey, _, err := winreg.CreateKey(winreg.CURRENT_USER, appKeyPath, winreg.SET_VALUE)
	if err != nil {
		return err
	}
	defer appKey.Close()

	if err := appKey.SetStringValue("DisplayName", "fdsync"); err != nil {
		return err
	}
	if err := appKey.SetStringValue("CustomActivator", toastActivatorCLSID); err != nil {
		return err
	}
	if iconPath := ensureToastIconFile(); iconPath != "" {
		_ = appKey.SetStringValue("IconUri", iconPath)
	}

	if exe, err := os.Executable(); err == nil && exe != "" {
		clsidKeyPath := filepath.Join("SOFTWARE", "Classes", "CLSID", toastActivatorCLSID, "LocalServer32")
		if clsidKey, _, err := winreg.CreateKey(winreg.CURRENT_USER, clsidKeyPath, winreg.SET_VALUE); err == nil {
			_ = clsidKey.SetStringValue("", fmt.Sprintf(`"%s" -toast-activated`, exe))
			_ = clsidKey.Close()
		}
	}
	return nil
}

func ensureToastIconFile() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "fdsync")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	iconPath := filepath.Join(dir, "toast-icon.png")
	if fi, err := os.Stat(iconPath); err == nil && fi.Size() > 0 {
		return iconPath
	}
	pngBytes := renderToastAppIconPNG(64)
	if len(pngBytes) == 0 {
		return ""
	}
	if err := os.WriteFile(iconPath, pngBytes, 0o600); err != nil {
		return ""
	}
	return iconPath
}

func newHString(s string) (uintptr, error) {
	var hstr uintptr
	u16 := utf16.Encode([]rune(s))
	var ptr *uint16
	if len(u16) > 0 {
		ptr = &u16[0]
	} else {
		var zero uint16
		ptr = &zero
	}
	hr, _, _ := procWindowsCreateString.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(len(u16)),
		uintptr(unsafe.Pointer(&hstr)),
	)
	if hresultFailed(hr) {
		return 0, fmt.Errorf("WindowsCreateString: HRESULT 0x%08x", uint32(hr))
	}
	return hstr, nil
}

func deleteHString(hstr uintptr) {
	if hstr != 0 {
		procWindowsDeleteString.Call(hstr)
	}
}

func showWinRTToastXML(xmlPayload string) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("winrt toast panic: %v", r)
		}
	}()

	// 1. Activate Windows.Data.Xml.Dom.XmlDocument and load XML.
	hXmlClass, err := newHString("Windows.Data.Xml.Dom.XmlDocument")
	if err != nil {
		return err
	}
	defer deleteHString(hXmlClass)

	var docInspectable *comObj
	hr, _, _ := procRoActivateInstance.Call(hXmlClass, uintptr(unsafe.Pointer(&docInspectable)))
	if hresultFailed(hr) || docInspectable == nil {
		return fmt.Errorf("RoActivateInstance(XmlDocument): HRESULT 0x%08x", uint32(hr))
	}
	defer docInspectable.release()

	docIO, err := docInspectable.queryInterface(&iidIXmlDocumentIO)
	if err != nil {
		return err
	}
	defer docIO.release()

	hXML, err := newHString(xmlPayload)
	if err != nil {
		return err
	}
	defer deleteHString(hXML)

	hr, _, _ = syscall.SyscallN(docIO.vtbl[6], uintptr(unsafe.Pointer(docIO)), hXML)
	if hresultFailed(hr) {
		return fmt.Errorf("IXmlDocumentIO.LoadXml: HRESULT 0x%08x", uint32(hr))
	}

	xmlDoc, err := docInspectable.queryInterface(&iidIXmlDocument)
	if err != nil {
		return err
	}
	defer xmlDoc.release()

	// 2. Create ToastNotification from the XmlDocument.
	hToastClass, err := newHString("Windows.UI.Notifications.ToastNotification")
	if err != nil {
		return err
	}
	defer deleteHString(hToastClass)

	var toastFactory *comObj
	hr, _, _ = procRoGetActivationFactory.Call(
		hToastClass,
		uintptr(unsafe.Pointer(&iidIToastNotificationFactory)),
		uintptr(unsafe.Pointer(&toastFactory)),
	)
	if hresultFailed(hr) || toastFactory == nil {
		return fmt.Errorf("RoGetActivationFactory(ToastNotification): HRESULT 0x%08x", uint32(hr))
	}
	defer toastFactory.release()

	var toastObj *comObj
	hr, _, _ = syscall.SyscallN(
		toastFactory.vtbl[6],
		uintptr(unsafe.Pointer(toastFactory)),
		uintptr(unsafe.Pointer(xmlDoc)),
		uintptr(unsafe.Pointer(&toastObj)),
	)
	if hresultFailed(hr) || toastObj == nil {
		return fmt.Errorf("CreateToastNotification: HRESULT 0x%08x", uint32(hr))
	}
	defer toastObj.release()

	// 3. Create ToastNotifier for our AppID and show the notification.
	hMgrClass, err := newHString("Windows.UI.Notifications.ToastNotificationManager")
	if err != nil {
		return err
	}
	defer deleteHString(hMgrClass)

	var mgrStatics *comObj
	hr, _, _ = procRoGetActivationFactory.Call(
		hMgrClass,
		uintptr(unsafe.Pointer(&iidIToastNotificationManagerStatic)),
		uintptr(unsafe.Pointer(&mgrStatics)),
	)
	if hresultFailed(hr) || mgrStatics == nil {
		return fmt.Errorf("RoGetActivationFactory(ToastNotificationManager): HRESULT 0x%08x", uint32(hr))
	}
	defer mgrStatics.release()

	hAppID, err := newHString(toastAppID)
	if err != nil {
		return err
	}
	defer deleteHString(hAppID)

	var notifierRaw *comObj
	hr, _, _ = syscall.SyscallN(
		mgrStatics.vtbl[7], // IToastNotificationManagerStatics::CreateToastNotifierWithId
		uintptr(unsafe.Pointer(mgrStatics)),
		hAppID,
		uintptr(unsafe.Pointer(&notifierRaw)),
	)
	if hresultFailed(hr) || notifierRaw == nil {
		return fmt.Errorf("CreateToastNotifierWithId: HRESULT 0x%08x", uint32(hr))
	}
	defer notifierRaw.release()

	notifier, err := notifierRaw.queryInterface(&iidIToastNotifier)
	if err != nil {
		return err
	}
	defer notifier.release()

	hr, _, _ = syscall.SyscallN(
		notifier.vtbl[6], // IToastNotifier::Show
		uintptr(unsafe.Pointer(notifier)),
		uintptr(unsafe.Pointer(toastObj)),
	)
	if hresultFailed(hr) {
		return fmt.Errorf("IToastNotifier.Show: HRESULT 0x%08x", uint32(hr))
	}
	return nil
}
