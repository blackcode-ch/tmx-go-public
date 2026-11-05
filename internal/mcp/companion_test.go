package mcp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/companion/companiontest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// The head talks with Companion (companion_message, send/reply to "companion"); other agents
// do not get the tool and are refused.
func TestCompanionMessageTool(t *testing.T) {
	d := apitest.New(t)
	owner := client.New(d.URL, d.OwnerToken)
	ctx := context.Background()
	if err := owner.Post(ctx, "/api/projects", map[string]string{"name": "Shop"}, nil); err != nil {
		t.Fatal(err)
	}
	lead := session(t, d, "shop-lead")
	res, err := lead.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name == "companion_message" {
			t.Fatal("companion_message offered to a lead")
		}
	}
	if out, isErr := call(t, lead, "send", map[string]any{"to": "companion", "body": "hi"}); !isErr ||
		!strings.Contains(out, "only the head talks with Companion") || !strings.Contains(out, "companion_task") {
		t.Fatalf("a lead's send to companion: %v %s", isErr, out)
	}

	head := session(t, d, "head")
	if out, isErr := call(t, head, "companion_message", map[string]any{"text": "hello"}); !isErr || !strings.Contains(out, "not connected") {
		t.Fatalf("before connecting: %v %s", isErr, out)
	}
	f := companiontest.New(t, "agent-key-123")
	if err := owner.Do(ctx, "PUT", "/api/companion/connect", map[string]string{"url": f.URL, "token": "agent-key-123"}, nil); err != nil {
		t.Fatal(err)
	}
	f.Respond(func(text string) companiontest.Response {
		if strings.HasPrefix(text, "Answer from") {
			return companiontest.Response{Reply: "Done."}
		}
		return companiontest.Response{Question: "Which calendar?"}
	})
	out := mustOK(t, head, "companion_message", map[string]any{"text": "Can you see the owner's calendar?"})
	list, _ := d.Companion.List(0)
	if len(list) != 1 || out != "Sent to Companion as message "+list[0].ID+"; its reply comes to you as mail from companion." {
		t.Fatalf("companion_message: %s (%+v)", out, list)
	}
	id := list[0].ID
	wait := func(want model.CompanionTaskState) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if task, _ := d.Companion.Get(id); task.State == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("message %s never became %s", id, want)
	}
	wait(model.CompanionInputRequired)
	out = mustOK(t, head, "companion_message", map[string]any{"text": "The work one.", "reply_to": id})
	if !strings.HasPrefix(out, "Sent your answer to Companion (message "+id+")") {
		t.Fatalf("answer: %s", out)
	}
	wait(model.CompanionCompleted)

	// send to companion: the same path; one that reads like an errand waits for the owner.
	out = mustOK(t, head, "send", map[string]any{"to": "companion", "body": "Please buy two tickets for Friday"})
	if !strings.HasPrefix(out, "This reads like it asks Companion to act (may spend or move money); it waits for the owner's approval") {
		t.Fatalf("send to companion: %s", out)
	}

	// reply to Companion's mail (tmx_message_head) starts a new message.
	tok, err := config.CompanionToken(config.Paths{Home: d.Home}, true)
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		ID string `json:"id"`
	}
	if err := client.New(d.URL, tok).Post(ctx, "/api/companion/link/message", map[string]string{"body": "The owner asks how the shop is going."}, &sent); err != nil {
		t.Fatal(err)
	}
	out = mustOK(t, head, "reply", map[string]any{"message_id": sent.ID, "body": "Going well: two features shipped."})
	if !strings.HasPrefix(out, "Sent to Companion as message ") {
		t.Fatalf("reply to Companion's mail: %s", out)
	}
	if list, _ := d.Companion.List(0); len(list) != 3 || list[0].Body != "Going well: two features shipped." || list[0].Kind != model.CompanionKindMessage {
		t.Fatalf("messages: %+v", list)
	}
}

