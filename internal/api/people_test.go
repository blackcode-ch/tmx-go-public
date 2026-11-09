package api_test

import (
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/people"
)

func TestPeopleRoutes(t *testing.T) {
	d := apitest.New(t)
	o := client{t: t, base: d.URL, token: d.OwnerToken}
	var a model.Agent
	if code := o.do("POST", "/api/agents", map[string]any{"name": "pworker", "project_id": workerProject(t, d)}, &a); code != 201 {
		t.Fatal(code)
	}
	w := client{t: t, base: d.URL, token: d.AgentToken(t, a.ID)}

	var p model.Person
	if code := o.do("POST", "/api/team", map[string]any{"key": "sam", "name": "Sam", "kind": "client",
		"channels": map[string]string{"whatsapp": "+41 79 000 00 00", "email": "sam@example.test"}}, &p); code != 200 || p.Channels["whatsapp"] != "41790000000" {
		t.Fatalf("add: %d %+v", code, p)
	}
	if code := o.do("POST", "/api/team", map[string]any{"key": "kim", "name": "Kim"}, nil); code != 200 {
		t.Fatal(code)
	}
	if code := o.do("POST", "/api/team", map[string]any{"key": "bad key"}, nil); code != 400 {
		t.Fatalf("bad key: %d", code)
	}
	if code := o.do("POST", "/api/team", map[string]any{"key": "kim", "aliases": []string{"sam"}}, nil); code != 409 {
		t.Fatalf("alias collision: %d", code)
	}
	if code := o.do("POST", "/api/team", map[string]any{"key": "kim", "channels": map[string]string{"email": "x@y.ch?bcc=z@w"}}, nil); code != 400 {
		t.Fatalf("bad email: %d", code)
	}
	var clients []model.Person
	if o.do("GET", "/api/team?kind=client", nil, &clients); len(clients) != 1 || clients[0].Key != "sam" {
		t.Fatalf("clients: %+v", clients)
	}

	// An agent: public view only, cannot edit.
	var pub []map[string]any
	if code := w.do("GET", "/api/team", nil, &pub); code != 200 || len(pub) != 2 {
		t.Fatalf("agent list: %d %v", code, pub)
	}
	for _, e := range pub {
		if _, ok := e["channels"]; ok {
			t.Fatalf("channels leaked to an agent: %v", e)
		}
	}
	if pub[0]["has_whatsapp"] != false && pub[1]["has_whatsapp"] != true {
		t.Fatalf("has_whatsapp: %v", pub)
	}
	if code := w.do("POST", "/api/team", map[string]any{"key": "sam", "name": "X"}, nil); code != 403 {
		t.Fatalf("agent edit: %d", code)
	}
	if code := w.do("DELETE", "/api/team/sam", nil, nil); code != 403 {
		t.Fatalf("agent delete: %d", code)
	}

	// Promises: an agent opens one (owner = itself); a past due date reminds it on the next tick.
	var pr model.Promise
	if code := w.do("POST", "/api/promises", map[string]any{"person_key": "sam", "title": "Send the quote", "due": "2026-01-02"}, &pr); code != 201 || pr.Owner != "pworker" || pr.Status != "open" {
		t.Fatalf("promise: %d %+v", code, pr)
	}
	if code := w.do("POST", "/api/promises", map[string]any{"person_key": "nobody", "title": "x"}, nil); code != 404 {
		t.Fatalf("unknown person: %d", code)
	}
	if code := w.do("POST", "/api/promises", map[string]any{"person_key": "sam", "title": "x", "due": "soon"}, nil); code != 400 {
		t.Fatalf("bad due: %d", code)
	}
	// The daemon's own scheduler loop is running; a manual tick must not double it.
	waitMail(t, d, o, a.ID, "Send the quote")
	if n, _ := d.Fleet.FireDueReminders(); n != 0 || mailCount(t, o, a.ID, "Send the quote") != 1 {
		t.Fatal("fired twice")
	}
	var pubOne map[string]any
	w.do("GET", "/api/team/sam", nil, &pubOne)
	if _, ok := pubOne["channels"]; ok {
		t.Fatal("channels leaked on GET one")
	}
	if code := w.do("POST", "/api/promises/"+pr.ID, map[string]any{"status": "done"}, &pr); code != 200 || pr.Status != "done" {
		t.Fatalf("close: %d %+v", code, pr)
	}

	// Reminders: an agent sets one for itself only.
	var rm model.Reminder
	if code := w.do("POST", "/api/reminders", map[string]any{"in": 3600, "text": "check the build"}, &rm); code != 201 || rm.Target != a.ID {
		t.Fatalf("reminder: %d %+v", code, rm)
	}
	if code := w.do("POST", "/api/reminders", map[string]any{"in": 60, "text": "x", "target": "head"}, nil); code != 403 {
		t.Fatalf("remind other: %d", code)
	}
	if code := w.do("POST", "/api/reminders", map[string]any{"text": "no time"}, nil); code != 400 {
		t.Fatalf("no time: %d", code)
	}
	var mine []model.Reminder
	if w.do("GET", "/api/reminders?state=pending", nil, &mine); len(mine) != 1 {
		t.Fatalf("list: %+v", mine)
	}
	if code := w.do("DELETE", "/api/reminders/"+rm.ID, nil, nil); code != 200 {
		t.Fatalf("cancel: %d", code)
	}
	if code := w.do("DELETE", "/api/reminders/"+rm.ID, nil, nil); code != 409 {
		t.Fatalf("cancel twice: %d", code)
	}
	if code := o.do("POST", "/api/reminders", map[string]any{"at": time.Now().Add(-time.Second).UnixMilli(), "text": "owner nudge", "target": "pworker"}, nil); code != 201 {
		t.Fatalf("owner reminder: %d", code)
	}
	waitMail(t, d, o, a.ID, "owner nudge")

	// Removing a person removes their promises.
	if code := o.do("DELETE", "/api/team/sam", nil, nil); code != 200 {
		t.Fatal(code)
	}
	var left []model.Promise
	if o.do("GET", "/api/promises?person=sam", nil, &left); len(left) != 0 {
		t.Fatalf("promises left: %+v", left)
	}
}

