package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/slots"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// Local voice transcription (DESIGN.md "Voice"): the daemon's service, and the CLI side of
// /api/voice and /api/transcribe.

// newVoice makes the daemon's voice service: the engine comes from the update repository's
// releases (the updater's token and keys), jobs take heavy slots, settings live in config.json
// "voice", and only the recorder's folder is transcribed.
func newVoice(paths config.Paths, st *store.Store, upd *update.Updater, sl *slots.Manager, f *fleet.Fleet, log *slog.Logger) *transcribe.Service {
	return transcribe.New(transcribe.Options{Paths: paths, Store: st, Releases: upd, Slots: sl, Publish: f.Publish, Logger: log,
		RecordingsDir: paths.RecordingsDir(), // the recorder's folder: only WAVs there are transcribed
		Version:       config.Version,
		Settings: func() config.VoiceConfig {
			if c, err := config.Load(paths); err == nil && c.Voice != nil {
				return *c.Voice
			}
			return config.VoiceConfig{}
		},
		Save: func(v config.VoiceConfig) error {
			c, err := config.Load(paths)
			if err != nil {
				return err
			}
			c.Voice = &v
			return config.Save(paths, c)
		},
	})
}

const voiceUsage = `usage: tmx voice [status]            engine, models, settings, the job running now
       tmx voice setup               download the engine, the chosen model and the voice activity model
       tmx voice get MODEL           download a model (base-q5_1, small-q5_1, large-v3-turbo-q5_0)
       tmx voice rm MODEL            remove a downloaded model
       tmx voice model MODEL         the model transcriptions use
       tmx voice language auto|CODE  the spoken language (auto = detected)
       tmx voice auto on|off         transcribe finished recordings by themselves (default on)`

const transcribeUsage = `usage: tmx transcribe FILE [--language auto|CODE] [--timestamps] [--json]
       FILE: WAV, MP3, FLAC or Ogg (up to 512 MB); prints the text. Nothing is stored.`

func cmdVoice(e *env, args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	arg := func() (string, error) {
		if len(args) != 2 {
			return "", usageErr(voiceUsage)
		}
		return args[1], nil
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var st transcribe.Status
		var err error
		follow := ""
		switch sub {
		case "status":
			if len(args) > 1 {
				return usageErr(voiceUsage)
			}
			err = c.Get(cx, "/api/voice", &st)
		case "setup":
			if err = c.Post(cx, "/api/voice/setup", nil, &st); err == nil {
				follow = "*"
			}
		case "get":
			var m string
			if m, err = arg(); err != nil {
				return err
			}
			if err = c.Post(cx, "/api/voice/models/"+client.Seg(m), nil, &st); err == nil {
				follow = m
			}
		case "rm":
			var m string
			if m, err = arg(); err != nil {
				return err
			}
			err = c.Do(cx, http.MethodDelete, "/api/voice/models/"+client.Seg(m), nil, &st)
		case "model", "language":
			var v string
			if v, err = arg(); err != nil {
				return err
			}
			err = c.Do(cx, http.MethodPut, "/api/voice", map[string]string{sub: v}, &st)
		case "auto":
			v, aerr := arg()
			if aerr != nil || (v != "on" && v != "off") {
				return usageErr(voiceUsage)
			}
			err = c.Do(cx, http.MethodPut, "/api/voice", map[string]bool{"auto_transcribe": v == "on"}, &st)
		default:
			return usageErr(voiceUsage)
		}
		if err != nil {
			return err
		}
		if follow != "" {
			if st, err = followVoice(e, c, follow); err != nil {
				return err
			}
		}
		fmt.Fprintln(e.out, describeVoice(st))
		return nil
	})
}

// followVoice prints download progress until what (a model, or "*" for the setup) is done.
func followVoice(e *env, c *client.Client, what string) (transcribe.Status, error) {
	var st transcribe.Status
	shown := map[string]int{}
	deadline := time.Now().Add(2 * time.Hour)
	for time.Now().Before(deadline) {
		cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := c.Get(cx, "/api/voice", &st)
		cancel()
		if err != nil {
			return st, err
		}
		busy := false
		report := func(name string, p *transcribe.Progress) {
			if p == nil || p.Total <= 0 {
				return
			}
			busy = true
			if step := int(p.Done * 10 / p.Total); step > shown[name] || shown[name] == 0 {
				shown[name] = max(step, 1)
				fmt.Fprintf(e.errw, "downloading %s: %s of %s\n", name, size(p.Done), size(p.Total))
			}
		}
		report("the engine", st.Engine.Downloading)
		for _, m := range append(st.Models, st.VAD) {
			report(m.Name, m.Downloading)
		}
		if what == "*" {
			busy = st.Busy
		} else {
			for _, m := range append(st.Models, st.VAD) {
				if m.Name == what && m.Downloading == nil {
					if m.Error != "" {
						return st, fmt.Errorf("%s: %s", m.Name, m.Error)
					}
					return st, nil
				}
			}
		}
		if !busy {
			return st, nil
		}
		time.Sleep(time.Second)
	}
	return st, nil
}

