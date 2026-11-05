//go:build windows && (amd64 || arm64)

package devaudio

// WASAPI loopback: the default output device's mix, captured in shared mode with
// AUDCLNT_STREAMFLAGS_LOOPBACK, through the COM interfaces' vtables called with
// syscall.SyscallN (no cgo, no COM library). Everything happens on one locked OS thread: COM
// objects belong to the thread (apartment) that made them.
//
// Loopback gives everything the PC plays, from every app, at the device's own mix format
// (usually 48 kHz stereo 32-bit float). When nothing plays, the device gives no packets at all;
// the page fills that time with silence. Every second the capture checks which device is the
// default output and follows it when the owner switches (headphones plugged in, another output
// picked), and it reopens the device after it was invalidated.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Supported reports whether this build has a native device-audio source.
const Supported = true

// System is WASAPI loopback of the default output device.
func System() (Source, error) { return loopback{}, nil }

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

// mmdeviceapi.h and audioclient.h.
var (
	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = windows.GUID{Data1: 0x1CB9AD4C, Data2: 0xDBFA, Data3: 0x4C32, Data4: [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient  = windows.GUID{Data1: 0xC8ADBD64, Data2: 0xE71E, Data3: 0x48A0, Data4: [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
)

const (
	clsctxAll           = 0x17 // CLSCTX_INPROC_SERVER | INPROC_HANDLER | LOCAL_SERVER | REMOTE_SERVER
	coinitMultithreaded = 0x0
	eRender             = 0
	eConsole            = 0
	sharemodeShared     = 0
	streamflagsLoopback = 0x00020000
	bufferflagsSilent   = 0x2
	bufferDuration      = 200 * 10000 // REFERENCE_TIME, 100 ns units: 200 ms

	hrSOK               = 0x00000000
	hrSFalse            = 0x00000001 // COM was already initialised on this thread
	hrRPCChangedMode    = 0x80010106 // … in the other threading model
	hrBufferEmpty       = 0x08890001 // AUDCLNT_S_BUFFER_EMPTY
	hrDeviceInvalidated = 0x88890004 // AUDCLNT_E_DEVICE_INVALIDATED
	hrNotFound          = 0x80070490 // E_NOTFOUND: no such device
)

// Vtable slots (0–2 are IUnknown's QueryInterface, AddRef, Release).
const (
	slotRelease                 = 2
	slotGetDefaultAudioEndpoint = 4  // IMMDeviceEnumerator
	slotActivate                = 3  // IMMDevice
	slotGetID                   = 5  // IMMDevice
	slotInitialize              = 3  // IAudioClient
	slotGetMixFormat            = 8  // IAudioClient
	slotStart                   = 10 // IAudioClient
	slotStop                    = 11 // IAudioClient
	slotGetService              = 14 // IAudioClient
	slotGetBuffer               = 3  // IAudioCaptureClient
	slotReleaseBuffer           = 4  // IAudioCaptureClient
	slotGetNextPacketSize       = 5  // IAudioCaptureClient
)

// method is slot i of a COM object's vtable. Every call passes it straight to
// syscall.SyscallN together with its pointer arguments, so they stay valid during the call.
func method(obj unsafe.Pointer, i int) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	return *(*uintptr)(unsafe.Add(vtbl, i*int(unsafe.Sizeof(uintptr(0)))))
}

func release(obj unsafe.Pointer) {
	if obj != nil {
		syscall.SyscallN(method(obj, slotRelease), uintptr(obj))
	}
}

func failed(hr uintptr) bool { return int32(uint32(hr)) < 0 }

var errDeviceGone = errors.New("the sound output device went away")

func hrErr(what string, hr uintptr) error {
	switch uint32(hr) {
	case hrNotFound:
		return errors.New("this PC has no sound output device")
	case hrDeviceInvalidated:
		return errDeviceGone
	}
	return fmt.Errorf("%s failed (0x%08X)", what, uint32(hr))
}

type loopback struct{}

func (loopback) Capture(ctx context.Context, opened func(Format), put func([]byte, int)) error {
	// The thread stays locked: it ends with this goroutine, and its COM state with it.
	runtime.LockOSThread()
	hr, _, _ := syscall.SyscallN(procCoInitializeEx.Addr(), 0, coinitMultithreaded)
	switch uint32(hr) {
	case hrSOK, hrSFalse:
		defer windows.CoUninitialize()
	case hrRPCChangedMode: // COM is there already (single-threaded): the calls work the same
	default:
		return hrErr("starting COM", hr)
	}
	var enum unsafe.Pointer
	hr, _, _ = syscall.SyscallN(procCoCreateInstance.Addr(),
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum)))
	if failed(hr) {
		return hrErr("listing the sound devices", hr)
	}
	defer release(enum)

	s, err := openLoopback(enum)
	if err != nil {
		return err
	}
	defer func() {
		if s != nil {
			s.close()
		}
	}()
	opened(s.format)

	poll := time.NewTicker(10 * time.Millisecond) // the device buffers 200 ms
	defer poll.Stop()
	checked := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
		}
		err := s.drain(put)
		if err == nil && s.id != "" && time.Since(checked) >= time.Second {
			checked = time.Now()
			if id, _ := defaultOutput(enum); id != "" && id != s.id {
				err = errDeviceGone // another output is the default now: follow it
			}
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, errDeviceGone) {
			return err
		}
		s.close()
		if s, err = reopen(ctx, enum); s == nil {
			return err // nil when ctx ended while waiting for a device
		}
		opened(s.format)
	}
}

