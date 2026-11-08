package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

type ProjectImportRequest struct {
	ID                string                `json:"id"`
	Source            string                `json:"source"`
	ExpectedUpdatedAt *int64                `json:"expected_updated_at,omitempty"`
	Section           *ProjectSectionImport `json:"section,omitempty"`
	Cards             []ProjectCardImport   `json:"cards,omitempty"`
	// Original retains a source export including fields the normalized import does not interpret.
	Original json.RawMessage `json:"original,omitempty"`
}

// UnmarshalJSON also reads each card's owner-only flag under model.LegacyOwnerOnlyKeys. It sits
// here, not on ProjectCardRequest: ProjectCardImport embeds that type, and a promoted
// UnmarshalJSON would decode only the embedded part and drop asker, status, answer and the rest.
func (r *ProjectImportRequest) UnmarshalJSON(b []byte) error {
	type plain ProjectImportRequest
	if err := json.Unmarshal(b, (*plain)(r)); err != nil {
		return err
	}
	if len(model.LegacyOwnerOnlyKeys) == 0 {
		return nil
	}
	var raw struct {
		Cards []json.RawMessage `json:"cards"`
	}
	if json.Unmarshal(b, &raw) != nil || len(raw.Cards) != len(r.Cards) {
		return nil
	}
	for i, c := range raw.Cards {
		if model.LegacyOwnerOnly(c) {
			r.Cards[i].OwnerOnly = true
		}
	}
	return nil
}

type ProjectSectionImport struct {
	Status      string                `json:"status"`
	Needs       []model.ProjectNeed   `json:"needs"`
	Links       []model.ProjectLink   `json:"links,omitempty"`
	FinalOutput []model.ProjectOutput `json:"final_output,omitempty"`
	PreviewDir  string                `json:"preview_dir,omitempty"`
	UpdatedAt   int64                 `json:"updated_at"` // Unix milliseconds
	UpdatedBy   string                `json:"updated_by"`
}

