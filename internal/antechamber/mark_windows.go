//go:build windows

package antechamber

import "os"

// markFromElsewhere writes the Mark of the Web (Protected View in Office, SmartScreen before
// running). Best effort.
func markFromElsewhere(path, referrer string) {
	_ = os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\nReferrerUrl="+referrer+"\r\n"), 0o644)
}

// noExec does nothing on Windows (no execute bits).
func noExec(string) {}
