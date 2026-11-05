// Package devaudio captures the device's own sound (what its speakers play) for a recording in
// the desktop window (DESIGN.md "Recorder", "Recording on each platform"). On Windows it is
// WASAPI loopback of the default output device, in the window process, through COM without
// cgo (wasapi_windows.go); there is none elsewhere (the macOS app records device audio with
// ScreenCaptureKit, browsers through getDisplayMedia).
//
// Only the owner's window drives it, and only while one of its recordings with device audio
// runs: the page starts it after the owner clicked Record (ui/src/lib/native-audio.ts), stops it
// at Pause and Stop, and the window stops it when it closes. A Capturer turns what the device
// gives into ~100 ms chunks of interleaved Int16 (mono or stereo, at the device's rate) and hands
// them to Emit, which the window sends to the page as Wails events. A lease that the page renews
// ends a capture nobody asks for any more (the page went away without stopping it).
package devaudio

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrUnsupported: this platform has no native device-audio source.
var ErrUnsupported = errors.New("device sound from this window is only available in the Windows app")

// Format is what a Source delivers: interleaved frames of Channels samples.
type Format struct {
	Rate     int  // frames per second, e.g. 48000
	Channels int  // samples per frame
	Bits     int  // bits per sample as stored: 16, 24 or 32
	Float    bool // 32-bit IEEE float samples (else signed integers)
	// Mask is the WAVE channel mask (which speaker each channel is for); 0 if the device gives
	// none. Only used to fold more than two channels into stereo.
	Mask uint32
}

func (f Format) frameBytes() int { return f.Channels * f.Bits / 8 }

func (f Format) check() error {
	switch {
	case f.Rate < 8000 || f.Rate > 384000:
		return fmt.Errorf("the sound device has an unusual rate (%d Hz)", f.Rate)
	case f.Channels < 1 || f.Channels > 32:
		return fmt.Errorf("the sound device has an unusual number of channels (%d)", f.Channels)
	case f.Float && f.Bits != 32, !f.Float && f.Bits != 16 && f.Bits != 24 && f.Bits != 32:
		return fmt.Errorf("the sound device's format (%d-bit, float %v) is not supported", f.Bits, f.Float)
	}
	return nil
}

// outChannels: mono stays mono, everything else becomes stereo.
func (f Format) outChannels() int {
	if f.Channels == 1 {
		return 1
	}
	return 2
}

// Source is where the sound comes from: WASAPI loopback (System) or a fake in tests.
type Source interface {
	// Capture opens the device and calls opened with its format (or returns why it cannot
	// without calling it), then hands every packet to put until ctx ends: data holds frames
	// whole frames in that format, or is nil for frames of silence. When the device changes
	// (the owner picked another output), it calls opened again with the new format. It returns
	// nil once ctx ended, else why the capture stopped. opened and put run on Capture's
	// goroutine.
	Capture(ctx context.Context, opened func(Format), put func(data []byte, frames int)) error
}

// Stream is what the page gets from Start: the format of the chunks to come.
type Stream struct {
	Rate     int `json:"rate"`
	Channels int `json:"channels"` // 1 or 2
}

// Chunk is about 100 ms of sound for the page.
type Chunk struct {
	Seq      uint64 `json:"seq"` // 1, 2, … within one capture; a gap means chunks were dropped
	Rate     int    `json:"rate"`
	Channels int    `json:"channels"` // 1 or 2
	Frames   int    `json:"frames"`
	PCM      string `json:"pcm"` // base64 of interleaved Int16, little-endian
}

// Samples decodes PCM (tests and tools).
func (c Chunk) Samples() ([]int16, error) {
	b, err := base64.StdEncoding.DecodeString(c.PCM)
	if err != nil {
		return nil, err
	}
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out, nil
}

