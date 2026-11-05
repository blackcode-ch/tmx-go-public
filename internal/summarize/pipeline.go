package summarize

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

// The watcher moves summaries from "transcribing" to "queued": it queues the segments that have
// no transcript yet (setting voice up first when it is missing and allowed), and lets a summary
// go once every transcript is done or failed. It runs on events and every CheckEvery.

func (s *Service) watch() {
	defer s.wg.Done()
	t := time.NewTicker(s.o.CheckEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wakeWatch:
		case <-t.C:
		}
		s.check()
	}
}

// check looks at every summary waiting for transcripts, and at the recordings waiting for voice.
func (s *Service) check() {
	waiting, err := s.o.Store.SummariesIn(model.SummaryTranscribing)
	if err != nil {
		s.log.Warn("summaries: read", "err", err)
		return
	}
	for _, x := range waiting {
		if s.ctx.Err() != nil {
			return
		}
		s.advance(x)
	}
	s.mu.Lock()
	ids := make([]string, 0, len(s.voiceWait))
	for id := range s.voiceWait {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		r, err := s.o.Recordings.Get(id)
		if err == nil {
			if p := s.transcription(r, true); p.settingUp {
				continue
			} else if p.note != "" {
				s.log.Info("summaries: automatic transcription", "id", id, "note", p.note)
			}
		}
		s.mu.Lock()
		delete(s.voiceWait, id)
		s.mu.Unlock()
	}
}

// advance moves one waiting summary on.
func (s *Service) advance(x model.RecordingSummary) {
	r, err := s.o.Recordings.Get(x.RecordingID)
	if fleet.KindOf(err) == fleet.KindNotFound {
		_ = s.o.Store.DeleteSummary(x.RecordingID)
		return
	}
	if err != nil {
		s.log.Warn("summaries: read recording", "id", x.RecordingID, "err", err)
		return
	}
	may := x.Requested || (s.o.Voice != nil && s.o.Voice.AutoTranscribe())
	p := s.transcription(r, may)
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.o.Store.GetSummary(x.RecordingID) // it may have changed meanwhile
	if err != nil || cur.State != model.SummaryTranscribing {
		return
	}
	switch {
	case p.pending() && cur.Note == p.note:
		return
	case p.pending():
		cur.Note = p.note
	default:
		cur.State, cur.Note = model.SummaryQueued, p.note
	}
	cur.UpdatedAt = s.now()
	if err := s.saveLocked(cur); err != nil {
		s.log.Warn("summaries: save", "id", cur.RecordingID, "err", err)
	}
	if cur.State == model.SummaryQueued {
		s.kickRun()
	}
}

// progress of a recording's transcription.
type progress struct {
	waiting   int    // transcripts queued or running
	settingUp bool   // voice is being set up; the missing segments are queued after
	note      string // what will not be transcribed, or what happens now
}

func (p progress) pending() bool { return p.waiting > 0 || p.settingUp }

// SettingUpNote is a waiting summary's note while voice downloads.
const SettingUpNote = "Setting up voice transcription (the engine and the default model, about 60 MB)…"

// wavSegments are the segments with audio to transcribe.
func wavSegments(r model.Recording) []model.RecordingSegment {
	var out []model.RecordingSegment
	for _, g := range r.Segments {
		if g.WAV != "" && g.WAVBytes > 44 {
			out = append(out, g)
		}
	}
	return out
}

