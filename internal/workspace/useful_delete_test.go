package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestUsefulDeletionPreservesPublicationOnFailureAndCanResume(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failure   error
		friendly  bool
		failureAt int
		progress  string
	}{
		{"too old", &nest.BotAPIError{Method: "deleteMessage", StatusCode: 400, Description: "Bad Request: message can't be deleted"}, true, 750, "Ни одно сообщение"},
		{"partial deletion", &nest.BotAPIError{Method: "deleteMessage", StatusCode: 400, Description: "Bad Request: message can't be deleted"}, true, 751, "1 из 3"},
		{"no rights", &nest.BotAPIError{Method: "deleteMessage", StatusCode: 403, Description: "Forbidden"}, true, 751, "1 из 3"},
		{"unknown delivery", errors.New("connection lost"), false, 751, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Now().UTC()
			doc, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{Type: "note", Status: "published", Title: "Полумарафон", SourceChatID: -1001, SourceMessageID: 12, TargetChatID: -1001, TargetTopicID: 18, TargetMessageID: 750}, sqlitestore.WorkspaceDocumentPart{SourceChatID: -1001, SourceMessageID: 12, Text: "source"}, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []int{750, 751, 752} {
				err = store.UpsertWorkspaceDerivedMessage(ctx, sqlitestore.WorkspaceDerivedMessage{SourceChatID: -1001, SourceMessageID: 12, DerivedType: "legacy_migration_migrate_part_" + strconv.Itoa(id-749), DerivedChatID: -1001, DerivedTopicID: 18, DerivedMessageID: id, Status: "published"}, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			var calls []int
			err = removeUsefulPublicationMessages(ctx, store, doc.ID, -1001, 18, []int{750, 751, 752}, now, func(_ context.Context, chatID int64, id int) error {
				if chatID != -1001 {
					t.Fatalf("chat=%d", chatID)
				}
				calls = append(calls, id)
				if id == tc.failureAt {
					return tc.failure
				}
				return nil
			})
			if err == nil || !reflect.DeepEqual(calls, []int{750, 751}[:tc.failureAt-749]) {
				t.Fatalf("calls=%v err=%v", calls, err)
			}
			if tc.friendly && (!strings.Contains(err.Error(), "/useful delete ") || !strings.Contains(err.Error(), tc.progress) || strings.Contains(err.Error(), "HTTP")) {
				t.Fatalf("unhelpful error: %v", err)
			}
			if !tc.friendly && !errors.Is(err, tc.failure) {
				t.Fatalf("cause lost: %v", err)
			}
			saved, err := store.WorkspaceDocumentByID(ctx, doc.ID)
			if err != nil || saved.Status != "published" {
				t.Fatalf("doc=%+v err=%v", saved, err)
			}
			derived, err := store.WorkspaceDerivedMessagesBySource(ctx, -1001, 12, "legacy_migration_", []string{"published"}, 10)
			if err != nil || len(derived) != 3 {
				t.Fatalf("provenance changed: %+v err=%v", derived, err)
			}
			calls = nil
			err = removeUsefulPublicationMessages(ctx, store, doc.ID, -1001, 18, []int{750, 751, 752}, now, func(_ context.Context, _ int64, id int) error {
				calls = append(calls, id)
				if id == 750 {
					return &nest.BotAPIError{Method: "deleteMessage", StatusCode: 400, Description: "Bad Request: message to delete not found"}
				}
				return nil
			})
			if err != nil || !reflect.DeepEqual(calls, []int{750, 751, 752}) {
				t.Fatalf("retry calls=%v err=%v", calls, err)
			}
			saved, err = store.WorkspaceDocumentByID(ctx, doc.ID)
			if err != nil || saved.Status != "archived" {
				t.Fatalf("doc=%+v err=%v", saved, err)
			}
			derived, err = store.WorkspaceDerivedMessagesBySource(ctx, -1001, 12, "legacy_migration_", []string{"closed"}, 10)
			if err != nil || len(derived) != 3 {
				t.Fatalf("provenance not closed: %+v err=%v", derived, err)
			}
			parts, err := store.WorkspaceDocumentParts(ctx, doc.ID)
			if err != nil || len(parts) != 1 || parts[0].Text != "source" {
				t.Fatalf("sources changed: %+v err=%v", parts, err)
			}
		})
	}
}

func TestTemplateNumberingRestartsForEveryGroup(t *testing.T) {
	types := []sqlitestore.WorkspaceDocumentType{{Name: "First"}, {Name: "Empty"}, {Name: "Second"}}
	docs := []sqlitestore.WorkspaceDocument{
		{ID: 4, Title: "D", Category: "Second"}, {ID: 2, Title: "B", Category: "First"},
		{ID: 3, Title: "C", Category: "Second"}, {ID: 1, Title: "A", Category: "First"},
	}
	text := renderTemplatesIndex(types, docs, nil)
	for _, want := range []string{"1. <b>A</b>", "2. <b>B</b>", "1. <b>C</b>", "2. <b>D</b>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "3. ") || strings.Index(text, "<b>A</b>") > strings.Index(text, "<b>B</b>") {
		t.Fatalf("incorrect numbering or ordering: %s", text)
	}
}

func TestUsefulDeleteConfirmationEndsAfterFailedAttempt(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := testWorkspaceLiveConfig()
	key := pendingTaskDateKey{chatID: cfg.Workspace.ChatID, threadID: cfg.Workspace.Topics.Inbox, userID: 7}
	pending := map[pendingTaskDateKey]pendingWorkspaceInput{key: {Kind: "useful_delete", DocumentID: 999}}
	message := nest.Message{Chat: nest.Chat{ID: cfg.Workspace.ChatID}, From: &nest.User{ID: 7}, Text: "Удалить"}
	// Missing document fails before any deletion. An empty token disables external sends.
	client := nest.New("")
	if !handlePendingWorkspaceInputMessage(ctx, cfg, store, client, pending, nil, message, key.threadID) {
		t.Fatal("confirmation was not handled")
	}
	if _, ok := pending[key]; ok {
		t.Fatal("failed attempt left a pending deletion")
	}
	message.Text = "#tasks\nКупить фильтры\nПоменять фильтры"
	if handlePendingWorkspaceInputMessage(ctx, cfg, store, client, pending, nil, message, key.threadID) {
		t.Fatal("unrelated task list was swallowed")
	}
}
