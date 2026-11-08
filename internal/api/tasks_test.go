package api_test

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/peers"
	"github.com/google/uuid"
)

func TestTaskClientWireContract(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	var caps map[string]any
	if status := owner.do("GET", "/api/tasks/capabilities", nil, &caps); status != 200 {
		t.Fatal(status)
	}
	if len(caps) != 2 || caps["task_receipts"] != float64(1) || caps["host"] != d.Fleet.Machine() {
		t.Fatal(caps)
	}
	id := mkWorker(t, d)
	a, err := d.Fleet.Agent(id)
	if err != nil {
		t.Fatal(err)
	}
	req := map[string]any{"id": "123456789abc", "to": a.Name + "@" + d.Fleet.Machine(), "body": "a tracked assignment", "from": "companion", "urgent": false}
	var envelope map[string]json.RawMessage
	if status := owner.do("POST", "/api/tasks", req, &envelope); status != 200 {
		t.Fatal(status)
	}
	if len(envelope) != 2 || string(envelope["task_receipts"]) != "1" {
		t.Fatal(envelope)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope["task"], &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "to", "sender", "state", "session_id", "agent_identity", "report", "detail", "created_at", "updated_at", "finished_at", "receipt_id", "verified"}
	got := make([]string, 0, len(fields))
	for k := range fields {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("client rejects field set", got)
	}
	var task model.TaskReceipt
	if err := json.Unmarshal(envelope["task"], &task); err != nil {
		t.Fatal(err)
	}
	if parsed, err := uuid.Parse(task.AgentIdentity); err != nil || parsed.String() != task.AgentIdentity {
		t.Fatal("invalid identity", task.AgentIdentity)
	}
	if task.ID != req["id"] || task.To != req["to"] || task.Sender != "companion@"+d.Fleet.Machine() || task.Verified || task.UpdatedAt < task.CreatedAt {
		t.Fatal(task)
	}
	var replay model.TaskEnvelope
	if status := owner.do("POST", "/api/tasks", req, &replay); status != 200 || replay.Task.ID != task.ID {
		t.Fatal(status, replay)
	}
	req["body"] = "different"
	if status := owner.do("POST", "/api/tasks", req, nil); status != 409 {
		t.Fatal("conflict", status)
	}
	if status := owner.do("GET", "/api/tasks/123456789abc", nil, &replay); status != 200 {
		t.Fatal(status)
	}
	worker := client{t: t, base: d.URL, token: d.AgentToken(t, id)}
	if status := worker.do("GET", "/api/tasks/123456789abc", nil, nil); status != 403 {
		t.Fatal("cross-principal read", status)
	}
	for _, extra := range []map[string]any{{"urgent": nil}, {"urgent": "true"}, {"start": true}, {"session_id": "forged"}} {
		invalid := map[string]any{"id": "123456789abd", "to": a.Name, "body": "work"}
		for k, v := range extra {
			invalid[k] = v
		}
		if status := owner.do("POST", "/api/tasks", invalid, nil); status != 400 {
			t.Fatal(extra, status)
		}
	}
}

func TestTaskPeerOperatorAndAgentBoundary(t *testing.T) {
	p := newPeerSetup(t)
	id := mkWorker(t, p.d)
	a, err := p.d.Fleet.Agent(id)
	if err != nil {
		t.Fatal(err)
	}
	req := map[string]any{"id": "abcde1234567", "to": a.Name + "@" + p.d.Fleet.Machine(), "body": "remote assignment", "from": "companion", "urgent": false}
	for _, behalf := range []string{"operator", "daemon", "agent:worker", "agent:head"} {
		if status := peerDo(t, p.url, "POST", "/api/tasks", "hosta", p.pair, map[string]string{peers.HeaderOnBehalf: behalf}, req, nil); status != 403 {
			t.Fatal(behalf, status)
		}
		if status := peerDo(t, p.url, "GET", "/api/tasks/capabilities", "hosta", p.pair, map[string]string{peers.HeaderOnBehalf: behalf}, nil, nil); status != 403 {
			t.Fatal(behalf, status)
		}
	}
	head := client{t: t, base: p.d.URL, token: p.d.AgentToken(t, "head")}
	if status := head.do("GET", "/api/tasks/capabilities", nil, nil); status != 403 {
		t.Fatal("head capability access", status)
	}
}
