package devaudio

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fake is a Source that plays a 440 Hz tone in 10 ms packets as fast as the test lets it.
type fake struct {
	format  Format
	openErr error
	never   bool          // never opens (until ctx ends)
	packets int           // stop with failErr after this many packets (0: run until ctx ends)
	failErr error         // what Capture returns after packets
	silent  bool          // packets of silence (nil data)
	then    *Format       // after packets, switch to this format instead of failing
	pace    time.Duration // between packets
	opens   atomic.Int32
	sent    atomic.Int64 // frames handed to put
	frame   int
}

func (s *fake) Capture(ctx context.Context, opened func(Format), put func([]byte, int)) error {
	s.opens.Add(1)
	if s.openErr != nil {
		return s.openErr
	}
	if s.never {
		<-ctx.Done()
		return nil
	}
	f := s.format
	opened(f)
	for n := 0; ctx.Err() == nil; n++ {
		if s.packets > 0 && n == s.packets {
			if s.then == nil {
				return s.failErr
			}
			f = *s.then
			s.then = nil
			opened(f)
		}
		frames := f.Rate / 100
		if s.silent {
			put(nil, frames)
		} else {
			put(s.tone(f, frames), frames)
		}
		s.sent.Add(int64(frames))
		if s.pace > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(s.pace):
			}
		}
	}
	return nil
}

// toneAt is the tone's value at frame i.
func toneAt(i, rate int) float64 { return 0.5 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)) }

func (s *fake) tone(f Format, frames int) []byte {
	vs := make([]float32, 0, frames*f.Channels)
	for i := 0; i < frames; i++ {
		v := float32(toneAt(s.frame+i, f.Rate))
		for c := 0; c < f.Channels; c++ {
			if c == 1 {
				vs = append(vs, -v) // the right channel is the tone upside down
			} else {
				vs = append(vs, v)
			}
		}
	}
	s.frame += frames
	return f32(vs...)
}

type sink struct {
	mu     sync.Mutex
	chunks []Chunk
	ended  []string
	got    chan struct{}
}

func newSink() *sink { return &sink{got: make(chan struct{}, 1000)} }

func (k *sink) emit(c Chunk) {
	k.mu.Lock()
	k.chunks = append(k.chunks, c)
	k.mu.Unlock()
	k.got <- struct{}{}
}

func (k *sink) end(r string) {
	k.mu.Lock()
	k.ended = append(k.ended, r)
	k.mu.Unlock()
	k.got <- struct{}{}
}

func (k *sink) waitChunks(t *testing.T, n int) []Chunk {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		k.mu.Lock()
		if len(k.chunks) >= n {
			out := append([]Chunk(nil), k.chunks...)
			k.mu.Unlock()
			return out
		}
		k.mu.Unlock()
		select {
		case <-k.got:
		case <-deadline:
			t.Fatalf("only %d of %d chunks arrived", len(k.chunks), n)
		}
	}
}

func (k *sink) waitEnded(t *testing.T) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		k.mu.Lock()
		if len(k.ended) > 0 {
			r := k.ended[0]
			k.mu.Unlock()
			return r
		}
		k.mu.Unlock()
		select {
		case <-k.got:
		case <-deadline:
			t.Fatal("Ended was not called")
		}
	}
}

var stereo48k = Format{Rate: 48000, Channels: 2, Bits: 32, Float: true, Mask: 3}

func newCapturer(src Source, k *sink, mod func(*Options)) *Capturer {
	o := Options{Source: func() (Source, error) { return src, nil }, Emit: k.emit, Ended: k.end}
	if mod != nil {
		mod(&o)
	}
	return New(o)
}

