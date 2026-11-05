//go:build !(desktop && darwin && cgo)

package nativerec

import (
	"context"
	"errors"
	"time"
)

// NewCapturer: this build has no native capture (Windows, Linux, the headless build); the
// window records through the WebView.
func NewCapturer() Capturer { return none{} }

type none struct{}

var errNone = errors.New("native screen recording is only in the macOS app")

func (none) Supported() (bool, string)            { return false, errNone.Error() }
func (none) Pick(context.Context) (Picked, error) { return Picked{}, errNone }
func (none) Open(StreamConfig, func([]int16), func(error)) (Opened, error) {
	return Opened{}, errNone
}
func (none) StartSegment(string) error          { return errNone }
func (none) Progress() (time.Duration, int64)   { return 0, 0 }
func (none) EndSegment() (time.Duration, error) { return 0, nil }
func (none) Frame() ([]byte, error)             { return nil, ErrNoFrame }
func (none) Close()                             {}
