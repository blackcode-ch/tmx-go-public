package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

type agentProjectRecord struct {
	Folder   string                     `json:"folder"`
	Brief    map[string]json.RawMessage `json:"brief"`
	Progress model.Project              `json:"progress"`
	Samples  []model.ProgressSample     `json:"samples,omitempty"`
}

type AgentProjectView struct {
	Name     string                     `json:"name"`
	Host     string                     `json:"host"`
	Folder   string                     `json:"folder"`
	Cwd      string                     `json:"cwd"`
	Brief    map[string]json.RawMessage `json:"brief"`
	Progress model.ProgressView         `json:"progress_view"`
}

// Agent project pages belong to the stable agent ID. Workers can edit their own
// page; the owner's local head/lead can edit descendants. Peers do not gain page
// or filesystem access merely by knowing an agent's name.
func (f *Fleet) agentProjectLocked(c Caller, id string) (*agentRec, error) {
	r, err := f.resolveLocked(id)
	if err != nil {
		return nil, err
	}
	if c.Companion || c.Peer != "" {
		return nil, errf(KindForbidden, "only the owner or local agent hierarchy can access this page")
	}
	if c.Owner || c.AgentID == r.a.ID {
		return r, nil
	}
	if err := f.authorizeActLocked(c, r); err != nil {
		return nil, err
	}
	return r, nil
}

func projectView(a model.Agent, host string, rec agentProjectRecord) AgentProjectView {
	folder := rec.Folder
	if folder == "" {
		folder = a.Cwd
	}
	brief := rec.Brief
	if brief == nil {
		brief = map[string]json.RawMessage{}
	}
	if rec.Progress.Progress != nil {
		for k, v := range map[string]any{"progress": rec.Progress.Progress, "progress_basis": rec.Progress.ProgressBasis, "progress_state": rec.Progress.ProgressState} {
			brief[k], _ = json.Marshal(v)
		}
	}
	return AgentProjectView{Name: a.Name, Host: host, Folder: folder, Cwd: a.Cwd, Brief: brief, Progress: model.ViewProgress(rec.Progress, rec.Samples, model.Now(), 0)}
}

func (f *Fleet) AgentProject(c Caller, id string) (AgentProjectView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.agentProjectLocked(c, id)
	if err != nil {
		return AgentProjectView{}, err
	}
	var rec agentProjectRecord
	if err := f.st.AgentProjectView(r.a.ID, &rec); err != nil {
		return AgentProjectView{}, err
	}
	return projectView(r.a, f.Machine(), rec), nil
}

