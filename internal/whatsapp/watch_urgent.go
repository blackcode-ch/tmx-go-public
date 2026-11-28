package whatsapp

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/peertext"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func watchTrigger(terms []string) *regexp.Regexp {
	if len(terms) == 0 {
		terms = []string{"urgent", "urgente", "panne", "bug", "dringend", "kaputt", "down", "broken"}
	}
	var termsQuoted []string
	for _, term := range terms {
		if term = strings.TrimSpace(term); term != "" {
			termsQuoted = append(termsQuoted, regexp.QuoteMeta(term))
		}
	}
	if len(termsQuoted) == 0 {
		return nil
	}
	return regexp.MustCompile(`(?i)(^|[^\pL\pN_])(?:` + strings.Join(termsQuoted, "|") + `)([^\pL\pN_]|$)`)
}

func (s *Service) watchLineUrgent(m model.WaMessage, trigger *regexp.Regexp, authors []string) bool {
	return !watchOurs(m, authors) && (trigger != nil && trigger.MatchString(firstNonEmpty(m.Text, m.Caption)) ||
		s.o.Store.WaIsWatchMention(m.ChatJID, m.ID))
}

func (s *Service) watchUrgent(targets watchFleet, cfg *config.WhatsAppConfig, routes []legacyWatchRoute, trigger *regexp.Regexp, minutes int, authors []string) {
	if minutes <= 0 {
		minutes = 10
	}
	now := s.o.Now()
	for _, route := range routes {
		if slices.Contains(cfg.PrivateChats, route.Chat) {
			continue
		}
		stateKey := "watch:urgent-last:" + route.Chat
		last, err := s.o.Store.WaMeta(stateKey)
		if err != nil {
			s.log.Warn("read WhatsApp urgent throttle", "err", err)
			continue
		}
		lastAt, _ := strconv.ParseInt(last, 10, 64)
		if lastAt != 0 && now.Sub(time.UnixMilli(lastAt)) < time.Duration(min(minutes, 24*60))*time.Minute {
			continue
		}
		sent := false
		to := append(slices.Clone(route.Recipients), route.urgentTo...)
		seenTarget := map[string]bool{}
		for _, address := range to {
			target, remote, err := targets.WhatsAppWatchTarget(watchLocalAddress(address, cfg.WatchLocalAlias))
			if err != nil || seenTarget[target] || !remote && !s.CanReadChat(target, route.Chat) {
				continue
			}
			seenTarget[target] = true
			var body strings.Builder
			var covered []string
			for offset := 0; len(covered) < 64; offset += 64 {
				rows, err := s.o.Store.WaWatchUrgentPending(route.Chat, target, route.Since.UnixMilli(), now.UnixMilli(), offset)
				if err != nil {
					s.log.Warn("read urgent WhatsApp backlog", "err", err)
					break
				}
				for _, row := range rows {
					if !s.watchLineUrgent(row.WaMessage, trigger, authors) {
						continue
					}
					body.WriteString(s.watchAttributedLine(row.WaMessage, authors) + "\n")
					covered = append(covered, store.WaWatchUrgentKey(route.Chat, target, row.ID))
					if len(covered) == 64 {
						break
					}
				}
				if len(rows) < 64 {
					break
				}
			}
			if len(covered) == 0 {
				continue
			}
			if !sent {
				if err := s.o.Store.WaSetMeta(stateKey, strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
					s.log.Warn("save WhatsApp urgent throttle", "err", err)
					break
				}
				sent = true
			}
			key := covered[0] + ":batch"
			header := fmt.Sprintf("%s\nUrgent WhatsApp messages in %s\n", Frame, peertext.Field(redactWatchText(route.title)))
			if err := queueWatchParts(targets, target, key, header, body.String(), true, covered...); err != nil {
				s.log.Warn("queue urgent WhatsApp messages", "recipient", address, "err", err)
			}
		}
	}
}
