package mcp

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/peertext"
)

type noArgs struct{}

type projectCreateArgs struct {
	Lead    *string  `json:"lead,omitempty" jsonschema:"omit to launch a new local lead; existing lead name or name@host to assign; empty or none for no lead"`
	Model   string   `json:"model,omitempty" jsonschema:"lead model; empty uses the profile default (Opus for lead); must be allowed by the profile"`
	Name    string   `json:"name" jsonschema:"project name; its slug names the folder and the lead (<slug>-lead)"`
	Brief   string   `json:"brief,omitempty" jsonschema:"what the project is for and what done looks like; the lead starts from this"`
	Folder  string   `json:"folder,omitempty" jsonschema:"project folder; default <workspace>/<slug>"`
	Profile string   `json:"profile,omitempty" jsonschema:"profile of the project's lead (a lead profile); default lead"`
	Harness string   `json:"harness,omitempty" jsonschema:"harness for the project's lead: claude (default) or codex"`
	Members []string `json:"members,omitempty" jsonschema:"existing agents (names) in no project yet that join it at once, e.g. a lead that must launch workers here"`
}

type projectMemberArgs struct {
	Project string `json:"project" jsonschema:"the project's name, slug or id"`
	Agent   string `json:"agent" jsonschema:"the agent to add (name or id); it must be in no other project"`
}

type launchArgs struct {
	Name    string `json:"name" jsonschema:"agent name: lowercase letters, digits and dashes, unique on this machine"`
	Task    string `json:"task,omitempty" jsonschema:"the first message the agent gets: its task"`
	Brief   string `json:"brief,omitempty" jsonschema:"standing brief kept in the agent's system prompt"`
	Role    string `json:"role,omitempty" jsonschema:"short role, e.g. backend, tests, docs"`
	Profile string `json:"profile,omitempty" jsonschema:"agent profile (the profiles tool lists yours); default: the built-in profile of the level"`
	Level   string `json:"level,omitempty" jsonschema:"worker (default) or lead; implied by the profile"`
	Project string `json:"project,omitempty" jsonschema:"project slug or id; a lead's own project by default"`
	Model   string `json:"model,omitempty" jsonschema:"empty uses profile default; worker choices: Opus for senior, Sonnet for junior, Haiku for intern; must be allowed by profile"`
	Harness string `json:"harness,omitempty" jsonschema:"claude (default), codex, opencode or pi"`
	Mode    string `json:"mode,omitempty" jsonschema:"chat (default for claude) or terminal"`
	Account string `json:"account,omitempty" jsonschema:"which of the owner's accounts of the harness it runs as (the Usage page names them); default the harness's default for new agents"`
	Cwd     string `json:"cwd,omitempty" jsonschema:"working folder, relative to the project folder; default the project folder"`
	Thread  string `json:"thread,omitempty" jsonschema:"the Matrix thread it works on (title or id, from matrix); default your current topic or its project's thread"`
}

type nameArgs struct {
	Name string `json:"name" jsonschema:"agent name"`
}

type restartArgs struct {
	Name   string `json:"name" jsonschema:"agent name"`
	Fresh  bool   `json:"fresh,omitempty" jsonschema:"true starts a brand-new conversation (clean context) instead of resuming the old one"`
	Prompt string `json:"prompt,omitempty" jsonschema:"first instruction of the fresh conversation (a baton pass); ignored unless fresh"`
}

type screenArgs struct {
	Name    string `json:"name" jsonschema:"agent name"`
	History int    `json:"history,omitempty" jsonschema:"extra scrollback lines on top of the current view"`
}

type typeArgs struct {
	Name   string `json:"name" jsonschema:"agent name"`
	Text   string `json:"text" jsonschema:"the text to type"`
	Submit *bool  `json:"submit,omitempty" jsonschema:"false leaves it unsent; default true presses Enter"`
}