// transcription queues the segments of r without a transcript when may (voice set up first if
// it is missing), and says what is still running.
func (s *Service) transcription(r model.Recording, may bool) progress {
	var p progress
	var missing []model.RecordingSegment
	for _, g := range wavSegments(r) {
		t, err := s.o.Store.TranscriptBySource(recorder.Source(r.ID, g.N))
		switch {
		case errors.Is(err, store.ErrNotFound):
			missing = append(missing, g)
		case err != nil:
			p.waiting++ // the database is busy: look again later
		case t.State == model.TranscriptQueued || t.State == model.TranscriptRunning:
			p.waiting++
		}
	}
	if len(missing) == 0 {
		return p
	}
	parts := plural(len(missing), "segment")
	switch {
	case s.o.Voice == nil:
		p.note = fmt.Sprintf("%s not transcribed: transcription isn't available on this daemon.", parts)
		return p
	case !may:
		p.note = fmt.Sprintf("%s not transcribed: transcription is not automatic (Settings → Voice).", parts)
		return p
	}
	st := s.o.Voice.Status()
	switch {
	case st.Ready:
		var errs []string
		for _, g := range missing {
			if err := s.o.Voice.Enqueue(recorder.Source(r.ID, g.N), g.WAV, ""); err != nil {
				errs = append(errs, fmt.Sprintf("segment %d: %v", g.N, err))
				continue
			}
			p.waiting++
		}
		if len(errs) > 0 {
			p.note = "Not transcribed: " + strings.Join(errs, "; ") + "."
		}
		return p
	case !st.Engine.Available:
		p.note = fmt.Sprintf("%s not transcribed: %s", parts, st.Reason)
		return p
	case st.Busy:
		p.settingUp, p.note = true, SettingUpNote
		return p
	}
	s.mu.Lock()
	tried := !s.setupAt.IsZero()
	if !tried {
		s.setupAt = s.o.Now()
	}
	s.mu.Unlock()
	if tried { // set up since the last new recording, and still not ready: it failed
		p.note = fmt.Sprintf("%s not transcribed: voice could not be set up (%s).", parts, setupError(st))
		return p
	}
	if _, err := s.o.Voice.Setup(); err != nil {
		p.note = fmt.Sprintf("%s not transcribed: voice could not be set up (%v).", parts, err)
		return p
	}
	s.log.Info("summaries: setting voice up for a recording", "id", r.ID)
	p.settingUp, p.note = true, SettingUpNote
	return p
}

