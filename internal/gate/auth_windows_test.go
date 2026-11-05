//go:build windows && !gatesim

package gate

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestWindowsIIDLiterals ties the GUID literals in auth_windows.go to the SDK strings and to
// the IID computed from the WinRT signature (hello_test.go).
func TestWindowsIIDLiterals(t *testing.T) {
	sig := "pinterface({9fc2b0bb-e446-44e2-aa61-9cab8f636af2};enum(" + ucvNamespace + ".UserConsentVerificationResult;i4))"
	for got, want := range map[string]string{
		iidAsyncOpVerificationResult.String():  "{" + pinterfaceIID(sig) + "}",
		iidUserConsentVerifierStatics.String(): "{af4f3f91-564c-4ddc-b8b5-973447627c65}",
		iidUserConsentVerifierInterop.String(): "{39e050c3-4e74-441a-8dc0-b81104df949c}",
		iidAsyncInfo.String():                  "{00000036-0000-0000-c000-000000000046}",
	} {
		if !strings.EqualFold(got, want) {
			t.Errorf("IID %s, want %s", got, want)
		}
	}
}

func TestWindowsHelloMetadata(t *testing.T) {
	a := OS()
	if a.Method() != "windows-hello" || a.Label() != "Windows Hello" {
		t.Fatal(a.Method(), a.Label())
	}
	t.Logf("available: %v", a.Available()) // must not panic or prompt
}

// TestWindowsHelloWithdrawn shows the real Windows Hello dialog and withdraws it after two
// seconds. It puts a dialog on the screen, so it runs only with TMXG_LIVE_GATE=1.
func TestWindowsHelloWithdrawn(t *testing.T) {
	if os.Getenv("TMXG_LIVE_GATE") != "1" {
		t.Skip("shows a system dialog; set TMXG_LIVE_GATE=1")
	}
	a := OS()
	if err := a.Available(); err != nil {
		t.Skipf("not available here: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := a.Authenticate(ctx, "check that tmx can ask you (test; it closes by itself)")
	if err == nil {
		t.Fatal("nobody answered, yet the prompt succeeded")
	}
	if d := time.Since(start); d > 9*time.Second {
		t.Fatalf("withdrawing took %s", d)
	}
	t.Logf("withdrawn after %s: %v", time.Since(start).Round(time.Millisecond), err)
}

// TestWindowsHelloAnswer shows the dialog for real and waits for the owner (TMXG_LIVE_GATE=answer).
func TestWindowsHelloAnswer(t *testing.T) {
	if os.Getenv("TMXG_LIVE_GATE") != "answer" {
		t.Skip("waits for you to answer a system dialog; set TMXG_LIVE_GATE=answer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), PromptTimeout)
	defer cancel()
	err := OS().Authenticate(ctx, "check that tmx can ask you (test: confirm, or cancel to see the error)")
	t.Logf("answer: %v", err)
}
