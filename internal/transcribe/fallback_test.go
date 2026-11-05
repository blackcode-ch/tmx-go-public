package transcribe

import (
	"context"
	"errors"
	"testing"
)

type fakeHosts struct {
	hosts              []TranscriptionHost
	lookups, calls     int
	selected, language string
	err                error
}

func (f *fakeHosts) Hosts(context.Context) ([]TranscriptionHost, error) {
	f.lookups++
	return f.hosts, nil
}
func (f *fakeHosts) Dictate(_ context.Context, id string, _ []byte, language string) (Result, error) {
	f.calls++
	f.selected = id
	f.language = language
	return Result{Text: "remote words"}, f.err
}
func fallbackWAV(silent bool) []byte {
	b := append(wavHeader(1, 16000, 16, 32000), make([]byte, 32000)...)
	if !silent {
		b[44] = 123
	}
	return b
}
func TestConfiguredHostFallback(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{true: "unavailable", false: "busy"}[unavailable], func(t *testing.T) {
			f := &fakeHosts{hosts: []TranscriptionHost{{ID: "busy", Ready: true, Busy: true}, {ID: "offline"}, {ID: "configured", Ready: true}}}
			opts := []envOpt{func(_ *testEnv, o *Options) { o.Hosts = f }}
			if unavailable {
				opts = append(opts, noEngine)
			}
			e := newEnv(t, opts...)
			if !unavailable {
				e.s.mu.Lock()
				e.s.running = &job{}
				e.s.mu.Unlock()
			}
			r, err := e.s.Dictate(context.Background(), fallbackWAV(false), "de")
			if err != nil || r.Text != "remote words" || f.calls != 1 || f.selected != "configured" || f.language != "de" {
				t.Fatalf("result=%+v err=%v hosts=%+v", r, err, f)
			}
		})
	}
}
func TestSilentFallbackSendsNothing(t *testing.T) {
	f := &fakeHosts{hosts: []TranscriptionHost{{ID: "configured", Ready: true}}}
	e := newEnv(t, noEngine, func(_ *testEnv, o *Options) { o.Hosts = f })
	_, err := e.s.Dictate(context.Background(), fallbackWAV(true), "")
	if !errors.Is(err, ErrNoSpeech) || err.Error() != "couldn't hear you" || f.lookups != 0 || f.calls != 0 {
		t.Fatalf("err=%v hosts=%+v", err, f)
	}
}

func TestSilentLocalDictationDoesNotRunEngine(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.Dictate(context.Background(), fallbackWAV(true), "")
	if !errors.Is(err, ErrNoSpeech) || len(e.runs()) != 0 {
		t.Fatalf("err=%v runs=%v", err, e.runs())
	}
}
func TestFallbackDoesNotRetryAmbiguousFailure(t *testing.T) {
	f := &fakeHosts{hosts: []TranscriptionHost{{ID: "first", Ready: true}, {ID: "second", Ready: true}}, err: errors.New("connection lost")}
	e := newEnv(t, noEngine, func(_ *testEnv, o *Options) { o.Hosts = f })
	_, err := e.s.Dictate(context.Background(), fallbackWAV(false), "")
	if !errors.Is(err, f.err) || f.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
}
func TestFallbackDisabledOrNoEligibleHost(t *testing.T) {
	for _, hosts := range []ConfiguredHosts{nil, &fakeHosts{hosts: []TranscriptionHost{{ID: "busy", Ready: true, Busy: true}}}} {
		t.Run("not-set-up", func(t *testing.T) {
			e := newEnv(t, noEngine, func(_ *testEnv, o *Options) { o.Hosts = hosts })
			_, err := e.s.Dictate(context.Background(), fallbackWAV(false), "")
			if !errors.Is(err, ErrNotSetUp) {
				t.Fatal(err)
			}
		})
	}
}
func TestReadyLocalEngineDoesNotUseHostAPI(t *testing.T) {
	f := &fakeHosts{}
	e := newEnv(t, func(_ *testEnv, o *Options) { o.Hosts = f })
	_, err := e.s.Dictate(context.Background(), fallbackWAV(false), "")
	if err != nil || f.lookups != 0 || f.calls != 0 || len(e.runs()) != 1 {
		t.Fatalf("err=%v hosts=%+v", err, f)
	}
}
