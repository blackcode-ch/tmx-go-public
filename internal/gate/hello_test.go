package gate

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestHelloAvailability(t *testing.T) {
	if err := helloAvailability(helloAvailable); err != nil {
		t.Fatalf("Available: %v", err)
	}
	for code, want := range map[int32]string{
		helloDeviceNotPresent:  "Sign-in options",
		helloNotConfiguredUser: "not set up for this user: Settings → Accounts → Sign-in options",
		helloDisabledByPolicy:  "policy",
		helloDeviceBusy:        "busy",
		42:                     "unknown",
		-1:                     "unknown",
	} {
		err := helloAvailability(code)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("availability %d: %v, want it to say %q", code, err, want)
		}
	}
}

func TestHelloVerdict(t *testing.T) {
	if err := helloVerdict(helloVerified, nil); err != nil {
		t.Fatalf("Verified: %v", err)
	}
	// Only Verified passes: a verified answer that raced the withdrawal still counts (as on macOS).
	if err := helloVerdict(helloVerified, context.Canceled); err != nil {
		t.Fatalf("Verified after cancel: %v", err)
	}
	cases := []struct {
		code   int32
		ctxErr error
		is     error
	}{
		{helloCanceled, nil, ErrCancelled},
		{helloCanceled, context.DeadlineExceeded, context.DeadlineExceeded},
		{helloCanceled, context.Canceled, context.Canceled},
		{helloRetriesExhausted, nil, ErrDeclined},
		{helloDeviceNotPresent, nil, ErrUnavailable},
		{helloNotConfiguredUser, nil, ErrUnavailable},
		{helloDisabledByPolicy, nil, ErrUnavailable},
		{helloDeviceBusy, nil, ErrUnavailable},
	}
	for _, c := range cases {
		if err := helloVerdict(c.code, c.ctxErr); !errors.Is(err, c.is) {
			t.Errorf("result %d (ctx %v): %v, want %v", c.code, c.ctxErr, err, c.is)
		}
	}
	for _, code := range []int32{-1, 7, 1 << 20} {
		if err := helloVerdict(code, nil); err == nil {
			t.Errorf("unknown result %d passed", code)
		}
	}
}

func TestHelloPrompt(t *testing.T) {
	for in, want := range map[string]string{
		"allow `sudo true` for worker-1": "tmx is trying to allow `sudo true` for worker-1.",
		"turn on the owner gate.":        "tmx is trying to turn on the owner gate.",
		"a\x00b":                         "tmx is trying to ab.",
		"  ":                             "tmx is trying to do something only you may do.",
	} {
		if got := helloPrompt(in); got != want {
			t.Errorf("helloPrompt(%q) = %q, want %q", in, got, want)
		}
	}
}

// pinterfaceIID computes a WinRT parameterized interface IID from its signature: a UUID
// version 5 (SHA-1) in the namespace 11f47ad5-7b73-42c0-abae-878b1e16adee, as described in
// the Windows Runtime type system specification.
func pinterfaceIID(sig string) string {
	ns, _ := hex.DecodeString("11f47ad57b7342c0abae878b1e16adee")
	h := sha1.Sum(append(ns, sig...))
	b := h[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	s := hex.EncodeToString(b)
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// TestWinRTParameterizedIIDs checks the IIDs auth_windows.go hard-codes (it cannot run here):
// first the algorithm against IIDs every WinRT projection knows, then ours.
func TestWinRTParameterizedIIDs(t *testing.T) {
	const asyncOp = "{9fc2b0bb-e446-44e2-aa61-9cab8f636af2}" // IAsyncOperation`1
	for sig, want := range map[string]string{
		"pinterface(" + asyncOp + ";b1)":                                                            "cdb5efb3-5788-509d-9be1-71ccb8a3362a", // IAsyncOperation<bool>
		"pinterface({faa585ea-6214-4217-afda-7f46de5869b3};string)":                                 "e2fcc7c1-3bfc-5a0b-b2b0-72e769d1cb7e", // IIterable<String>
		"pinterface({913337e9-11a1-4345-a3a2-4e7f956e222d};string)":                                 "98b9acc1-4b56-532e-ac73-03d5291cca90", // IVector<String>
		"pinterface({61c17706-2d65-11e0-9ae8-d48564015472};i4)":                                     "548cefbd-bc8a-5fa0-8df2-957440fc8bf4", // IReference<Int32>
		"pinterface(" + asyncOp + ";enum(" + ucvNamespace + ".UserConsentVerificationResult;i4))":   "fd596ffd-2318-558f-9dbe-d21df43764a5",
		"pinterface(" + asyncOp + ";enum(" + ucvNamespace + ".UserConsentVerifierAvailability;i4))": "ddd384f3-d818-5d83-ab4b-32119c28587c",
	} {
		if got := pinterfaceIID(sig); got != want {
			t.Errorf("%s: %s, want %s", sig, got, want)
		}
	}
}

const ucvNamespace = "Windows.Security.Credentials.UI"
