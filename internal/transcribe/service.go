// Package transcribe is local voice transcription with whisper.cpp (DESIGN.md "Voice"): it
// downloads the engine (whisper-cli, built by tmx's CI) and the models on demand, verified;
// runs one job at a time in a heavy slot at low priority; stores transcripts of recordings
// (searchable) and answers dictation directly. Audio stays local unless an explicitly
// configured host adapter is supplied for dictation fallback.
package transcribe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/slots"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// Limits.
const (
	// MaxDictation is the longest audio POST /api/transcribe takes as JSON (dictation).
	MaxDictation = 2 * time.Minute
	// MaxFileBytes bounds a file sent to be transcribed (tmx transcribe FILE).
	MaxFileBytes = 512 << 20
	maxSource    = 200
)

// partLength: a WAV longer than 1.5 times this runs part by part (whisper-cli holds a whole
// file in memory). A variable for tests.
var partLength = 10 * time.Minute

// slotPatience: how long dictation (someone is waiting) waits for a heavy slot before it runs
// without one; it is short. A variable for tests.
var slotPatience = 20 * time.Second

// ErrNotSetUp: the engine or the chosen model is not downloaded (or there is no engine for
// this platform). The error's text says which, and where to fix it.
var ErrNotSetUp = errors.New("voice transcription isn't set up")

// ErrNotFound: no such transcript.
var ErrNotFound = errors.New("no such transcript")

// ErrInvalid marks a request that is wrong (a bad path, file or setting).
var ErrInvalid = errors.New("invalid")

// Releases is where the engine comes from: the updater (internal/update), which knows the
// release repository, its token and the release keys.
type Releases interface {
	ReleaseByTag(ctx context.Context, tag string) (update.Release, error)
	OpenAsset(ctx context.Context, url string, offset int64) (*http.Response, error)
	FetchSmall(ctx context.Context, a update.Asset, max int64) ([]byte, error)
	VerifySums(tag string, sums, sig []byte) (map[string]string, error)
	Explain(err error) string
	Repo() string
}

// Options configures a Service.
type Options struct {
	Paths config.Paths
	Store *store.Store
	// Settings returns config.json "voice"; Save writes it back.
	Settings func() config.VoiceConfig
	Save     func(config.VoiceConfig) error
	Releases Releases       // nil = the engine cannot be downloaded
	Slots    *slots.Manager // nil = jobs take no slot
	Publish  func(model.BusEvent)
	Logger   *slog.Logger
	// RecordingsDir: Enqueue only transcribes files inside it (the recorder's folder).
	RecordingsDir string
	Version       string // tmx's, for the User-Agent

	HTTP      *http.Client // model downloads (default: no overall timeout; stalls are cut off)
	ModelBase string       // default https://huggingface.co
	GOOS      string       // default runtime's (tests)
	GOARCH    string
	MemTotal  uint64 // bytes of RAM (default: measured; 0 = unknown)
	NumCPU    int    // default runtime.NumCPU()
	// NoLowPriority runs the engine at normal priority (tests).
	NoLowPriority bool
	// Hosts is an explicitly configured host API adapter. Nil keeps dictation local.
	Hosts     ConfiguredHosts
	Peer      VoicePeer
	PeerRetry time.Duration                // default 30 seconds
	DiskFree  func(string) (uint64, error) // tests; nil uses sysinfo.DiskSpace
}