func waitMail(t *testing.T, d *apitest.Daemon, o client, agentID, text string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, err := d.Fleet.FireDueReminders(); err != nil {
			t.Fatal(err)
		}
		if mailCount(t, o, agentID, text) == 1 {
			return
		}
	}
	t.Fatalf("reminder %q not delivered", text)
}

func mailCount(t *testing.T, o client, agentID, text string) int {
	t.Helper()
	var msgs []model.Message
	o.do("GET", "/api/messages?agent="+agentID+"&limit=50", nil, &msgs)
	n := 0
	for _, m := range msgs {
		if m.From == model.ReminderParty && m.To == agentID && strings.Contains(m.Body, text) {
			n++
		}
	}
	return n
}

func TestPeopleImport(t *testing.T) {
	d := apitest.New(t)
	o := client{t: t, base: d.URL, token: d.OwnerToken}
	plan, err := people.ReadPython("../people/testdata/py")
	if err != nil {
		t.Fatal(err)
	}
	var a model.Agent
	if code := o.do("POST", "/api/agents", map[string]any{"name": "worker1", "project_id": workerProject(t, d)}, &a); code != 201 {
		t.Fatal(code)
	}
	w := client{t: t, base: d.URL, token: d.AgentToken(t, a.ID)}
	if code := w.do("POST", "/api/team/import", plan, nil); code != 403 {
		t.Fatalf("agent import: %d", code)
	}
	var rep fleet.ImportReport
	for i := 0; i < 2; i++ { // the second run changes nothing
		if code := o.do("POST", "/api/team/import", plan, &rep); code != 200 || !rep.Verified || rep.Planned != rep.Stored {
			t.Fatalf("run %d: %d %+v", i, code, rep)
		}
	}
	var all []model.Person
	o.do("GET", "/api/team", nil, &all)
	if len(all) != 4 {
		t.Fatalf("people: %d", len(all))
	}
	var prs []model.Promise
	o.do("GET", "/api/promises", nil, &prs)
	if len(prs) != 4 {
		t.Fatalf("promises: %d", len(prs))
	}
	// a1 was reminded by Python: a fired record, never sent again; a4 (future) is pending for worker1.
	var rems []model.Reminder
	o.do("GET", "/api/reminders", nil, &rems)
	states := map[string]string{}
	for _, r := range rems {
		states[r.PromiseID] = r.State
	}
	if states["py-ana-a1"] != "fired" || states["py-ana-a4"] != "pending" || len(rems) != 2 {
		t.Fatalf("reminders: %+v", rems)
	}
	if n, _ := d.Fleet.FireDueReminders(); n != 0 || mailCount(t, o, a.ID, "Mockups") != 0 {
		t.Fatal("an already-reminded ask was reminded again")
	}
	// An edit made here survives a re-import.
	o.do("POST", "/api/team", map[string]any{"key": "bo", "kind": "team"}, nil)
	o.do("POST", "/api/team/import", plan, &rep)
	var bo model.Person
	if o.do("GET", "/api/team/bo", nil, &bo); bo.Kind != "team" {
		t.Fatalf("re-import overwrote an edit: %+v", bo)
	}
}