type keysArgs struct {
	Name string `json:"name" jsonschema:"agent name"`
	Keys string `json:"keys" jsonschema:"keys to press, space-separated: enter, esc, ctrl+c, up, down, shift+tab, y, 1 ..."`
}

type sendArgs struct {
	RetryID string `json:"retry_id,omitempty" jsonschema:"stable local retry id; reuse unchanged on retry, never for a different message"`
	To      string `json:"to" jsonschema:"agent name, owner for the human, or (head only) companion for Companion"`
	Body    string `json:"body" jsonschema:"the message (up to 32 KB)"`
}

type reportArgs struct {
	Body string `json:"body" jsonschema:"the report for your parent: what changed, where, how verified, what is next or blocked"`
	Done bool   `json:"done,omitempty" jsonschema:"true only when your assigned work is finished; an eligible worker is shelved after this turn, keeping its conversation"`
}

type replyArgs struct {
	RetryID   string `json:"retry_id,omitempty" jsonschema:"stable local retry id; reuse unchanged on retry, never for a different reply"`
	MessageID string `json:"message_id" jsonschema:"id of the message you answer (from inbox)"`
	Body      string `json:"body" jsonschema:"your reply"`
}

type statusArgs struct {
	Name    string `json:"name" jsonschema:"agent name"`
	Entries int    `json:"entries,omitempty" jsonschema:"how many recent timeline entries to show (default 10, max 50)"`
}

type threadArgs struct {
	Topic string `json:"topic" jsonschema:"the topic to file this turn under: a project's name, an existing topic, or a new title (up to 80 characters)"`
}

type threadUpdateArgs struct {
	Topic    string `json:"topic" jsonschema:"the topic to change, by its title (as threads lists it)"`
	Title    string `json:"title,omitempty" jsonschema:"a new title (your own topics only)"`
	Archived *bool  `json:"archived,omitempty" jsonschema:"true archives it (hidden from the board), false restores it"`
	Delete   bool   `json:"delete,omitempty" jsonschema:"true deletes the topic; its cards move to General. Projects' and chats' strings can only be archived"`
}

type askArgs struct {
	ID             string            `json:"id,omitempty" jsonschema:"edit your own pending card by its id"`
	Type           string            `json:"type,omitempty" jsonschema:"decision or human_action"`
	Context        string            `json:"context,omitempty"`
	Category       string            `json:"category,omitempty"`
	Recommendation string            `json:"recommendation,omitempty"`
	Action         *model.CardAction `json:"action,omitempty"`
	Question       string            `json:"question" jsonschema:"the question for the owner, self-contained"`
	Options        []string          `json:"options,omitempty" jsonschema:"suggested answers, if any"`
}

