package whatsapp

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/peertext"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

type watchFleet interface {
	WhatsAppWatchTarget(address string) (target string, remote bool, err error)
	QueueWhatsAppWatch(to, key, body string, urgent bool, seen ...string) error
	QueueWhatsAppWatchParts(to, key string, bodies []string, urgent bool, seen ...string) error
}

func (s *Service) wakeWatch() {
	select {
	case s.watchWake <- struct{}{}:
	default:
	}
}

func (s *Service) watchChats() {
	defer s.wg.Done()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if s.ctx.Err() != nil {
			return
		}
		s.forwardWatchedChats(s.o.Fleet.(watchFleet))
		select {
		case <-s.ctx.Done():
			return
		case <-s.watchWake:
		case <-tick.C:
		}
	}
}

func (s *Service) forwardWatchedChats(targets watchFleet) {
	cfg := s.o.Config()
	if !cfg.On() {
		return
	}
	routes, authors := cfg.WatchRoutes, cfg.WatchAgentAuthors
	legacyImmediate := map[string]bool{}
	var trigger *regexp.Regexp
	if cfg.WatchConfig != "" && cfg.WatchSince != nil {
		legacy, err := readLegacyWatchConfig(cfg.WatchConfig)
		if err != nil {
			s.log.Warn("read WhatsApp watcher config", "err", err)
		} else if !legacy.DryRun {
			authors = append(slices.Clone(authors), legacy.AgentAuthors...)
			legacyRoutes := s.legacyRoutes(cfg, legacy)
			for _, route := range legacyRoutes {
				if route.immediate {
					routes = append(routes, route.WhatsAppWatchRoute)
					legacyImmediate[route.Chat] = true
				}
			}
			trigger = watchTrigger(legacy.Triggers)
			s.watchUrgent(targets, cfg, legacyRoutes, trigger, legacy.UrgentThrottleMinutes, authors)
			s.watchDigests(targets, cfg, legacy, authors)
			s.watchBlind(targets, cfg, legacy.BlindAlertTo)
		}
	}
	for _, route := range routes {
		if route.Chat == "" || route.Since.IsZero() || slices.Contains(cfg.PrivateChats, route.Chat) {
			continue
		}
		for _, address := range route.Recipients {
			target, remote, err := targets.WhatsAppWatchTarget(watchLocalAddress(address, cfg.WatchLocalAlias))
			if err != nil {
				s.log.Warn("WhatsApp watch recipient unavailable", "recipient", address, "err", err)
				continue
			}
			// A remote recipient is explicitly authorized by its named route.
			// Local agents must also retain their existing WhatsApp read access.
			if !remote && !s.CanReadChat(target, route.Chat) {
				continue
			}
			offset, sent := 0, 0
			for sent < 64 {
				rows, err := s.o.Store.WaWatchPending(route.Chat, target, route.Since.UnixMilli(), offset)
				if err != nil {
					s.log.Warn("read WhatsApp watch backlog", "err", err)
					break
				}
				for _, row := range rows {
					if legacyImmediate[route.Chat] && s.watchLineUrgent(row.WaMessage, trigger, authors) {
						offset++
						continue
					}
					if err := s.forwardWatchedLine(targets, target, route.Urgent && !watchOurs(row.WaMessage, authors), row.WaMessage, authors); err != nil {
						offset++
						s.log.Warn("queue WhatsApp watch line", "recipient", address, "err", err)
						continue
					}
					sent++
				}
				if len(rows) < 64 {
					break
				}
			}
		}
	}
}

type legacyWatchRoute struct {
	config.WhatsAppWatchRoute
	title     string
	immediate bool
	urgentTo  []string
}

func (s *Service) legacyRoutes(cfg *config.WhatsAppConfig, legacy legacyWatchConfig) []legacyWatchRoute {
	titles := make([]string, 0, len(legacy.Chats))
	for title := range legacy.Chats {
		titles = append(titles, title)
	}
	sort.Strings(titles)
	var routes []legacyWatchRoute
	for _, title := range titles {
		r := legacy.Chats[title]
		if r.Enabled != nil && !*r.Enabled || excludedWatchChat(legacy.ExcludeChats, title, "") {
			continue
		}
		jid, err := s.watchChatID(cfg, title)
		if err != nil || excludedWatchChat(legacy.ExcludeChats, title, jid) {
			continue
		}
		routes = append(routes, legacyWatchRoute{WhatsAppWatchRoute: config.WhatsAppWatchRoute{Chat: jid, Recipients: r.Owners, Since: *cfg.WatchSince},
			title: title, immediate: r.Immediate, urgentTo: r.UrgentTo})
	}
	return routes
}

