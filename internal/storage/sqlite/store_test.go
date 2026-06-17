package sqlite

import (
	"context"
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

func TestTelegramMessagesAreIdempotentAndRecent(t *testing.T) {
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
	}

	newMessages, err := store.FilterNewTelegramMessages(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(newMessages) != 1 {
		t.Fatalf("new messages = %d", len(newMessages))
	}
	inserted, total, err := store.InsertTelegramMessages(ctx, newMessages)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 1 || total != 1 {
		t.Fatalf("inserted,total = %d,%d", inserted, total)
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
	if len(recent) != 1 {
		t.Fatalf("recent messages = %d", len(recent))
	}
	if recent[0].SourceTitle != "Study" || recent[0].Text != "deadline tomorrow" {
		t.Fatalf("recent[0] = %+v", recent[0])
	}
}