func (t *Tools) register(s *sdk.Server) {
	t.registerProcesses(s)
	t.registerProjectSections(s)
	sdk.AddTool(s, &sdk.Tool{Name: "whoami", Description: "Who you are in tmx: name, level, project, parent, model, folder."}, t.whoami)
	sdk.AddTool(s, &sdk.Tool{Name: "fleet", Description: "All projects and agents on this machine with their state and model."}, t.fleet)
	sdk.AddTool(s, &sdk.Tool{Name: "project_create", Description: "Create a project (folder + lead agent). Head only."}, t.projectCreate)
	sdk.AddTool(s, &sdk.Tool{Name: "project_member_add", Description: "Put an existing agent into a project, so a lead with no project can launch workers there. " +
		"The head or that project's lead only (the owner from the dashboard). An agent belongs to one project; it applies on the agent's next launch."}, t.projectMemberAdd)
	sdk.AddTool(s, &sdk.Tool{Name: "agent_launch", Description: "Launch an agent below you with one of the profiles you may launch (see profiles; head: leads and workers; lead: workers in its project). The profile sets the model."}, t.agentLaunch)
	sdk.AddTool(s, &sdk.Tool{Name: "agent_stop", Description: "Stop an agent below you (it can be started again later)."}, t.agentStop)
	sdk.AddTool(s, &sdk.Tool{Name: "agent_start", Description: "Start a stopped agent below you again, resuming its conversation where it left off."}, t.agentStart)
	sdk.AddTool(s, &sdk.Tool{Name: "agent_restart", Description: "Stop an agent below you and resume it (a clean reset of its process). With fresh: true it starts a NEW conversation instead (clean context); prompt becomes its first instruction, a baton pass."}, t.agentRestart)
	sdk.AddTool(s, &sdk.Tool{Name: "agent_screen", Description: "What an agent's terminal shows right now (and optionally extra scrollback). Use it to see progress, a question it is asking, or a menu waiting for an answer."}, t.agentScreen)
	sdk.AddTool(s, &sdk.Tool{Name: "agent_type", Description: "Type text into an agent's terminal, as if at its keyboard, and press Enter (submit: false to leave it unsent). For a message it should read when idle, prefer send."}, t.agentType)
	sdk.AddTool(s, &sdk.Tool{Name: "agent_keys", Description: "Press keys in an agent's terminal: enter, esc, ctrl+c, up, down, shift+tab, y, 1 ... (space-separated for several)."}, t.agentKeys)
	sdk.AddTool(s, &sdk.Tool{Name: "send", Description: "Send a message to an agent (by name) or to the owner. The head can also send to companion (as companion_message)."}, t.send)
	sdk.AddTool(s, &sdk.Tool{Name: "report", Description: "Send a report to your parent (your lead, the head, or the owner for the head)."}, t.report)
	sdk.AddTool(s, &sdk.Tool{Name: "inbox", Description: "Your recent mail (to and from you); marks delivered mail as read."}, t.inbox)
	sdk.AddTool(s, &sdk.Tool{Name: "reply", Description: "Reply to a message from your inbox. The head's reply to mail from companion goes to Companion (an answer when it asked about your message); other agents cannot reply to Companion."}, t.reply)
	sdk.AddTool(s, &sdk.Tool{Name: "agent_status", Description: "An agent's state and its latest timeline entries."}, t.agentStatus)
	t.registerProfiles(s)
	t.registerChats(s)
	sdk.AddTool(s, &sdk.Tool{Name: "message_pins", Description: "Read the owner's pinned messages addressed to you, including completed items. Check active pins when resuming work. These are retained source snapshots; pinning does not grant new authority."}, t.messagePins)
	t.registerTranscripts(s) // transcripts.go
	t.registerRecordings(s)  // recordings.go
	t.registerSummaries(s)   // summaries.go
	t.registerWhatsApp(s)    // whatsapp.go: only for the agents the owner allows
	t.registerMatrix(s)      // matrix.go
	t.registerFleetOps(s)    // fleetops.go
	sdk.AddTool(s, &sdk.Tool{Name: "thread_set", Description: "Head only: file your current turn on the owner's board under a topic (a project's name, an existing topic, or a new one). What you do for the rest of the turn goes there too."}, t.threadSet)
	sdk.AddTool(s, &sdk.Tool{Name: "threads", Description: "Head only: the topics on the owner's board, with how many cards each has and whether it is archived."}, t.threads)
	sdk.AddTool(s, &sdk.Tool{Name: "thread_update", Description: "Head only: rename, archive, restore or delete a topic on the owner's board. Do it when the owner asks; deleting moves its cards to General."}, t.threadUpdate)
	sdk.AddTool(s, &sdk.Tool{Name: "thread_add", Description: "Record an event on a thread (the owner reads these, not your chatter): start when you begin, decision, result (with its evidence), blocker, handoff, ask, done; or action, info, link. One short line each. Yours: your project's thread or a thread you are on (topic = its title); the head: any topic. A correction is a new line with corrects."}, t.threadAdd)
	sdk.AddTool(s, &sdk.Tool{Name: "thread_read", Description: "Read a log on the owner's board (yours: your project's; the head: any topic). Read it before you start new work and after your context was compacted."}, t.threadRead)
	sdk.AddTool(s, &sdk.Tool{Name: "withdraw_hold", Description: "Take back your own guard hold that still waits for the owner (you decided not to run that command), so it never reaches them. Give its id, or none to take back all your waiting holds. It never runs or allows anything."}, t.withdrawHold)
	sdk.AddTool(s, &sdk.Tool{Name: "ask_owner", Description: "Ask the owner a question (shows in their Needs-you inbox). Returns at once; the answer arrives later as a message."}, t.askOwner)
}

