package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

const updateUsage = `usage: tmx update                 check; a newer release installs once no agent is busy
       tmx update check           only check
       tmx update now [TAG]       install the newest release (or exactly TAG) now (cuts off turns in progress)
       tmx update install TAG     install exactly TAG once no agent is busy (a published release, newer than this one)
       tmx update status
       tmx update auto off        automatic installs are off in this version (a lead installs each machine)
       tmx update keys            the release keys this machine trusts (fingerprints)
       tmx update keys pin ID:BASE64... [--require ed25519[,webauthn]] | --file KEYS.json
                                  replace them (ed25519 keys; --file: {fleet_keys, require} incl. webauthn)
       tmx update channel stable|latest
       tmx update token TOKEN|-   GitHub token for a private release repository (- removes it)
       tmx update drill-rollback on|off
                                  the next install is rolled back on purpose once healthy (drill)`

func cmdUpdate(e *env, args []string) error {
	sub := "default"
	if len(args) > 0 {
		sub = args[0]
	}
	// Installing by hand and changing the settings or the token are behind the owner gate (once
	// it is on): ownerDo waits for the owner's confirmation on this machine and retries.
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var st update.Status
		var err error
		switch sub {
		case "default", "check":
			ctx, cancel := context.WithTimeout(cx, 45*time.Second)
			err = c.Post(ctx, "/api/update/check", nil, &st)
			cancel()
			if err == nil && sub == "default" && st.Available && !st.Auto {
				err = ownerDo(e, c, cx, http.MethodPost, "/api/update/install", map[string]bool{"now": false}, &st)
			}
		case "now", "install":
			body := map[string]any{"now": sub == "now"}
			switch {
			case len(args) == 2:
				body["tag"] = args[1]
			case len(args) > 2, sub == "install" && len(args) != 2:
				return usageErr(updateUsage)
			}
			if err = ownerDo(e, c, cx, http.MethodPost, "/api/update/install", body, &st); err == nil && sub == "now" {
				st, err = waitUpdate(cx, c)
			}
		case "keys":
			return updateKeys(e, c, cx, args[1:])
		case "status":
			err = c.Get(cx, "/api/update", &st)
		case "auto":
			if len(args) == 2 && args[1] == "on" {
				return errors.New("automatic updates are off in this version: a lead installs each machine (tmx update now)")
			}
			if len(args) != 2 || args[1] != "off" {
				return usageErr(updateUsage)
			}
			err = ownerDo(e, c, cx, http.MethodPut, "/api/update", map[string]any{"auto": false}, &st)
		case "channel":
			if len(args) != 2 {
				return usageErr(updateUsage)
			}
			err = ownerDo(e, c, cx, http.MethodPut, "/api/update", map[string]any{"channel": args[1]}, &st)
		case "drill-rollback":
			if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
				return usageErr(updateUsage)
			}
			err = ownerDo(e, c, cx, http.MethodPut, "/api/update/drill", map[string]bool{"arm": args[1] == "on"}, &st)
		case "token":
			if len(args) != 2 {
				return usageErr(updateUsage)
			}
			tok := args[1]
			if tok == "-" {
				tok = ""
			}
			err = ownerDo(e, c, cx, http.MethodPut, "/api/update/token", map[string]string{"token": tok}, &st)
		default:
			return usageErr(updateUsage)
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(e.out, describeUpdate(st))
		return nil
	})
}

// waitUpdate follows an install for up to 15 minutes (downloading, then the restart).
func waitUpdate(cx context.Context, c *client.Client) (update.Status, error) {
	var st update.Status
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		if err := c.Get(cx, "/api/update", &st); err != nil {
			// The daemon is restarting onto the new release.
			return update.Status{State: update.StateInstalling, Latest: st.Latest, Current: st.Current}, nil
		}
		switch st.State {
		case update.StateError, update.StateIdle, update.StateOff:
			return st, nil
		}
		time.Sleep(time.Second)
	}
	return st, nil
}

