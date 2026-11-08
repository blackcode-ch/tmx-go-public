package model

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// TaskReceipt is the exact version-one Companion wire schema. Nullable fields
// must remain present; verified is always false, even for an observed turn end.
type TaskReceipt struct {
	ID            string   `json:"id"`
	To            string   `json:"to"`
	Sender        string   `json:"sender"`
	State         string   `json:"state"`
	SessionID     *string  `json:"session_id"`
	AgentIdentity string   `json:"agent_identity"`
	Report        string   `json:"report"`
	Detail        string   `json:"detail"`
	CreatedAt     float64  `json:"created_at"`
	UpdatedAt     float64  `json:"updated_at"`
	FinishedAt    *float64 `json:"finished_at"`
	ReceiptID     *string  `json:"receipt_id"`
	Verified      bool     `json:"verified"`
}

type TaskEnvelope struct {
	Version int         `json:"task_receipts"`
	Task    TaskReceipt `json:"task"`
}

func (t TaskReceipt) Envelope() TaskEnvelope { return TaskEnvelope{Version: 1, Task: t} }
func TaskNow() float64                       { return float64(time.Now().UnixMicro()) / 1e6 }
func (t TaskReceipt) Active() bool {
	return t.State == "queued" || t.State == "dispatching" || t.State == "running"
}

// Finish records an immutable, unverified observation, never objective success.
func (t *TaskReceipt) Finish(state, detail, report string) {
	if !t.Active() {
		return
	}
	now := TaskNow()
	if now < t.UpdatedAt {
		now = t.UpdatedAt
	}
	t.State, t.Detail, t.Report = state, TaskText(detail, 1024), TaskText(report, 8192)
	t.UpdatedAt, t.FinishedAt = now, &now
	id := fmt.Sprintf("%x", sha256.Sum256([]byte("tmx-task-v1:"+t.ID+":"+state)))
	t.ReceiptID = &id
}

func TaskText(s string, limit int) string {
	if len(s) > limit {
		s = s[len(s)-limit:]
	}
	return strings.ToValidUTF8(s, "")
}

// TaskRecord holds private admission/observation metadata; never expose this
// record over HTTP. Only Receipt is part of the Companion protocol.
type TaskRecord struct {
	Receipt     TaskReceipt
	Principal   string
	AgentID     string
	Body        string
	Urgent      bool
	Submitted   bool
	Ambiguous   bool
	SawWorking  bool
	SawFinished bool
	AdmittedAt  float64
}