func text(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}

func untrusted(s string) *sdk.CallToolResult { return text(Untrusted + "\n" + s) }

func (t *Tools) me(ctx context.Context) (model.Agent, error) {
	var a model.Agent
	err := t.c.Get(ctx, "/api/agents/"+client.Seg(t.agentID), &a)
	return a, err
}

func (t *Tools) whoami(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
	var snap model.Snapshot
	if err := t.c.Get(ctx, "/api/snapshot", &snap); err != nil {
		return nil, nil, err
	}
	ix := indexSnapshot(snap)
	a, ok := ix.agents[t.agentID]
	if !ok {
		return nil, nil, fmt.Errorf("this agent (%s) is not in the fleet any more", t.agentID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, a %s on machine %s (id %s).\n", a.Name, a.Level, snap.Machine, a.ID)
	if p, ok := ix.projects[a.ProjectID]; ok {
		fmt.Fprintf(&b, "Project: %s (slug %s, folder %s).\n", p.Name, p.Slug, p.Folder)
	}
	fmt.Fprintf(&b, "Parent: %s.\n", ix.name(a.ParentID, "owner"))
	fmt.Fprintf(&b, "Model: %s. Folder: %s. Mode: %s.\n", orDash(a.Model), a.Cwd, a.Mode)
	if p, err := t.myProfile(ctx, a); err == nil {
		fmt.Fprintf(&b, "Profile: %s (%s)", p.Name, p.Slug)
		if p.ReadOnly {
			b.WriteString(", read-only")
		}
		b.WriteString(". The profiles tool lists what you may launch.\n")
	}
	if a.Role != "" {
		fmt.Fprintf(&b, "Role: %s\n", a.Role)
	}
	if a.Brief != "" {
		fmt.Fprintf(&b, "Brief: %s\n", a.Brief)
	}
	return untrusted(b.String()), nil, nil
}

func (t *Tools) fleet(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
	var snap model.Snapshot
	if err := t.c.Get(ctx, "/api/snapshot", &snap); err != nil {
		return nil, nil, err
	}
	return untrusted(FormatTree(snap)), nil, nil
}

func (t *Tools) projectCreate(ctx context.Context, _ *sdk.CallToolRequest, in projectCreateArgs) (*sdk.CallToolResult, any, error) {
	var p model.Project
	if err := t.c.Post(ctx, "/api/projects", in, &p); err != nil {
		return nil, nil, err
	}
	if p.LeadID == "" {
		return text(fmt.Sprintf("Project %q created in %s with no lead assigned.", p.Name, p.Folder)), nil, nil
	}
	lead := strings.TrimPrefix(p.LeadID, "fleet:")
	if in.Lead != nil {
		return text(fmt.Sprintf("Project %q created in %s; assigned lead %s. No agent was launched or restarted.", p.Name, p.Folder, *in.Lead)), nil, nil
	}
	var a model.Agent
	if err := t.c.Get(ctx, "/api/agents/"+client.Seg(p.LeadID), &a); err == nil {
		lead = fmt.Sprintf("%s (%s, %s)", a.Name, a.Model, a.State)
	}
	return text(fmt.Sprintf("Project %q created in %s. Its lead is %s and has the brief; talk to it with send.", p.Name, p.Folder, lead)), nil, nil
}

func (t *Tools) projectMemberAdd(ctx context.Context, _ *sdk.CallToolRequest, in projectMemberArgs) (*sdk.CallToolResult, any, error) {
	var a model.Agent
	if err := t.c.Post(ctx, "/api/projects/"+client.Seg(in.Project)+"/members", map[string]string{"agent": in.Agent}, &a); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("%s is in project %s now; it can launch there (restart it if it is running).", a.Name, in.Project)), nil, nil
}

func (t *Tools) agentLaunch(ctx context.Context, _ *sdk.CallToolRequest, in launchArgs) (*sdk.CallToolResult, any, error) {
	body := map[string]any{"name": in.Name, "profile": in.Profile, "level": in.Level, "project_id": in.Project, "model": in.Model,
		"harness": in.Harness, "account": in.Account, "mode": in.Mode, "cwd": in.Cwd, "role": in.Role, "brief": in.Brief, "prompt": in.Task,
		"thread_id": in.Thread}
	for k, v := range body {
		if v == "" {
			delete(body, k)
		}
	}
	var a model.Agent
	if err := t.c.Post(ctx, "/api/agents", body, &a); err != nil {
		return nil, nil, err
	}
	msg := fmt.Sprintf("Launched %s: %s on %s in %s, state %s (profile %s).", a.Name, a.Level, orDash(a.Model), a.Cwd, a.State, a.Profile)
	if a.Error != "" {
		msg += " Error: " + a.Error
	}
	return text(msg), nil, nil
}

func (t *Tools) agentStop(ctx context.Context, _ *sdk.CallToolRequest, in nameArgs) (*sdk.CallToolResult, any, error) {
	var a model.Agent
	if err := t.c.Post(ctx, "/api/agents/"+client.Seg(in.Name)+"/stop", nil, &a); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("%s is %s.", a.Name, a.State)), nil, nil
}

