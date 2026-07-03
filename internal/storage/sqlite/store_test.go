package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestOverviewCooldownSharedAcrossTriggers(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(context.Background(), "scheduled", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishOverview(context.Background(), run.ID, "success", "done", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	_, err = store.TryStartOverview(context.Background(), "nest_button", now.Add(14*time.Minute), 15*time.Minute)
	var cooldownErr *CooldownError
	if !errors.As(err, &cooldownErr) {
		t.Fatalf("expected cooldown error, got %v", err)
	}

	if _, err := store.TryStartOverview(context.Background(), "manual", now.Add(15*time.Minute), 15*time.Minute); err != nil {
		t.Fatalf("run at cooldown boundary: %v", err)
	}
}

func TestRejectsConcurrentRun(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC()
	if _, err := store.TryStartOverview(context.Background(), "manual", now, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TryStartOverview(context.Background(), "scheduled", now.Add(time.Hour), 15*time.Minute); !errors.Is(err, ErrRunActive) {
		t.Fatalf("expected active run error, got %v", err)
	}
}

func TestRecoverFailedOverview(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 6, 18, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishOverview(ctx, run.ID, "failed", "codex failed", "codex digest: missing", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverFailedOverview(ctx, run.ID, "recovered", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	recovered, ok, err := store.RunByID(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || recovered.Status != "success" || recovered.Summary != "recovered" || recovered.Error != "" {
		t.Fatalf("recovered run = %+v, ok=%t", recovered, ok)
	}
	if err := store.RecoverFailedOverview(ctx, run.ID, "again", now.Add(3*time.Minute)); err == nil {
		t.Fatal("expected a recovered run to reject a second recovery")
	}
}

func TestTelegramMessagesAreIdempotentCursorAndRecent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref:      "telegram:channel:100",
		PeerKind: "channel",
		ChatID:   100,
		Title:    "Study",
		Username: "studychat",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	messages := []TelegramMessage{
		{
			SourceID:   source.ID,
			ChatID:     100,
			MessageID:  10,
			Date:       time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC),
			Kind:       "message",
			Text:       "deadline tomorrow",
			SourceLink: "https://t.me/studychat/10",
			RawJSON:    `{"message_id":10}`,
		},
		{
			SourceID:   source.ID,
			ChatID:     100,
			MessageID:  12,
			Date:       time.Date(2026, 6, 17, 10, 5, 0, 0, time.UTC),
			Kind:       "message",
			Text:       "room changed",
			SourceLink: "https://t.me/studychat/12",
			RawJSON:    `{"message_id":12}`,
		},
	}

	newMessages, err := store.FilterNewTelegramMessages(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(newMessages) != 2 {
		t.Fatalf("new messages = %d", len(newMessages))
	}
	inserted, total, err := store.InsertTelegramMessages(ctx, newMessages)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 2 || total != 2 {
		t.Fatalf("inserted,total = %d,%d", inserted, total)
	}
	source, err = store.TelegramSourceByRef(ctx, source.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if source.LastMessageID != 12 {
		t.Fatalf("last message id = %d", source.LastMessageID)
	}
	newMessages, err = store.FilterNewTelegramMessages(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(newMessages) != 0 {
		t.Fatalf("duplicate messages reported new: %d", len(newMessages))
	}

	recent, err := store.RecentTelegramMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("recent messages = %d", len(recent))
	}
	if recent[0].SourceTitle != "Study" || recent[0].Text != "room changed" {
		t.Fatalf("recent[0] = %+v", recent[0])
	}
	personal, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref:      "telegram:channel:200",
		PeerKind: "channel",
		ChatID:   200,
		Title:    "Personal",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{{
		SourceID:   personal.ID,
		ChatID:     200,
		MessageID:  20,
		Date:       time.Date(2026, 6, 17, 10, 10, 0, 0, time.UTC),
		Kind:       "message",
		Text:       "personal note",
		SourceLink: "https://t.me/c/200/20",
		RawJSON:    `{"message_id":20}`,
	}}); err != nil {
		t.Fatal(err)
	}
	filteredRecent, err := store.RecentTelegramMessagesBySourceRefs(ctx, []string{source.Ref}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(filteredRecent) != 2 {
		t.Fatalf("filtered recent messages = %d", len(filteredRecent))
	}
	for _, message := range filteredRecent {
		if message.SourceRef != source.Ref {
			t.Fatalf("filtered recent included %q", message.SourceRef)
		}
	}

	olderNewMessage := TelegramMessage{
		SourceID:   source.ID,
		ChatID:     100,
		MessageID:  11,
		Date:       time.Date(2026, 6, 17, 10, 2, 0, 0, time.UTC),
		Kind:       "message",
		Text:       "extra note",
		SourceLink: "https://t.me/studychat/11",
		RawJSON:    `{"message_id":11}`,
	}
	inserted, total, err = store.InsertTelegramMessages(ctx, []TelegramMessage{olderNewMessage})
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 1 || total != 1 {
		t.Fatalf("inserted,total for older new message = %d,%d", inserted, total)
	}
	source, err = store.TelegramSourceByRef(ctx, source.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if source.LastMessageID != 12 {
		t.Fatalf("last message id after older insert = %d", source.LastMessageID)
	}
}

func TestTelegramMessagesCreatedBetween(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref: "telegram:channel:100", PeerKind: "channel", ChatID: 100, Title: "Study",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Second)
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{{
		SourceID: source.ID, ChatID: 100, MessageID: 90, Date: time.Now().UTC(),
		Kind: "message", Text: "exam", SourceLink: "https://t.me/c/100/90",
	}}); err != nil {
		t.Fatal(err)
	}
	end := time.Now().UTC().Add(time.Second)
	messages, err := store.TelegramMessagesCreatedBetween(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].MessageID != 90 || messages[0].SourceTitle != "Study" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestSampleTelegramTextMessagesSkipsServiceAndEmpty(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref: "telegram:channel:100", PeerKind: "channel", ChatID: 100, Title: "Study",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 18, 8, 0, 0, 0, time.UTC)
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{
		{SourceID: source.ID, ChatID: 100, MessageID: 1, Date: now, Kind: "message", Text: "exam"},
		{SourceID: source.ID, ChatID: 100, MessageID: 2, Date: now, Kind: "message", Text: ""},
		{SourceID: source.ID, ChatID: 100, MessageID: 3, Date: now, Kind: "service", Text: "joined"},
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := store.SampleTelegramTextMessages(ctx, 10, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].MessageID != 1 {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestInsertMessageDecisions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref:      "telegram:channel:100",
		PeerKind: "channel",
		ChatID:   100,
		Title:    "Study",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{{
		SourceID:   source.ID,
		ChatID:     100,
		MessageID:  42,
		Date:       now,
		Kind:       "message",
		Text:       "exam tomorrow",
		SourceLink: "https://t.me/c/100/42",
		RawJSON:    `{"message_id":42}`,
	}}); err != nil {
		t.Fatal(err)
	}

	err = store.InsertMessageDecisions(ctx, []MessageDecision{{
		RunID:      run.ID,
		ChatID:     100,
		MessageID:  42,
		Keep:       true,
		Importance: 3,
		Reason:     "экзамен",
		Tags:       []string{"exam", "urgent"},
		HasEvent:   true,
		Model:      "qwen3:14b",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertMessageDecisions(ctx, []MessageDecision{{
		RunID: run.ID, ChatID: 100, MessageID: 42, Keep: true, Importance: 3,
		Reason: "экзамен", Tags: []string{"exam", "urgent"}, HasEvent: true, Model: "qwen3:14b",
	}}, now); err != nil {
		t.Fatalf("idempotent decision insert: %v", err)
	}

	var keep, importance, hasEvent int
	var reason, tags, model string
	err = store.db.QueryRowContext(ctx, `
SELECT keep, importance, reason, tags_json, has_event, model
FROM message_decisions
WHERE run_id = ? AND chat_id = ? AND message_id = ?`, run.ID, 100, 42).
		Scan(&keep, &importance, &reason, &tags, &hasEvent, &model)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatal("decision was not inserted")
		}
		t.Fatal(err)
	}
	if keep != 1 || importance != 3 || reason != "экзамен" || tags != `["exam","urgent"]` || hasEvent != 1 || model != "qwen3:14b" {
		t.Fatalf("stored decision = keep:%d importance:%d reason:%q tags:%q event:%d model:%q",
			keep, importance, reason, tags, hasEvent, model)
	}
}

func TestInsertModelCallAndRecent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 6, 18, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertModelCall(ctx, ModelCall{
		RunID:          run.ID,
		Stage:          "qwen_classify",
		BatchIndex:     1,
		InputMessages:  24,
		InputChars:     1800,
		DurationMillis: 1200,
		Success:        true,
		Model:          "qwen3:14b",
	}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertModelCall(ctx, ModelCall{
		RunID:          run.ID,
		Stage:          "qwen_classify",
		BatchIndex:     2,
		InputMessages:  24,
		InputChars:     1700,
		DurationMillis: 75000,
		Success:        false,
		Fallbacks:      24,
		Error:          "deadline exceeded",
		Model:          "qwen3:14b",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	calls, err := store.RecentModelCalls(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if calls[0].BatchIndex != 2 || calls[0].Success || calls[0].Fallbacks != 24 {
		t.Fatalf("latest call = %+v", calls[0])
	}
	if calls[1].BatchIndex != 1 || !calls[1].Success {
		t.Fatalf("older call = %+v", calls[1])
	}
}

func TestCalendarCandidatesLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref:      "telegram:channel:100",
		PeerKind: "channel",
		ChatID:   100,
		Title:    "Study",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{{
		SourceID:   source.ID,
		ChatID:     100,
		MessageID:  50,
		Date:       now,
		Kind:       "message",
		Text:       "exam tomorrow",
		SourceLink: "https://t.me/c/100/50",
		RawJSON:    `{"message_id":50}`,
	}}); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	inserted, err := store.InsertCalendarCandidates(ctx, []CalendarCandidate{{
		RunID:       run.ID,
		ChatID:      100,
		MessageID:   50,
		SourceLink:  "https://t.me/c/100/50",
		Title:       "[ОММ] Экзамен",
		StartAt:     start,
		EndAt:       start.Add(2 * time.Hour),
		Timezone:    "Europe/Moscow",
		Location:    "504",
		Description: "Экзамен",
		Confidence:  "medium",
		Status:      "pending",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted) != 1 || inserted[0].ID == 0 {
		t.Fatalf("inserted = %+v", inserted)
	}
	again, err := store.InsertCalendarCandidates(ctx, []CalendarCandidate{inserted[0]}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("duplicate candidate inserted again: %+v", again)
	}
	shiftedStart := start.Add(24 * time.Hour)
	if err := store.UpdateCalendarCandidateTime(ctx, inserted[0].ID, shiftedStart, shiftedStart.Add(2*time.Hour), now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	shifted, err := store.CalendarCandidateByID(ctx, inserted[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !shifted.StartAt.Equal(shiftedStart) || shifted.EndAt.Sub(shifted.StartAt) != 2*time.Hour {
		t.Fatalf("candidate after time update = %+v", shifted)
	}

	if err := store.UpdateCalendarCandidateStatus(ctx, inserted[0].ID, "created", "google-event-1", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	candidate, err := store.CalendarCandidateByID(ctx, inserted[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != "created" || candidate.CalendarEventID != "google-event-1" {
		t.Fatalf("candidate after update = %+v", candidate)
	}
	if err := store.UpdateCalendarCandidateTime(ctx, inserted[0].ID, start.Add(24*time.Hour), start.Add(25*time.Hour), now.Add(2*time.Minute)); err == nil {
		t.Fatal("expected created candidate time update to be rejected")
	}
	recent, err := store.RecentCalendarCandidates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].ID != inserted[0].ID {
		t.Fatalf("recent = %+v", recent)
	}
}
