package calendarflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
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
	editAction, editID, editOK := ParseCallback(CallbackData(actionEditDate, 43))
	if !editOK || !IsDateEditAction(editAction) || editID != 43 {
		t.Fatalf("edit callback parsed as %q %d ok=%t", editAction, editID, editOK)
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
		Title:       "[ОММ] Экзамен <важно>",
		StartAt:     start,
		EndAt:       start.Add(time.Hour),
		Location:    "504",
		Confidence:  "medium",
		SourceLink:  "https://t.me/c/100/42",
		Description: "Экзамен по ОММ <проверить>",
	}, "Europe/Moscow")
	for _, want := range []string{
		"Кандидат в календарь #7",
		"[ОММ] Экзамен &lt;важно&gt;",
		"<b>Место:</b> 504",
		"<b>Уверенность:</b> medium",
		"https://t.me/c/100/42",
		"<blockquote>Экзамен по ОММ &lt;проверить&gt;</blockquote>",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("candidate message missing %q:\n%s", want, message)
		}
	}
}

func TestShiftedCandidateTimeParsesDateAndDateTime(t *testing.T) {
	start := time.Date(2026, 6, 18, 8, 30, 0, 0, time.UTC)
	candidate := sqlitestore.CalendarCandidate{
		ID:      7,
		StartAt: start,
		EndAt:   start.Add(90 * time.Minute),
	}

	nextStart, nextEnd, err := shiftedCandidateTime(candidate, "2026-06-28", "Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	location := mustLocation("Europe/Moscow")
	if got := nextStart.In(location).Format("2006-01-02 15:04"); got != "2026-06-28 11:30" {
		t.Fatalf("date-only start = %s", got)
	}
	if nextEnd.Sub(nextStart) != 90*time.Minute {
		t.Fatalf("duration = %s", nextEnd.Sub(nextStart))
	}

	nextStart, nextEnd, err = shiftedCandidateTime(candidate, "2026-06-29 14:15", "Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	if got := nextStart.In(location).Format("2006-01-02 15:04"); got != "2026-06-29 14:15" {
		t.Fatalf("date-time start = %s", got)
	}
	if nextEnd.Sub(nextStart) != 90*time.Minute {
		t.Fatalf("duration after date-time = %s", nextEnd.Sub(nextStart))
	}

	if _, _, err := shiftedCandidateTime(candidate, "29.06.2026", "Europe/Moscow"); err == nil {
		t.Fatal("expected invalid format error")
	}
}

func TestDateEditPromptIncludesFormatHint(t *testing.T) {
	prompt := DateEditPrompt(sqlitestore.CalendarCandidate{ID: 5, Title: "Экзамен"})
	for _, want := range []string{"кандидата <code>#5</code>", "2026-06-28", "2026-06-28 11:00"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestUpdateCandidateDatePersistsDateChange(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sova.db")
	store, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.UpsertTelegramSource(ctx, sqlitestore.TelegramSource{
		Ref: "telegram:channel:100", PeerKind: "channel", ChatID: 100, Title: "Study",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertTelegramMessages(ctx, []sqlitestore.TelegramMessage{{
		SourceID: source.ID, ChatID: 100, MessageID: 42, Date: now,
		Kind: "message", Text: "Экзамен", SourceLink: "https://t.me/c/100/42",
	}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 18, 8, 30, 0, 0, time.UTC)
	inserted, err := store.InsertCalendarCandidates(ctx, []sqlitestore.CalendarCandidate{{
		RunID: run.ID, ChatID: 100, MessageID: 42, SourceLink: "https://t.me/c/100/42",
		Title: "Экзамен", StartAt: start, EndAt: start.Add(time.Hour),
		Timezone: "Europe/Moscow", Status: "pending",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	cfg := config.Config{DatabasePath: dbPath, Timezone: "Europe/Moscow"}
	updated, text, err := UpdateCandidateDate(ctx, cfg, inserted[0].ID, "2026-06-29 14:15", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.StartAt.In(mustLocation("Europe/Moscow")).Format("2006-01-02 15:04"); got != "2026-06-29 14:15" {
		t.Fatalf("updated start = %s", got)
	}
	if updated.EndAt.Sub(updated.StartAt) != time.Hour {
		t.Fatalf("duration = %s", updated.EndAt.Sub(updated.StartAt))
	}
	if !strings.Contains(text, "Дата обновлена") {
		t.Fatalf("response text = %s", text)
	}
}
