//go:build desktop

package desktop

import (
	"sync"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/blackcode-ch/tmx-go/internal/devaudio"
)

// Device audio for recordings (DESIGN.md "Recorder", "Recording on each platform"): in the
// Windows app, WASAPI loopback of the default output, captured in this window process and
// streamed to the page as "tmx:devaudio" events (a devaudio.Chunk: ~100 ms of Int16 PCM,
// base64), where ui/src/lib/native-audio.ts turns it into a MediaStreamTrack for the recorder.
// "tmx:devaudio-ended" carries why a capture stopped by itself (the device went away, the page
// stopped renewing the lease). The page starts it only for a recording the owner started with
// device audio, stops it at Pause and Stop, and the window stops it when it closes. Other
// platforms answer devaudio.ErrUnsupported.
var devAudio struct {
	mu sync.Mutex
	c  *devaudio.Capturer
}

func (a *App) deviceAudio() *devaudio.Capturer {
	devAudio.mu.Lock()
	defer devAudio.mu.Unlock()
	if devAudio.c == nil {
		devAudio.c = devaudio.New(devaudio.Options{
			Emit:  func(c devaudio.Chunk) { wruntime.EventsEmit(a.ctx, "tmx:devaudio", c) },
			Ended: func(reason string) { wruntime.EventsEmit(a.ctx, "tmx:devaudio-ended", reason) },
		})
	}
	return devAudio.c
}

// DeviceAudioStart starts capturing what this PC plays (or renews the capture that runs) and
// reports the chunks' rate and channels.
func (a *App) DeviceAudioStart() (devaudio.Stream, error) {
	if !devaudio.Supported {
		return devaudio.Stream{}, devaudio.ErrUnsupported
	}
	return a.deviceAudio().Start()
}

// DeviceAudioPing renews the capture's lease; false when none runs.
func (a *App) DeviceAudioPing() bool {
	if !devaudio.Supported {
		return false
	}
	return a.deviceAudio().Ping()
}

// DeviceAudioStop stops the capture (Pause, Stop, or the page starting afresh).
func (a *App) DeviceAudioStop() {
	if devaudio.Supported {
		a.deviceAudio().Stop()
	}
}

// stopDeviceAudio: the window closes.
func stopDeviceAudio() {
	devAudio.mu.Lock()
	c := devAudio.c
	devAudio.mu.Unlock()
	if c != nil {
		c.Stop()
	}
}
