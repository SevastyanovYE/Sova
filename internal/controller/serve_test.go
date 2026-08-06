package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	"github.com/SevastyanovYE/Sova/internal/overview"
)

type stubDailySettings struct {
	enabled bool
	readErr error
}

type stubNestBotIdentity struct {
	results []struct {
		user nest.User
		err  error
	}
	calls int
}

func (client *stubNestBotIdentity) GetMe(context.Context) (nest.User, error) {
	index := client.calls
	client.calls++
	if index >= len(client.results) {
		return nest.User{}, errors.New("unexpected GetMe call")
	}
	return client.results[index].user, client.results[index].err
}

func (settings stubDailySettings) DailyOverviewEnabled(context.Context) (bool, error) {
	return settings.enabled, settings.readErr
}

func (stubDailySettings) SetDailyOverviewEnabled(context.Context, bool, time.Time) error {
	return nil
}

func TestCommandName(t *testing.T) {
	tests := map[string]string{
		"/run":                 "run",
		"/run@sova_nest_bot":   "run",
		"/help please":         "help",
		"run":                  "",
		"":                     "",
		"   /button   please":  "button",
		"/daily@other_bot off": "",
	}
	for input, want := range tests {
		if got := commandName(input, "sova_nest_bot"); got != want {
			t.Fatalf("commandName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWaitForNestBotUsernameRetriesTemporaryFailure(t *testing.T) {
	client := &stubNestBotIdentity{results: []struct {
		user nest.User
		err  error
	}{
		{err: errors.New("temporary network error")},
		{user: nest.User{Username: "  sova_nest_bot  "}},
	}}
	username, err := waitForNestBotUsername(context.Background(), client, func(int) time.Duration { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if username != "sova_nest_bot" || client.calls != 2 {
		t.Fatalf("username = %q, calls = %d", username, client.calls)
	}
}

func TestControlMessageRequestUsesChatTopicAndButton(t *testing.T) {
	cfg := config.Config{NestChatID: -1001, NestTopics: config.TopicIDs{Chat: 2, Digest: 4, Calendar: 6, Status: 8}}
	request := ControlMessageRequest(cfg)
	if request.ChatID != cfg.NestChatID || request.MessageThreadID != cfg.NestTopics.Chat {
		t.Fatalf("request target = %+v", request)
	}
	if request.ReplyMarkup == nil || len(request.ReplyMarkup.InlineKeyboard) != 1 {
		t.Fatalf("reply markup = %+v", request.ReplyMarkup)
	}
	if request.ParseMode != "HTML" {
		t.Fatalf("parse mode = %q", request.ParseMode)
	}
	button := request.ReplyMarkup.InlineKeyboard[0][0]
	if button.Text != "Создать обзор" || button.CallbackData != createOverviewCallback {
		t.Fatalf("button = %+v", button)
	}
	if !strings.Contains(request.Text, "<code>/run</code>") || !strings.Contains(request.Text, "служебном топике") {
		t.Fatalf("control text = %q", request.Text)
	}
}

func TestFormatProgressMessage(t *testing.T) {
	message := formatProgressMessage(overview.ProgressEvent{
		RunID:              7,
		Message:            "Классифицирую сообщения через Qwen.",
		Current:            2,
		Total:              5,
		EstimatedRemaining: 3 * time.Minute,
	})
	for _, want := range []string{"Sova run #7", "Выполняется", "2/5", "3 мин", "<b>", "<i>"} {
		if !strings.Contains(message, want) {
			t.Fatalf("progress missing %q:\n%s", want, message)
		}
	}
	done := formatProgressMessage(overview.ProgressEvent{RunID: 7, Message: "Готово", Done: true})
	if !strings.Contains(done, "Готово") || strings.Contains(done, "Осталось") {
		t.Fatalf("done progress = %s", done)
	}
}

func TestNextDailyRun(t *testing.T) {
	location := mustLocation("Europe/Moscow")
	before := time.Date(2026, 6, 17, 7, 59, 0, 0, location)
	if got := nextDailyRun(before, "08:00", location); !got.Equal(time.Date(2026, 6, 17, 8, 0, 0, 0, location)) {
		t.Fatalf("next before = %s", got)
	}
	after := time.Date(2026, 6, 17, 8, 0, 0, 0, location)
	if got := nextDailyRun(after, "08:00", location); !got.Equal(time.Date(2026, 6, 18, 8, 0, 0, 0, location)) {
		t.Fatalf("next after = %s", got)
	}
}

func TestDailyOverviewAction(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "/daily", want: "status", ok: true},
		{input: "/daily@sova_nest_bot", want: "status", ok: true},
		{input: "/daily on", want: "on", ok: true},
		{input: "/daily OFF", want: "off", ok: true},
		{input: "/daily status", want: "status", ok: true},
		{input: "/daily maybe", ok: false},
		{input: "/daily on later", ok: false},
	}
	for _, test := range tests {
		got, ok := dailyOverviewAction(test.input)
		if got != test.want || ok != test.ok {
			t.Errorf("dailyOverviewAction(%q) = %q, %t; want %q, %t", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestDailyOverviewStatusTextExplainsManualTriggers(t *testing.T) {
	cfg := config.Config{Timezone: "Europe/Moscow", DailyRunTime: "08:00"}
	now := time.Date(2026, 7, 29, 9, 0, 0, 0, mustLocation(cfg.Timezone))
	disabled := dailyOverviewStatusText(false, cfg, now)
	for _, want := range []string{"выключены", "/run", "Создать обзор", "/daily on"} {
		if !strings.Contains(disabled, want) {
			t.Fatalf("disabled status missing %q:\n%s", want, disabled)
		}
	}
	enabled := dailyOverviewStatusText(true, cfg, now)
	for _, want := range []string{"включены", "30.07.2026 08:00 MSK", "/daily off"} {
		if !strings.Contains(enabled, want) {
			t.Fatalf("enabled status missing %q:\n%s", want, enabled)
		}
	}
}

func TestSubmitScheduledOverviewHonorsSetting(t *testing.T) {
	scheduledAt := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		settings      stubDailySettings
		submitResult  bool
		wantEnabled   bool
		wantSubmitted bool
		wantErr       bool
		wantCalls     int
	}{
		{name: "disabled", settings: stubDailySettings{enabled: false}},
		{name: "read error", settings: stubDailySettings{readErr: errors.New("database unavailable")}, wantErr: true},
		{name: "enabled and accepted", settings: stubDailySettings{enabled: true}, submitResult: true, wantEnabled: true, wantSubmitted: true, wantCalls: 1},
		{name: "enabled but busy", settings: stubDailySettings{enabled: true}, wantEnabled: true, wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			enabled, submitted, err := submitScheduledOverview(context.Background(), test.settings, scheduledAt, func(job overviewJob) bool {
				calls++
				if job.trigger != "scheduled" || !job.scheduledAt.Equal(scheduledAt) {
					t.Fatalf("job = %+v", job)
				}
				return test.submitResult
			})
			if enabled != test.wantEnabled || submitted != test.wantSubmitted || (err != nil) != test.wantErr || calls != test.wantCalls {
				t.Fatalf("result = enabled:%t submitted:%t err:%v calls:%d", enabled, submitted, err, calls)
			}
		})
	}
}

func TestTopicRoles(t *testing.T) {
	cfg := config.Config{NestChatID: -1001, NestTopics: config.TopicIDs{Chat: 2, Digest: 4, Calendar: 6, Status: 8}}
	if !isCommandTopicMessage(cfg, nest.Message{Chat: nest.Chat{ID: -1001}, MessageThreadID: 8}) {
		t.Fatal("expected Status topic to accept text commands")
	}
	if isCommandTopicMessage(cfg, nest.Message{Chat: nest.Chat{ID: -1001}, MessageThreadID: 2}) {
		t.Fatal("Chat topic must not accept text commands")
	}
	if !isControlButtonTopicMessage(cfg, nest.Message{Chat: nest.Chat{ID: -1001}, MessageThreadID: 2}) {
		t.Fatal("expected Chat topic to accept the pinned control button")
	}
	if isControlButtonTopicMessage(cfg, nest.Message{Chat: nest.Chat{ID: -1001}, MessageThreadID: 8}) {
		t.Fatal("Status topic must not be treated as the control-button topic")
	}
	if isCommandTopicMessage(cfg, nest.Message{Chat: nest.Chat{ID: -1002}, MessageThreadID: 8}) {
		t.Fatal("different chat must not be accepted")
	}
}

func TestPollRetryDelayAndLogging(t *testing.T) {
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute}
	for index, expected := range want {
		if got := pollRetryDelay(index + 1); got != expected {
			t.Fatalf("attempt %d delay = %s, want %s", index+1, got, expected)
		}
	}
	for _, attempt := range []int{1, 2, 3, 5, 10} {
		if !shouldLogPollFailure(attempt) {
			t.Fatalf("attempt %d should be logged", attempt)
		}
	}
	for _, attempt := range []int{4, 6, 7, 8, 9} {
		if shouldLogPollFailure(attempt) {
			t.Fatalf("attempt %d should be suppressed", attempt)
		}
	}
}