// Options configure a Capturer.
type Options struct {
	// Source opens the device for one capture; System by default.
	Source func() (Source, error)
	// Emit gets the chunks, in order, from one goroutine. It should not block for long: while
	// it does, up to Queue chunks wait and newer ones are dropped.
	Emit func(Chunk)
	// Ended is told why a capture stopped by itself (the device failed, the lease ran out). Not
	// called after Stop.
	Ended func(reason string)
	// Lease: a capture whose page has not called Start or Ping for this long stops (60 s).
	Lease time.Duration
	// ChunkMS is the length of a chunk (100 ms).
	ChunkMS int
	// Queue is how many chunks may wait for Emit (8).
	Queue int
	// OpenTimeout bounds how long Start waits for the device (5 s).
	OpenTimeout time.Duration
}

// Capturer runs at most one capture at a time; its methods are safe for concurrent use.
type Capturer struct {
	o Options

	mu  sync.Mutex
	cur *capture
}

type capture struct {
	cancel context.CancelFunc
	stream Stream
	ping   time.Time
	done   chan struct{} // closed when the capture and its emitter have stopped
}

// wait waits (briefly) until cp has stopped: a device that hangs must not hang the window.
func (cp *capture) wait() {
	select {
	case <-cp.done:
	case <-time.After(3 * time.Second):
	}
}

// New returns a Capturer with o's defaults filled in.
func New(o Options) *Capturer {
	if o.Source == nil {
		o.Source = System
	}
	if o.Emit == nil {
		o.Emit = func(Chunk) {}
	}
	if o.Ended == nil {
		o.Ended = func(string) {}
	}
	if o.Lease <= 0 {
		o.Lease = 60 * time.Second
	}
	if o.ChunkMS <= 0 {
		o.ChunkMS = 100
	}
	if o.Queue <= 0 {
		o.Queue = 8
	}
	if o.OpenTimeout <= 0 {
		o.OpenTimeout = 5 * time.Second
	}
	return &Capturer{o: o}
}

// Running reports whether a capture is live.
func (c *Capturer) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur != nil
}

// Ping renews the live capture's lease; false when none is live.
func (c *Capturer) Ping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur == nil {
		return false
	}
	c.cur.ping = time.Now()
	return true
}

// Start begins a capture, or renews the live one, and reports the chunks' format.
func (c *Capturer) Start() (Stream, error) {
	c.mu.Lock()
	if c.cur != nil {
		c.cur.ping = time.Now()
		s := c.cur.stream
		c.mu.Unlock()
		return s, nil
	}
	c.mu.Unlock()

	src, err := c.o.Source()
	if err != nil {
		return Stream{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	cp := &capture{cancel: cancel, ping: time.Now(), done: make(chan struct{})}
	opened := make(chan Stream, 1)
	failed := make(chan error, 1)
	go c.run(ctx, cp, src, opened, failed)

	timer := time.NewTimer(c.o.OpenTimeout)
	defer timer.Stop()
	select {
	case s := <-opened: // the live capture now: this one, or one that another Start opened first
		if c.live() != cp {
			cancel()
			cp.wait()
		}
		return s, nil
	case err := <-failed:
		cancel()
		cp.wait()
		return Stream{}, err
	case <-timer.C:
		c.mu.Lock()
		if c.cur == cp { // it opened just now: too late
			c.cur = nil
		}
		c.mu.Unlock()
		cancel()
		cp.wait()
		return Stream{}, errors.New("the sound device did not start in time")
	}
}

func (c *Capturer) live() *capture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

// Stop ends the live capture and waits (briefly) until no more chunks will be emitted.
func (c *Capturer) Stop() {
	c.mu.Lock()
	cp := c.cur
	c.cur = nil // so the capture does not report Ended
	c.mu.Unlock()
	if cp == nil {
		return
	}
	cp.cancel()
	cp.wait()
}

// run is one capture: the source on this goroutine, the framer in put, the emitter and the
// lease watchdog beside it.
func (c *Capturer) run(ctx context.Context, cp *capture, src Source, opened chan<- Stream, failed chan<- error) {
	defer close(cp.done)
	out := make(chan Chunk, c.o.Queue)
	emitted := make(chan struct{})
	go func() {
		defer close(emitted)
		for ch := range out {
			c.o.Emit(ch)
		}
	}()

	var reason string // why the capture stopped by itself
	watchdog := make(chan struct{})
	go func() {
		defer close(watchdog)
		t := time.NewTicker(c.leaseTick())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.mu.Lock()
				stale := c.cur == cp && time.Since(cp.ping) > c.o.Lease
				c.mu.Unlock()
				if stale {
					reason = "the window stopped asking for the device sound"
					cp.cancel()
					return
				}
			}
		}
	}()

	f := &framer{chunkMS: c.o.ChunkMS, out: out}
	first := true
	var ferr error
	err := src.Capture(ctx,
		func(fm Format) {
			if ferr = fm.check(); ferr != nil {
				cp.cancel()
				return
			}
			f.reset(fm)
			if first {
				// It is the live capture from now on (so a device that fails at once still
				// reports it), unless another Start's capture opened first: then that one is.
				first = false
				st := Stream{Rate: fm.Rate, Channels: fm.outChannels()}
				c.mu.Lock()
				if c.cur == nil {
					cp.stream = st
					c.cur = cp
				} else {
					st = c.cur.stream
					cp.cancel()
				}
				c.mu.Unlock()
				opened <- st
			}
		},
		func(data []byte, frames int) {
			if ctx.Err() == nil && ferr == nil {
				f.put(data, frames)
			}
		})
	cp.cancel()
	<-watchdog
	close(out)
	<-emitted
	if ferr != nil {
		err = ferr
	}
	if first { // never opened
		if err == nil {
			err = errors.New("the sound device did not start")
		}
		failed <- err
		return
	}
	if reason == "" && err != nil {
		reason = err.Error()
	}
	// Still the live capture: it stopped by itself (Stop and a lost Start race clear c.cur first).
	c.mu.Lock()
	report := c.cur == cp
	if report {
		c.cur = nil
	}
	c.mu.Unlock()
	if report {
		if reason == "" {
			reason = "the sound device stopped"
		}
		c.o.Ended(reason)
	}
}