func (t *Tools) agentStart(ctx context.Context, _ *sdk.CallToolRequest, in nameArgs) (*sdk.CallToolResult, any, error) {
	var a model.Agent
	if err := t.c.Post(ctx, "/api/agents/"+client.Seg(in.Name)+"/start", nil, &a); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("%s is %s.", a.Name, a.State)), nil, nil
}

func (t *Tools) agentRestart(ctx context.Context, _ *sdk.CallToolRequest, in restartArgs) (*sdk.CallToolResult, any, error) {
	body := map[string]any{"fresh": in.Fresh}
	if in.Fresh && in.Prompt != "" {
		body["prompt"] = in.Prompt
	}
	var a model.Agent
	if err := t.c.Post(ctx, "/api/agents/"+client.Seg(in.Name)+"/restart", body, &a); err != nil {
		return nil, nil, err
	}
	how := "resumed"
	if in.Fresh {
		how = "restarted with a fresh conversation"
	}
	return text(fmt.Sprintf("%s is %s, state %s.", a.Name, how, a.State)), nil, nil
}

func (t *Tools) agentScreen(ctx context.Context, _ *sdk.CallToolRequest, in screenArgs) (*sdk.CallToolResult, any, error) {
	q := url.Values{}
	if in.History > 0 {
		q.Set("history", strconv.Itoa(in.History))
	}
	path := "/api/agents/" + client.Seg(in.Name) + "/screen"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := t.c.Get(ctx, path, &out); err != nil {
		return nil, nil, err
	}
	return untrusted(out.Text), nil, nil
}

func (t *Tools) agentType(ctx context.Context, _ *sdk.CallToolRequest, in typeArgs) (*sdk.CallToolResult, any, error) {
	body := map[string]any{"text": in.Text}
	if in.Submit != nil {
		body["submit"] = *in.Submit
	}
	if err := t.c.Post(ctx, "/api/agents/"+client.Seg(in.Name)+"/type", body, nil); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Typed into %s.", in.Name)), nil, nil
}

func (t *Tools) agentKeys(ctx context.Context, _ *sdk.CallToolRequest, in keysArgs) (*sdk.CallToolResult, any, error) {
	if err := t.c.Post(ctx, "/api/agents/"+client.Seg(in.Name)+"/keys", map[string]string{"keys": in.Keys}, nil); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Sent %q to %s.", in.Keys, in.Name)), nil, nil
}