func describeUpdate(st update.Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tmx %s · channel %s · auto %s", st.Current, st.Channel, map[bool]string{true: "on", false: "off"}[st.Auto])
	if st.Probation != "" {
		fmt.Fprintf(&b, "\n%s is on probation: it must report healthy or the previous version comes back", st.Probation)
	}
	if st.DrillArmed {
		b.WriteString("\nrollback drill armed: the next update is rolled back on purpose once healthy")
	}
	if l := st.LastUpdate; l != nil {
		fmt.Fprintf(&b, "\nlast update %s -> %s: %s", l.From, l.To, l.Outcome)
		if l.Reason != "" {
			fmt.Fprintf(&b, " (%s)", l.Reason)
		}
	}
	switch st.State {
	case update.StateOff:
		fmt.Fprintf(&b, "\nupdates off: %s", st.Reason)
	case update.StateError:
		fmt.Fprintf(&b, "\nerror: %s", st.Error)
	case update.StateIdle:
		if st.Latest != "" {
			b.WriteString("\nup to date")
		}
	case update.StateAvailable:
		fmt.Fprintf(&b, "\n%s is available (%s); install it with: tmx update now (or turn auto on)", st.Latest, st.LatestURL)
	case update.StateDownloading:
		fmt.Fprintf(&b, "\ndownloading and checking %s", st.Latest)
	case update.StateReady:
		fmt.Fprintf(&b, "\n%s is downloaded and checked; it installs once these are done: %s (or: tmx update now)", st.Latest, strings.Join(st.Waiting, ", "))
	case update.StateInstalling:
		fmt.Fprintf(&b, "\ninstalling %s; the daemon restarts on it (agents resume)", st.Latest)
	default:
		fmt.Fprintf(&b, "\n%s", st.State)
	}
	return b.String()
}

// updateKeys shows or replaces the pinned release keys (PUT /api/update/keys: always a fresh
// owner proof). Only public keys and their fingerprints are printed.
func updateKeys(e *env, c *client.Client, cx context.Context, args []string) error {
	var got update.PinnedKeys
	if len(args) == 0 || (len(args) == 1 && args[0] == "show") {
		if err := c.Get(cx, "/api/update/keys", &got); err != nil {
			return err
		}
		fmt.Fprintln(e.out, describeKeys(got))
		return nil
	}
	if args[0] != "pin" {
		return usageErr(updateUsage)
	}
	var req struct {
		FleetKeys []config.FleetKey `json:"fleet_keys"`
		Require   []string          `json:"require,omitempty"`
	}
	rest := args[1:]
	switch {
	case len(rest) == 2 && rest[0] == "--file":
		b, err := os.ReadFile(rest[1])
		if err != nil {
			return err
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return fmt.Errorf("%s: %w", rest[1], err)
		}
	default:
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--require" && i+1 < len(rest) {
				req.Require = strings.Split(rest[i+1], ",")
				i++
				continue
			}
			id, key, ok := strings.Cut(rest[i], ":")
			if t, k, typed := strings.Cut(key, ":"); typed && t == config.KeyEd25519 {
				key = k
			}
			if !ok || id == "" || key == "" || strings.HasPrefix(rest[i], "-") {
				return usageErr(updateUsage)
			}
			req.FleetKeys = append(req.FleetKeys, config.FleetKey{ID: id, Type: config.KeyEd25519, Key: key})
		}
	}
	if len(req.FleetKeys) == 0 {
		return usageErr(updateUsage)
	}
	if err := ownerDo(e, c, cx, http.MethodPut, "/api/update/keys", req, &got); err != nil {
		return err
	}
	fmt.Fprintln(e.out, describeKeys(got))
	return nil
}

func describeKeys(p update.PinnedKeys) string {
	var b strings.Builder
	fmt.Fprintf(&b, "trusted release keys (require %s, rotation serial %d):", strings.Join(p.Require, "+"), p.KeysSerial)
	for _, k := range p.FleetKeys {
		fmt.Fprintf(&b, "\n  %s  %s  %s", k.ID, k.Type, keyFingerprint(k.Key))
	}
	if len(p.FleetKeys) == 0 {
		b.WriteString("\n  none: every release is refused")
	}
	if p.Error != "" {
		fmt.Fprintf(&b, "\nupdates are off: %s", p.Error)
	}
	return b.String()
}

// keyFingerprint is the form the tmx-go page publishes: hex sha256(raw key)[:12].
func keyFingerprint(key string) string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil {
		return "?"
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])[:12]
}
