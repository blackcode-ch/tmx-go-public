package api

import "time"

// SetHostTimeoutForTest widens hostTimeout for the duration of one test (restore with the
// returned func, typically via t.Cleanup) - fleethosts.go, bc #214: configuredVoiceHosts'
// 2s probe deadline is a real UX/stall bound in production and must not grow just because
// go test -race is slower; this lets exactly one test ask for more room instead.
func SetHostTimeoutForTest(d time.Duration) (restore func()) {
	prev := hostTimeout
	hostTimeout = d
	return func() { hostTimeout = prev }
}