func (t *Tools) send(ctx context.Context, _ *sdk.CallToolRequest, in sendArgs) (*sdk.CallToolResult, any, error) {
	if strings.EqualFold(strings.TrimSpace(in.To), model.CompanionParty) && in.RetryID == "" {
		return t.toCompanion(ctx, map[string]string{"to": model.CompanionParty, "body": in.Body}) // the head only
	}
	var m model.Message
	if err := t.c.Post(ctx, "/api/messages", map[string]string{"to": in.To, "body": in.Body, "retry_id": in.RetryID}, &m); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Message %s to %s: %s.", m.ID, in.To, m.State)), nil, nil
}

func (t *Tools) report(ctx context.Context, _ *sdk.CallToolRequest, in reportArgs) (*sdk.CallToolResult, any, error) {
	a, err := t.me(ctx)
	if err != nil {
		return nil, nil, err
	}
	to := a.ParentID
	if to == "" {
		to = model.Owner
	}
	var m model.Message
	if err := t.c.Post(ctx, "/api/messages", map[string]string{"to": to, "body": in.Body}, &m); err != nil {
		return nil, nil, err
	}
	if in.Done {
		if err := t.c.Post(ctx, "/api/agents/"+client.Seg(a.ID)+"/done", nil, nil); err != nil {
			return nil, nil, err
		}
	}
	return text(fmt.Sprintf("Report %s sent to your parent: %s.", m.ID, m.State)), nil, nil
}

func (t *Tools) inbox(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
	var snap model.Snapshot
	if err := t.c.Get(ctx, "/api/snapshot", &snap); err != nil {
		return nil, nil, err
	}
	var msgs []model.Message
	if err := t.c.Get(ctx, "/api/messages?limit=50", &msgs); err != nil {
		return nil, nil, err
	}
	if len(msgs) == 0 {
		return text("Your inbox is empty."), nil, nil
	}
	ix := indexSnapshot(snap)
	var b strings.Builder
	for _, m := range msgs {
		dir := "from " + ix.name(m.From, m.From)
		if m.From == t.agentID {
			dir = "to " + ix.name(m.To, m.To)
		}
		fmt.Fprintf(&b, "[%s] %s (%s)", m.ID, dir, m.State)
		if m.ReplyTo != "" {
			fmt.Fprintf(&b, " reply to %s", m.ReplyTo)
		}
		fmt.Fprintf(&b, ":\n%s\n\n", inboxBody(m))
	}
	return untrusted(strings.TrimSpace(b.String())), nil, nil
}

// inboxBody is a message's body as inbox shows it: an agent's words (here or on another
// machine) quoted line by line, so none of them reads as tmx's framing; the owner's as written;
// chat, Companion and WhatsApp mail as stored, labelled and quoted by its own layer.
func inboxBody(m model.Message) string {
	if m.From == model.Owner || m.From == model.CompanionParty || m.From == model.WhatsAppParty || fleet.IsExternal(m.From) {
		return m.Body
	}
	return peertext.Quote(m.Body)
}

func (t *Tools) reply(ctx context.Context, _ *sdk.CallToolRequest, in replyArgs) (*sdk.CallToolResult, any, error) {
	var m model.Message
	if err := t.c.Post(ctx, "/api/messages", map[string]string{"reply_to": in.MessageID, "body": in.Body, "retry_id": in.RetryID}, &m); err != nil {
		return nil, nil, err
	}
	if m.To == model.CompanionParty { // the head answered Companion's mail
		return text(t.companionSent(ctx, m)), nil, nil
	}
	return text(fmt.Sprintf("Reply %s sent: %s.", m.ID, m.State)), nil, nil
}

