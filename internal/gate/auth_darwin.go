//go:build darwin && cgo && !gatesim

package gate

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework LocalAuthentication -framework Foundation
#import <LocalAuthentication/LocalAuthentication.h>
#include <stdio.h>
#include <stdlib.h>

static void *tmx_la_new(void) {
	LAContext *ctx = [[LAContext alloc] init];
	return (__bridge_retained void *)ctx;
}

static void tmx_la_free(void *p) {
	LAContext *ctx = (__bridge_transfer LAContext *)p;
	ctx = nil;
}

static void tmx_la_cancel(void *p) {
	[(__bridge LAContext *)p invalidate];
}

// tmx_la_can: 0 when the owner can be asked, else LAError code and a message.
static int tmx_la_can(void *p, char *msg, int n) {
	NSError *err = nil;
	if ([(__bridge LAContext *)p canEvaluatePolicy:LAPolicyDeviceOwnerAuthentication error:&err]) {
		return 0;
	}
	if (err) {
		snprintf(msg, n, "%s", [[err localizedDescription] UTF8String]);
		return (int)[err code];
	}
	return -1;
}

// tmx_la_eval shows the prompt and blocks until it is answered or invalidated.
static int tmx_la_eval(void *p, const char *reason, char *msg, int n) {
	LAContext *ctx = (__bridge LAContext *)p;
	__block int code = 0;
	__block NSString *desc = nil;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[ctx evaluatePolicy:LAPolicyDeviceOwnerAuthentication
	    localizedReason:[NSString stringWithUTF8String:reason]
	              reply:^(BOOL ok, NSError *error) {
		if (!ok) {
			code = error ? (int)error.code : -1;
			desc = error ? [error localizedDescription] : @"not confirmed";
		}
		dispatch_semaphore_signal(sem);
	}];
	dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
	if (desc) {
		snprintf(msg, n, "%s", [desc UTF8String]);
	}
	return code;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"unsafe"
)

// LocalAuth asks macOS's LocalAuthentication with LAPolicyDeviceOwnerAuthentication: Touch
// ID where there is a sensor, and always the login password as the fallback.
type LocalAuth struct{}

// OS returns this machine's authenticator.
func OS() Authenticator { return LocalAuth{} }

func (LocalAuth) Method() string { return "touchid" }
func (LocalAuth) Label() string  { return "Touch ID or your Mac password" }

func (LocalAuth) Available() error {
	h := C.tmx_la_new()
	defer C.tmx_la_free(h)
	var buf [256]C.char
	if code := C.tmx_la_can(h, &buf[0], C.int(len(buf))); code != 0 {
		return fmt.Errorf("macOS cannot ask for the owner here (%s)", C.GoString(&buf[0]))
	}
	return nil
}

// LAError codes that mean "withdrawn" rather than "wrong".
const (
	laUserCancel   = -2
	laSystemCancel = -4
	laAppCancel    = -9
	laNotInteract  = -1004
)

func (LocalAuth) Authenticate(ctx context.Context, reason string) error {
	h := C.tmx_la_new()
	defer C.tmx_la_free(h)
	cr := C.CString(reason)
	defer C.free(unsafe.Pointer(cr))
	var buf [256]C.char
	done := make(chan C.int, 1)
	go func() { done <- C.tmx_la_eval(h, cr, &buf[0], C.int(len(buf))) }()
	var code C.int
	select {
	case code = <-done:
	case <-ctx.Done():
		C.tmx_la_cancel(h) // withdraws the dialog; the reply comes with LAErrorAppCancel
		code = <-done
	}
	switch code {
	case 0:
		return nil
	case laUserCancel, laSystemCancel, laAppCancel:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrCancelled
	case laNotInteract:
		return fmt.Errorf("%w: %s", ErrUnavailable, C.GoString(&buf[0]))
	}
	return errors.New(C.GoString(&buf[0]))
}
