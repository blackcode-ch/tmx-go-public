//go:build windows && !gatesim

package gate

// Windows Hello through the WinRT class Windows.Security.Credentials.UI.UserConsentVerifier,
// called in pure Go (Windows builds are CGO_ENABLED=0): combase.dll's RoGetActivationFactory and
// the COM vtables, through golang.org/x/sys/windows and syscall.SyscallN.
//
// Every IID and vtable layout below was checked against the Windows SDK headers
// (windows.security.credentials.ui.h, UserConsentVerifierInterop.h, asyncinfo.h, roapi.h; as
// mirrored in github.com/microsoft/win32metadata), and the two parameterized IIDs were also
// recomputed from their WinRT signatures (hello_test.go). Unverified on real hardware until
// someone runs it on Windows 10/11 (DESIGN.md "Safety").
//
// The dialog: a process without a window (the daemon) gets the Hello dialog behind other
// windows when it uses the plain IUserConsentVerifierStatics. Windows 11 (build 22000+) offers
// IUserConsentVerifierInterop::RequestVerificationForWindowAsync, which takes an owner window.
// The daemon makes its own owner for each prompt (ownerWindow: a 1x1, fully transparent,
// topmost tool window in the middle of the screen, on the prompting thread, whose messages it
// pumps while it waits), so the dialog opens centred and above other windows. It never hands
// Windows another process's window: making the tmx app's window the owner of a system dialog
// from another process took the app down (v0.1.0). Where the interop is missing or refused
// (Windows 10), it falls back to the statics. While the prompt opens it keeps asking for the
// foreground for its owner and then for the dialog ("Credential Dialog Xaml Host", the
// workaround Bitwarden and Chromium use); the desktop app allows that when the confirmation
// starts (desktop.App.AllowForeground). Each step is written to stderr (logs/daemon.stderr.log),
// so a prompt that fails leaves a trace even if the process does not survive it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const ucvClass = "Windows.Security.Credentials.UI.UserConsentVerifier"