func (t *Tools) agentStatus(ctx context.Context, _ *sdk.CallToolRequest, in statusArgs) (*sdk.CallToolResult, any, error) {
	var a model.Agent
	if err := t.c.Get(ctx, "/api/agents/"+client.Seg(in.Name), &a); err != nil {
		return nil, nil, err
	}
	n := in.Entries
	if n <= 0 {
		n = 10
	}
	if n > 50 {
		n = 50
	}
	var tl struct {
		Entries []model.TimelineEntry `json:"entries"`
	}
	q := url.Values{"limit": {strconv.Itoa(n)}}
	if err := t.c.Get(ctx, "/api/agents/"+client.Seg(a.ID)+"/timeline?"+q.Encode(), &tl); err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, %s): %s", a.Name, a.Level, orDash(a.Model), a.State)
	if a.Queued > 0 {
		fmt.Fprintf(&b, ", %d messages waiting", a.Queued)
	}
	fmt.Fprintf(&b, ", %d turns.", a.Turns)
	if a.Error != "" {
		fmt.Fprintf(&b, " Error: %s", a.Error)
	}
	b.WriteString("\n")
	for _, e := range tl.Entries {
		line := e.Text
		if e.Kind == model.EntryToolUse {
			line = e.Tool + " " + truncate(string(e.Input), 200)
		}
		fmt.Fprintf(&b, "#%d %s: %s\n", e.Seq, e.Kind, truncate(line, 500))
	}
	return untrusted(strings.TrimSpace(b.String())), nil, nil
}

type withdrawArgs struct {
	ID string `json:"id,omitempty" jsonschema:"the hold's id; empty = all your waiting holds"`
}

func (t *Tools) withdrawHold(ctx context.Context, _ *sdk.CallToolRequest, in withdrawArgs) (*sdk.CallToolResult, any, error) {
	if in.ID != "" {
		var ap model.Approval
		if err := t.c.Post(ctx, "/api/guard/holds/"+url.PathEscape(in.ID)+"/withdraw", nil, &ap); err != nil {
			return nil, nil, err
		}
		return text("Withdrawn: the owner won't be asked about it, and it does not run."), nil, nil
	}
	var list []model.Approval
	if err := t.c.Post(ctx, "/api/guard/holds/withdraw", nil, &list); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Withdrew %d waiting hold(s); none of them runs.", len(list))), nil, nil
}

func (t *Tools) askOwner(ctx context.Context, _ *sdk.CallToolRequest, in askArgs) (*sdk.CallToolResult, any, error) {
	var ap model.Approval
	if err := t.c.Post(ctx, "/api/approvals", in, &ap); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Asked the owner (question %s). Carry on with other work; the answer arrives as a message.", ap.ID)), nil, nil
}

func (t *Tools) threadSet(ctx context.Context, _ *sdk.CallToolRequest, in threadArgs) (*sdk.CallToolResult, any, error) {
	var out struct {
		Thread model.Thread `json:"thread"`
		Topics []string     `json:"topics"`
	}
	if err := t.c.Post(ctx, "/api/board/file", map[string]string{"title": in.Topic}, &out); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("This turn is filed under %q. Topics on the board: %s.", out.Thread.Title, strings.Join(out.Topics, ", "))), nil, nil
}

type threadInfo struct {
	model.Thread
	Cards   int `json:"cards"`
	Entries int `json:"entries"`
}

type threadAddArgs struct {
	Kind     string `json:"kind" jsonschema:"start, decision, result, blocker, handoff, ask, pause, done, action, info or link"`
	Text     string `json:"text" jsonschema:"one to three lines, at most 400 characters, plain language; never a secret"`
	Topic    string `json:"topic,omitempty" jsonschema:"head: the topic (default: this turn's); other agents: empty for your project's thread, or the title of a thread you are on"`
	Link     string `json:"link,omitempty" jsonschema:"a URL or file path the line refers to"`
	Corrects string `json:"corrects,omitempty" jsonschema:"the id of the line this one corrects (from thread_read)"`
}

type threadReadArgs struct {
	Topic string `json:"topic,omitempty" jsonschema:"head: the topic (default: this turn's); other agents: leave empty (your project's log)"`
}