// o6: an agent sees and changes only promises it opened or is assigned; only the owner or the
// promise's own agent can move its reminder to another agent; a peer sees none.
func TestPromiseOwnership(t *testing.T) {
	d := apitest.New(t)
	o := client{t: t, base: d.URL, token: d.OwnerToken}
	proj := workerProject(t, d)
	var a, b model.Agent
	if code := o.do("POST", "/api/agents", map[string]any{"name": "pa", "project_id": proj}, &a); code != 201 {
		t.Fatal(code)
	}
	if code := o.do("POST", "/api/agents", map[string]any{"name": "pb", "project_id": proj}, &b); code != 201 {
		t.Fatal(code)
	}
	wa := client{t: t, base: d.URL, token: d.AgentToken(t, a.ID)}
	wb := client{t: t, base: d.URL, token: d.AgentToken(t, b.ID)}
	if code := o.do("POST", "/api/team", map[string]any{"key": "sam", "name": "Sam"}, nil); code != 200 {
		t.Fatal(code)
	}

	var pa, po, pAssigned model.Promise
	if code := wa.do("POST", "/api/promises", map[string]any{"person_key": "sam", "title": "A's"}, &pa); code != 201 || pa.CreatedBy != a.ID {
		t.Fatalf("a open: %d %+v", code, pa)
	}
	if code := o.do("POST", "/api/promises", map[string]any{"person_key": "sam", "title": "owner's"}, &po); code != 201 {
		t.Fatal(code)
	}
	if code := o.do("POST", "/api/promises", map[string]any{"person_key": "sam", "title": "for b", "owner": "pb"}, &pAssigned); code != 201 {
		t.Fatal(code)
	}
	// An agent cannot open a promise in another agent's name.
	if code := wa.do("POST", "/api/promises", map[string]any{"person_key": "sam", "title": "x", "owner": "pb"}, nil); code != 403 {
		t.Fatalf("a opens for b: %d", code)
	}

	// (2) listing
	titles := func(c client) []string {
		var l []model.Promise
		if code := c.do("GET", "/api/promises", nil, &l); code != 200 {
			t.Fatalf("list: %d", code)
		}
		var s []string
		for _, p := range l {
			s = append(s, p.Title)
		}
		return s
	}
	if got := titles(o); len(got) != 3 {
		t.Fatalf("owner sees %v", got)
	}
	if got := titles(wa); len(got) != 1 || got[0] != "A's" {
		t.Fatalf("a sees %v", got)
	}
	if got := titles(wb); len(got) != 1 || got[0] != "for b" {
		t.Fatalf("b sees %v", got)
	}
	if _, err := d.Fleet.Promises(fleet.PeerCaller("hosta"), "", ""); err == nil {
		t.Fatal("a peer listed promises")
	}
	if _, err := d.Fleet.Promises(fleet.CompanionCaller, "", ""); err == nil {
		t.Fatal("companion listed promises")
	}

	// (1) editing / closing
	if code := wb.do("POST", "/api/promises/"+pa.ID, map[string]any{"status": "done"}, nil); code != 404 {
		t.Fatalf("b closes a's: %d", code)
	}
	if code := wa.do("POST", "/api/promises/"+po.ID, map[string]any{"title": "hijack"}, nil); code != 404 {
		t.Fatalf("a edits owner's: %d", code)
	}
	if code := wa.do("POST", "/api/promises/"+pAssigned.ID, map[string]any{"status": "dropped"}, nil); code != 404 {
		t.Fatalf("a closes b's: %d", code)
	}
	if code := wb.do("POST", "/api/promises/"+pAssigned.ID, map[string]any{"status": "done"}, nil); code != 200 {
		t.Fatalf("b closes its assigned: %d", code)
	}
	if code := o.do("POST", "/api/promises/"+pa.ID, map[string]any{"title": "owner edit"}, nil); code != 200 {
		t.Fatalf("owner edits any: %d", code)
	}

	// Redirecting the reminder target: b can't take over a's promise, a (its own agent) may hand it on.
	if code := wb.do("POST", "/api/promises/"+pa.ID, map[string]any{"owner": "pb"}, nil); code != 404 {
		t.Fatalf("b grabs a's: %d", code)
	}
	var moved model.Promise
	if code := wa.do("POST", "/api/promises/"+pa.ID, map[string]any{"owner": "pb", "due": "2026-01-02"}, &moved); code != 200 || moved.Owner != "pb" {
		t.Fatalf("a hands on: %d %+v", code, moved)
	}
	waitMail(t, d, o, b.ID, "owner edit") // the due reminder now goes to b
	// The owner may redirect any promise.
	if code := o.do("POST", "/api/promises/"+po.ID, map[string]any{"owner": "pa"}, nil); code != 200 {
		t.Fatalf("owner redirect: %d", code)
	}
	// a, still the creator, may take it back to its own inbox.
	if code := wa.do("POST", "/api/promises/"+pa.ID, map[string]any{"owner": "pa"}, nil); code != 200 {
		t.Fatalf("a takes back: %d", code)
	}
	if code := wa.do("POST", "/api/promises/"+po.ID, map[string]any{"owner": "pb"}, nil); code != 200 {
		t.Fatalf("a (assigned by owner) hands on: %d", code)
	}
}
