package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/loafykitten/hekate/service/internal/calendar"
	"github.com/loafykitten/hekate/service/internal/inspect"
	"github.com/loafykitten/hekate/service/internal/lab"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/mail"
	"github.com/loafykitten/hekate/service/internal/money"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/telegram"
	"github.com/loafykitten/hekate/service/internal/voice"
	"github.com/loafykitten/hekate/service/internal/web"
)

// healthChecks are what self_status reports on, one line each.
func healthChecks(llmClient *llm.Client, tts bool, stt voice.Transcriber, cal *calendar.Service, mailSvc *mail.Service, moneySvc *money.Service,
	pushSvc *push.Service, tg *telegram.Service, webSvc *web.Service, labSvc *lab.Service) []inspect.Check {
	configured := func(ok bool, yes, no string) (bool, string) {
		if ok {
			return true, yes
		}
		return false, no
	}
	return []inspect.Check{
		{Name: "Language model", Run: func(context.Context) (bool, string) {
			return configured(llmClient.Configured(), "", "no API key is set")
		}},
		{Name: "Voice (text to speech)", Run: func(context.Context) (bool, string) {
			return configured(tts, "", "not set up: replies are text only")
		}},
		{Name: "Listening (speech to text)", Run: func(context.Context) (bool, string) {
			switch {
			case stt == nil:
				return false, "switched off"
			case !stt.Ready():
				return false, "still loading the model"
			}
			return true, ""
		}},
		{Name: "Calendar", Run: func(context.Context) (bool, string) {
			return configured(cal.Configured(), "", "not set up")
		}},
		{Name: "Mail", Run: func(ctx context.Context) (bool, string) {
			if !mailSvc.Configured() {
				return false, "not set up"
			}
			accounts, err := mailSvc.Status(ctx)
			if err != nil {
				return false, "can't read the account status: " + err.Error()
			}
			ok := true
			parts := make([]string, 0, len(accounts))
			for _, a := range accounts {
				state := "fine"
				if !a.OK {
					ok = false
					state = "failing"
					if a.Error != nil {
						state += ": " + *a.Error
					}
				}
				if a.LastSweepAt != nil {
					state += ", last checked " + *a.LastSweepAt
				}
				parts = append(parts, fmt.Sprintf("%s %s", a.Name, state))
			}
			return ok, strings.Join(parts, "; ")
		}},
		{Name: "Bank data", Run: func(ctx context.Context) (bool, string) {
			if !moneySvc.Configured() {
				return false, "not set up"
			}
			items, err := moneySvc.Items(ctx)
			if err != nil {
				return false, "can't read the bank status: " + err.Error()
			}
			if len(items) == 0 {
				return true, "no bank connected yet"
			}
			ok := true
			parts := make([]string, 0, len(items))
			for _, it := range items {
				state := "fine"
				if it.Status != money.StatusOK {
					ok = false
					state = it.Status
					if it.Error != "" {
						state += ": " + it.Error
					}
				}
				if it.LastSyncAt != nil {
					state += ", last synced " + *it.LastSyncAt
				}
				parts = append(parts, fmt.Sprintf("%s %s", it.Institution, state))
			}
			return ok, strings.Join(parts, "; ")
		}},
		{Name: "Notifications (web push)", Run: func(ctx context.Context) (bool, string) {
			n, err := pushSvc.Count(ctx)
			if err != nil {
				return false, "can't count devices: " + err.Error()
			}
			return configured(n > 0, fmt.Sprintf("%d device(s) subscribed", n), "no device subscribed")
		}},
		{Name: "Telegram backup", Run: func(context.Context) (bool, string) {
			return configured(tg.Configured(), "", "not set up")
		}},
		{Name: "Web search", Run: func(context.Context) (bool, string) {
			return configured(webSvc.SearchConfigured(), "", "no search key: web_read still works")
		}},
		{Name: "Lab", Run: func(ctx context.Context) (bool, string) {
			if !labSvc.Configured() {
				return false, "not set up"
			}
			return labSvc.Health(ctx)
		}},
	}
}