func (t *Tools) threads(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
	var list []threadInfo
	if err := t.c.Get(ctx, "/api/threads", &list); err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	ordered := false
	for _, th := range list {
		ordered = ordered || th.Position > 0
	}
	if ordered {
		b.WriteString("Topics in the owner's order of urgency, most urgent first:\n")
	} else {
		b.WriteString("Topics (the owner has not ordered them yet; most recently active first):\n")
	}
	for i, th := range list {
		title := peertext.Field(th.Title) // agents and chats' members name topics: one line, no brackets
		fmt.Fprintf(&b, "%d. %s (%s): %d %s, %d log %s", i+1, title, th.Kind, th.Cards, plural(th.Cards, "card", "cards"),
			th.Entries, plural(th.Entries, "line", "lines"))
		if th.Archived {
			b.WriteString(", archived")
		}
		b.WriteString("\n")
	}
	return untrusted(strings.TrimSpace(b.String())), nil, nil
}

func (t *Tools) threadUpdate(ctx context.Context, _ *sdk.CallToolRequest, in threadUpdateArgs) (*sdk.CallToolResult, any, error) {
	path := "/api/threads/" + client.Seg(in.Topic)
	var th model.Thread
	if in.Delete {
		if err := t.c.Do(ctx, "DELETE", path, nil, &th); err != nil {
			return nil, nil, err
		}
		return text(fmt.Sprintf("Deleted the topic %q; its cards are now under General.", th.Title)), nil, nil
	}
	body := map[string]any{}
	if in.Title != "" {
		body["title"] = in.Title
	}
	if in.Archived != nil {
		body["archived"] = *in.Archived
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("say what to change: title, archived or delete")
	}
	if err := t.c.Do(ctx, "PATCH", path, body, &th); err != nil {
		return nil, nil, err
	}
	state := "on the board"
	if th.Archived {
		state = "archived"
	}
	return text(fmt.Sprintf("The topic %q is %s.", th.Title, state)), nil, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

type logEntry struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Link     string `json:"link"`
	By       string `json:"by"`
	At       int64  `json:"at"`
	Corrects string `json:"corrects"`
}

func (t *Tools) threadAdd(ctx context.Context, _ *sdk.CallToolRequest, in threadAddArgs) (*sdk.CallToolResult, any, error) {
	var e logEntry
	body := map[string]any{"thread": in.Topic, "kind": in.Kind, "text": in.Text, "link": in.Link, "corrects": in.Corrects}
	if err := t.c.Post(ctx, "/api/board/log", body, &e); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Filed (%s) [%s].", e.Kind, e.ID)), nil, nil
}

func (t *Tools) threadRead(ctx context.Context, _ *sdk.CallToolRequest, in threadReadArgs) (*sdk.CallToolResult, any, error) {
	var out struct {
		Thread  model.Thread `json:"thread"`
		Entries []logEntry   `json:"entries"`
	}
	if err := t.c.Get(ctx, "/api/board/log?thread="+url.QueryEscape(in.Topic), &out); err != nil {
		return nil, nil, err
	}
	if len(out.Entries) == 0 {
		return text(fmt.Sprintf("The log of %q is empty.", out.Thread.Title)), nil, nil
	}
	corrected := map[string]string{}
	for _, e := range out.Entries {
		if e.Corrects != "" {
			corrected[e.Corrects] = e.ID
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Log of %q, oldest first:\n", out.Thread.Title)
	for _, e := range out.Entries {
		line := fmt.Sprintf("- %s · %s · %s (%s)", time.UnixMilli(e.At).Local().Format("2 Jan 15:04"), e.Kind, peertext.Inline(e.Text, 0), e.By) // mail lines included: one line each
		if e.Link != "" {
			line += " → " + e.Link
		}
		line += " [" + e.ID + "]"
		if by, ok := corrected[e.ID]; ok {
			line += " (corrected by " + by + ")"
		}
		b.WriteString(line + "\n")
	}
	return untrusted(strings.TrimSpace(b.String())), nil, nil
}