var (
	// IUserConsentVerifierStatics (windows.security.credentials.ui.h): IInspectable, then
	// CheckAvailabilityAsync(IAsyncOperation<UserConsentVerifierAvailability>**) at 6 and
	// RequestVerificationAsync(HSTRING, IAsyncOperation<UserConsentVerificationResult>**) at 7.
	iidUserConsentVerifierStatics = windows.GUID{Data1: 0xaf4f3f91, Data2: 0x564c, Data3: 0x4ddc,
		Data4: [8]byte{0xb8, 0xb5, 0x97, 0x34, 0x47, 0x62, 0x7c, 0x65}}
	// IUserConsentVerifierInterop (UserConsentVerifierInterop.h, Windows build 22000+):
	// IInspectable, then RequestVerificationForWindowAsync(HWND, HSTRING, REFIID, void**) at 6.
	iidUserConsentVerifierInterop = windows.GUID{Data1: 0x39e050c3, Data2: 0x4e74, Data3: 0x441a,
		Data4: [8]byte{0x8d, 0xc0, 0xb8, 0x11, 0x04, 0xdf, 0x94, 0x9c}}
	// IAsyncOperation<UserConsentVerificationResult>, fd596ffd-2318-558f-9dbe-d21df43764a5
	// (__declspec(uuid) in windows.security.credentials.ui.h; recomputed in hello_test.go).
	// IAsyncOperation<T>: IInspectable, put_Completed 6, get_Completed 7, GetResults(T*) 8.
	iidAsyncOpVerificationResult = windows.GUID{Data1: 0xfd596ffd, Data2: 0x2318, Data3: 0x558f,
		Data4: [8]byte{0x9d, 0xbe, 0xd2, 0x1d, 0xf4, 0x37, 0x64, 0xa5}}
	// IAsyncInfo (asyncinfo.h), 00000036-0000-0000-C000-000000000046: IInspectable, then
	// get_Id 6, get_Status 7, get_ErrorCode 8, Cancel 9, Close 10.
	iidAsyncInfo = windows.GUID{Data1: 0x00000036, Data2: 0x0000, Data3: 0x0000,
		Data4: [8]byte{0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
)

// vtable slots.
const (
	vtQueryInterface    = 0
	vtRelease           = 2
	vtCheckAvailability = 6 // IUserConsentVerifierStatics
	vtRequestVerify     = 7 // IUserConsentVerifierStatics
	vtRequestForWindow  = 6 // IUserConsentVerifierInterop
	vtGetResults        = 8 // IAsyncOperation<T>
	vtGetStatus         = 7 // IAsyncInfo
	vtGetErrorCode      = 8
	vtCancel            = 9
	vtClose             = 10
)

// AsyncStatus (asyncinfo.h).
const (
	asyncStarted   = 0
	asyncCompleted = 1
	asyncCanceled  = 2
	asyncError     = 3
)

const (
	roInitMultithreaded = 1
	rpcEChangedMode     = 0x80010106
	availableTimeout    = 5 * time.Second
	availableCacheFor   = 10 * time.Second
	withdrawWait        = 5 * time.Second
	pollEvery           = 50 * time.Millisecond
)

var (
	combase                    = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize           = combase.NewProc("RoInitialize")
	procRoUninitialize         = combase.NewProc("RoUninitialize")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")
	user32                     = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW            = user32.NewProc("FindWindowW")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procRegisterClassExW       = user32.NewProc("RegisterClassExW")
	procCreateWindowExW        = user32.NewProc("CreateWindowExW")
	procDefWindowProcW         = user32.NewProc("DefWindowProcW")
	procDestroyWindow          = user32.NewProc("DestroyWindow")
	procShowWindow             = user32.NewProc("ShowWindow")
	procSetLayeredWindowAttr   = user32.NewProc("SetLayeredWindowAttributes")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procPeekMessageW           = user32.NewProc("PeekMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessageW       = user32.NewProc("DispatchMessageW")
)

// trace writes one step of a prompt to stderr (the daemon's logs/daemon.stderr.log).
func trace(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s gate/windows-hello: %s\n", time.Now().Format("2006-01-02T15:04:05.000"), fmt.Sprintf(format, args...))
}

// WindowsHello asks Windows Hello (face, fingerprint or PIN) through UserConsentVerifier.
type WindowsHello struct{}

// OS returns this machine's authenticator.
func OS() Authenticator { return WindowsHello{} }

func (WindowsHello) Method() string { return "windows-hello" }
func (WindowsHello) Label() string  { return "Windows Hello" }

// Available is called while the gate holds its lock (Status, Require), so the answer is
// cached briefly and the check is bounded.
var availCache struct {
	sync.Mutex
	at  time.Time
	err error
}

func (WindowsHello) Available() error {
	availCache.Lock()
	defer availCache.Unlock()
	if !availCache.at.IsZero() && time.Since(availCache.at) < availableCacheFor {
		return availCache.err
	}
	availCache.err = checkAvailability()
	availCache.at = time.Now()
	return availCache.err
}

func checkAvailability() error {
	if err := loadProcs(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), availableTimeout)
	defer cancel()
	var code int32
	err := withWinRT(func() error {
		statics, err := activationFactory(&iidUserConsentVerifierStatics)
		if err != nil {
			return err
		}
		defer release(statics)
		var op unsafe.Pointer
		r, _, _ := syscall.SyscallN(method(statics, vtCheckAvailability), uintptr(statics), uintptr(unsafe.Pointer(&op)))
		if err := hrErr("CheckAvailabilityAsync", r); err != nil {
			return err
		}
		defer release(op)
		code, err = await(ctx, op, nil)
		if err == nil && ctx.Err() != nil {
			err = errors.New("no answer in time")
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("cannot ask Windows Hello (%v)", err)
	}
	return helloAvailability(code)
}

func (WindowsHello) Authenticate(ctx context.Context, reason string) error {
	if err := loadProcs(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var code int32
	trace("prompt: start")
	err := withWinRT(func() error {
		msg, err := newHString(helloPrompt(reason))
		if err != nil {
			return err
		}
		defer procWindowsDeleteString.Call(msg)
		owner := newOwnerWindow()
		defer owner.destroy()
		trace("prompt: owner window %#x", owner.hwnd)
		op, err := requestVerification(msg, owner.hwnd)
		if err != nil {
			return err
		}
		defer release(op)
		trace("prompt: dialog requested, waiting")
		front := newFrontKeeper(owner.hwnd)
		code, err = await(ctx, op, func() {
			pumpMessages()
			front()
		})
		return err
	})
	trace("prompt: done: result %d, err %v, ctx %v", code, err, ctx.Err())
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Windows Hello failed: %v", err)
	}
	return helloVerdict(code, ctx.Err())
}

// requestVerification starts the dialog: with owner (this process's own window) as its owner
// where Windows offers that (11), else without one.
func requestVerification(msg, owner uintptr) (unsafe.Pointer, error) {
	if owner != 0 {
		if interop, err := activationFactory(&iidUserConsentVerifierInterop); err == nil {
			var op unsafe.Pointer
			r, _, _ := syscall.SyscallN(method(interop, vtRequestForWindow), uintptr(interop), owner, msg,
				uintptr(unsafe.Pointer(&iidAsyncOpVerificationResult)), uintptr(unsafe.Pointer(&op)))
			release(interop)
			if hrErr("", r) == nil && op != nil {
				return op, nil
			}
			trace("prompt: RequestVerificationForWindowAsync refused (hr 0x%08X); trying without an owner", uint32(r))
		} else {
			trace("prompt: no interop (%v); trying without an owner", err)
		}
	}
	statics, err := activationFactory(&iidUserConsentVerifierStatics)
	if err != nil {
		return nil, err
	}
	defer release(statics)
	var op unsafe.Pointer
	r, _, _ := syscall.SyscallN(method(statics, vtRequestVerify), uintptr(statics), msg, uintptr(unsafe.Pointer(&op)))
	if err := hrErr("RequestVerificationAsync", r); err != nil {
		return nil, err
	}
	return op, nil
}

// await polls an IAsyncOperation<enum> (no completion handler: a Go callback object is not
// worth it for one prompt) and returns its result. When ctx ends it cancels the operation,
// which withdraws the dialog, and reports Canceled once the operation agrees or after
// withdrawWait. tick, if set, runs between polls.
func await(ctx context.Context, op unsafe.Pointer, tick func()) (int32, error) {
	var info unsafe.Pointer
	r, _, _ := syscall.SyscallN(method(op, vtQueryInterface), uintptr(op),
		uintptr(unsafe.Pointer(&iidAsyncInfo)), uintptr(unsafe.Pointer(&info)))
	if err := hrErr("IAsyncInfo", r); err != nil {
		return 0, err
	}
	defer release(info)
	defer syscall.SyscallN(method(info, vtClose), uintptr(info))
	var giveUp time.Time
	for {
		var status int32
		r, _, _ := syscall.SyscallN(method(info, vtGetStatus), uintptr(info), uintptr(unsafe.Pointer(&status)))
		if err := hrErr("AsyncInfo.Status", r); err != nil {
			return 0, err
		}
		switch status {
		case asyncCompleted:
			res := int32(-1) // stays unknown (an error) unless GetResults writes it
			r, _, _ := syscall.SyscallN(method(op, vtGetResults), uintptr(op), uintptr(unsafe.Pointer(&res)))
			if err := hrErr("GetResults", r); err != nil {
				return 0, err
			}
			return res, nil
		case asyncCanceled:
			return helloCanceled, nil
		case asyncError:
			var code int32
			syscall.SyscallN(method(info, vtGetErrorCode), uintptr(info), uintptr(unsafe.Pointer(&code)))
			if err := hrErr("the operation", uintptr(uint32(code))); err != nil {
				return 0, err
			}
			return 0, errors.New("the operation failed without an error code")
		}
		if giveUp.IsZero() && ctx.Err() != nil {
			syscall.SyscallN(method(info, vtCancel), uintptr(info))
			giveUp = time.Now().Add(withdrawWait)
		}
		if !giveUp.IsZero() && time.Now().After(giveUp) {
			return helloCanceled, nil
		}
		if tick != nil {
			tick()
		}
		if giveUp.IsZero() {
			select {
			case <-time.After(pollEvery):
			case <-ctx.Done():
			}
		} else {
			time.Sleep(pollEvery)
		}
	}
}

// newFrontKeeper keeps asking, for the first 15 s, for the foreground: for the Hello dialog
// once it exists, else for owner. Windows refuses unless the foreground app allowed it (the
// desktop app does, desktop.App.AllowForeground); refusals are harmless and it stops once the
// dialog is in front.
func newFrontKeeper(owner uintptr) func() {
	start, done := time.Now(), false
	cls, _ := windows.UTF16PtrFromString("Credential Dialog Xaml Host")
	return func() {
		if done || cls == nil || time.Since(start) > 15*time.Second {
			return
		}
		if h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0); h != 0 {
			if uintptr(windows.GetForegroundWindow()) == h {
				done = true
				return
			}
			procSetForegroundWindow.Call(h)
			return
		}
		if owner != 0 && uintptr(windows.GetForegroundWindow()) != owner {
			procSetForegroundWindow.Call(owner)
		}
	}
}

// ownerWindow is the daemon's own owner for the dialog: 1x1, fully transparent (layered,
// alpha 0), topmost, no taskbar button (tool window), centred on the primary screen, so the
// dialog opens in the middle and above other windows. It lives on the prompting thread.
type ownerWindow struct{ hwnd uintptr }

type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background windows.Handle
	MenuName, ClassName                *uint16
	IconSm                             windows.Handle
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

const (
	wsPopup          = 0x80000000
	wsExToolWindow   = 0x00000080
	wsExTopmost      = 0x00000008
	wsExLayered      = 0x00080000
	lwaAlpha         = 0x00000002
	swShow           = 5
	smCxScreen       = 0
	smCyScreen       = 1
	pmRemove         = 0x0001
	ownerWindowClass = "tmxHelloOwner"
)

var ownerClass = sync.OnceValues(func() (*uint16, error) {
	for _, p := range []*windows.LazyProc{procRegisterClassExW, procCreateWindowExW, procDefWindowProcW, procDestroyWindow,
		procShowWindow, procSetLayeredWindowAttr, procGetSystemMetrics, procPeekMessageW, procTranslateMessage, procDispatchMessageW} {
		if err := p.Find(); err != nil {
			return nil, err
		}
	}
	cls, err := windows.UTF16PtrFromString(ownerWindowClass)
	if err != nil {
		return nil, err
	}
	wc := wndClassEx{WndProc: windows.NewCallback(func(h, m, w, l uintptr) uintptr {
		r, _, _ := procDefWindowProcW.Call(h, m, w, l)
		return r
	}), ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return nil, fmt.Errorf("RegisterClassExW: %v", err)
	}
	return cls, nil
})

// newOwnerWindow makes the owner on this (locked) thread; hwnd 0 when it cannot (the prompt
// then goes without an owner).
func newOwnerWindow() ownerWindow {
	cls, err := ownerClass()
	if err != nil {
		trace("owner window: %v", err)
		return ownerWindow{}
	}
	cx, _, _ := procGetSystemMetrics.Call(smCxScreen)
	cy, _, _ := procGetSystemMetrics.Call(smCyScreen)
	title, _ := windows.UTF16PtrFromString("tmx")
	h, _, err := procCreateWindowExW.Call(wsExToolWindow|wsExTopmost|wsExLayered, uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(title)), wsPopup, cx/2, cy/2, 1, 1, 0, 0, 0, 0)
	if h == 0 {
		trace("owner window: CreateWindowExW: %v", err)
		return ownerWindow{}
	}
	procSetLayeredWindowAttr.Call(h, 0, 0, lwaAlpha)
	procShowWindow.Call(h, swShow)
	pumpMessages()
	return ownerWindow{hwnd: h}
}

func (o ownerWindow) destroy() {
	if o.hwnd != 0 {
		procDestroyWindow.Call(o.hwnd)
		pumpMessages()
	}
}

// pumpMessages handles this thread's window messages (the owner window's).
func pumpMessages() {
	if ownerClassReady() != nil {
		return
	}
	var m winMsg
	for i := 0; i < 64; i++ {
		if r, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmRemove); r == 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func ownerClassReady() error {
	_, err := ownerClass()
	return err
}

// withWinRT runs f on one OS thread in the multithreaded apartment.
func withWinRT(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, _, _ := procRoInitialize.Call(roInitMultithreaded)
	switch hr := uint32(r); {
	case hr == 0 || hr == 1: // S_OK, S_FALSE (already initialized here): balance it
		defer procRoUninitialize.Call()
	case hr == rpcEChangedMode: // this thread is single-threaded already; the class supports both
	default:
		return hrErr("RoInitialize", r)
	}
	return f()
}

func activationFactory(iid *windows.GUID) (unsafe.Pointer, error) {
	cls, err := newHString(ucvClass)
	if err != nil {
		return nil, err
	}
	defer procWindowsDeleteString.Call(cls)
	var f unsafe.Pointer
	r, _, _ := procRoGetActivationFactory.Call(cls, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&f)))
	if err := hrErr("RoGetActivationFactory", r); err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("RoGetActivationFactory returned nothing")
	}
	return f, nil
}