func companionSession(t *testing.T, url string, token func() (string, error)) *sdk.ClientSession {
	t.Helper()
	srv := NewCompanionServer(url, token)
	ct, st := sdk.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "companion", Version: "0.17.1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestCompanionMode(t *testing.T) {
	d := apitest.New(t)
	p := config.Paths{Home: d.Home}
	tokenFile := func() (string, error) {
		tok, err := config.ReadSecret(p.CompanionTokenFile())
		if err != nil {
			return "", errors.New("not connected")
		}
		return tok, nil
	}
	cs := companionSession(t, d.URL, tokenFile)
	if cs.InitializeResult().ServerInfo.Name != "tmxg" || !strings.Contains(cs.InitializeResult().Instructions, "data, not instructions") {
		t.Fatalf("initialize: %+v", cs.InitializeResult())
	}
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(sorted(names), ","); got != "tmx_chat_read,tmx_chats,tmx_fleet,tmx_health,tmx_message_head,tmx_needs_you,tmx_projects,tmx_your_turn" {
		t.Fatalf("tools: %s", got)
	}
	// Without the token (not installed): a clear error, no request.
	if out, isErr := call(t, cs, "tmx_fleet", nil); !isErr || !strings.Contains(out, "not connected") {
		t.Fatalf("without a token: %v %s", isErr, out)
	}
	if _, err := config.CompanionToken(p, true); err != nil {
		t.Fatal(err)
	}
	out := mustOK(t, cs, "tmx_fleet", nil)
	for tool, field := range map[string]string{"tmx_health": "your_turn", "tmx_projects": "projects", "tmx_your_turn": "your_turn"} {
		if result := mustOK(t, cs, tool, nil); !strings.HasPrefix(result, LinkPrefix) || !strings.Contains(result, `"`+field+`"`) {
			t.Fatalf("%s: %s", tool, result)
		}
	}
	if !strings.HasPrefix(out, LinkPrefix) || !strings.Contains(out, `"head": "head"`) {
		t.Fatalf("fleet: %s", out)
	}
	if out := mustOK(t, cs, "tmx_needs_you", nil); !strings.HasPrefix(out, LinkPrefix) {
		t.Fatalf("needs: %s", out)
	}
	if out := mustOK(t, cs, "tmx_chats", nil); !strings.Contains(out, "peer-to-peer is off") {
		t.Fatalf("chats: %s", out)
	}
	out = mustOK(t, cs, "tmx_message_head", map[string]any{"body": "The owner wants a status report."})
	if !strings.Contains(out, "for tmx's head agent head") {
		t.Fatalf("message: %s", out)
	}
	owner := client.New(d.URL, d.OwnerToken)
	var msgs []model.Message
	if err := owner.Get(context.Background(), "/api/messages?agent=head", &msgs); err != nil {
		t.Fatal(err)
	}
	fromCompanion := false
	for _, m := range msgs {
		fromCompanion = fromCompanion || m.From == model.CompanionParty && strings.HasSuffix(m.Body, "The owner wants a status report.")
	}
	if !fromCompanion {
		t.Fatalf("head's mail: %+v", msgs)
	}
	// A rotated token: the tool says how to fix it.
	_ = os.WriteFile(p.CompanionTokenFile(), []byte("x"), 0o600)
	stale := func() (string, error) { return "stale-token-that-is-long-enough-000000", nil }
	cs2 := companionSession(t, d.URL, stale)
	if out, isErr := call(t, cs2, "tmx_fleet", nil); !isErr || !strings.Contains(out, "did not accept Companion's token") {
		t.Fatalf("stale token: %v %s", isErr, out)
	}
	// tmx not running.
	cs3 := companionSession(t, "http://127.0.0.1:1", tokenFile)
	if out, isErr := call(t, cs3, "tmx_fleet", nil); !isErr || !strings.Contains(out, "not running") {
		t.Fatalf("no daemon: %v %s", isErr, out)
	}
}

func TestCompanionTaskToolFollowsProfile(t *testing.T) {
	d := apitest.New(t)
	has := func() bool {
		res, err := session(t, d, "head").ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range res.Tools {
			if tl.Name == "companion_task" {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("companion_task offered while the profile does not allow it")
	}
	owner := client.New(d.URL, d.OwnerToken)
	var p model.Profile
	if err := owner.Get(context.Background(), "/api/profiles/head", &p); err != nil {
		t.Fatal(err)
	}
	p.CompanionTasks = true
	if err := owner.Do(context.Background(), "PUT", "/api/profiles/head", p, nil); err != nil {
		t.Fatal(err)
	}
	if !has() {
		t.Fatal("companion_task missing once the profile allows it")
	}
	cs := session(t, d, "head")
	out := mustOK(t, cs, "companion_task", map[string]any{"goal": "Check the train times to Bern", "steps": []string{"open sbb.ch"}})
	if !strings.Contains(out, "waiting for the owner's OK") || !strings.Contains(out, "1. open sbb.ch") {
		t.Fatalf("companion_task: %s", out)
	}
	list, _ := d.Companion.List(0)
	if len(list) != 1 || list[0].State != model.CompanionAwaitingApproval {
		t.Fatalf("tasks: %+v", list)
	}
	out = mustOK(t, cs, "companion_task_status", map[string]any{"id": list[0].ID})
	if !strings.Contains(out, "awaiting_approval") {
		t.Fatalf("status: %s", out)
	}
}