func (c *Capturer) leaseTick() time.Duration {
	t := c.o.Lease / 4
	if t > time.Second {
		t = time.Second
	}
	if t < 5*time.Millisecond {
		t = 5 * time.Millisecond
	}
	return t
}

// framer collects converted frames into chunks of chunkMS.
type framer struct {
	chunkMS int
	out     chan Chunk
	in      Format
	ch      int // output channels
	per     int // frames per chunk
	buf     []int16
	seq     uint64
	dropped int
}

// reset starts a new format; what is pending in the old one goes out first.
func (f *framer) reset(fm Format) {
	f.flush()
	f.in = fm
	f.ch = fm.outChannels()
	f.per = fm.Rate * f.chunkMS / 1000
	if f.per < 1 {
		f.per = 1
	}
	f.buf = make([]int16, 0, f.per*f.ch)
}

func (f *framer) put(data []byte, frames int) {
	if f.per == 0 || frames <= 0 {
		return
	}
	if data != nil && len(data) < frames*f.in.frameBytes() {
		frames = len(data) / f.in.frameBytes()
	}
	fb := f.in.frameBytes()
	for frames > 0 {
		room := f.per - len(f.buf)/f.ch
		n := min(room, frames)
		if data == nil {
			f.buf = appendSilence(f.buf, n*f.ch)
		} else {
			f.buf = convert(f.buf, data[:n*fb], n, f.in)
			data = data[n*fb:]
		}
		frames -= n
		if len(f.buf)/f.ch == f.per {
			f.flush()
		}
	}
}

func (f *framer) flush() {
	if len(f.buf) == 0 {
		return
	}
	b := make([]byte, 2*len(f.buf))
	for i, s := range f.buf {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	f.seq++
	ch := Chunk{Seq: f.seq, Rate: f.in.Rate, Channels: f.ch, Frames: len(f.buf) / f.ch, PCM: base64.StdEncoding.EncodeToString(b)}
	f.buf = f.buf[:0]
	select {
	case f.out <- ch:
	default:
		f.dropped++ // the page is behind: the newest chunk goes, the capture never waits
	}
}
