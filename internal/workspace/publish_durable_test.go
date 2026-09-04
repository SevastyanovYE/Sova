package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type fakePublishMessageTelegram struct {
	mu       sync.Mutex
	calls    int
	message  nest.Message
	err      error
	requests []nest.SendMessageRequest
}

func (f *fakePublishMessageTelegram) SendMessageResult(_ context.Context, request nest.SendMessageRequest) (nest.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.requests = append(f.requests, request)
	return f.message, f.err
}

func (f *fakePublishMessageTelegram) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakePublishMessageTelegram) sentRequests() []nest.SendMessageRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nest.SendMessageRequest(nil), f.requests...)
}

func TestSendDurablePublishMessageDoesNotResendSendingOrUnknown(t *testing.T) {
	store, item := publishDurableTestMessage(t)
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	client := &fakePublishMessageTelegram{message: nest.Message{MessageID: 501}}
	request := nest.SendMessageRequest{ChatID: -1001, MessageThreadID: 22, Text: item.Text}
	if _, sent, err := sendDurablePublishMessage(ctx, store, client, item, request, now); err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	item.Status = "sending"
	if _, sent, err := sendDurablePublishMessage(ctx, store, client, item, request, now); err != nil || sent {
		t.Fatalf("sending row must be skipped: sent=%v err=%v", sent, err)
	}
	item.Status = "unknown"
	if _, sent, err := sendDurablePublishMessage(ctx, store, client, item, request, now); err != nil || sent {
		t.Fatalf("unknown row must be skipped: sent=%v err=%v", sent, err)
	}
	if client.callCount() != 1 {
		t.Fatalf("Telegram calls=%d, want 1", client.callCount())
	}
}

func TestSendDurablePublishMessageClassifiesRejectedAndAmbiguousFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sendErr    error
		wantStatus string
	}{
		{name: "known rejection remains retryable", sendErr: errors.New("Bot API sendMessage failed: Bad Request"), wantStatus: "pending"},
		{name: "definitely unsent dial failure remains retryable", sendErr: &nest.DefinitelyUnsentError{}, wantStatus: "pending"},
		{name: "ambiguous transport becomes unknown", sendErr: errors.New("read response: EOF"), wantStatus: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, item := publishDurableTestMessage(t)
			defer store.Close()
			client := &fakePublishMessageTelegram{err: tc.sendErr}
			_, _, err := sendDurablePublishMessage(context.Background(), store, client, item, nest.SendMessageRequest{
				ChatID: -1001, MessageThreadID: 22, Text: item.Text,
			}, time.Now().UTC())
			if err == nil {
				t.Fatal("expected send error")
			}
			items, err := store.WorkspacePublishMessages(context.Background(), item.RunID, "final")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0].Status != tc.wantStatus {
				t.Fatalf("items=%+v want status=%s", items, tc.wantStatus)
			}
		})
	}
}

func TestSendDurablePublishMessageConcurrentClaimSendsOnce(t *testing.T) {
	store, item := publishDurableTestMessage(t)
	defer store.Close()
	client := &fakePublishMessageTelegram{message: nest.Message{MessageID: 601}}
	request := nest.SendMessageRequest{ChatID: -1001, MessageThreadID: 22, Text: item.Text}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, _ = sendDurablePublishMessage(context.Background(), store, client, item, request, time.Now().UTC())
		}()
	}
	close(start)
	wg.Wait()
	if client.callCount() != 1 {
		t.Fatalf("Telegram calls=%d, want 1", client.callCount())
	}
}

