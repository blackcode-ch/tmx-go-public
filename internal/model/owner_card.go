package model

import (
	"bytes"
	"encoding/json"
)

// CardEffect describes what a human action touches. It is computed by the server,
// not an authorization or a promise that an action is safe.
type CardEffect string

const (
	CardEffectNone    CardEffect = "none"
	CardEffectMoney   CardEffect = "money"
	CardEffectOutward CardEffect = "outward"
	CardEffectAccount CardEffect = "account"
)

type CardAction struct {
	Goal       string     `json:"goal"`
	Steps      []string   `json:"steps"`
	Account    string     `json:"account,omitempty"`
	Success    string     `json:"success"`
	DoNotTouch []string   `json:"do_not_touch,omitempty"`
	Effects    CardEffect `json:"effects"`
}

// OwnerCard is the typed content of an ask_owner card. Version changes when
// content or deferral state changes, not when it is answered.
type OwnerCard struct {
	Origin      *ImportOrigin `json:"origin,omitempty"`
	ProjectID   string        `json:"project_id,omitempty"` // resolved by the server
	Priority    int           `json:"priority,omitempty"`
	Urgent      bool          `json:"urgent,omitempty"`
	OwnerOnly   bool          `json:"owner_only,omitempty"`
	Batchable   bool          `json:"batchable,omitempty"`
	CloseWhen   string        `json:"close_when,omitempty"`
	CloseReason string        `json:"close_reason,omitempty"`

	Type           string      `json:"type"` // decision | human_action
	Context        string      `json:"context,omitempty"`
	Recommendation string      `json:"recommendation,omitempty"`
	Category       string      `json:"category,omitempty"`
	Action         *CardAction `json:"action,omitempty"`
}

// LegacyOwnerOnlyKeys are earlier JSON names of owner_only. A card is read with any of them and,
// when owner-only, written with them too, so stored cards, peers on an older version and a
// rolled-back binary keep agreeing. Set by the private build (owner_card_private.go); a public
// snapshot has none, so shipped code holds no private name (scrub-neutral, tmx-lead 4 Nov).
var LegacyOwnerOnlyKeys []string

// LegacyOwnerOnly reports whether the JSON object b sets owner_only under a legacy key.
func LegacyOwnerOnly(b []byte) bool {
	if len(LegacyOwnerOnlyKeys) == 0 {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	for _, k := range LegacyOwnerOnlyKeys {
		var v bool
		if raw, ok := m[k]; ok && json.Unmarshal(raw, &v) == nil && v {
			return true
		}
	}
	return false
}

func (c OwnerCard) MarshalJSON() ([]byte, error) {
	type plain OwnerCard
	b, err := json.Marshal(plain(c))
	if err != nil || !c.OwnerOnly || len(LegacyOwnerOnlyKeys) == 0 {
		return b, err
	}
	// b is a non-empty object ("type" is always present): add the legacy keys before its "}".
	var out bytes.Buffer
	out.Write(b[:len(b)-1])
	for _, k := range LegacyOwnerOnlyKeys {
		kb, _ := json.Marshal(k)
		out.WriteByte(',')
		out.Write(kb)
		out.WriteString(":true")
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func (c *OwnerCard) UnmarshalJSON(b []byte) error {
	type plain OwnerCard
	if err := json.Unmarshal(b, (*plain)(c)); err != nil {
		return err
	}
	c.OwnerOnly = c.OwnerOnly || LegacyOwnerOnly(b)
	return nil
}
