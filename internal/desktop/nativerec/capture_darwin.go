//go:build desktop && cgo

package nativerec

// The Go side of capture_darwin.m: ScreenCaptureKit, in the macOS app's window process.

/*
#cgo CFLAGS: -fobjc-arc -Wall
#cgo LDFLAGS: -framework Foundation -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework CoreGraphics -framework ImageIO -framework ScreenCaptureKit
#include <stdlib.h>

int  tmxrecSupported(void);
void tmxrecPick(void);
void tmxrecPickCancel(void);
int  tmxrecOpen(int fps, int maxSide, int systemAudio, int mic, int *micOn, char **err);
int  tmxrecStartSegment(const char *path, char **err);
void tmxrecProgress(double *sec, long long *bytes);
int  tmxrecEndSegment(double *sec, char **err);
int  tmxrecFrame(void **data, int *len);
void tmxrecClose(void);
*/
import "C"

import (
	"context"
	"errors"
	"sync"
	"time"
	"unsafe"
)

// NewCapturer is ScreenCaptureKit's capture (macOS 15 or later; Supported says).
func NewCapturer() Capturer { return &sck{} }

type sck struct{}

// What the capture's threads call back with; there is one capture per window.
var cb struct {
	mu      sync.Mutex
	picks   chan pickAnswer
	pcm     func([]int16)
	stopped func(error)
}

type pickAnswer struct {
	status, kind, w, h int
	msg                string
}

const scUserStopped = -3817 // SCStreamErrorUserStopped: Stop in the menu bar or Control Center

func (sck) Supported() (bool, string) {
	if C.tmxrecSupported() == 0 {
		return false, "native screen recording needs macOS 15 or later; this window records without device audio"
	}
	return true, ""
}

func (sck) Pick(ctx context.Context) (Picked, error) {
	ch := make(chan pickAnswer, 1)
	cb.mu.Lock()
	cb.picks = ch
	cb.mu.Unlock()
	C.tmxrecPick()
	select {
	case a := <-ch:
		switch a.status {
		case 0:
			kind := map[int]string{1: "window", 2: "display", 3: "application"}[a.kind]
			if kind == "" {
				kind = "display"
			}
			return Picked{Kind: kind, Width: a.w, Height: a.h}, nil
		case 1:
			return Picked{}, ErrCancelled
		}
		return Picked{}, errors.New(a.msg)
	case <-ctx.Done():
		C.tmxrecPickCancel()
		return Picked{}, ctx.Err()
	}
}

func (sck) Open(cfg StreamConfig, pcm func([]int16), stopped func(error)) (Opened, error) {
	cb.mu.Lock()
	cb.pcm, cb.stopped = pcm, stopped
	cb.mu.Unlock()
	var micOn C.int
	var cerr *C.char
	rc := C.tmxrecOpen(C.int(cfg.FPS), C.int(cfg.MaxSide), cbool(cfg.SystemAudio), cbool(cfg.Mic), &micOn, &cerr)
	if err := takeErr(cerr); rc != 0 {
		if err == nil {
			err = errors.New("the capture did not start")
		}
		return Opened{}, err
	}
	o := Opened{Mic: micOn == 1}
	if micOn == -1 {
		o.Warning = "The microphone isn't allowed for tmx, so it is not recorded. Allow it in System Settings → Privacy & Security → Microphone."
	}
	return o, nil
}

func (sck) StartSegment(path string) error {
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	var cerr *C.char
	rc := C.tmxrecStartSegment(p, &cerr)
	if err := takeErr(cerr); rc != 0 {
		if err == nil {
			err = errors.New("the recording did not start")
		}
		return err
	}
	return nil
}

func (sck) Progress() (time.Duration, int64) {
	var sec C.double
	var bytes C.longlong
	C.tmxrecProgress(&sec, &bytes)
	return time.Duration(float64(sec) * float64(time.Second)), int64(bytes)
}

func (sck) EndSegment() (time.Duration, error) {
	var sec C.double
	var cerr *C.char
	C.tmxrecEndSegment(&sec, &cerr)
	return time.Duration(float64(sec) * float64(time.Second)), takeErr(cerr)
}

func (sck) Frame() ([]byte, error) {
	var data unsafe.Pointer
	var n C.int
	if C.tmxrecFrame(&data, &n) != 0 || data == nil {
		return nil, ErrNoFrame
	}
	defer C.free(data)
	return C.GoBytes(data, n), nil
}

func (sck) Close() {
	C.tmxrecClose()
	cb.mu.Lock()
	cb.pcm, cb.stopped = nil, nil
	cb.mu.Unlock()
}

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// takeErr turns a malloc'd C message into an error (nil for none) and frees it.
func takeErr(s *C.char) error {
	if s == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(s))
	return errors.New(C.GoString(s))
}

//export tmxrecGoPicked
func tmxrecGoPicked(status, kind, w, h C.int, msg *C.char) {
	a := pickAnswer{status: int(status), kind: int(kind), w: int(w), h: int(h)}
	if msg != nil {
		a.msg = C.GoString(msg)
	}
	cb.mu.Lock()
	ch := cb.picks
	cb.picks = nil
	cb.mu.Unlock()
	if ch != nil {
		ch <- a
	}
}

//export tmxrecGoPCM
func tmxrecGoPCM(p *C.short, n C.int) {
	cb.mu.Lock()
	fn := cb.pcm
	cb.mu.Unlock()
	if fn == nil || n <= 0 {
		return
	}
	fn(append([]int16(nil), unsafe.Slice((*int16)(unsafe.Pointer(p)), int(n))...))
}

//export tmxrecGoStopped
func tmxrecGoStopped(code C.long, msg *C.char) {
	cb.mu.Lock()
	fn := cb.stopped
	cb.stopped = nil // once
	cb.mu.Unlock()
	if fn == nil {
		return
	}
	if code == scUserStopped {
		fn(nil) // the owner ended it from macOS: an ordinary stop
		return
	}
	text := "the capture stopped"
	if msg != nil {
		text = C.GoString(msg)
	}
	fn(errors.New(text))
}
