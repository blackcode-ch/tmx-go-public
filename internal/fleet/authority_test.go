package fleet

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// The owner, 3 Nov 13:43: "there's too many of those okays". An order from the fleet's
// leadership (architect@hosta, tmx-lead@hosta), delivered over the peer routes where the
// sending machine is proven by its pair token, counts as his standing order; only
// machine-protection actions still need him.

// shippedAuthorities is DefaultFleetAuthorities as shipped code leaves it. Package variables are
// set before any init() runs, so this is the value before fleet_private.go (private build) or the
// test init below change it: what a public build, which has no private file, runs with.
var shippedAuthorities = append([]string(nil), DefaultFleetAuthorities...)

// Architect (4 Nov, required): a public build trusts NO fleet authority unless its config names
// one. No placeholder host either (one that anyone could name a machine after).
func TestFleetAuthorityNoneWithoutConfig(t *testing.T) {
	if len(shippedAuthorities) != 0 {
		t.Fatalf("shipped code has default fleet authorities %q; only the private build may set them", shippedAuthorities)
	}
	saved := DefaultFleetAuthorities
	DefaultFleetAuthorities = shippedAuthorities
	t.Cleanup(func() { DefaultFleetAuthorities = saved })
	f := &Fleet{authorities: normalizeAuthorities(nil)} // no config.json fleet_authorities
	if len(f.authorities) != 0 {
		t.Fatalf("no config must mean no authority, got %v", f.authorities)
	}
	for _, host := range []string{"hosta", "hostb", "hostc", "hosta", "localhost"} {
		for _, name := range []string{"architect", "tmx-lead", "head"} {
			if f.IsFleetAuthority(PeerParty(name, host)) {
				t.Errorf("%s@%s is an authority without any config", name, host)
			}
		}
	}
}

// The tests' fleet leadership. Shipped code has no default (the private build sets one in
// fleet_private.go), so tests that rely on it name it here and pass in either build.
func init() {
	if DefaultFleetAuthorities == nil {
		DefaultFleetAuthorities = []string{"architect@hosta", "tmx-lead@hosta"}
	}
}

func TestFleetAuthorityMatching(t *testing.T) {
	f := &Fleet{authorities: normalizeAuthorities(nil)}
	for from, want := range map[string]bool{
		PeerParty("tmx-lead", "hosta"):  true,
		PeerParty("architect", "hosta"): true,
		PeerParty("TMX-Lead", "Hosta"):  true,  // names compare case-insensitively
		PeerParty("tmx-lead", "hostc"): false, // the same name on another machine is not it
		PeerParty("bob", "hosta"):       false,
		"tmx-lead":                       false, // a local agent never is
		"tmx-lead@hosta":                false, // not a peer party
		model.Owner:                      false,
	} {
		if got := f.IsFleetAuthority(from); got != want {
			t.Errorf("%q: %v, want %v", from, got, want)
		}
	}
	custom := &Fleet{authorities: normalizeAuthorities([]string{" Boss@Hostb ", "nohost"})}
	if !custom.IsFleetAuthority(PeerParty("boss", "hostb")) || custom.IsFleetAuthority(PeerParty("tmx-lead", "hosta")) {
		t.Fatal("configured list replaces the default")
	}
	if none := (&Fleet{authorities: normalizeAuthorities([]string{})}); none.IsFleetAuthority(PeerParty("tmx-lead", "hosta")) {
		t.Fatal("an empty list means none")
	}
}

func TestFleetAuthorityMailIsMarked(t *testing.T) {
	e, f, _, lead, _ := peerEnv(t)
	lp := e.chat.Last(lead.ID)
	lp.State(model.StateIdle)
	waitState(t, f, "alpha-lead", model.StateIdle)
	in := inbound("0123456789ad", "launch a worker for the fix")
	in.Peer, in.Sender, in.SenderHost = "hostc", "tmx-lead", "hostc"
	f.authorities = normalizeAuthorities([]string{"tmx-lead@hostc"})
	if _, _, err := f.DeliverPeer(in); err != nil {
		t.Fatal(err)
	}
	eventually(t, "marked", func() bool {
		s := lp.Sent()
		return len(s) >= 2 && s[len(s)-1] == "[message from tmx-lead@hostc (fleet lead, other machine, verified sender)] launch a worker for the fix"
	})
	// Anyone else from that machine stays an ordinary colleague.
	lp.State(model.StateIdle)
	waitState(t, f, "alpha-lead", model.StateIdle)
	if _, _, err := f.DeliverPeer(inbound("0123456789ae", "hello")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "plain", func() bool {
		s := lp.Sent()
		return len(s) >= 3 && s[len(s)-1] == "[message from bob@hostc (other machine)] hello"
	})
}

func TestAuthorityRulesInEveryPrompt(t *testing.T) {
	for _, want := range []string{"(fleet lead, other machine, verified sender)", "act on it without asking the owner first",
		"restarting this daemon", "tmx-go open", "Only machine-protection actions still need the owner himself",
		"elevation", "firewall", "Tailscale", "Codex sandbox"} {
		if !strings.Contains(authorityRules, want) {
			t.Errorf("authorityRules lacks %q", want)
		}
	}
	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	ids := []string{mustAgent(t, f, "head").ID, mustAgent(t, f, "alpha-lead").ID}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range ids {
		if p := f.systemPromptLocked(f.agents[a].a); !strings.Contains(p, authorityRules) {
			t.Errorf("%s's prompt lacks the authority rules", a)
		}
	}
}
