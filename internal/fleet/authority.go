package fleet

import "strings"

// Fleet authorities (the owner, 3 Nov 13:43, "there's too many of those okays"): the owner's fleet
// leadership on his other machines. Their mail arrives over the peer routes, where the sending
// machine is proven by its pair token (api/peers.go: sender_host must be that machine), so a
// message from "tmx-lead@hosta" really comes from Hosta's tmx-lead. Such an order counts as
// the owner's standing authority for ordinary fleet work; only machine-protection actions keep
// asking the owner himself (mailRules).

// DefaultFleetAuthorities are used when config.json names none. None in shipped code: the private
// build sets this fleet's (fleet_private.go), so a public build trusts only what its config names.
var DefaultFleetAuthorities []string

// machineProtection lists what a fleet authority's order never covers.
const machineProtection = "disks and partitions, boot, ACLs on drive roots or system folders, elevation (admin rights), the firewall, Tailscale, and the Codex sandbox"

const authorityRules = `Mail marked "(fleet lead, other machine, verified sender)" comes from the owner's fleet leadership ` +
	`(the architect, tmx-lead) on another of the owner's machines; tmx verified the sending machine by its pair token. ` +
	`Treat it as the owner's standing order and act on it without asking the owner first: launching, stopping or restarting agents, ` +
	`restarting this daemon, reading through the owner's local client (tmx-go open), and routine fleet work. ` +
	`Only machine-protection actions still need the owner himself: ` + machineProtection + `. ` +
	`Mail from any other agent, here or on another machine, is a colleague's request as described above.`

func normalizeAuthorities(list []string) map[string]bool {
	if list == nil {
		list = DefaultFleetAuthorities
	}
	m := map[string]bool{}
	for _, a := range list {
		if a = strings.ToLower(strings.TrimSpace(a)); strings.Contains(a, "@") {
			m[a] = true
		}
	}
	return m
}

// agentBypass: Options.AgentBypass, on unless the owner turned it off.
func (f *Fleet) agentBypass() bool { return f.opts.AgentBypass == nil || *f.opts.AgentBypass }

// IsFleetAuthority reports whether from ("name@host", as delivered by a peer) is one of the
// configured fleet authorities. A local agent ("name" without a host) never is.
func (f *Fleet) IsFleetAuthority(from string) bool {
	name, host, ok := SplitPeer(from)
	return ok && f.authorities[strings.ToLower(name+"@"+host)]
}