func TestChunksCarryTheToneIn100ms(t *testing.T) {
	src := &fake{format: stereo48k, pace: time.Millisecond}
	k := newSink()
	c := newCapturer(src, k, nil)
	st, err := c.Start()
	if err != nil {
		t.Fatal(err)
	}
	if st != (Stream{Rate: 48000, Channels: 2}) {
		t.Fatalf("stream %+v", st)
	}
	chunks := k.waitChunks(t, 4)
	c.Stop()
	if c.Running() {
		t.Fatal("still running after Stop")
	}
	frame := 0
	for i, ch := range chunks[:4] {
		if ch.Seq != uint64(i+1) || ch.Rate != 48000 || ch.Channels != 2 || ch.Frames != 4800 {
			t.Fatalf("chunk %d: %+v", i, Chunk{Seq: ch.Seq, Rate: ch.Rate, Channels: ch.Channels, Frames: ch.Frames})
		}
		s, err := ch.Samples()
		if err != nil || len(s) != 9600 {
			t.Fatalf("chunk %d: %d samples, %v", i, len(s), err)
		}
		for j := 0; j < 4800; j++ {
			want := toInt16(toneAt(frame, 48000))
			if absDiff(s[2*j], want) > 1 || absDiff(s[2*j+1], -want) > 1 {
				t.Fatalf("chunk %d frame %d: got %d/%d, want %d/%d", i, j, s[2*j], s[2*j+1], want, -want)
			}
			frame++
		}
	}
	// Nothing comes after Stop returned.
	k.mu.Lock()
	n := len(k.chunks)
	k.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.chunks) != n {
		t.Fatalf("%d chunks after Stop", len(k.chunks)-n)
	}
	if len(k.ended) != 0 {
		t.Fatalf("Ended after Stop: %v", k.ended)
	}
}

func TestSilencePacketsAreZeros(t *testing.T) {
	src := &fake{format: Format{Rate: 16000, Channels: 1, Bits: 16}, silent: true, pace: time.Millisecond}
	k := newSink()
	c := newCapturer(src, k, nil)
	if st, err := c.Start(); err != nil || st != (Stream{Rate: 16000, Channels: 1}) {
		t.Fatal(st, err)
	}
	ch := k.waitChunks(t, 1)[0]
	c.Stop()
	s, _ := ch.Samples()
	if ch.Frames != 1600 || len(s) != 1600 {
		t.Fatalf("frames %d, samples %d", ch.Frames, len(s))
	}
	for i, v := range s {
		if v != 0 {
			t.Fatalf("sample %d is %d", i, v)
		}
	}
}

func TestStartAgainRenewsTheLiveCapture(t *testing.T) {
	src := &fake{format: stereo48k, pace: time.Millisecond}
	k := newSink()
	c := newCapturer(src, k, nil)
	a, err := c.Start()
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Start()
	if err != nil || a != b {
		t.Fatal(a, b, err)
	}
	if n := src.opens.Load(); n != 1 {
		t.Fatalf("the device was opened %d times", n)
	}
	if !c.Ping() {
		t.Fatal("Ping: not running")
	}
	c.Stop()
	c.Stop() // twice is fine
	if c.Ping() {
		t.Fatal("Ping after Stop")
	}
	// Pause and resume: a new capture, numbered from 1 again.
	k.mu.Lock()
	k.chunks = nil
	k.mu.Unlock()
	if _, err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if ch := k.waitChunks(t, 1)[0]; ch.Seq != 1 {
		t.Fatalf("first chunk after a restart has seq %d", ch.Seq)
	}
	c.Stop()
	if n := src.opens.Load(); n != 2 {
		t.Fatalf("the device was opened %d times", n)
	}
}

func TestStartFails(t *testing.T) {
	k := newSink()
	boom := errors.New("this PC has no sound output device")
	c := newCapturer(&fake{openErr: boom}, k, nil)
	if _, err := c.Start(); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if c.Running() {
		t.Fatal("running")
	}
	// No source at all (another platform).
	c = New(Options{Source: func() (Source, error) { return nil, ErrUnsupported }})
	if _, err := c.Start(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err %v", err)
	}
	// A format it cannot convert.
	c = newCapturer(&fake{format: Format{Rate: 48000, Channels: 2, Bits: 8}}, k, nil)
	if _, err := c.Start(); err == nil || c.Running() {
		t.Fatalf("8-bit: err %v, running %v", err, c.Running())
	}
	// A device that never opens.
	src := &fake{never: true}
	c = newCapturer(src, k, func(o *Options) { o.OpenTimeout = 50 * time.Millisecond })
	start := time.Now()
	if _, err := c.Start(); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
	if c.Running() {
		t.Fatal("running")
	}
	if len(k.ended) != 0 {
		t.Fatalf("Ended for a capture that never started: %v", k.ended)
	}
}