func TestRecoverInterruptedGeneratingPublishCreatesActionableRevisionPreview(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	doc, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{Type: "note", Status: "active", Title: "Document"}, sqlitestore.WorkspaceDocumentPart{
		SourceChatID: -1001, SourceMessageID: 77, Text: "Source body",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateWorkspacePublishRun(ctx, doc.ID, "добавь вывод", -1001, 11, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakePublishMessageTelegram{message: nest.Message{MessageID: 701}}
	cfg := config.Config{Workspace: config.WorkspaceConfig{ChatID: -1001, Topics: config.WorkspaceTopicIDs{Inbox: 11}}}
	if err := recoverInterruptedWorkspacePublishRuns(ctx, cfg, store, client, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.WorkspacePublishRunByID(ctx, run.ID)
	if err != nil || recovered.Status != "awaiting_approval" {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	requests := client.sentRequests()
	if len(requests) != 1 || requests[0].ReplyMarkup == nil {
		t.Fatalf("requests=%+v", requests)
	}
	if !strings.Contains(requests[0].Text, "Свободная ревизия") {
		t.Fatalf("free-revision preview is not visibly marked: %s", requests[0].Text)
	}
	if strings.Contains(recovered.Messages[0].Text, "Свободная ревизия") {
		t.Fatal("preview-only notice leaked into durable final material")
	}
}

func TestRecoverInterruptedPreviewSendingNeverRepeatsAmbiguousSend(t *testing.T) {
	store, item := publishPreviewSendingTestMessage(t)
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.MarkWorkspacePublishMessageSending(ctx, item.ID, -1001, 11, now); err != nil {
		t.Fatal(err)
	}
	client := &fakePublishMessageTelegram{message: nest.Message{MessageID: 702}}
	cfg := config.Config{Workspace: config.WorkspaceConfig{ChatID: -1001, Topics: config.WorkspaceTopicIDs{Inbox: 11}}}
	if err := recoverInterruptedWorkspacePublishRuns(ctx, cfg, store, client, now.Add(time.Second)); err == nil {
		t.Fatal("ambiguous interrupted preview should require reconciliation")
	}
	if client.callCount() != 0 {
		t.Fatalf("ambiguous send repeated %d times", client.callCount())
	}
	run, err := store.WorkspacePublishRunByID(ctx, item.RunID)
	if err != nil || run.Status != "failed" || run.Messages[0].Status != "unknown" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
}

func TestRecoverInterruptedPreviewAlreadySentActivatesWithoutResend(t *testing.T) {
	store, item := publishPreviewSendingTestMessage(t)
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.MarkWorkspacePublishMessageSending(ctx, item.ID, -1001, 11, now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspacePublishMessageSent(ctx, item.ID, 703, now); err != nil {
		t.Fatal(err)
	}
	client := &fakePublishMessageTelegram{message: nest.Message{MessageID: 704}}
	cfg := config.Config{Workspace: config.WorkspaceConfig{ChatID: -1001, Topics: config.WorkspaceTopicIDs{Inbox: 11}}}
	if err := recoverInterruptedWorkspacePublishRuns(ctx, cfg, store, client, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if client.callCount() != 0 {
		t.Fatalf("confirmed preview resent %d times", client.callCount())
	}
	run, err := store.WorkspacePublishRunByID(ctx, item.RunID)
	if err != nil || run.Status != "awaiting_approval" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
}

func TestWorkspacePublishRunsIndexOmitsDurableContent(t *testing.T) {
	stateDir := t.TempDir()
	store, item := publishDurableTestMessage(t)
	defer store.Close()
	providerRaw := `https://api.telegram.org/bot123:SECRET/sendMessage {"raw":"SOURCE BODY"}`
	if err := store.SetWorkspacePublishRunError(context.Background(), item.RunID, publishTelemetryError(errors.New(providerRaw)), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspacePublishRunsIndex(context.Background(), config.Config{StateDir: stateDir}, store); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "index", "workspace-runs.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{item.Text, "SECRET REVISION", "SOURCE BODY", "bot123:SECRET", providerRaw} {
		if strings.Contains(text, secret) {
			t.Fatalf("index leaked durable content %q:\n%s", secret, text)
		}
	}
	if !strings.Contains(text, "| publishing |") {
		t.Fatalf("index does not contain stage:\n%s", text)
	}
}

func publishDurableTestMessage(t *testing.T) (*sqlitestore.Store, sqlitestore.WorkspacePublishMessage) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	doc, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{Type: "note", Status: "active", Title: "Document"}, sqlitestore.WorkspaceDocumentPart{
		SourceChatID: -1001, SourceMessageID: 77, Text: "SOURCE BODY",
	}, now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	run, err := store.CreateWorkspacePublishRun(ctx, doc.ID, "SECRET REVISION", -1001, 11, 0, now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.SetWorkspacePublishPreview(ctx, run.ID, "gemini-test", []string{"PRIVATE PREVIEW"}, now); err != nil {
		store.Close()
		t.Fatal(err)
	}
	preview, _ := store.WorkspacePublishMessages(ctx, run.ID, "preview")
	if err := store.MarkWorkspacePublishMessageSending(ctx, preview[0].ID, -1001, 11, now); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.MarkWorkspacePublishMessageSent(ctx, preview[0].ID, 401, now); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.ActivateWorkspacePublishPreview(ctx, run.ID, now); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.ApproveWorkspacePublishRun(ctx, run.ID, now); err != nil {
		store.Close()
		t.Fatal(err)
	}
	finals, err := store.WorkspacePublishMessages(ctx, run.ID, "final")
	if err != nil || len(finals) != 1 {
		store.Close()
		t.Fatalf("finals=%+v err=%v", finals, err)
	}
	return store, finals[0]
}

func publishPreviewSendingTestMessage(t *testing.T) (*sqlitestore.Store, sqlitestore.WorkspacePublishMessage) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	doc, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{Type: "note", Status: "active", Title: "Document"}, sqlitestore.WorkspaceDocumentPart{
		SourceChatID: -1001, SourceMessageID: 77, Text: "Source body",
	}, now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	run, err := store.CreateWorkspacePublishRun(ctx, doc.ID, "", -1001, 11, 0, now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.SetWorkspacePublishPreview(ctx, run.ID, "gemini", []string{"Preview"}, now); err != nil {
		store.Close()
		t.Fatal(err)
	}
	items, err := store.WorkspacePublishMessages(ctx, run.ID, "preview")
	if err != nil || len(items) != 1 {
		store.Close()
		t.Fatalf("items=%+v err=%v", items, err)
	}
	return store, items[0]
}
