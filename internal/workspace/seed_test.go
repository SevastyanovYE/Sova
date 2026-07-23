package workspace

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type fakeCommandHelpTelegram struct {
	nextID int
	sends  []nest.SendMessageRequest
	edits  []nest.EditMessageTextRequest
	pins   []nest.PinChatMessageRequest
}

func (f *fakeCommandHelpTelegram) SendMessageResult(_ context.Context, request nest.SendMessageRequest) (nest.Message, error) {
	f.sends = append(f.sends, request)
	f.nextID++
	return nest.Message{MessageID: f.nextID}, nil
}

func (f *fakeCommandHelpTelegram) EditMessageText(_ context.Context, request nest.EditMessageTextRequest) error {
	f.edits = append(f.edits, request)
	return nil
}

func (f *fakeCommandHelpTelegram) PinChatMessage(_ context.Context, request nest.PinChatMessageRequest) error {
	f.pins = append(f.pins, request)
	return nil
}

func TestSeedWorkspaceCommandHelpCreatesThenUpdatesTrackedPins(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := testWorkspaceLiveConfig()
	client := &fakeCommandHelpTelegram{nextID: 100}
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)

	created, err := seedWorkspaceCommandHelpWithClient(context.Background(), cfg, store, client, SeedTopicPinsOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Items) != 7 || len(client.sends) != 7 || len(client.edits) != 0 || len(client.pins) != 7 {
		t.Fatalf("created=%+v sends=%d edits=%d pins=%d", created.Items, len(client.sends), len(client.edits), len(client.pins))
	}
	for _, item := range created.Items {
		if item.Status != "sent_pinned" || item.MessageID == 0 {
			t.Fatalf("created item=%+v", item)
		}
		storedID, ok, err := store.WorkspaceTopicIndexMessage(context.Background(), cfg.Workspace.ChatID, item.TopicID, workspaceCommandHelpIndexKey)
		if err != nil || !ok || storedID != item.MessageID {
			t.Fatalf("tracked item=%+v stored=%d ok=%t err=%v", item, storedID, ok, err)
		}
	}

	updated, err := seedWorkspaceCommandHelpWithClient(context.Background(), cfg, store, client, SeedTopicPinsOptions{Now: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Items) != 7 || len(client.sends) != 7 || len(client.edits) != 7 || len(client.pins) != 14 {
		t.Fatalf("updated=%+v sends=%d edits=%d pins=%d", updated.Items, len(client.sends), len(client.edits), len(client.pins))
	}
	for _, item := range updated.Items {
		if item.Status != "updated_pinned" || item.MessageID == 0 {
			t.Fatalf("updated item=%+v", item)
		}
	}
}
