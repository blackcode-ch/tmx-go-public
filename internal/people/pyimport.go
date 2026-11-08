package people

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Importing Python tmx's People (team.py): <state>/team/team.json and threads/<key>.jsonl.
// The files are only read, never changed or moved (the originals are kept). Promise IDs are
// derived from the Python IDs, so running the import again changes nothing.

// Plan is what one import would write.
type Plan struct {
	People   []model.Person  `json:"people"`
	Promises []model.Promise `json:"promises"`
	// Reminded: promise ID -> when Python already sent its overdue reminder (unix ms); such a
	// promise gets a fired reminder record, so it is never reminded again.
	Reminded map[string]int64 `json:"reminded,omitempty"`
	// Skipped counts what is not ported (plain outbox messages, pasted replies).
	SkippedMessages int `json:"skipped_messages"`
}

// Counts are the plan's numbers by kind (no names): the report and the post-import check.
type Counts struct {
	Team, External, Clients                     int
	PromisesOpen, PromisesDone, PromisesDropped int
	Reminded, Skipped                           int
}

func (p Plan) Counts() Counts {
	var c Counts
	for _, x := range p.People {
		switch x.Kind {
		case "client":
			c.Clients++
		case "external":
			c.External++
		default:
			c.Team++
		}
	}
	for _, x := range p.Promises {
		switch x.Status {
		case Open:
			c.PromisesOpen++
		case Done:
			c.PromisesDone++
		default:
			c.PromisesDropped++
		}
	}
	c.Reminded, c.Skipped = len(p.Reminded), p.SkippedMessages
	return c
}

func (c Counts) String() string {
	return fmt.Sprintf("people: %d team, %d external, %d clients; promises: %d open, %d done, %d dropped; already reminded: %d; messages not ported: %d",
		c.Team, c.External, c.Clients, c.PromisesOpen, c.PromisesDone, c.PromisesDropped, c.Reminded, c.Skipped)
}

type pyPerson struct {
	Key       string            `json:"key"`
	Name      string            `json:"name"`
	Role      string            `json:"role"`
	Lang      string            `json:"lang"`
	Kind      string            `json:"kind"`
	Preferred string            `json:"preferred"`
	Aliases   []string          `json:"aliases"`
	Channels  map[string]string `json:"channels"`
}

// ReadPython reads a Python state dir (the folder that holds team/). Any malformed file is an
// error, never a partial import.
func ReadPython(stateDir string) (Plan, error) {
	plan := Plan{Reminded: map[string]int64{}}
	dir := filepath.Join(stateDir, "team")
	raw, err := os.ReadFile(filepath.Join(dir, "team.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	var doc struct {
		Team []pyPerson `json:"team"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return plan, fmt.Errorf("team.json is not valid: %w", err)
	}
	seen := map[string]bool{}
	for i, e := range doc.Team {
		if ValidKey(e.Key) != nil || seen[e.Key] {
			return plan, fmt.Errorf("team.json: entry %d has a bad or duplicate key", i+1)
		}
		seen[e.Key] = true
		kind, err := NormKind(e.Kind)
		if err != nil {
			return plan, fmt.Errorf("team.json: entry %d: %v", i+1, err)
		}
		p := model.Person{Key: e.Key, Name: e.Name, Role: e.Role, Lang: e.Lang, Kind: kind, Preferred: e.Preferred, Aliases: e.Aliases, Channels: map[string]string{}}
		for ch, v := range e.Channels {
			if v != "" && (ch == "whatsapp" || ch == "email" || ch == "github") {
				p.Channels[ch] = v
			}
		}
		plan.People = append(plan.People, p)
		if err := readThread(&plan, filepath.Join(dir, "threads", e.Key+".jsonl"), e.Key); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

// readThread folds the log like team.items(): the first record of an id carries every field, a
// later one overlays the fields it has; batch records ({"ids": [...]}) set a status on each id.
func readThread(plan *Plan, path, key string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := bytes.Split(data, []byte("\n"))
	lines = lines[:len(lines)-1] // after the last newline: a torn tail or nothing (Python ignores it too)
	byID := map[string]map[string]any{}
	var order []string
	for n, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("the thread of %s has an unreadable line %d", key, n+1)
		}
		if ids, ok := rec["ids"].([]any); ok {
			for _, x := range ids {
				if id, _ := x.(string); byID[id] != nil {
					byID[id]["status"] = rec["status"]
				}
			}
			continue
		}
		id, _ := rec["id"].(string)
		if id == "" {
			continue
		}
		if byID[id] == nil {
			byID[id] = rec
			order = append(order, id)
			continue
		}
		for k, v := range rec {
			if k != "id" && k != "at" {
				byID[id][k] = v
			}
		}
	}
	for _, id := range order {
		it := byID[id]
		if ask, _ := it["ask"].(bool); !ask {
			plan.SkippedMessages++
			continue
		}
		str := func(k string) string { s, _ := it[k].(string); return s }
		at := int64(num(it["at"]) * 1000)
		status := Open
		switch str("status") {
		case "answered", "done":
			status = Done
		case "dropped":
			status = Dropped
		}
		title := str("title")
		if title == "" {
			title = firstLine(str("text"), 120)
		}
		due, err := Due(str("due"))
		if err != nil {
			due = ""
		}
		owner := str("owner")
		if owner == "" {
			owner = str("from")
		}
		pid := "py-" + key + "-" + id
		plan.Promises = append(plan.Promises, model.Promise{ID: pid, PersonKey: key, Title: title, Text: str("text"),
			Owner: owner, Due: due, Status: status, Source: "python", CreatedAt: at, UpdatedAt: at})
		if r := num(it["reminded_at"]); r > 0 {
			plan.Reminded[pid] = int64(r * 1000)
		}
	}
	return nil
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	if s == "" {
		s = "(untitled ask)"
	}
	return s
}
