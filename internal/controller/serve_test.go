package controller

import (
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
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