func TestLeaseEndsAForgottenCapture(t *testing.T) {
	src := &fake{format: stereo48k, pace: time.Millisecond}
	k := newSink()
	c := newCapturer(src, k, func(o *Options) { o.Lease = 150 * time.Millisecond })
	if _, err := c.Start(); err != nil {
		t.Fatal(err)
	}
	// Pinged: it stays.
	for i := 0; i < 6; i++ {
		time.Sleep(50 * time.Millisecond)
		if !c.Ping() {
			t.Fatalf("stopped although pinged (after %d pings)", i)
		}
	}
	// Forgotten: it ends by itself and says why.
	if r := k.waitEnded(t); r != "the window stopped asking for the device sound" {
		t.Fatalf("reason %q", r)
	}
	if c.Running() || c.Ping() {
		t.Fatal("still running")
	}
}

func TestDeviceFailureEndsTheCapture(t *testing.T) {
	src := &fake{format: stereo48k, packets: 25, failErr: errors.New("the sound output device went away")}
	k := newSink()
	c := newCapturer(src, k, nil)
	if _, err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if r := k.waitEnded(t); r != "the sound output device went away" {
		t.Fatalf("reason %q", r)
	}
	if c.Running() {
		t.Fatal("still running")
	}
	if n := len(k.waitChunks(t, 2)); n != 2 { // 25 packets of 10 ms: two whole chunks
		t.Fatalf("%d chunks", n)
	}
	// It can start again.
	src.packets = 0
	if _, err := c.Start(); err != nil {
		t.Fatal(err)
	}
	c.Stop()
}

func TestANewFormatFlushesWhatIsPending(t *testing.T) {
	mono := Format{Rate: 44100, Channels: 1, Bits: 16}
	src := &fake{format: stereo48k, packets: 15, then: &mono, pace: time.Millisecond}
	k := newSink()
	c := newCapturer(src, k, nil)
	if _, err := c.Start(); err != nil {
		t.Fatal(err)
	}
	chunks := k.waitChunks(t, 3)
	c.Stop()
	// 15 packets of 480 frames: one chunk of 4800, then the 2400 pending, then the new format.
	if chunks[0].Frames != 4800 || chunks[1].Frames != 2400 || chunks[1].Rate != 48000 {
		t.Fatalf("before the change: %d, %d frames at %d Hz", chunks[0].Frames, chunks[1].Frames, chunks[1].Rate)
	}
	if chunks[2].Rate != 44100 || chunks[2].Channels != 1 || chunks[2].Frames != 4410 || chunks[2].Seq != 3 {
		t.Fatalf("after the change: %+v", Chunk{Seq: chunks[2].Seq, Rate: chunks[2].Rate, Channels: chunks[2].Channels, Frames: chunks[2].Frames})
	}
}

func TestASlowPageNeverStallsTheDevice(t *testing.T) {
	src := &fake{format: stereo48k} // as fast as it goes
	release := make(chan struct{})
	var emitted atomic.Int32
	c := New(Options{
		Source: func() (Source, error) { return src, nil },
		Emit: func(Chunk) {
			emitted.Add(1)
			<-release // the page is stuck
		},
		Queue: 2,
	})
	if _, err := c.Start(); err != nil {
		t.Fatal(err)
	}
	// The device keeps going far past what the queue holds.
	deadline := time.Now().Add(5 * time.Second)
	for src.sent.Load() < 48000*10 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if src.sent.Load() < 48000*10 {
		t.Fatalf("the capture stalled after %d frames", src.sent.Load())
	}
	if n := emitted.Load(); n != 1 {
		t.Fatalf("%d emits while the page was stuck", n)
	}
	// Stop first, then the page gets going again: only what the queue held is left to emit.
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(release)
	}()
	c.Stop()
	if n := emitted.Load(); n > 1+2 {
		t.Fatalf("%d chunks emitted: the queue was not bounded", n)
	}
}
