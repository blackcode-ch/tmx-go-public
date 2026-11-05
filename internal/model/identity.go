package model

import "github.com/google/uuid"

// AgentIdentity is stable across agent/host renames and daemon restarts, but
// changes when an agent is deleted and recreated. Roster and task adapters share
// this derivation from the immutable Go agent ID.
func AgentIdentity(id string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("tmx-go-agent:"+id)).String()
}
