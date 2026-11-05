//go:build desktop

package desktop

// The macOS app's native screen recording (DESIGN.md "Recorder", native capture; package
// nativerec): ScreenCaptureKit in this window process, bound to the page as
// window.go.desktop.App.NativeRecord*. Changes reach the page as the Wails event
// "tmx:native-rec" (a nativerec.Status). Elsewhere (Windows) Status says it is not supported and
// the page uses the WebView recorder.

import (
	"log/slog"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/blackcode-ch/tmx-go/internal/desktop/nativerec"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

var native struct {
	once sync.Once
	rec  *nativerec.Recorder
}

func (a *App) nativeRec() *nativerec.Recorder {
	native.once.Do(func() {
		native.rec = nativerec.New(nativerec.Options{URL: a.opts.URL, Token: a.opts.Token, Capturer: nativerec.NewCapturer(),
			Logger: slog.Default(), Tick: 250 * time.Millisecond,
			Notify: func(st nativerec.Status) {
				if a.ctx != nil {
					wruntime.EventsEmit(a.ctx, "tmx:native-rec", st)
				}
			}})
	})
	return native.rec
}

// NativeRecordStatus says whether this window records natively, and what it records now.
func (a *App) NativeRecordStatus() nativerec.Status { return a.nativeRec().Status() }

// NativeRecordStart shows the system picker, then records what the owner picked; the page
// calls it from the owner's click on Start. A closed picker answers {cancelled: true}.
func (a *App) NativeRecordStart(req nativerec.StartRequest) (nativerec.StartResult, error) {
	return a.nativeRec().Start(a.ctx, req)
}

// NativeRecordPause pauses the recording and ends its open segment.
func (a *App) NativeRecordPause() error { return a.nativeRec().Pause() }

// NativeRecordResume resumes it with a new segment.
func (a *App) NativeRecordResume() error { return a.nativeRec().Resume() }

// NativeRecordStop ends the capture and returns the finished recording.
func (a *App) NativeRecordStop() (*model.Recording, error) { return a.nativeRec().Stop() }

// NativeRecordSync follows the daemon now (a pause or stop made in another window).
func (a *App) NativeRecordSync() { a.nativeRec().Sync() }

// nativeShutdown finishes a recording when the window closes, so it is done, not interrupted.
func (a *App) nativeShutdown() {
	if native.rec != nil {
		native.rec.Close()
	}
}