type ProjectCardImport struct {
	ProjectCardRequest
	Asker     string `json:"asker,omitempty"`
	Status    string `json:"status,omitempty"` // open | later | answered | closed
	Answer    string `json:"answer,omitempty"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt int64  `json:"created_at"`
	ClosedAt  int64  `json:"closed_at,omitempty"`
}

func importKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func importLabel(s string) bool {
	return s != "" && len(s) <= 160 && !strings.ContainsAny(s, "\x00\r\n")
}

// ImportProject imports documentation and question cards, never runnable permissions,
// tool approvals, owner grants, or outbound messages.
func (f *Fleet) ImportProject(c Caller, project string, req ProjectImportRequest) (model.ProjectImportReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var empty model.ProjectImportReceipt
	p, err := f.projectLocked(project)
	if err != nil {
		return empty, err
	}
	if _, err := f.projectWriterLocked(c, p); err != nil {
		return empty, err
	}
	if !importLabel(req.ID) || !importLabel(req.Source) {
		return empty, errf(KindInvalid, "source and id must be nonempty single-line identifiers of at most 160 bytes")
	}
	data, err := json.Marshal(req)
	if err != nil {
		return empty, errf(KindInvalid, "invalid import JSON: %v", err)
	}
	if len(data) > 2<<20 {
		return empty, errf(KindInvalid, "import exceeds the API body limit")
	}
	hash := importKey(string(data))
	if old, err := f.st.ProjectImport(p.ID, req.Source, req.ID); err == nil {
		if old.SHA256 != hash {
			return empty, errf(KindConflict, "import id already used for different content")
		}
		return old, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return empty, err
	}
	if req.Section == nil && len(req.Cards) == 0 {
		return empty, errf(KindInvalid, "import contains no section or cards")
	}
	now := model.Now()
	origin := model.ImportOrigin{Source: req.Source, ID: req.ID, ImportedBy: c.ID(), ImportedAt: now}
	rec := model.ProjectImportReceipt{ProjectID: p.ID, Source: req.Source, ID: req.ID, SHA256: hash, ImportedBy: c.ID(), ImportedAt: now, CardIDs: []string{}, Snapshot: data}
	var sec *model.ProjectSection
	if req.Section != nil {
		in := req.Section
		status := strings.NewReplacer("\r\n", "\n", "\r", "\n", "\v", "\n", "\f", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n").Replace(in.Status)
		if len(strings.Split(status, "\n")) > 3 || utf8.RuneCountInString(status) > 600 {
			return empty, errf(KindInvalid, "status must be at most three lines and 600 characters")
		}
		if in.UpdatedAt <= 0 || !importLabel(in.UpdatedBy) {
			return empty, errf(KindInvalid, "section needs source updated_at (Unix milliseconds) and updated_by")
		}
		if len(in.Needs) > 20 || len(in.Links) > 20 {
			return empty, errf(KindInvalid, "section has more than 20 needs or links")
		}
		for _, need := range in.Needs {
			if strings.TrimSpace(need.Text) == "" || utf8.RuneCountInString(need.Text) > 300 {
				return empty, errf(KindInvalid, "need text is empty or exceeds 300 characters")
			}
		}
		for _, link := range in.Links {
			if len(link.Title) > 300 || len(link.URL) > 4096 || strings.ContainsAny(link.URL, "\x00\r\n") {
				return empty, errf(KindInvalid, "invalid imported link")
			}
		}
		if len(in.PreviewDir) > 4096 || strings.ContainsAny(in.PreviewDir, "\x00\r\n") {
			return empty, errf(KindInvalid, "invalid preview_dir")
		}
		outputs, err := validateProjectOutputs(in.FinalOutput)
		if err != nil {
			return empty, err
		}
		current, err := f.st.ProjectSection(p.ID)
		if err == nil {
			if req.ExpectedUpdatedAt == nil || current.UpdatedAt != *req.ExpectedUpdatedAt {
				return empty, errf(KindConflict, "section exists or changed; supply its current expected_updated_at")
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return empty, err
		} else if req.ExpectedUpdatedAt != nil {
			return empty, errf(KindConflict, "section no longer exists")
		}
		sectionOrigin := origin
		sectionOrigin.SourceAuthor = in.UpdatedBy
		sec = &model.ProjectSection{ProjectID: p.ID, Name: p.Name, Slug: p.Slug, Status: status, Needs: in.Needs, Links: in.Links, FinalOutput: outputs, PreviewDir: in.PreviewDir, UpdatedAt: in.UpdatedAt, UpdatedBy: in.UpdatedBy, Origin: &sectionOrigin, Lead: current.Lead}
	}
	cards := []model.Approval{}
	seen := map[string]bool{}
	for _, in := range req.Cards {
		if !importLabel(in.ID) || seen[in.ID] {
			return empty, errf(KindInvalid, "card IDs must be nonempty and unique within the source")
		}
		seen[in.ID] = true
		card, err := prepareOwnerCard(&in.AskRequest)
		if err != nil {
			return empty, err
		}
		if strings.TrimSpace(in.Question) == "" || len(in.Question) > MaxBodyBytes || len(in.Answer) > MaxBodyBytes || len(in.Reason) > MaxBodyBytes {
			return empty, errf(KindInvalid, "card question is empty or card text is too long")
		}
		if in.CreatedAt <= 0 || in.Priority < 0 || (in.CloseWhen != "" && !projectCardCloseWhen.MatchString(in.CloseWhen)) {
			return empty, errf(KindInvalid, "invalid card date, priority or close_when")
		}
		state := in.Status
		if state == "" {
			state = "open"
		}
		if state != "open" && state != "later" && state != "answered" && state != "closed" {
			return empty, errf(KindInvalid, "card status must be open, later, answered or closed")
		}
		if (state == "open" || state == "later") && (in.Answer != "" || in.ClosedAt != 0) {
			return empty, errf(KindInvalid, "open cards cannot carry a historical answer or closure")
		}
		id := "import-" + importKey(p.ID, req.Source, in.ID)[:24]
		if _, err := f.st.GetApproval(id); err == nil {
			return empty, errf(KindConflict, "source card already imported; use the original import id to retry")
		} else if !errors.Is(err, store.ErrNotFound) {
			return empty, err
		}
		card.ProjectID, card.Priority, card.Urgent, card.OwnerOnly = p.ID, in.Priority, in.Urgent, in.OwnerOnly
		card.Batchable, card.CloseWhen = in.Batchable, in.CloseWhen
		cardOrigin := origin
		cardOrigin.ID, cardOrigin.SourceAuthor, cardOrigin.SourceState = in.ID, in.Asker, state
		card.Origin = &cardOrigin
		// Unavailable/foreign legacy askers retain a durable identity. The ordinary
		// project-card answer path chooses the assigned lead or configured architect.
		asker := "imported:" + importKey(req.Source, in.Asker, in.ID)[:24]
		if r, err := f.resolveLocked(in.Asker); err == nil && (r.a.ProjectID == p.ID || r.a.ID == p.LeadID) {
			asker = r.a.ID
		}
		if target, err := f.peerTargetLocked(in.Asker); err == nil && IsPeer(target) && target == p.LeadID {
			asker = target
		}
		ap := model.Approval{ID: id, AgentID: asker, Kind: model.ApprovalQuestion, Question: in.Question, Options: in.Options, State: model.ApprovalPending, CreatedAt: in.CreatedAt, Version: 1, Card: card, Deferred: state == "later"}
		if in.Later != nil {
			ap.Deferred = *in.Later
		}
		if state == "answered" || state == "closed" {
			// History is an expired imported record, never a fresh owner decision.
			ap.State, ap.Answer, ap.DecidedAt, ap.Deferred = model.ApprovalExpired, in.Answer, in.ClosedAt, false
			card.CloseReason = "Imported history (" + state + "): " + in.Reason
		}
		cards = append(cards, ap)
		rec.CardIDs = append(rec.CardIDs, id)
	}
	if err := f.st.CommitProjectImport(sec, req.ExpectedUpdatedAt, cards, rec); err != nil {
		return empty, err
	}
	if sec != nil {
		f.bus.Publish(model.BusEvent{Type: "project.section.updated", ProjectID: p.ID, Data: *sec})
	}
	for _, a := range cards {
		f.Publish(model.BusEvent{Type: model.EvApprovalCreated, AgentID: a.AgentID, ProjectID: p.ID, Data: a})
	}
	return rec, nil
}