func newHString(s string) (uintptr, error) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h uintptr
	r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h)))
	runtime.KeepAlive(u)
	if err := hrErr("WindowsCreateString", r); err != nil {
		return 0, err
	}
	return h, nil
}

var procsErr = sync.OnceValue(func() error {
	for _, p := range []*windows.LazyProc{procRoInitialize, procRoUninitialize, procRoGetActivationFactory,
		procWindowsCreateString, procWindowsDeleteString} {
		if err := p.Find(); err != nil {
			return fmt.Errorf("this Windows has no WinRT (%v)", err)
		}
	}
	return nil
})

func loadProcs() error { return procsErr() }

// method returns slot i of a COM object's vtable.
func method(obj unsafe.Pointer, i int) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	return *(*uintptr)(unsafe.Add(vtbl, uintptr(i)*unsafe.Sizeof(uintptr(0))))
}

func release(obj unsafe.Pointer) {
	if obj != nil {
		syscall.SyscallN(method(obj, vtRelease), uintptr(obj))
	}
}

// hrErr turns a failed HRESULT (negative as int32) into an error.
func hrErr(what string, r uintptr) error {
	hr := uint32(r)
	if int32(hr) >= 0 {
		return nil
	}
	if what == "" {
		what = "call"
	}
	return fmt.Errorf("%s: %v (0x%08X)", what, windows.Errno(hr), hr)
}