func describeVoice(st transcribe.Status) string {
	var b strings.Builder
	switch {
	case st.Ready:
		fmt.Fprintf(&b, "voice: ready (%s, language %s)", st.Settings.Model, st.Settings.Language)
	default:
		fmt.Fprintf(&b, "voice: not ready: %s", st.Reason)
	}
	eng := "not downloaded"
	switch {
	case !st.Engine.Available:
		eng = "not available on " + st.Platform
	case st.Engine.Installed:
		eng = "installed (" + size(st.Engine.Size) + ")"
	case st.Engine.Downloading != nil:
		eng = "downloading"
	}
	fmt.Fprintf(&b, "\nengine: whisper.cpp %s, %s", st.Engine.Version, eng)
	if st.Engine.Error != "" {
		fmt.Fprintf(&b, "\n  error: %s", st.Engine.Error)
	}
	for _, m := range append(st.Models, st.VAD) {
		state := "-"
		switch {
		case m.Installed:
			state = "installed"
		case m.Downloading != nil:
			state = fmt.Sprintf("downloading %s of %s", size(m.Downloading.Done), size(m.Size))
		}
		mark := " "
		if m.Selected {
			mark = "*"
		}
		fmt.Fprintf(&b, "\n%s %-20s %7s  %s", mark, m.Name, size(m.Size), state)
		if m.Error != "" {
			fmt.Fprintf(&b, "\n    error: %s", m.Error)
		}
		if m.Warning != "" && !m.Installed {
			fmt.Fprintf(&b, "\n    %s", m.Warning)
		}
	}
	fmt.Fprintf(&b, "\nauto-transcribe recordings: %s · voice activity detection: %s · threads: %d",
		onOff(st.Settings.AutoTranscribe), onOff(st.Settings.VAD), st.Settings.Threads)
	if j := st.Job; j != nil {
		fmt.Fprintf(&b, "\nnow: %s (%s)", j.Source, j.Phase)
	}
	if st.Queued > 0 {
		fmt.Fprintf(&b, ", %d waiting", st.Queued)
	}
	return b.String()
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n > 0:
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	}
	return "-"
}

// audioTypes are the Content-Types tmx transcribe sends (the daemon looks at the bytes).
var audioTypes = map[string]string{".wav": "audio/wav", ".mp3": "audio/mpeg", ".flac": "audio/flac", ".ogg": "audio/ogg", ".oga": "audio/ogg"}

func cmdTranscribe(e *env, args []string) error {
	fs := newFlags("transcribe")
	lang := fs.String("language", "", "")
	stamps := fs.Bool("timestamps", false, "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr(transcribeUsage)
	}
	f, err := os.Open(pos[0])
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.IsDir() {
		return fmt.Errorf("%s is not a file", pos[0])
	}
	c, err := e.owner()
	if err != nil {
		return err
	}
	q := url.Values{}
	if *lang != "" {
		q.Set("language", *lang)
	}
	// No time limit but the job's own: a long file takes a while, and waits for its turn.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.BaseURL+"/api/transcribe?"+q.Encode(), f)
	if err != nil {
		return err
	}
	ct := audioTypes[strings.ToLower(filepath.Ext(pos[0]))]
	if ct == "" {
		ct = "application/octet-stream"
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		if !healthy(c.BaseURL) {
			return fmt.Errorf("the tmx daemon is not running at %s (start it with: tmx up)", c.BaseURL)
		}
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var ae struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &ae) != nil || ae.Error == "" {
			ae.Error = "the daemon answered " + resp.Status
		}
		return &client.APIError{Status: resp.StatusCode, Msg: ae.Error, Body: body}
	}
	var r transcribe.Result
	if err := json.Unmarshal(body, &r); err != nil {
		return err
	}
	switch {
	case *asJSON:
		e.printJSON(r)
	case *stamps:
		for _, s := range r.Segments {
			fmt.Fprintf(e.out, "[%s] %s\n", clock(s.FromMS), s.Text)
		}
	default:
		fmt.Fprintln(e.out, r.Text)
	}
	return nil
}

func clock(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d >= time.Hour {
		return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}
