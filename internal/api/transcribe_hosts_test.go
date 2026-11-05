package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func enableVoiceHosts(t *testing.T, home string) {
	t.Helper()
	p := config.Paths{Home: home}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Voice == nil {
		c.Voice = &config.VoiceConfig{}
	}
	c.Voice.FallbackHosts = true
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredVoiceHostOptInSilenceReplayAndOneHop(t *testing.T) {
	// bc #214, run 41692174665: hostTimeout's 2s host-probe deadline (fleethosts.go) is a real
	// production UX/stall bound and stays that way; go test -race's well-documented 2-10x
	// slowdown, on top of the rest of this package's ~190s run contending for CPU, could still
	// lose that race against the test's own httptest server even though its handler does no
	// real work. Widen it for this test alone.
	t.Cleanup(api.SetHostTimeoutForTest(10 * time.Second))
	local := apitest.New(t)
	token := model.NewID()
	var lookups, posts atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong host credential")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/voice":
			lookups.Add(1)
			io.WriteString(w, `{"ready":true}`)
		case "/api/transcribe":
			posts.Add(1)
			if r.Header.Get("Idempotency-Key") != "dictation-1" || r.Header.Get("X-Transcription-Local-Only") != "true" {
				t.Error("missing receipt or one-hop marker")
			}
			io.WriteString(w, `{"text":"configured host words","language":"en","segments":[]}`)
		default:
			t.Error("unexpected host route")
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	registerHost(t, local.Home, config.Host{Name: "voice-host", Port: hostPort(t, remote.URL)}, token)
	post := func(key string, wav []byte, localOnly bool) rawResp {
		body, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(wav)})
		req, _ := http.NewRequest("POST", local.URL+"/api/transcribe", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+local.OwnerToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		if localOnly {
			req.Header.Set("X-Transcription-Local-Only", "true")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return rawResp{resp.StatusCode, string(b)}
	}
	if r := post("disabled", testWAV(1), false); r.code != 409 || lookups.Load() != 0 {
		t.Fatalf("default must stay local: %+v", r)
	}
	enableVoiceHosts(t, local.Home)
	silence := testWAV(1)
	clear(silence[44:])
	if r := post("silence", silence, false); r.code != 422 || lookups.Load() != 0 || posts.Load() != 0 {
		t.Fatalf("silence sent: %+v", r)
	}
	for i := 0; i < 2; i++ {
		if r := post("dictation-1", testWAV(1), false); r.code != 200 || !strings.Contains(r.body, "configured host words") {
			t.Fatalf("fallback: %+v", r)
		}
	}
	if lookups.Load() != 1 || posts.Load() != 1 {
		t.Fatalf("duplicate remote work: lookups=%d posts=%d", lookups.Load(), posts.Load())
	}
	if r := post("dictation-1", testWAV(1), true); r.code != 409 {
		t.Fatalf("changed execution option reused receipt: %+v", r)
	}
	if r := post("forwarded", testWAV(1), true); r.code != 409 || posts.Load() != 1 || lookups.Load() != 1 {
		t.Fatalf("forwarded request looped: %+v", r)
	}
}

func TestConfiguredVoiceHostDoesNotFollowRedirects(t *testing.T) {
	local := apitest.New(t)
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer sink.Close()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer remote.Close()
	registerHost(t, local.Home, config.Host{Name: "voice-host", Port: hostPort(t, remote.URL)}, model.NewID())
	enableVoiceHosts(t, local.Home)
	body, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(testWAV(1))})
	r := rawPost(t, local, "application/json", bytes.NewReader(body))
	if r.code != 409 || leaked.Load() != 0 {
		t.Fatalf("redirect: %+v leaked=%d", r, leaked.Load())
	}
}