// setupError is why voice is not ready after a setup.
func setupError(st transcribe.Status) string {
	if st.Engine.Error != "" {
		return strings.TrimSuffix(st.Engine.Error, ".")
	}
	for _, m := range st.Models {
		if m.Selected && m.Error != "" {
			return strings.TrimSuffix(m.Error, ".")
		}
	}
	if st.Reason != "" {
		return strings.TrimSuffix(lowerFirst(st.Reason), ".")
	}
	return "unknown reason"
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// ---- the runner: one summary at a time

func (s *Service) runner() {
	defer s.wg.Done()
	for {
		x, ok := s.next()
		if !ok {
			select {
			case <-s.ctx.Done():
				return
			case <-s.wakeRun:
			case <-time.After(s.o.CheckEvery):
			}
			continue
		}
		s.runOne(x)
		if s.ctx.Err() != nil {
			return
		}
	}
}

// next takes the summary that has waited longest and marks it running.
func (s *Service) next() (model.RecordingSummary, bool) {
	if s.ctx.Err() != nil {
		return model.RecordingSummary{}, false
	}
	queued, err := s.o.Store.SummariesIn(model.SummaryQueued)
	if err != nil {
		s.log.Warn("summaries: read", "err", err)
		return model.RecordingSummary{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range queued {
		x, err := s.o.Store.GetSummary(q.RecordingID)
		if err != nil || x.State != model.SummaryQueued {
			continue
		}
		now := s.now()
		x.State, x.StartedAt, x.UpdatedAt = model.SummaryRunning, now, now
		if err := s.saveLocked(x); err != nil {
			s.log.Warn("summaries: save", "id", x.RecordingID, "err", err)
			continue
		}
		s.running = x.RecordingID
		return x, true
	}
	return model.RecordingSummary{}, false
}

// runOne writes one summary and stores the outcome.
func (s *Service) runOne(x model.RecordingSummary) {
	out := s.make(s.ctx, x)
	s.mu.Lock()
	defer s.mu.Unlock()
	again := s.again[x.RecordingID]
	delete(s.again, x.RecordingID)
	s.running = ""
	if s.ctx.Err() != nil {
		return // the daemon stops: it stays running and is queued again at the next start
	}
	cur, err := s.o.Store.GetSummary(x.RecordingID)
	if err != nil {
		return // the recording was deleted meanwhile
	}
	now := s.now()
	cur.FinishedAt, cur.UpdatedAt = now, now
	switch out.state {
	case model.SummaryDone:
		cur.State, cur.Error, cur.Note = model.SummaryDone, "", out.note
		cur.Model, cur.Summary, cur.KeyPoints, cur.Decisions = out.model, out.sum.Summary, out.sum.KeyPoints, out.sum.Decisions
		cur.ActionItems, cur.Chapters = out.sum.ActionItems, out.sum.Chapters
		cur.Transcripts, cur.Frames, cur.Parts = out.transcripts, out.frames, out.parts
		s.log.Info("summaries: written", "id", cur.RecordingID, "model", out.model, "frames", out.frames, "parts", out.parts)
	case model.SummarySkipped:
		cur.State, cur.Error, cur.Note = model.SummarySkipped, out.err, out.note
		cur.Model, cur.Summary, cur.KeyPoints, cur.Decisions, cur.ActionItems, cur.Chapters = "", "", nil, nil, nil, nil
		cur.Transcripts, cur.Frames, cur.Parts = 0, 0, 0
	default: // failed: an earlier summary stays, with the error
		cur.State, cur.Error, cur.Note = model.SummaryFailed, out.err, out.note
		s.log.Warn("summaries: failed", "id", cur.RecordingID, "err", out.err)
	}
	if again {
		cur.State = model.SummaryTranscribing
	} else if why, ok := s.madeHere[cur.RecordingID]; ok { // which path made it, so a broken worker is seen
		delete(s.madeHere, cur.RecordingID)
		cur.Note = strings.TrimSpace("Made by the daemon: " + why + ". " + cur.Note)
	}
	if err := s.saveLocked(cur); err != nil {
		s.log.Warn("summaries: save", "id", cur.RecordingID, "err", err)
	}
	if again {
		s.kickWatch()
	}
}

// outcome of one summary.
type outcome struct {
	state                      model.SummaryState
	sum                        model.RecordingSummary // Summary and the lists
	model                      string
	err, note                  string
	transcripts, frames, parts int
}

// make gathers the recording's transcripts and frames and has Claude write the summary.
func (s *Service) make(ctx context.Context, x model.RecordingSummary) outcome {
	r, err := s.o.Recordings.Get(x.RecordingID)
	if err != nil {
		return outcome{state: model.SummaryFailed, err: err.Error()}
	}
	in := s.collect(r)
	notes := in.notes
	if x.Note == "" || x.Note == SettingUpNote {
		notes = append(notes, in.missing) // the watcher's note says why, when it has one
	}
	note := joinNotes(x.Note, notes)
	if in.empty() {
		return outcome{state: model.SummarySkipped, err: in.nothingSaid(r), note: note}
	}
	m := s.model()
	s.takeWrote("")
	sum, parts, err := s.write(ctx, r, in, m)
	m = s.takeWrote(m) // the local model, for a clinic recording or summaries.backend "local"
	if err != nil {
		return outcome{state: model.SummaryFailed, err: err.Error(), note: note}
	}
	sum.Chapters = place(sum.Chapters, r)
	return outcome{state: model.SummaryDone, sum: sum, model: m, note: note,
		transcripts: in.transcripts, frames: len(in.frames), parts: parts}
}

// joinNotes adds the notes of collecting to the watcher's (without repeating one).
func joinNotes(first string, more []string) string {
	var out []string
	if first != "" && first != SettingUpNote {
		out = append(out, first)
	}
	for _, n := range more {
		if n != "" && !strings.Contains(first, n) {
			out = append(out, n)
		}
	}
	return strings.Join(out, " ")
}
