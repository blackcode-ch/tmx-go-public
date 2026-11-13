package fleet

import (
	"encoding/json"
	"strings"
	"testing"
)

// People (people.go) is where the owner keeps the team: a team member whose entry names shared
// projects is a teammate of those projects, recognised by the WhatsApp number or tmx peer ID
// the owner entered. Clients and team members without projects are not; agents see the projects
// but never the number or the peer ID.
func TestTeammatesFromPeople(t *testing.T) {
	f := newEnv(t).started()
	for _, name := range []string{"Dev Tools", "Beta"} {
		if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	str := func(s string) *string { return &s }
	if _, err := f.PutPerson(OwnerCaller, PersonInput{Key: "alex", Name: str("Alex"), Kind: str("team"),
		Channels: map[string]string{"whatsapp": "+41 79 111 11 11", "tmx": alexPeer},
		Projects: []string{"dev-tools", "Beta", " Beta ", ""}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.PutPerson(OwnerCaller, PersonInput{Key: "carla", Kind: str("client"),
		Channels: map[string]string{"whatsapp": "+41 79 222 22 22"}, Projects: []string{"dev-tools"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.PutPerson(OwnerCaller, PersonInput{Key: "dan", Kind: str("team"),
		Channels: map[string]string{"whatsapp": "+41 79 333 33 33"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.PutPerson(OwnerCaller, PersonInput{Key: "eve", Kind: str("team"),
		Channels: map[string]string{"tmx": "short"}, Projects: []string{"Beta"}}); err == nil {
		t.Fatal("a tmx channel that is no peer ID was accepted")
	}
	for _, tc := range []struct {
		name, peer, jid string
		projects        string
		ok              bool
	}{
		{"team member by tmx peer ID", alexPeer, "", "Beta|Dev Tools", true},
		{"team member by WhatsApp number", "", "41791111111@s.whatsapp.net", "Beta|Dev Tools", true},
		{"the same number from another device", "", "41791111111:7@s.whatsapp.net", "Beta|Dev Tools", true},
		{"a client with projects", "", "41792222222@s.whatsapp.net", "", false},
		{"a team member without projects", "", "41793333333@s.whatsapp.net", "", false},
		{"a group JID", "", "120363000000000001@g.us", "", false},
		{"an unknown number", "", "41790000000@s.whatsapp.net", "", false},
	} {
		got, ok := f.TeammateOf(tc.peer, tc.jid)
		if ok != tc.ok || strings.Join(got.Projects, "|") != tc.projects || (ok && got.Name != "Alex") {
			t.Errorf("%s: TeammateOf(%q, %q) = %+v, %v; want projects %q, %v", tc.name, tc.peer, tc.jid, got, ok, tc.projects, tc.ok)
		}
	}
	// Agents learn the rules once People marks someone, with no config.json list at all.
	head := mustAgent(t, f, "head")
	f.mu.Lock()
	prompt := f.systemPromptLocked(f.agents[head.ID].a)
	f.mu.Unlock()
	if !strings.Contains(prompt, teammateRules) {
		t.Error("the head's prompt lacks the teammate rules although People marks a teammate")
	}
	// Agents see that Alex is a teammate of those projects, never the number or the peer ID.
	pub, err := f.Person(AgentCaller(head.ID), "alex")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(pub)
	for _, want := range []string{`"has_tmx":true`, `"has_whatsapp":true`, `"projects":["dev-tools","Beta"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("public person lacks %s: %s", want, b)
		}
	}
	for _, secret := range []string{"4179", alexPeer} {
		if strings.Contains(string(b), secret) {
			t.Errorf("public person leaks %q: %s", secret, b)
		}
	}
	// Taking the projects away un-marks the person.
	if _, err := f.PutPerson(OwnerCaller, PersonInput{Key: "alex", Projects: []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.TeammateOf(alexPeer, ""); ok || f.hasTeammates() {
		t.Error("still a teammate without projects")
	}
}