// UpdateAgentProject merges named metadata fields, validating the whole update
// before a single durable write. Reported outputs never authorize file access.
func (f *Fleet) UpdateAgentProject(c Caller, id string, patch map[string]json.RawMessage) (AgentProjectView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.agentProjectLocked(c, id)
	if err != nil {
		return AgentProjectView{}, err
	}
	var rec agentProjectRecord
	if err := f.st.AgentProjectView(r.a.ID, &rec); err != nil {
		return AgentProjectView{}, err
	}
	if rec.Brief == nil {
		rec.Brief = map[string]json.RawMessage{}
	}
	pp := model.ProgressPatch{}
	for key, raw := range patch {
		var target any
		switch key {
		case "folder":
			var folder string
			if err := json.Unmarshal(raw, &folder); err != nil {
				return AgentProjectView{}, errf(KindInvalid, "folder must be text")
			}
			if folder != "" {
				if !filepath.IsAbs(folder) {
					folder = filepath.Join(r.a.Cwd, folder)
				}
				folder, err = filepath.EvalSymlinks(folder)
				if err != nil {
					return AgentProjectView{}, errf(KindInvalid, "project folder does not exist")
				}
				folder, err = filepath.Abs(folder)
				if err != nil {
					return AgentProjectView{}, err
				}
				st, e := os.Stat(folder)
				if e != nil || !st.IsDir() {
					return AgentProjectView{}, errf(KindInvalid, "project folder must be a directory")
				}
				if !c.Owner {
					root, e := filepath.EvalSymlinks(r.a.Cwd)
					rel, re := filepath.Rel(root, folder)
					if e != nil || re != nil || !filepath.IsLocal(rel) {
						return AgentProjectView{}, errf(KindForbidden, "an agent's project folder must stay inside its working folder")
					}
				}
			}
			rec.Folder = folder
			continue
		case "expected_progress_version":
			target = &pp.ExpectedVersion
		case "progress":
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				pp.Clear = true
				continue
			}
			pp.HasPercent = true
			target = &pp.Percent
		case "progress_basis":
			pp.HasBasis = true
			target = &pp.Basis
		case "progress_state":
			pp.HasState = true
			target = &pp.State
		case "progress_confirm":
			pp.HasConfirm = true
			target = &pp.Confirm
		case "what", "status", "architecture", "notes":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || len(value) > 16000 {
				return AgentProjectView{}, errf(KindInvalid, "%s must be text of at most 16000 bytes", key)
			}
			rec.Brief[key] = append(json.RawMessage(nil), raw...)
			continue
		case "final_output", "outputs":
			var outputs []model.ProjectOutput
			if err := json.Unmarshal(raw, &outputs); err != nil {
				return AgentProjectView{}, errf(KindInvalid, "%s must be a list of outputs", key)
			}
			if _, err := validateProjectOutputs(outputs); err != nil {
				return AgentProjectView{}, err
			}
			rec.Brief[key] = append(json.RawMessage(nil), raw...)
			continue
		case "key_files", "decisions", "run":
			var rows []map[string]string
			if err := json.Unmarshal(raw, &rows); err != nil || len(rows) > 40 {
				return AgentProjectView{}, errf(KindInvalid, "%s must be a list of at most 40 text objects", key)
			}
			rec.Brief[key] = append(json.RawMessage(nil), raw...)
			continue
		case "diagram":
			var diagram struct {
				Nodes []map[string]string `json:"nodes"`
				Links []map[string]string `json:"links"`
			}
			if err := json.Unmarshal(raw, &diagram); err != nil || len(diagram.Nodes) > 20 || len(diagram.Links) > 40 {
				return AgentProjectView{}, errf(KindInvalid, "diagram must contain at most 20 nodes and 40 links")
			}
			rec.Brief[key] = append(json.RawMessage(nil), raw...)
			continue
		default:
			return AgentProjectView{}, errf(KindInvalid, "unknown project field %q", key)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil {
			return AgentProjectView{}, errf(KindInvalid, "invalid %s", key)
		}
	}
	prior := rec.Progress.ProgressVersion
	rec.Progress, err = model.UpdateProgress(rec.Progress, pp, model.Now())
	if errors.Is(err, model.ErrProgressConflict) {
		return AgentProjectView{}, errf(KindConflict, "progress changed; read the current page before updating")
	}
	if err != nil {
		return AgentProjectView{}, errf(KindInvalid, "%s", err)
	}
	if rec.Progress.ProgressVersion != prior && rec.Progress.Progress != nil {
		p := rec.Progress
		rec.Samples = append(rec.Samples, model.ProgressSample{Version: p.ProgressVersion, At: p.ProgressUpdatedAt, Run: p.ProgressRun, Percent: *p.Progress})
		if len(rec.Samples) > 100 {
			rec.Samples = rec.Samples[len(rec.Samples)-100:]
		}
	}
	if err := f.st.PutAgentProjectView(r.a.ID, rec); err != nil {
		return AgentProjectView{}, err
	}
	f.bus.Publish(model.BusEvent{Type: "agent.project.updated", AgentID: r.a.ID, ProjectID: r.a.ProjectID})
	return projectView(r.a, f.Machine(), rec), nil
}

// ValidateAgentUpdateFields prevents the compatibility endpoint from silently
// accepting Python-only fields that Go does not implement.
func ValidateAgentUpdateFields(patch map[string]json.RawMessage) error {
	for key := range patch {
		switch key {
		case "role", "brief", "model", "profile", "protected":
		default:
			return errf(KindInvalid, "unsupported agent update field %q", key)
		}
	}
	return nil
}