// reopen waits up to 10 s for a default output device again (nil, nil when ctx ends first).
func reopen(ctx context.Context, enum unsafe.Pointer) (*stream, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, err := openLoopback(enum)
		if err == nil {
			return s, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// stream is one opened loopback capture.
type stream struct {
	dev, client, capture unsafe.Pointer
	id                   string
	format               Format
	frameBytes           int
}

func (s *stream) close() {
	if s.client != nil {
		syscall.SyscallN(method(s.client, slotStop), uintptr(s.client))
	}
	release(s.capture)
	release(s.client)
	release(s.dev)
	*s = stream{}
}

func openLoopback(enum unsafe.Pointer) (s *stream, err error) {
	s = &stream{}
	defer func() {
		if err != nil {
			s.close()
			s = nil
		}
	}()
	hr, _, _ := syscall.SyscallN(method(enum, slotGetDefaultAudioEndpoint), uintptr(enum), eRender, eConsole, uintptr(unsafe.Pointer(&s.dev)))
	if failed(hr) {
		return s, hrErr("finding the sound output", hr)
	}
	s.id = deviceID(s.dev)
	hr, _, _ = syscall.SyscallN(method(s.dev, slotActivate), uintptr(s.dev), uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&s.client)))
	if failed(hr) {
		return s, hrErr("opening the sound output", hr)
	}
	var wfx unsafe.Pointer
	hr, _, _ = syscall.SyscallN(method(s.client, slotGetMixFormat), uintptr(s.client), uintptr(unsafe.Pointer(&wfx)))
	if failed(hr) {
		return s, hrErr("reading the sound output's format", hr)
	}
	defer windows.CoTaskMemFree(wfx)
	head := unsafe.Slice((*byte)(wfx), waveFormatExSize)
	size := waveFormatExSize + (int(head[16]) | int(head[17])<<8) // + cbSize
	if s.format, err = parseWaveFormat(unsafe.Slice((*byte)(wfx), size)); err != nil {
		return s, err
	}
	s.frameBytes = s.format.frameBytes()
	// Shared mode takes the mix format as it is; the conversion to Int16 happens here.
	hr, _, _ = syscall.SyscallN(method(s.client, slotInitialize), uintptr(s.client), sharemodeShared, streamflagsLoopback, bufferDuration, 0, uintptr(wfx), 0)
	if failed(hr) {
		return s, hrErr("starting the loopback capture", hr)
	}
	hr, _, _ = syscall.SyscallN(method(s.client, slotGetService), uintptr(s.client), uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&s.capture)))
	if failed(hr) {
		return s, hrErr("starting the loopback capture", hr)
	}
	hr, _, _ = syscall.SyscallN(method(s.client, slotStart), uintptr(s.client))
	if failed(hr) {
		return s, hrErr("starting the loopback capture", hr)
	}
	return s, nil
}

// drain hands every packet the device has to put (copied out before it is released).
func (s *stream) drain(put func([]byte, int)) error {
	for {
		var n uint32
		hr, _, _ := syscall.SyscallN(method(s.capture, slotGetNextPacketSize), uintptr(s.capture), uintptr(unsafe.Pointer(&n)))
		if failed(hr) {
			return hrErr("reading the device sound", hr)
		}
		if n == 0 {
			return nil
		}
		var data unsafe.Pointer
		var frames, flags uint32
		hr, _, _ = syscall.SyscallN(method(s.capture, slotGetBuffer), uintptr(s.capture),
			uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), 0, 0)
		if uint32(hr) == hrBufferEmpty {
			return nil
		}
		if failed(hr) {
			return hrErr("reading the device sound", hr)
		}
		if frames > 0 {
			if flags&bufferflagsSilent != 0 || data == nil {
				put(nil, int(frames))
			} else {
				put(unsafe.Slice((*byte)(data), int(frames)*s.frameBytes), int(frames))
			}
		}
		hr, _, _ = syscall.SyscallN(method(s.capture, slotReleaseBuffer), uintptr(s.capture), uintptr(frames))
		if failed(hr) {
			return hrErr("reading the device sound", hr)
		}
	}
}

// defaultOutput is the id of the default output device now.
func defaultOutput(enum unsafe.Pointer) (string, error) {
	var dev unsafe.Pointer
	hr, _, _ := syscall.SyscallN(method(enum, slotGetDefaultAudioEndpoint), uintptr(enum), eRender, eConsole, uintptr(unsafe.Pointer(&dev)))
	if failed(hr) {
		return "", hrErr("finding the sound output", hr)
	}
	defer release(dev)
	return deviceID(dev), nil
}

func deviceID(dev unsafe.Pointer) string {
	var p *uint16
	hr, _, _ := syscall.SyscallN(method(dev, slotGetID), uintptr(dev), uintptr(unsafe.Pointer(&p)))
	if failed(hr) || p == nil {
		return ""
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(p))
	return windows.UTF16PtrToString(p)
}
