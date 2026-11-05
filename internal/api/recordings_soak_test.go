package api_test

// The recorder soak (tmx-lead, 3 Nov; the Python fleet's recorder-soak.yml ported): a REAL clock, the daemon's
// real HTTP routes and sweep, a client that behaves like the recording window (a chunk a second, a new segment
// every 5 minutes). 12 minutes = 3 parts; the network is gone for 30 s in the middle of part 2 (nothing reaches
// the daemon, the chunks queue), then the queued chunks arrive in order; then Stop. Asserts no interruption, every
// byte in 3 segments, one recording, done. Run on demand: TMXG_SOAK=1 (.github/workflows/recorder-soak.yml).

import (
	"bytes"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestRecorderSoakRealClock(t *testing.T) {
	if os.Getenv("TMXG_SOAK") != "1" {
		t.Skip("the 12-min soak runs on demand (TMXG_SOAK=1)")
	}
	minutes := 12.0
	if m, err := strconv.ParseFloat(os.Getenv("TMXG_SOAK_MINUTES"), 64); err == nil && m > 0 {
		minutes = m
	}
	total := time.Duration(minutes * float64(time.Minute))
	part := total / 12 * 5                         // 5 min of 12
	outageAt, outage := total/12*7, 30*time.Second // mid part 2
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	var rec model.Recording
	if s := owner.do("POST", "/api/recordings", map[string]any{"kind": "screen", "mic": true, "mime": "video/webm;codecs=vp8,opus", "title": "Soak"}, &rec); s != 201 {
		t.Fatalf("begin: %d", s)
	}
	type chunk struct {
		n   int
		off int64
	}
	chunkBytes := bytes.Repeat([]byte{0x5a}, 4000)
	offsets := map[int]int64{}
	var queue []chunk
	start := time.Now()
	sent, maxQueue := 0, 0
	for tick := time.Duration(0); tick < total; tick += time.Second {
		time.Sleep(time.Until(start.Add(tick)))
		n := 1 + int(tick/part)
		queue = append(queue, chunk{n, offsets[n]})
		offsets[n] += int64(len(chunkBytes))
		if tick >= outageAt && tick < outageAt+outage {
			maxQueue = max(maxQueue, len(queue))
			continue // offline: the window keeps it queued
		}
		for len(queue) > 0 { // online: everything queued goes, in order
			c := queue[0]
			dur := strconv.FormatInt(int64(tick/time.Millisecond)-int64(time.Duration(c.n-1)*part/time.Millisecond), 10)
			s, b, _ := raw(t, "PUT", chunkURL(d.URL, rec.ID, c.n, "media", c.off)+"&dur="+dur, d.OwnerToken, chunkBytes, nil)
			if s != 200 && s != 204 {
				t.Fatalf("chunk %+v at %s: %d %s", c, tick, s, b)
			}
			queue = queue[1:]
			sent++
		}
		if tick == outageAt+outage { // just back: the daemon still has it live
			var got model.Recording
			owner.do("GET", "/api/recordings/"+rec.ID, nil, &got)
			if got.State != model.RecordingActive {
				t.Fatalf("interrupted by a %s outage: %+v", outage, got)
			}
		}
	}
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/state", map[string]string{"state": "stopped"}, &rec); s != 200 {
		t.Fatalf("stop: %d", s)
	}
	var want int64
	for _, o := range offsets {
		want += o
	}
	t.Logf("soak: %s, %d chunks, %d queued during the outage, %d segments, %d bytes, state %s %q",
		time.Since(start).Round(time.Second), sent, maxQueue, len(rec.Segments), rec.Bytes, rec.State, rec.Error)
	if rec.State != model.RecordingDone || rec.Error != "" || len(rec.Segments) != 3 || rec.Bytes != want {
		t.Fatalf("after Stop: state %s %q, %d segments, %d of %d bytes", rec.State, rec.Error, len(rec.Segments), rec.Bytes, want)
	}
	if maxQueue < 29 {
		t.Fatalf("the outage queued only %d chunks", maxQueue)
	}
	var list []model.Recording
	owner.do("GET", "/api/recordings", nil, &list)
	if len(list) != 1 {
		t.Fatalf("one recording in the list, got %d", len(list))
	}
}
