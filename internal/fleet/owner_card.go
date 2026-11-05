package fleet

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

var cardMoney = regexp.MustCompile(`\b(pay|payment|price|invoice|card|chf|eur|usd|buy|purchase|subscribe)\b`)
var cardOutward = regexp.MustCompile(`\b(publish|post|send|email|message|tweet|deploy|announce)\b`)
var cardAccount = regexp.MustCompile(`\b(sign ?up|log ?in|account|password|token|key|settings|permission|invite)\b`)

func cardEffects(a model.CardAction, category string) model.CardEffect {
	text := strings.ToLower(strings.Join(append([]string{a.Goal, a.Account}, a.Steps...), " "))
	switch {
	case category == "money" || cardMoney.MatchString(text):
		return model.CardEffectMoney
	case category == "outward" || cardOutward.MatchString(text):
		return model.CardEffectOutward
	case category == "security" || cardAccount.MatchString(text):
		return model.CardEffectAccount
	default:
		return model.CardEffectNone
	}
}

func prepareOwnerCard(req *AskRequest) (*model.OwnerCard, error) {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.Is(unicode.Cf, r) {
				return -1
			}
			return r
		}, s)
	}
	req.Question, req.Context, req.Recommendation, req.Category = clean(req.Question), clean(req.Context), clean(req.Recommendation), clean(req.Category)
	req.Options = append([]string{}, req.Options...)
	for i := range req.Options {
		req.Options[i] = clean(req.Options[i])
	}
	c := model.OwnerCard{Type: req.Type, Context: req.Context, Recommendation: req.Recommendation, Category: req.Category}
	if c.Type == "" {
		c.Type = "decision"
		if req.Action != nil {
			c.Type = "human_action"
		}
	}
	if c.Type != "decision" && c.Type != "human_action" {
		return nil, errf(KindInvalid, "type must be decision or human_action")
	}
	if len(c.Context) > MaxBodyBytes || len(c.Recommendation) > MaxBodyBytes {
		return nil, errf(KindInvalid, "card text is too long")
	}
	if c.Recommendation != "" && len(req.Options) > 0 {
		found := false
		for _, o := range req.Options {
			found = found || o == c.Recommendation
		}
		if !found {
			return nil, errf(KindInvalid, "recommendation must be one of the options")
		}
	}
	if c.Type == "human_action" {
		if req.Action == nil {
			return nil, errf(KindInvalid, "a human action needs goal, steps and success")
		}
		a := *req.Action
		a.Goal, a.Account, a.Success = clean(a.Goal), clean(a.Account), clean(a.Success)
		a.Steps, a.DoNotTouch = append([]string{}, a.Steps...), append([]string(nil), a.DoNotTouch...)
		for i := range a.Steps {
			a.Steps[i] = clean(a.Steps[i])
		}
		for i := range a.DoNotTouch {
			a.DoNotTouch[i] = clean(a.DoNotTouch[i])
		}
		if strings.TrimSpace(a.Goal) == "" || len(a.Steps) == 0 || strings.TrimSpace(a.Success) == "" {
			return nil, errf(KindInvalid, "a human action needs goal, steps and success")
		}
		for _, step := range a.Steps {
			if strings.TrimSpace(step) == "" {
				return nil, errf(KindInvalid, "action steps cannot be empty")
			}
		}
		a.Effects = cardEffects(a, c.Category)
		c.Action = &a
		if strings.TrimSpace(req.Question) == "" {
			req.Question = a.Goal
		}
	} else if req.Action != nil {
		return nil, errf(KindInvalid, "decision cards cannot contain a human action")
	}
	return &c, nil
}
