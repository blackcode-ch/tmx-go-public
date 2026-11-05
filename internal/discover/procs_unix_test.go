//go:build !windows

package discover

import "testing"

func TestUnixParsers(t *testing.T) {
	ps := parsePS([]byte("  871     1 Wed Oct 21 16:05:18 2026     /x/tmx daemon\n 12 871 Tue Oct  7 09:01:02 2026 claude --resume abc\nbad line\n"))
	if len(ps) != 2 || ps[0].PID != 871 || ps[1].PPID != 871 || ps[1].Args[2] != "abc" || ps[1].Started.Day() != 7 || ps[1].Started.Hour() != 9 {
		t.Fatalf("%+v", ps)
	}
	m := parseLsof([]byte("p12\nfcwd\nn/Users/x/code\np13\nfcwd\nn/tmp/y\n"))
	if m[12] != "/Users/x/code" || m[13] != "/tmp/y" {
		t.Fatal(m)
	}
}
