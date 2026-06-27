package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	"github.com/SevastyanovYE/Sova/internal/overview"
)

func TestCommandName(t *testing.T) {
	tests := map[string]string{
		"/run":                "run",
		"/run@sova_nest_bot":  "run",
		"/help please":        "help",
		"run":                 "",
		"":                    "",
		"   /button   please": "button",
	}
	for input, want := range tests {
		if got := commandName(input); got != want {
			t.Fatalf("commandName(%q) = %q, want %q", input, got, want)
		}
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
	if !strings.Contains(request.Text, "<code>/run</code>") || !strings.Contains(request.Text, "cooldown") {
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

func TestIsChatTopicMessage(t *testing.T) {
	cfg := config.Config{NestChatID: -1001, NestTopics: config.TopicIDs{Chat: 2, Digest: 4, Calendar: 6, Status: 8}}
	if !isChatTopicMessage(cfg, nest.Message{Chat: nest.Chat{ID: -1001}, MessageThreadID: 2}) {
		t.Fatal("expected Chat topic message")
	}
	if isChatTopicMessage(cfg, nest.Message{Chat: nest.Chat{ID: -1001}, MessageThreadID: 4}) {
		t.Fatal("Digest topic must not be accepted as Chat commands")
	}
	if isChatTopicMessage(cfg, nest.Message{Chat: nest.Chat{ID: -1002}, MessageThreadID: 2}) {
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
