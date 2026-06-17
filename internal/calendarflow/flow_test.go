package calendarflow

import (
	"strings"
	"testing"
	"time"

	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestCallbackDataRoundTrip(t *testing.T) {
	data := CallbackData(actionApprove, 42)
	action, id, ok := ParseCallback(data)
	if !ok {
		t.Fatal("callback did not parse")
	}
	if action != actionApprove || id != 42 {
		t.Fatalf("parsed callback = %q %d", action, id)
	}
	for _, invalid := range []string{"", "run", "cal:approve", "cal:approve:x", "cal:unknown:42"} {
		if IsCallback(invalid) {
			t.Fatalf("invalid callback parsed: %q", invalid)
		}
	}
}

func TestCandidateMessage(t *testing.T) {
	start := time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC)
	message := CandidateMessage(sqlitestore.CalendarCandidate{
		ID:          7,
		Title:       "[ОММ] Экзамен",
		StartAt:     start,
		EndAt:       start.Add(time.Hour),
		Location:    "504",
		Confidence:  "medium",
		SourceLink:  "https://t.me/c/100/42",
		Description: "Экзамен по ОММ",
	}, "Europe/Moscow")
	for _, want := range []string{
		"Calendar candidate #7",
		"[ОММ] Экзамен",
		"Location: 504",
		"Confidence: medium",
		"https://t.me/c/100/42",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("candidate message missing %q:\n%s", want, message)
		}
	}
}
