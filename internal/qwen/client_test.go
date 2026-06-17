package qwen

import (
	"strings"
	"testing"
	"time"
)

func TestBuildPromptRequiresIDs(t *testing.T) {
	if _, err := BuildPrompt([]MessageInput{{Text: "привет"}}); err == nil {
		t.Fatal("expected missing id error")
	}
}

func TestValidateResultRejectsUnknownID(t *testing.T) {
	err := validateResult(
		[]MessageInput{{ID: "a"}},
		BatchResult{Decisions: []MessageDecision{{ID: "b"}}},
	)
	if err == nil {
		t.Fatal("expected unknown id error")
	}
}

func TestBuildEventPrompt(t *testing.T) {
	prompt, err := BuildEventPrompt([]EventInput{{
		ID:         "telegram:100:42",
		SourceRef:  "telegram:channel:100",
		SourceLink: "https://t.me/c/100/42",
		Text:       "Экзамен завтра в 10:00 в 504",
	}}, time.Date(2026, 6, 17, 7, 0, 0, 0, time.UTC), "Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Return JSON only",
		"Europe/Moscow",
		"telegram:100:42",
		"RFC3339",
		"has_event",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("event prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestValidateEventResult(t *testing.T) {
	inputs := []EventInput{{ID: "a"}, {ID: "b"}}
	if err := validateEventResult(inputs, EventExtractionResult{Events: []EventCandidate{{ID: "a"}, {ID: "b"}}}); err != nil {
		t.Fatal(err)
	}
	if err := validateEventResult(inputs, EventExtractionResult{Events: []EventCandidate{{ID: "a"}}}); err == nil {
		t.Fatal("expected missing event validation error")
	}
	if err := validateEventResult(inputs, EventExtractionResult{Events: []EventCandidate{{ID: "a"}, {ID: "x"}}}); err == nil {
		t.Fatal("expected unknown id validation error")
	}
}

func TestParseBatchSizesDefault(t *testing.T) {
	sizes, err := ParseBatchSizes("")
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) == 0 || sizes[0] != 4 {
		t.Fatalf("unexpected sizes: %v", sizes)
	}
}