// Service is the voice service. New, then Start; Close at the end.
type Service struct {
	o   Options
	log *slog.Logger

	mu          sync.Mutex
	peerFilesMu sync.Mutex // checkpoint reads/replacements/deletion, including Windows sharing rules
	queue       []*job
	running     *job
	wake        chan struct{}
	downloads   map[string]*Progress // what is downloading now: "engine" or a model name
	cancels     map[string]context.CancelFunc
	dlErrors    map[string]string
	needsToken  bool
	settingUp   bool // Setup is working through its downloads
	lastPub     time.Time
	pubTimer    *time.Timer
	closed      bool
	pubWG       sync.WaitGroup // publications and delayed callbacks admitted under mu

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates the service.
func New(o Options) *Service {
	if o.PeerRetry <= 0 {
		o.PeerRetry = 30 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Publish == nil {
		o.Publish = func(model.BusEvent) {}
	}
	if o.Settings == nil {
		o.Settings = func() config.VoiceConfig { return config.VoiceConfig{} }
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 30 * time.Second, TLSHandshakeTimeout: 15 * time.Second}}
	}
	if o.ModelBase == "" {
		o.ModelBase = "https://huggingface.co"
	}
	if o.GOOS == "" {
		o.GOOS, o.GOARCH = runtime.GOOS, runtime.GOARCH
	}
	if o.NumCPU <= 0 {
		o.NumCPU = runtime.NumCPU()
	}
	if o.MemTotal == 0 {
		if m, err := sysinfo.MachineStats(); err == nil {
			o.MemTotal = m.MemTotal
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{o: o, log: o.Logger, wake: make(chan struct{}, 1), downloads: map[string]*Progress{},
		cancels: map[string]context.CancelFunc{}, dlErrors: map[string]string{}, ctx: ctx, cancel: cancel}
}

// Start picks up what a restart cut off and starts the job worker.
func (s *Service) Start() error {
	os.RemoveAll(s.tmpDir()) // leftovers of jobs a crash cut off
	if err := os.MkdirAll(s.tmpDir(), 0o700); err != nil {
		return err
	}
	if s.o.Store != nil {
		un, err := s.o.Store.UnfinishedTranscripts()
		if err != nil {
			return err
		}
		ready, reason := s.readiness()
		for _, u := range un {
			t := u.Transcript
			if host, ok := strings.CutPrefix(t.Model, "peer:"); ok {
				// Destination is pinned to this job, not whatever settings say now.
				if config.ValidHostName(host) {
					s.push(&job{tid: t.ID, source: t.Source, audio: u.Audio, language: t.Language, peer: host, label: t.Source, report: strings.HasPrefix(t.Source, "upload:")})
					continue
				}
			}
			switch {
			case !ready:
				s.fail(t, "the daemon restarted before this was done, and "+lowerFirst(reason))
			case u.Audio == "" || !s.inRecordings(u.Audio):
				s.fail(t, "the daemon restarted before this was done, and its audio is gone")
			default:
				t.State, t.Error, t.UpdatedAt = model.TranscriptQueued, "", model.Now()
				if err := s.o.Store.PutTranscript(t, u.Audio); err == nil {
					s.push(&job{tid: t.ID, source: t.Source, audio: u.Audio, language: t.Language, prio: 1, label: t.Source})
				}
			}
		}
	}
	s.wg.Add(1)
	go s.worker()
	return nil
}

// Close stops the worker and the downloads (a job in progress is cut off; it runs again at the
// next start).
func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	s.closed = true
	if s.pubTimer != nil && s.pubTimer.Stop() {
		s.pubTimer = nil
		s.pubWG.Done()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.pubWG.Wait()
}

// ---- paths

func (s *Service) voiceDir() string  { return s.o.Paths.VoiceDir() }
func (s *Service) tmpDir() string    { return filepath.Join(s.voiceDir(), "tmp") }
func (s *Service) modelsDir() string { return filepath.Join(s.voiceDir(), "models") }
func (s *Service) engineDir() string { return filepath.Join(s.voiceDir(), "engine", EngineTag) }
func (s *Service) enginePath() string {
	return filepath.Join(s.engineDir(), "whisper-cli"+exeExt(s.o.GOOS))
}
func (s *Service) modelPath(m Model) string { return filepath.Join(s.modelsDir(), m.File) }

func (s *Service) platform() string { return s.o.GOOS + "/" + s.o.GOARCH }

func (s *Service) engineInstalled() bool {
	st, err := os.Stat(s.enginePath())
	return err == nil && st.Mode().IsRegular()
}

// modelInstalled: a file only gets its name once verified; the size check catches one that
// was cut short some other way.
func (s *Service) modelInstalled(m Model) bool {
	st, err := os.Stat(s.modelPath(m))
	return err == nil && st.Mode().IsRegular() && st.Size() == m.Size
}

// ---- settings

// Settings are the voice settings as they apply (GET /api/voice "settings").
type Settings struct {
	ViaPeer  string `json:"via_peer"`
	Model    string `json:"model"`
	Language string `json:"language"` // "auto" or a code
	// Threads the engine uses (Threads 0 in config = ThreadsAuto).
	Threads        int  `json:"threads"`
	ThreadsAuto    bool `json:"threads_auto"`
	VAD            bool `json:"vad"`
	AutoTranscribe bool `json:"auto_transcribe"`
}

func (s *Service) settings() Settings {
	c := s.o.Settings()
	out := Settings{Model: c.Model, Language: c.Language, Threads: c.Threads, VAD: c.VAD == nil || *c.VAD, AutoTranscribe: c.AutoTranscribeOn()}
	out.ViaPeer = c.ViaPeer
	if _, ok := FindModel(out.Model); !ok || out.Model == VADModel.Name {
		out.Model = DefaultModel
	}
	if out.Language == "" {
		out.Language = "auto"
	}
	if out.Threads <= 0 {
		out.ThreadsAuto = true
		out.Threads = min(4, max(1, s.o.NumCPU/2))
	}
	return out
}

// AutoTranscribe is config voice.auto_transcribe (default on): whether finished recordings are
// transcribed by themselves (internal/summarize does it, setting voice up first when needed).
func (s *Service) AutoTranscribe() bool { return s.settings().AutoTranscribe }

// SettingsPatch is PUT /api/voice; nil fields stay.
type SettingsPatch struct {
	ViaPeer        *string `json:"via_peer"`
	Model          *string `json:"model"`
	Language       *string `json:"language"`
	Threads        *int    `json:"threads"`
	VAD            *bool   `json:"vad"`
	AutoTranscribe *bool   `json:"auto_transcribe"`
}

var langRx = regexp.MustCompile(`^[a-z]{2,3}$`)

// Configure changes the settings (config.json "voice").
func (s *Service) Configure(p SettingsPatch) (Status, error) {
	c := s.o.Settings()
	if p.ViaPeer != nil {
		name := strings.ToLower(strings.TrimSpace(*p.ViaPeer))
		if !validatePeerName(name) {
			return s.Status(), fmt.Errorf("%w: choose a paired fleet machine name", ErrInvalid)
		}
		if name != "" {
			if s.o.Peer == nil {
				return s.Status(), fmt.Errorf("%w: no fleet peers configured", ErrInvalid)
			}
			if err := s.o.Peer.ValidateVoicePeer(name); err != nil {
				return s.Status(), err
			}
		}
		c.ViaPeer = name
	}
	if p.Model != nil {
		m, ok := FindModel(*p.Model)
		if !ok || m.VAD {
			return s.Status(), fmt.Errorf("%w: there is no model %q (%s)", ErrInvalid, *p.Model, strings.Join(modelNames(), ", "))
		}
		c.Model = m.Name
	}
	if p.Language != nil {
		l := strings.ToLower(strings.TrimSpace(*p.Language))
		if l == "" {
			l = "auto"
		}
		if l != "auto" && !langRx.MatchString(l) {
			return s.Status(), fmt.Errorf("%w: the language is \"auto\" or a code like \"de\" or \"en\"", ErrInvalid)
		}
		c.Language = l
	}
	if p.Threads != nil {
		if *p.Threads < 0 || *p.Threads > 64 {
			return s.Status(), fmt.Errorf("%w: threads is 0 (automatic) to 64", ErrInvalid)
		}
		c.Threads = *p.Threads
	}
	if p.VAD != nil {
		v := *p.VAD
		c.VAD = &v
	}
	if p.AutoTranscribe != nil {
		v := *p.AutoTranscribe
		c.AutoTranscribe = &v
	}
	if s.o.Save != nil {
		if err := s.o.Save(c); err != nil {
			return s.Status(), err
		}
	}
	s.publish(true)
	return s.Status(), nil
}

func modelNames() []string {
	var out []string
	for _, m := range Models {
		out = append(out, m.Name)
	}
	return out
}

// ---- status

// Progress of a download.
type Progress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

// EngineStatus is the whisper-cli part of Status.
type EngineStatus struct {
	Version     string    `json:"version"`   // the pinned whisper.cpp tag
	Available   bool      `json:"available"` // built for this platform
	Installed   bool      `json:"installed"`
	Size        int64     `json:"size,omitempty"`
	Downloading *Progress `json:"downloading,omitempty"`
	Error       string    `json:"error,omitempty"`
	NeedsToken  bool      `json:"needs_token,omitempty"` // the private release repository needs a GitHub token
}

// ModelStatus is one model of the catalog.
type ModelStatus struct {
	Name        string    `json:"name"`
	Label       string    `json:"label"`
	File        string    `json:"file"`
	Size        int64     `json:"size"`
	RAMMB       int       `json:"ram_mb"`
	Installed   bool      `json:"installed"`
	Default     bool      `json:"default,omitempty"`
	Selected    bool      `json:"selected,omitempty"`
	Warning     string    `json:"warning,omitempty"`
	Downloading *Progress `json:"downloading,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// JobStatus is the job running now.
type JobStatus struct {
	Source    string `json:"source"`          // what is transcribed ("dictation", "file", a recording)
	Phase     string `json:"phase"`           // waiting (for a heavy slot) | transcribing
	Position  int    `json:"position"`        // waiting: place in the slot's line
	Part      int    `json:"part,omitempty"`  // a long file: which part (1-based)
	Parts     int    `json:"parts,omitempty"` // and how many
	StartedAt int64  `json:"started_at"`
}

// Status is GET /api/voice and the data of the "voice.status" event.
type Status struct {
	Platform string        `json:"platform"`
	Ready    bool          `json:"ready"`
	Reason   string        `json:"reason,omitempty"` // not ready: why, and what to do
	Engine   EngineStatus  `json:"engine"`
	Models   []ModelStatus `json:"models"`
	VAD      ModelStatus   `json:"vad"`
	Settings Settings      `json:"settings"`
	Job      *JobStatus    `json:"job,omitempty"`
	Queued   int           `json:"queued"`
	MemoryMB int           `json:"memory_mb,omitempty"` // this machine's RAM
	Busy     bool          `json:"busy"`                // a download is running
}

// Status says what is installed, downloading and running.
func (s *Service) Status() Status {
	set := s.settings()
	ready, reason := s.readiness()
	st := Status{Platform: s.platform(), Ready: ready, Reason: reason, Settings: set, MemoryMB: int(s.o.MemTotal >> 20)}
	st.Engine = EngineStatus{Version: EngineTag, Available: engineAsset(s.o.GOOS, s.o.GOARCH) != "", Installed: s.engineInstalled()}
	if fi, err := os.Stat(s.enginePath()); err == nil {
		st.Engine.Size = fi.Size()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st.Engine.Downloading = copyProgress(s.downloads["engine"])
	st.Engine.Error = s.dlErrors["engine"]
	st.Engine.NeedsToken = s.needsToken
	small := s.o.MemTotal > 0 && s.o.MemTotal < 15<<30 // "16 GB" machines report a little less
	for _, m := range Models {
		ms := s.modelStatusLocked(m)
		ms.Default, ms.Selected = m.Name == DefaultModel, m.Name == set.Model
		if m.Heavy && small {
			ms.Warning = fmt.Sprintf("This machine has %d GB of memory: while it runs, this model can make it slow. %s is recommended here.",
				(s.o.MemTotal+(1<<29))>>30, Models[0].Label)
		}
		st.Models = append(st.Models, ms)
	}
	st.VAD = s.modelStatusLocked(VADModel)
	st.Busy = len(s.downloads) > 0 || s.settingUp
	st.Queued = len(s.queue)
	if j := s.running; j != nil {
		js := j.status
		st.Job = &js
	}
	return st
}

func (s *Service) modelStatusLocked(m Model) ModelStatus {
	return ModelStatus{Name: m.Name, Label: m.Label, File: m.File, Size: m.Size, RAMMB: m.RAMMB, Installed: s.modelInstalled(m),
		Downloading: copyProgress(s.downloads[m.Name]), Error: s.dlErrors[m.Name]}
}

func copyProgress(p *Progress) *Progress {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

// readiness: whether jobs can run, else why not (a sentence for the owner).
func (s *Service) readiness() (bool, string) {
	if s.ViaPeer() != "" {
		return true, ""
	} // queue is available even while the peer is offline
	if engineAsset(s.o.GOOS, s.o.GOARCH) == "" {
		return false, "Voice transcription is not available on this platform (" + s.platform() + ")."
	}
	if !s.engineInstalled() {
		return false, "The voice engine is not downloaded yet: set it up in Settings → Voice (tmx voice setup)."
	}
	m, _ := FindModel(s.settings().Model)
	if !s.modelInstalled(m) {
		return false, fmt.Sprintf("The %s model (%s) is not downloaded yet: download it in Settings → Voice (tmx voice get %s).", m.Label, m.Name, m.Name)
	}
	return true, ""
}

func (s *Service) notSetUp() error {
	_, reason := s.readiness()
	return fmt.Errorf("%w: %s", ErrNotSetUp, lowerFirst(reason))
}

// publish sends "voice.status"; without force at most four times a second (download progress).
func (s *Service) publish(force bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if !force && time.Since(s.lastPub) < 250*time.Millisecond {
		if s.pubTimer == nil {
			s.pubWG.Add(1)
			s.pubTimer = time.AfterFunc(300*time.Millisecond, func() {
				defer s.pubWG.Done()
				s.mu.Lock()
				s.pubTimer = nil
				s.mu.Unlock()
				s.publish(true)
			})
		}
		s.mu.Unlock()
		return
	}
	s.lastPub = time.Now()
	s.pubWG.Add(1)
	s.mu.Unlock()
	defer s.pubWG.Done()
	s.o.Publish(model.BusEvent{Type: model.EvVoiceStatus, Data: s.Status(), At: model.Now()})
}

// ---- downloads

// Setup downloads what is missing for the chosen model: the engine, the model and the voice
// activity model, in the background. Status follows the progress.
func (s *Service) Setup() (Status, error) {
	if s.ViaPeer() != "" {
		return s.Status(), nil
	}
	if engineAsset(s.o.GOOS, s.o.GOARCH) == "" {
		_, reason := s.readiness()
		return s.Status(), fmt.Errorf("%w: %s", ErrNotSetUp, lowerFirst(reason))
	}
	m, _ := FindModel(s.settings().Model)
	s.mu.Lock()
	if s.settingUp || s.ctx.Err() != nil {
		s.mu.Unlock()
		return s.Status(), nil
	}
	s.settingUp = true
	for _, k := range []string{"engine", m.Name, VADModel.Name} {
		delete(s.dlErrors, k)
	}
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			s.settingUp = false
			s.mu.Unlock()
			s.publish(true)
		}()
		if !s.engineInstalled() {
			if err := s.runDownload("engine", s.downloadEngine); err != nil {
				return
			}
		}
		if !s.modelInstalled(m) {
			if err := s.runDownload(m.Name, s.modelDownloader(m)); err != nil {
				return
			}
		}
		if !s.modelInstalled(VADModel) {
			_ = s.runDownload(VADModel.Name, s.modelDownloader(VADModel))
		}
	}()
	return s.Status(), nil
}

// GetModel downloads a model of the catalog (or the VAD model) in the background.
func (s *Service) GetModel(name string) (Status, error) {
	m, ok := FindModel(name)
	if !ok {
		return s.Status(), fmt.Errorf("%w: there is no model %q (%s, %s)", ErrInvalid, name, strings.Join(modelNames(), ", "), VADModel.Name)
	}
	if !s.modelInstalled(m) && s.ctx.Err() == nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			_ = s.runDownload(m.Name, s.modelDownloader(m))
		}()
	}
	return s.Status(), nil
}

// RemoveModel deletes a downloaded model (and stops its download).
func (s *Service) RemoveModel(name string) (Status, error) {
	m, ok := FindModel(name)
	if !ok {
		return s.Status(), fmt.Errorf("%w: there is no model %q", ErrInvalid, name)
	}
	s.mu.Lock()
	if c := s.cancels[m.Name]; c != nil {
		c()
	}
	delete(s.dlErrors, m.Name)
	s.mu.Unlock()
	for _, p := range []string{s.modelPath(m), s.modelPath(m) + partialSuffix} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return s.Status(), err
		}
	}
	s.log.Info("voice: model removed", "model", m.Name)
	s.publish(true)
	return s.Status(), nil
}

// runDownload runs one download (key "engine" or a model name), unless it is running already.
func (s *Service) runDownload(key string, fn func(ctx context.Context, progress func(done, total int64)) error) error {
	s.mu.Lock()
	if _, busy := s.downloads[key]; busy {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	s.downloads[key] = &Progress{}
	s.cancels[key] = cancel
	delete(s.dlErrors, key)
	if key == "engine" {
		s.needsToken = false
	}
	s.mu.Unlock()
	s.publish(true)
	s.log.Info("voice: download started", "what", key)

	err := fn(ctx, func(done, total int64) {
		s.mu.Lock()
		if p := s.downloads[key]; p != nil {
			p.Done, p.Total = done, total
		}
		s.mu.Unlock()
		s.publish(false)
	})

	s.mu.Lock()
	delete(s.downloads, key)
	delete(s.cancels, key)
	canceled := ctx.Err() != nil
	if err != nil && !canceled {
		s.dlErrors[key] = err.Error()
	}
	s.mu.Unlock()
	switch {
	case err == nil:
		s.log.Info("voice: downloaded and verified", "what", key)
	case canceled:
		s.log.Info("voice: download stopped", "what", key)
	default:
		s.log.Warn("voice: download failed", "what", key, "err", err)
	}
	s.publish(true)
	return err
}

func (s *Service) modelDownloader(m Model) func(context.Context, func(int64, int64)) error {
	return func(ctx context.Context, progress func(done, total int64)) error {
		ua := "tmx/" + s.o.Version
		return fetchVerified(ctx, httpOpener(s.o.HTTP, m.url(s.o.ModelBase), ua), s.modelPath(m), m.Size, m.SHA256,
			func(done int64) { progress(done, m.Size) })
	}
}

// maxEngine bounds the engine download.
const maxEngine = 200 << 20

// downloadEngine fetches whisper-cli from the release whisper-engine-<tag>: its SHA256SUMS
// must carry the release key's signature and list the file, whose hash must match. Then the
// binary must start and say it is the pinned whisper.cpp.
func (s *Service) downloadEngine(ctx context.Context, progress func(done, total int64)) error {
	name := engineAsset(s.o.GOOS, s.o.GOARCH)
	if name == "" {
		return errors.New("there is no voice engine for " + s.platform())
	}
	if s.o.Releases == nil {
		return errors.New("this tmx cannot download the voice engine (no release source)")
	}
	tag := EngineRelease()
	rel, err := s.o.Releases.ReleaseByTag(ctx, tag)
	switch {
	case errors.Is(err, update.ErrNoRelease):
		return fmt.Errorf("the voice engine (whisper.cpp %s) is not published yet: %s has no release %s. It comes with a later tmx build; try again then", EngineTag, s.o.Releases.Repo(), tag)
	case errors.Is(err, update.ErrNoAccess):
		s.mu.Lock()
		s.needsToken = true
		s.mu.Unlock()
		return errors.New(s.o.Releases.Explain(err))
	case err != nil:
		return fmt.Errorf("looking for the voice engine: %w", err)
	}
	sumsA, ok1 := rel.Find("SHA256SUMS")
	sigA, ok2 := rel.Find("SHA256SUMS.sig")
	if !ok1 || !ok2 {
		return fmt.Errorf("release %s is not signed (no SHA256SUMS.sig), so tmx does not use it", tag)
	}
	sums, err := s.o.Releases.FetchSmall(ctx, sumsA, 64<<10)
	if err != nil {
		return err
	}
	sig, err := s.o.Releases.FetchSmall(ctx, sigA, 16<<10)
	if err != nil {
		return err
	}
	list, err := s.o.Releases.VerifySums(tag, sums, sig)
	if err != nil {
		return fmt.Errorf("release %s: %w; tmx does not use it", tag, err)
	}
	want, ok := list[name]
	a, ok2 := rel.Find(name)
	if !ok || !ok2 {
		return fmt.Errorf("release %s has no voice engine for %s yet", tag, s.platform())
	}
	if a.Size <= 0 || a.Size > maxEngine {
		return fmt.Errorf("%s has an unexpected size (%d bytes)", name, a.Size)
	}
	dst := s.enginePath()
	open := func(ctx context.Context, off int64) (*http.Response, error) {
		return s.o.Releases.OpenAsset(ctx, a.URL, off)
	}
	if err := fetchVerified(ctx, open, dst, a.Size, want, func(done int64) { progress(done, a.Size) }); err != nil {
		return err
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		os.Remove(dst)
		return err
	}
	if err := probeEngine(ctx, dst); err != nil {
		os.Remove(dst)
		return fmt.Errorf("the downloaded voice engine was removed: %w", err)
	}
	// Engines of other whisper.cpp versions (before a pin change) are not used any more.
	if old, _ := filepath.Glob(filepath.Join(s.voiceDir(), "engine", "*")); len(old) > 0 {
		for _, d := range old {
			if filepath.Base(d) != EngineTag {
				os.RemoveAll(d)
			}
		}
	}
	return nil
}

// ---- jobs

// Result is a transcription that is not stored (dictation, a file).
type Result struct {
	Report   string                    `json:"report,omitempty"`
	Text     string                    `json:"text"`
	Language string                    `json:"language"`
	Segments []model.TranscriptSegment `json:"segments"`
}

type job struct {
	peer        string
	report      bool
	retryAt     time.Time
	peerRetries int
	tid         string // the stored transcript ("" = a direct job)
	source      string
	audio       string
	removeAudio bool // a temporary copy of ours
	language    string
	prio        int // 0 = someone is waiting for it (dictation), 1 = the rest
	label       string
	duration    time.Duration // when known (WAV)

	done    chan jobOut // direct jobs (buffered)
	waitCtx context.Context

	cancel  context.CancelFunc // set while it runs
	deleted bool               // its transcript was deleted meanwhile
	status  JobStatus
}

type jobOut struct {
	r   Result
	err error
}

func (s *Service) push(j *job) {
	s.mu.Lock()
	s.queue = append(s.queue, j)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	s.publish(true)
}

// next takes the next job: dictation first, otherwise in order.
func (s *Service) next() *job {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			best := -1
			for i, j := range s.queue {
				if j.retryAt.After(time.Now()) {
					continue
				}
				if best < 0 || j.prio < s.queue[best].prio {
					best = i
				}
			}
			if best < 0 {
				s.mu.Unlock()
				select {
				case <-s.ctx.Done():
					return nil
				case <-s.wake:
				case <-time.After(time.Second):
				}
				continue
			}
			j := s.queue[best]
			s.queue = slices.Delete(s.queue, best, best+1)
			s.running = j
			j.status = JobStatus{Source: j.label, Phase: "waiting", StartedAt: model.Now()}
			s.mu.Unlock()
			return j
		}
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return nil
		case <-s.wake:
		}
	}
}

func (s *Service) worker() {
	defer s.wg.Done()
	for {
		j := s.next()
		if j == nil {
			return
		}
		s.run(j)
		s.mu.Lock()
		s.running = nil
		s.mu.Unlock()
		s.publish(true)
	}
}

func (s *Service) run(j *job) {
	defer func() {
		s.mu.Lock()
		deleted := j.deleted
		s.mu.Unlock()
		if deleted && j.peer != "" {
			s.removePeerFiles(j.tid, strings.HasPrefix(j.source, "upload:"))
		}
	}()
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	if j.waitCtx != nil {
		stop := context.AfterFunc(j.waitCtx, cancel) // the caller went away
		defer stop()
	}
	s.mu.Lock()
	j.cancel = cancel
	deleted := j.deleted
	s.mu.Unlock()
	if j.removeAudio {
		defer os.Remove(j.audio)
	}
	if deleted {
		return
	}
	var t model.Transcript
	if j.tid != "" {
		var err error
		if t, err = s.o.Store.GetTranscript(j.tid); err != nil {
			return // deleted meanwhile
		}
		t.State, t.Error, t.UpdatedAt = model.TranscriptRunning, "", model.Now()
		if j.peer != "" {
			t.Error = ErrPending.Error()
		}
		s.save(j, t)
	}
	s.publish(true)

	r, modelName, err := s.transcribe(ctx, j)
	if j.done != nil {
		answerErr := err
		if j.peer != "" && errors.Is(err, ErrPending) {
			answerErr = PendingError{ID: j.tid}
		}
		j.done <- jobOut{r: r, err: answerErr}
		if j.peer != "" {
			j.done = nil
		}
	}
	if j.tid == "" {
		return
	}
	if s.ctx.Err() != nil {
		return // the daemon is stopping: the transcript stays running and runs again at the next start
	}
	if j.peer != "" && errors.Is(err, ErrPending) {
		// Only transient failures retry; permanent refusals keep the audio and fail.
		s.retryPeer(j, t, err)
		return
	}
	t.Model, t.UpdatedAt = modelName, model.Now()
	if err != nil {
		t.State, t.Error = model.TranscriptFailed, err.Error()
		s.log.Warn("voice: transcription failed", "source", t.Source, "err", err)
	} else {
		if strings.HasPrefix(t.Source, "recording:") { // a recording keeps only what was said (clean.go)
			r.Segments = CleanSegments(r.Segments)
			r.Text = joinText(r.Segments)
		}
		t.State, t.Error, t.Language, t.Text, t.Segments = model.TranscriptDone, "", r.Language, r.Text, r.Segments
		s.log.Info("voice: transcribed", "source", t.Source, "segments", len(r.Segments))
	}
	s.save(j, t)
}

// save stores and announces a transcript, unless it was deleted meanwhile.
func (s *Service) save(j *job, t model.Transcript) {
	s.mu.Lock()
	deleted := j.deleted
	s.mu.Unlock()
	if deleted {
		return
	}
	if err := s.o.Store.PutTranscript(t, j.audio); err != nil {
		s.log.Warn("voice: store transcript", "id", t.ID, "err", err)
		return
	}
	s.o.Publish(model.BusEvent{Type: model.EvTranscriptUpdated, Data: t, At: model.Now()})
}

func (s *Service) fail(t model.Transcript, why string) {
	t.State, t.Error, t.UpdatedAt = model.TranscriptFailed, why, model.Now()
	if err := s.o.Store.PutTranscript(t, ""); err == nil {
		s.o.Publish(model.BusEvent{Type: model.EvTranscriptUpdated, Data: t, At: model.Now()})
	}
}

// transcribe runs the engine on a job's audio (part by part for a long WAV), in a heavy slot.
func (s *Service) transcribe(ctx context.Context, j *job) (Result, string, error) {
	if j.peer != "" {
		return s.peerTranscribe(ctx, j)
	}
	set := s.settings()
	m, _ := FindModel(set.Model)
	if ready, _ := s.readiness(); !ready {
		return Result{}, m.Name, s.notSetUp()
	}
	lang := j.language
	if lang == "" {
		lang = set.Language
	}
	vad := ""
	if set.VAD && s.modelInstalled(VADModel) {
		vad = s.modelPath(VADModel)
	}

	release := s.takeSlot(ctx, j)
	defer release()
	if err := ctx.Err(); err != nil {
		return Result{}, m.Name, err
	}

	work, err := os.MkdirTemp(s.tmpDir(), "job-")
	if err != nil {
		return Result{}, m.Name, err
	}
	defer os.RemoveAll(work)
	parts := []part{{Path: j.audio}}
	dur := j.duration
	if w, err := readWAV(j.audio); err == nil {
		dur = w.Duration()
		if w.pcm16() && dur > partLength+partLength/2 {
			if parts, err = splitWAV(j.audio, w, partLength, work); err != nil {
				return Result{}, m.Name, fmt.Errorf("cutting the audio into parts: %w", err)
			}
		}
	}
	if dur <= 0 { // MP3, FLAC, Ogg: at least 4 KB a second (32 kbit/s), so this bounds it
		if st, err := os.Stat(j.audio); err == nil {
			dur = time.Duration(st.Size()/4000) * time.Second
		}
	}
	each := dur
	if len(parts) > 1 {
		each = partLength + partLength/2
	}

	r := Result{Segments: []model.TranscriptSegment{}}
	for i, p := range parts {
		s.mu.Lock()
		j.status.Phase, j.status.Position = "transcribing", 0
		if len(parts) > 1 {
			j.status.Part, j.status.Parts = i+1, len(parts)
		}
		s.mu.Unlock()
		s.publish(true)
		out, err := runEngine(ctx, runSpec{Engine: s.enginePath(), Model: s.modelPath(m), VAD: vad, Input: p.Path,
			Language: lang, Threads: set.Threads, Timeout: timeoutFor(each), LowPriority: !s.o.NoLowPriority, TmpDir: work})
		if err != nil {
			if len(parts) > 1 {
				err = fmt.Errorf("part %d of %d: %w", i+1, len(parts), err)
			}
			return Result{}, m.Name, err
		}
		if r.Language == "" {
			r.Language = out.Language
		}
		off := p.Offset.Milliseconds()
		for _, seg := range out.Segments {
			seg.FromMS += off
			seg.ToMS += off
			r.Segments = append(r.Segments, seg)
		}
	}
	if r.Language == "" && lang != "auto" {
		r.Language = lang
	}
	r.Text = joinText(r.Segments)
	return r, m.Name, nil
}

// takeSlot waits for a heavy slot and holds it (renewed) until release is called. Dictation
// waits at most slotPatience, then runs without one. Without slots, or if the slot cannot be
// had (the kind is not configured), the job runs anyway.
func (s *Service) takeSlot(ctx context.Context, j *job) (release func()) {
	none := func() {}
	if s.o.Slots == nil {
		return none
	}
	h := slots.Holder{} // the owner: the daemon works for them
	l, err := s.o.Slots.Acquire("heavy", h, "voice transcription: "+j.label, os.Getpid())
	if err != nil {
		s.log.Info("voice: running without a heavy slot", "err", err)
		return none
	}
	start := time.Now()
	for l.State != slots.Held {
		s.mu.Lock()
		j.status.Phase, j.status.Position = "waiting", l.Position
		s.mu.Unlock()
		s.publish(true)
		wait := 10 * time.Second
		if j.prio == 0 {
			left := slotPatience - time.Since(start)
			if left <= 0 {
				_ = s.o.Slots.Release(l.ID, h)
				s.log.Info("voice: dictation runs without waiting longer for a heavy slot")
				return none
			}
			wait = min(wait, left)
		}
		if l, err = s.o.Slots.Wait(ctx, l.ID, h, wait); err != nil {
			return none // the lease lapsed
		}
		if ctx.Err() != nil {
			_ = s.o.Slots.Release(l.ID, h)
			return none
		}
	}
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_, _ = s.o.Slots.Renew(l.ID, h)
			}
		}
	}()
	return func() {
		close(stop)
		_ = s.o.Slots.Release(l.ID, h)
	}
}

// Enqueue transcribes a recording's audio in the background: wavPath is a 16 kHz mono PCM16
// WAV inside the recordings folder, source names it ("recording:<id>/<segment>"), language ""
// = the setting. It returns at once (an error when voice is not set up or the file is not
// acceptable); the transcript is stored and announced ("transcript.updated"). A source that
// is queued or running already is left alone; a finished one runs again.
func (s *Service) Enqueue(source, wavPath, language string) error {
	source = strings.TrimSpace(source)
	if source == "" || len(source) > maxSource {
		return fmt.Errorf("%w: a source of 1 to %d characters", ErrInvalid, maxSource)
	}
	language = strings.ToLower(strings.TrimSpace(language))
	if language != "" && language != "auto" && !langRx.MatchString(language) {
		return fmt.Errorf("%w: the language is \"auto\" or a code like \"de\"", ErrInvalid)
	}
	if !s.inRecordings(wavPath) {
		return fmt.Errorf("%w: only files in the recordings folder are transcribed", ErrInvalid)
	}
	w, err := readWAV(wavPath)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalid, filepath.Base(wavPath), err)
	}
	if ready, _ := s.readiness(); !ready {
		return s.notSetUp()
	}
	if s.o.Store == nil {
		return errors.New("voice transcripts are not stored on this daemon")
	}
	now := model.Now()
	t, err := s.o.Store.TranscriptBySource(source)
	switch {
	case errors.Is(err, store.ErrNotFound):
		t = model.Transcript{ID: model.NewID(), Source: source, CreatedAt: now}
	case err != nil:
		return err
	case t.State == model.TranscriptQueued || t.State == model.TranscriptRunning:
		return nil
	}
	// An explicit re-transcription of a completed source gets a fresh remote
	// receipt; an unfinished source above keeps its checkpoint for resume.
	if s.ViaPeer() != "" {
		s.removePeerFiles(t.ID, false)
	}
	t.State, t.Error, t.Language, t.Text, t.Segments, t.UpdatedAt = model.TranscriptQueued, "", language, "", nil, now
	t.Model = s.settings().Model
	if s.ViaPeer() != "" {
		t.Model = "peer:" + s.ViaPeer()
		t.Error = ErrPending.Error()
	}
	if err := s.o.Store.PutTranscript(t, wavPath); err != nil {
		return err
	}
	s.o.Publish(model.BusEvent{Type: model.EvTranscriptUpdated, Data: t, At: now})
	s.push(&job{tid: t.ID, source: source, audio: wavPath, language: language, prio: 1, label: source, duration: w.Duration(), peer: s.ViaPeer()})
	return nil
}

// inRecordings reports whether p is a file inside the recordings folder (symlinks resolved).
func (s *Service) inRecordings(p string) bool {
	if s.o.RecordingsDir == "" || p == "" || !filepath.IsAbs(p) {
		return false
	}
	dir, err := filepath.EvalSymlinks(s.o.RecordingsDir)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, real)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return false
	}
	st, err := os.Stat(real)
	return err == nil && st.Mode().IsRegular()
}

// Dictate transcribes a short WAV (at most MaxDictation) and returns the text; nothing is
// stored. A configured fallback can handle a busy/unavailable local engine; otherwise
// it waits for its turn (dictation goes first) until ctx ends.
func (s *Service) Dictate(ctx context.Context, wav []byte, language string) (Result, error) {
	return s.DictateWithHosts(ctx, wav, language, s.o.Hosts)
}

// DictateWithHosts supplies a request-scoped configured-host adapter. Nil forces local
// execution, including for forwarded requests, so a fallback cannot loop between hosts.
func (s *Service) DictateWithHosts(ctx context.Context, wav []byte, language string, hosts ConfiguredHosts) (Result, error) {
	w, err := parseWAV(bytes.NewReader(wav), int64(len(wav)))
	if err != nil {
		return Result{}, fmt.Errorf("%w: the recording is %v", ErrInvalid, err)
	}
	if d := w.Duration(); d > MaxDictation+5*time.Second {
		return Result{}, fmt.Errorf("%w: dictation is at most %s (this is %s)", ErrInvalid, MaxDictation, d.Round(time.Second))
	}
	language = strings.ToLower(strings.TrimSpace(language))
	if language != "" && language != "auto" && !langRx.MatchString(language) {
		return Result{}, fmt.Errorf("%w: invalid language", ErrInvalid)
	}
	if silentPCM(wav, w) {
		return Result{}, ErrNoSpeech
	}
	if s.ViaPeer() != "" {
		return s.spoolPeer(ctx, bytes.NewReader(wav), "wav", language)
	}
	if res, used, err := s.dictationFallback(ctx, wav, w, language, hosts); used {
		return res, err
	}
	if ready, _ := s.readiness(); !ready {
		return Result{}, s.notSetUp()
	}
	if err := os.MkdirAll(s.tmpDir(), 0o700); err != nil {
		return Result{}, err
	}
	f, err := os.CreateTemp(s.tmpDir(), "dictation-*.wav")
	if err != nil {
		return Result{}, err
	}
	_, err = f.Write(wav)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return Result{}, err
	}
	res, err := s.direct(ctx, &job{source: "dictation", audio: f.Name(), removeAudio: true, language: language, prio: 0, label: "dictation", duration: w.Duration()})
	if err == nil && strings.TrimSpace(res.Text) == "" {
		return Result{}, ErrNoSpeech
	}
	return res, err
}

// TranscribeFile transcribes a WAV, MP3, FLAC or Ogg file read from r (at most MaxFileBytes);
// nothing is stored. It waits for its turn until ctx ends.
func (s *Service) TranscribeFile(ctx context.Context, r io.Reader, language string) (Result, error) {
	if ready, _ := s.readiness(); !ready {
		return Result{}, s.notSetUp()
	}
	head := make([]byte, 12)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return Result{}, fmt.Errorf("%w: the file is empty", ErrInvalid)
	}
	head = head[:n]
	kind := audioKind(head)
	if kind == "" {
		return Result{}, fmt.Errorf("%w: that is not a WAV, MP3, FLAC or Ogg file", ErrInvalid)
	}
	if s.ViaPeer() != "" {
		return s.spoolPeer(ctx, io.MultiReader(bytes.NewReader(head), r), kind, language)
	}
	if err := os.MkdirAll(s.tmpDir(), 0o700); err != nil {
		return Result{}, err
	}
	f, err := os.CreateTemp(s.tmpDir(), "file-*."+kind)
	if err != nil {
		return Result{}, err
	}
	_, err = f.Write(head)
	var copied int64
	if err == nil {
		copied, err = io.Copy(f, io.LimitReader(r, MaxFileBytes-int64(n)+1))
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && copied+int64(n) > MaxFileBytes {
		err = fmt.Errorf("%w: the file is larger than %s", ErrInvalid, mb(MaxFileBytes))
	}
	if err != nil {
		os.Remove(f.Name())
		return Result{}, err
	}
	return s.direct(ctx, &job{source: "file", audio: f.Name(), removeAudio: true, language: language, prio: 1, label: "a file"})
}

// direct queues a job somebody waits for and returns its result.
func (s *Service) direct(ctx context.Context, j *job) (Result, error) {
	j.language = strings.ToLower(strings.TrimSpace(j.language))
	if j.language != "" && j.language != "auto" && !langRx.MatchString(j.language) {
		os.Remove(j.audio)
		return Result{}, fmt.Errorf("%w: the language is \"auto\" or a code like \"de\"", ErrInvalid)
	}
	j.done, j.waitCtx = make(chan jobOut, 1), ctx
	s.push(j)
	select {
	case out := <-j.done:
		return out.r, out.err
	case <-ctx.Done():
		s.drop(j)
		return Result{}, ctx.Err()
	case <-s.ctx.Done():
		return Result{}, errors.New("the daemon is stopping")
	}
}

// drop takes a job out of the queue, or stops it when it runs.
func (s *Service) drop(j *job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j.deleted = true
	if i := slices.Index(s.queue, j); i >= 0 {
		s.queue = slices.Delete(s.queue, i, i+1)
		if j.removeAudio {
			os.Remove(j.audio)
		}
		return
	}
	if j.cancel != nil {
		j.cancel()
	}
}

// ---- stored transcripts

// List returns stored transcripts (see store.TranscriptQuery).
func (s *Service) List(q store.TranscriptQuery) ([]model.Transcript, error) {
	if s.o.Store == nil {
		return []model.Transcript{}, nil
	}
	list, err := s.o.Store.ListTranscripts(q)
	for i := range list {
		s.fillPeerReport(&list[i])
	}
	return list, err
}

// Get returns one stored transcript.
func (s *Service) Get(id string) (model.Transcript, error) {
	if s.o.Store == nil {
		return model.Transcript{}, ErrNotFound
	}
	t, err := s.o.Store.GetTranscript(id)
	if errors.Is(err, store.ErrNotFound) {
		return t, ErrNotFound
	}
	if err == nil {
		s.fillPeerReport(&t)
	}
	return t, err
}

// EvTranscriptRemoved is announced when a transcript is deleted (data: {"id"}).
const EvTranscriptRemoved = "transcript.removed"

// Delete removes a stored transcript (a job for it is stopped). The audio stays: it belongs
// to the recording.
func (s *Service) Delete(id string) error {
	if s.o.Store == nil {
		return ErrNotFound
	}
	t, _ := s.o.Store.GetTranscript(id)
	s.mu.Lock()
	for _, j := range s.queue {
		if j.tid == id {
			j.deleted = true
		}
	}
	s.queue = slices.DeleteFunc(s.queue, func(j *job) bool { return j.tid == id })
	if j := s.running; j != nil && j.tid == id {
		j.deleted = true
		if j.cancel != nil {
			j.cancel()
		}
	}
	s.mu.Unlock()
	if err := s.o.Store.DeleteTranscript(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if strings.HasPrefix(t.Model, "peer:") {
		s.removePeerFiles(id, strings.HasPrefix(t.Source, "upload:"))
	}
	s.o.Publish(model.BusEvent{Type: EvTranscriptRemoved, Data: map[string]string{"id": id}, At: model.Now()})
	s.publish(true)
	return nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
