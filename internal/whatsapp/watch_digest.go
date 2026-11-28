package whatsapp

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/peertext"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func (s *Service) watchDigests(targets watchFleet, cfg *config.WhatsAppConfig, legacy legacyWatchConfig, authors []string) {
	minutes := legacy.DigestMinutes
	if minutes <= 0 {
		minutes = 30
	}
	window := time.Duration(min(minutes, 24*60)) * time.Minute
	boundary := s.o.Now().Truncate(window)
	if !boundary.After(*cfg.WatchSince) {
		return
	}
	for _, batch := range s.watchDigestSnapshot(targets, cfg, legacy, boundary, authors) {
		if err := queueWatchParts(targets, batch.target, batch.key, Frame+"\nWhatsApp digest\n", batch.body, false, batch.covered...); err != nil {
			s.log.Warn("queue WhatsApp digest", "err", err)
		}
	}
}

type watchDigestBatch struct {
	target, key, body string
	covered           []string
}

func (s *Service) watchDigestSnapshot(targets watchFleet, cfg *config.WhatsAppConfig, legacy legacyWatchConfig, boundary time.Time, authors []string) []watchDigestBatch {
	// A history event stores its chats and messages one at a time under
	// evMu. Resolve titles and read the digest only between complete events.
	s.evMu.Lock()
	defer s.evMu.Unlock()
	routes := s.legacyRoutes(cfg, legacy)
	byTarget := map[string][]legacyWatchRoute{}
	seen := map[[2]string]bool{}
	for _, route := range routes {
		if slices.Contains(cfg.PrivateChats, route.Chat) {
			continue
		}
		for _, address := range route.Recipients {
			target, remote, err := targets.WhatsAppWatchTarget(watchLocalAddress(address, cfg.WatchLocalAlias))
			pair := [2]string{target, route.Chat}
			if err != nil || seen[pair] || !remote && !s.CanReadChat(target, route.Chat) {
				continue
			}
			seen[pair] = true
			byTarget[target] = append(byTarget[target], route)
		}
	}
	var batches []watchDigestBatch
	for target, routes := range byTarget {
		key := "wa-digest-batch:" + target + ":" + strconv.FormatInt(boundary.UnixMilli(), 10)
		if _, err := s.o.Store.MessageByRetry(model.WhatsAppParty, key); err == nil {
			continue
		}
		var body strings.Builder
		var covered []string
		for _, route := range routes {
			rows, err := s.o.Store.WaWatchDigestPending(route.Chat, target, route.Since.UnixMilli(), boundary.UnixMilli())
			if err != nil {
				s.log.Warn("read WhatsApp digest backlog", "err", err)
				covered = nil
				break
			}
			if len(rows) == 0 {
				continue
			}
			fmt.Fprintf(&body, "\nChat %s\n", peertext.Field(redactWatchText(route.title)))
			for _, row := range rows {
				body.WriteString(s.watchAttributedLine(row.WaMessage, authors) + "\n")
				covered = append(covered, store.WaWatchDigestKey(route.Chat, target, row.ID))
			}
		}
		if len(covered) == 0 {
			continue
		}
		batches = append(batches, watchDigestBatch{target: target, key: key, body: body.String(), covered: covered})
	}
	return batches
}
