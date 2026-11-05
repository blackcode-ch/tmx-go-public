//go:build windows

package sysinfo

import "testing"

func TestResumeAcceptsLegacyFILETIMETokens(t *testing.T) {
	current := int64(134379000000000000)
	legacy := int64(uint64(current) * 100)
	if !matchesCreation(current, current, false) || !matchesCreation(current, legacy, true) {
		t.Fatal("valid current or old ledger token rejected")
	}
	if matchesCreation(current, legacy, false) || matchesCreation(current, legacy+1, true) || matchesCreation(current, 0, true) {
		t.Fatal("mismatched token accepted")
	}
}