func (s *Service) watchBlind(targets watchFleet, cfg *config.WhatsAppConfig, recipients []string) {
	if len(recipients) == 0 {
		return
	}
	const key = "watch:blind-since"
	started, err := s.o.Store.WaMeta(key)
	if err != nil {
		s.log.Warn("read WhatsApp watcher connection state", "err", err)
		return
	}
	save := func(value string) {
		if err := s.o.Store.WaSetMeta(key, value); err != nil {
			s.log.Warn("save WhatsApp watcher connection state", "err", err)
		}
	}
	if s.ready(true) == nil {
		if started != "" {
			save("")
		}
		return // a quiet chat on a healthy link is not a capture outage
	}
	now := s.o.Now()
	if started == "" {
		save(now.UTC().Format(time.RFC3339Nano))
		return
	}
	since, err := time.Parse(time.RFC3339Nano, started)
	if err != nil || now.Sub(since) < 5*time.Minute {
		return
	}
	for _, address := range recipients {
		target, _, err := targets.WhatsAppWatchTarget(watchLocalAddress(address, cfg.WatchLocalAlias))
		if err == nil {
			err = targets.QueueWhatsAppWatch(target, key+":"+started+":"+target, "WhatsApp watcher has had no linked connection for at least five minutes.", true)
		}
		if err != nil {
			s.log.Warn("queue WhatsApp watcher blind alert", "recipient", address, "err", err)
		}
	}
}

func (s *Service) forwardWatchedLine(targets watchFleet, target string, urgent bool, m model.WaMessage, agentAuthors []string) error {
	title := "configured chat"
	if chat, err := s.Chat(m.ChatJID); err == nil && chat.Name != "" && chat.Name != chat.JID {
		title = chat.Name
	}
	header := fmt.Sprintf("%s\nWhatsApp chat %s\n", Frame, peertext.Field(redactWatchText(title)))
	return queueWatchParts(targets, target, store.WaWatchKey(m.ChatJID, target, m.ID), header, s.watchAttributedLine(m, agentAuthors), urgent)
}

func (s *Service) watchAttributedLine(m model.WaMessage, agentAuthors []string) string {
	author := redactWatchText(s.senderLabel(m))
	if author == "" {
		author = "Unknown sender"
	}
	if watchOurs(m, agentAuthors) {
		author += " (ours)"
	}
	return fmt.Sprintf("%s · %s\n%s", author, time.UnixMilli(m.At).UTC().Format(time.RFC3339Nano), peertext.Quote(watchLine(m)))
}

func watchOurs(m model.WaMessage, agentAuthors []string) bool {
	return m.FromMe || slices.ContainsFunc(agentAuthors, func(a string) bool {
		return strings.EqualFold(a, m.SenderName) || strings.EqualFold(a, m.SenderJID)
	})
}

func queueWatchParts(targets watchFleet, target, key, header, body string, urgent bool, seen ...string) error {
	// The fleet wire has a 32 KiB cap. Keep every quoted byte, with a
	// per-notice receipt committed together with every part.
	const partLimit = 28 << 10
	part := 1
	var bodies []string
	for len(body) > partLimit {
		end := strings.LastIndexByte(body[:partLimit], '\n') + 1
		if end < partLimit/2 {
			end = partLimit
			for !utf8.RuneStart(body[end]) {
				end--
			}
		}
		prefix := fmt.Sprintf("Part %d (continues)\n", part)
		bodies = append(bodies, header+prefix+body[:end])
		part++
		body = body[end:]
		if body != "" && !strings.HasPrefix(body, peertext.Marker) {
			body = peertext.Marker + body
		}
	}
	if part > 1 {
		header += fmt.Sprintf("Part %d (final)\n", part)
	}
	return targets.QueueWhatsAppWatchParts(target, key, append(bodies, header+body), urgent, seen...)
}

// Redact before formatting so every outbound representation uses the same text.
var watchSecrets = regexp.MustCompile(`(?i)(bearer[ \t]+|(?:api[ _-]?key|access[ _-]?token|token|password|passcode|pin|secret|cookie)["']?[ \t]*[=:][ \t]*["']?)[^\s"',;&}]+|\b(?:sk-|ghp_|github_pat_|xox[baprs]-)[A-Za-z0-9_-]{12,}\b|\b[0-9a-f]{32,}\b|\b[0-9]+@(?:s\.whatsapp\.net|g\.us|lid)\b`)

func redactWatchText(text string) string {
	return watchSecrets.ReplaceAllString(text, "${1}[redacted]")
}

func watchLine(m model.WaMessage) string {
	if body := firstNonEmpty(m.Text, m.Caption); body != "" {
		return redactWatchText(body)
	}
	switch m.Type {
	case model.WaImage:
		return "[image]"
	case model.WaVideo:
		return "[video]"
	case model.WaVoice:
		return "[voice]"
	}
	return ""
}
