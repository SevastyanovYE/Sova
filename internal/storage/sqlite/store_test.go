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
