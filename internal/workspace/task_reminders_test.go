package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type fakeTaskReminderTelegram struct {
	mu        sync.Mutex
	sends     []nest.SendMessageRequest
	edits     []nest.EditMessageTextRequest
	sendError error
	editError error
	nextID    int
	sent      chan struct{}
	cancel    context.CancelFunc
}

func (f *fakeTaskReminderTelegram) SendMessageResult(_ context.Context, request nest.SendMessageRequest) (nest.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, request)
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	if f.sendError != nil {
		return nest.Message{}, f.sendError
	}
	f.nextID++
	if f.sent != nil && len(f.sends) == 1 {
		close(f.sent)
		f.sent = nil
	}
	return nest.Message{MessageID: 900 + f.nextID}, nil
}

func (f *fakeTaskReminderTelegram) EditMessageText(_ context.Context, request nest.EditMessageTextRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, request)
	return f.editError
}

func TestProcessWorkspaceTaskRemindersDeliversReopensAndDedupes(t *testing.T) {
	store, task, dueAt, now := openReminderWorkspaceTest(t)
	client := &fakeTaskReminderTelegram{}
	cfg := testWorkspaceLiveConfig()
	cfg.Timezone = "Europe/Moscow"
	if err := processWorkspaceTaskReminders(context.Background(), cfg, store, client, now); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	if len(client.sends) != 2 { // fresh reminder, then the refreshed backlog index
		t.Fatalf("send count = %d, want 2", len(client.sends))
	}
	reminderSend := client.sends[0]
	if reminderSend.MessageThreadID != cfg.Workspace.Topics.Tasks ||
		!strings.Contains(reminderSend.Text, "Пора вернуться") ||
		!strings.Contains(reminderSend.Text, "/10/211") {
		t.Fatalf("reminder send = %+v", reminderSend)
	}
	if len(client.edits) != 1 || strings.Contains(client.edits[0].Text, "Отложено") {
		t.Fatalf("task edits = %+v", client.edits)
	}
	client.mu.Unlock()
	storedTask, err := store.WorkspaceTaskByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.Status != "open" || storedTask.DeferredUntil != nil {
		t.Fatalf("stored task = %+v", storedTask)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(context.Background(), task.ID, dueAt)
	if err != nil || !ok || reminder.Status != "completed" || reminder.ReminderMessageID == 0 {
		t.Fatalf("reminder=%+v ok=%t err=%v", reminder, ok, err)
	}
	if err := processWorkspaceTaskReminders(context.Background(), cfg, store, client, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sends) != 2 {
		t.Fatalf("completed reminder was sent again: sends=%d", len(client.sends))
	}
}

func TestProcessWorkspaceTaskRemindersRetriesConfirmedFailure(t *testing.T) {
	store, task, dueAt, now := openReminderWorkspaceTest(t)
	client := &fakeTaskReminderTelegram{sendError: errors.New("Bot API sendMessage failed: Too Many Requests")}
	if err := processWorkspaceTaskReminders(context.Background(), testWorkspaceLiveConfig(), store, client, now); err != nil {
		t.Fatal(err)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(context.Background(), task.ID, dueAt)
	if err != nil || !ok {
		t.Fatalf("reminder ok=%t err=%v", ok, err)
	}
	if reminder.Status != "retry" || reminder.Attempts != 1 || reminder.NextAttemptAt == nil || !reminder.NextAttemptAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("retry reminder = %+v", reminder)
	}
	storedTask, _ := store.WorkspaceTaskByID(context.Background(), task.ID)
	if storedTask.Status != "deferred" {
		t.Fatalf("failed delivery reopened task: %+v", storedTask)
	}
}

func TestProcessWorkspaceTaskRemindersRetriesDefinitelyUnsentDialFailure(t *testing.T) {
	store, task, dueAt, now := openReminderWorkspaceTest(t)
	client := &fakeTaskReminderTelegram{sendError: &nest.DefinitelyUnsentError{}}
	if err := processWorkspaceTaskReminders(context.Background(), testWorkspaceLiveConfig(), store, client, now); err != nil {
		t.Fatal(err)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(context.Background(), task.ID, dueAt)
	if err != nil || !ok {
		t.Fatalf("reminder ok=%t err=%v", ok, err)
	}
	if reminder.Status != "retry" || reminder.Attempts != 1 || reminder.NextAttemptAt == nil || !reminder.NextAttemptAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("retry reminder = %+v", reminder)
	}
}

func TestProcessWorkspaceTaskRemindersStopsOnAmbiguousSend(t *testing.T) {
	store, task, dueAt, now := openReminderWorkspaceTest(t)
	client := &fakeTaskReminderTelegram{sendError: errors.New("Bot API sendMessage request failed: unexpected EOF")}
	if err := processWorkspaceTaskReminders(context.Background(), testWorkspaceLiveConfig(), store, client, now); err != nil {
		t.Fatal(err)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(context.Background(), task.ID, dueAt)
	if err != nil || !ok || reminder.Status != "unknown" || reminder.NextAttemptAt != nil {
		t.Fatalf("ambiguous reminder=%+v ok=%t err=%v", reminder, ok, err)
	}
	if err := processWorkspaceTaskReminders(context.Background(), testWorkspaceLiveConfig(), store, client, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sends) != 1 {
		t.Fatalf("ambiguous reminder resent: sends=%d", len(client.sends))
	}
}

func TestProcessWorkspaceTaskRemindersPersistsAmbiguousCancellation(t *testing.T) {
	store, task, dueAt, now := openReminderWorkspaceTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeTaskReminderTelegram{
		sendError: errors.New("Bot API sendMessage request failed: context canceled"),
		cancel:    cancel,
	}
	if err := processWorkspaceTaskReminders(ctx, testWorkspaceLiveConfig(), store, client, now); err != nil {
		t.Fatal(err)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(context.Background(), task.ID, dueAt)
	if err != nil || !ok || reminder.Status != "unknown" {
		t.Fatalf("cancelled send reminder=%+v ok=%t err=%v", reminder, ok, err)
	}
}

func TestProcessWorkspaceTaskRemindersResumesFinalizationWithoutResend(t *testing.T) {
	store, task, dueAt, now := openReminderWorkspaceTest(t)
	client := &fakeTaskReminderTelegram{editError: errors.New("temporary edit failure")}
	cfg := testWorkspaceLiveConfig()
	if err := processWorkspaceTaskReminders(context.Background(), cfg, store, client, now); err != nil {
		t.Fatal(err)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(context.Background(), task.ID, dueAt)
	if err != nil || !ok || reminder.Status != "sent" || reminder.NextAttemptAt == nil {
		t.Fatalf("pending finalization=%+v ok=%t err=%v", reminder, ok, err)
	}
	storedTask, err := store.WorkspaceTaskByID(context.Background(), task.ID)
	if err != nil || storedTask.Status != "open" {
		t.Fatalf("reopened task=%+v err=%v", storedTask, err)
	}
	client.editError = nil
	if err := processWorkspaceTaskReminders(context.Background(), cfg, store, client, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	reminder, _, err = store.WorkspaceTaskReminderByTaskAndSchedule(context.Background(), task.ID, dueAt)
	if err != nil || reminder.Status != "completed" {
		t.Fatalf("completed finalization=%+v err=%v", reminder, err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sends) != 2 { // original reminder plus backlog; no second reminder
		t.Fatalf("resume sent %d messages, want reminder plus backlog", len(client.sends))
	}
}

func TestWorkspaceTaskReminderLoopScansImmediatelyAndStopsWithContext(t *testing.T) {
	store, _, _, now := openReminderWorkspaceTest(t)
	sent := make(chan struct{})
	client := &fakeTaskReminderTelegram{sent: sent}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runWorkspaceTaskReminderLoopWithClock(ctx, testWorkspaceLiveConfig(), store, client, time.Hour, func() time.Time { return now })
		close(done)
	}()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("startup scan did not send reminder")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reminder loop did not stop with context")
	}
}

func TestWorkspaceTaskReminderRetrySchedule(t *testing.T) {
	want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, time.Hour}
	for i, expected := range want {
		if got := workspaceTaskReminderRetryDelay(i + 1); got != expected {
			t.Fatalf("attempt %d delay = %s, want %s", i+1, got, expected)
		}
	}
}

func TestTaskReminderClosuresCoverEveryDeliveryAndKeepLinks(t *testing.T) {
	for _, status := range []string{"done", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			store, task, due, now := openReminderWorkspaceTest(t)
			ctx := context.Background()
			cfg := testWorkspaceLiveConfig()
			client := &fakeTaskReminderTelegram{}
			if err := processWorkspaceTaskReminders(ctx, cfg, store, client, now); err != nil {
				t.Fatal(err)
			}
			// Reusing the exact same date is a new generation; both messages need closure.
			if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "deferred", &due, now); err != nil {
				t.Fatal(err)
			}
			if err := processWorkspaceTaskReminders(ctx, cfg, store, client, now); err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, status, nil, now); err != nil {
				t.Fatal(err)
			}
			client.edits = nil
			sendsBefore := len(client.sends)
			if err := closeWorkspaceTaskReminders(ctx, cfg, store, client, task.ID, now); err != nil {
				t.Fatal(err)
			}
			if len(client.edits) != 2 {
				t.Fatalf("closure edits = %d, want both deliveries", len(client.edits))
			}
			for _, edit := range client.edits {
				if !strings.HasPrefix(edit.Text, "<s>⏰ <b>Пора вернуться к задаче</b>") || !strings.Contains(edit.Text, `/10/211"`) || !strings.Contains(edit.Text, "</a></s>") || edit.ParseMode != "HTML" {
					t.Fatalf("closure = %+v", edit)
				}
				if strings.Contains(edit.Text, "Отменено") != (status == "cancelled") {
					t.Fatalf("wrong cancellation label: %s", edit.Text)
				}
			}
			if err := closeWorkspaceTaskReminders(ctx, cfg, store, client, task.ID, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if len(client.edits) != 2 || len(client.sends) != sendsBefore {
				t.Fatal("completed edits repeated or reminder resent")
			}
		})
	}
}

func TestTaskReminderClosureRetrySurvivesRestartAndNotModified(t *testing.T) {
	store, task, _, now := openReminderWorkspaceTest(t)
	ctx := context.Background()
	cfg := testWorkspaceLiveConfig()
	client := &fakeTaskReminderTelegram{}
	if err := processWorkspaceTaskReminders(ctx, cfg, store, client, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "done", nil, now); err != nil {
		t.Fatal(err)
	}
	client.edits = nil
	client.editError = errors.New("temporary network failure")
	if err := closeWorkspaceTaskReminders(ctx, cfg, store, client, task.ID, now); err == nil {
		t.Fatal("expected edit error")
	}
	if err := closeWorkspaceTaskReminders(ctx, cfg, store, client, task.ID, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(client.edits) != 1 {
		t.Fatal("retried before backoff elapsed")
	}
	// A fresh client/maintenance pass has no process-local retry state.
	restarted := &fakeTaskReminderTelegram{editError: errors.New("Bad Request: message is not modified")}
	if err := maintainWorkspaceTasks(ctx, cfg, store, restarted, now.Add(time.Minute)); err != nil && !isTelegramMessageNotModified(err) {
		t.Fatal(err)
	}
	ready, err := store.ReadyWorkspaceTaskReminderClosures(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks, 0, now.Add(time.Hour), 100)
	if err != nil || len(ready) != 0 {
		t.Fatalf("closure remained pending: %+v, %v", ready, err)
	}
	if len(restarted.sends) != 0 {
		t.Fatal("maintenance sent a replacement reminder or index")
	}
}

func TestTaskReminderTerminalRenderingRespectsTelegramLimit(t *testing.T) {
	for _, status := range []string{"open", "done", "cancelled"} {
		text := formatWorkspaceTaskReminder(sqlitestore.WorkspaceTask{
			Text: strings.Repeat("🙂<&", 1400), Emoji: "✨", Status: status,
		}, "https://t.me/c/4301779750/10/211")
		if !telegramHTMLFits(text, workspaceTelegramSafeTextLimit) ||
			!strings.Contains(text, `href="https://t.me/c/4301779750/10/211"`) {
			t.Fatalf("invalid %s reminder", status)
		}
		if status == "cancelled" && !strings.HasSuffix(text, "</s>\n<i>Отменено.</i>") {
			t.Fatal("cancellation suffix truncated")
		}
	}
}

type blockingTaskClosureTelegram struct {
	fakeTaskReminderTelegram
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (f *blockingTaskClosureTelegram) EditMessageText(ctx context.Context, request nest.EditMessageTextRequest) error {
	f.once.Do(func() { close(f.entered); <-f.release })
	return f.fakeTaskReminderTelegram.EditMessageText(ctx, request)
}

func TestTaskReminderConcurrentClosuresKeepLatestTerminalStatus(t *testing.T) {
	store, task, _, now := openReminderWorkspaceTest(t)
	ctx := context.Background()
	cfg := testWorkspaceLiveConfig()
	if err := processWorkspaceTaskReminders(ctx, cfg, store, &fakeTaskReminderTelegram{}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "done", nil, now); err != nil {
		t.Fatal(err)
	}
	client := &blockingTaskClosureTelegram{entered: make(chan struct{}), release: make(chan struct{})}
	firstResult := make(chan error, 1)
	go func() { firstResult <- closeWorkspaceTaskReminders(ctx, cfg, store, client, 0, now) }()
	<-client.entered
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "cancelled", nil, now); err != nil {
		t.Fatal(err)
	}
	secondResult := make(chan error, 1)
	go func() { secondResult <- closeWorkspaceTaskReminders(ctx, cfg, store, client, task.ID, now) }()
	select {
	case err := <-secondResult:
		close(client.release)
		<-firstResult
		t.Fatalf("concurrent closure bypassed gate: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(client.release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if err := <-secondResult; err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.edits) != 2 || !strings.HasSuffix(client.edits[1].Text, "</s>\n<i>Отменено.</i>") {
		t.Fatalf("latest status lost: %+v", client.edits)
	}
	ready, err := store.ReadyWorkspaceTaskReminderClosures(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks, 0, now, 100)
	if err != nil || len(ready) != 0 {
		t.Fatalf("closure marker = %+v, %v", ready, err)
	}
}

func openReminderWorkspaceTest(t *testing.T) (*sqlitestore.Store, sqlitestore.WorkspaceTask, time.Time, time.Time) {
	t.Helper()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)
	task, err := store.CreateWorkspaceTask(context.Background(), sqlitestore.WorkspaceTask{
		SourceChatID:    -1004301779750,
		SourceMessageID: 111,
		SourceLink:      "https://t.me/c/4301779750/8/111",
		CardChatID:      -1004301779750,
		CardTopicID:     10,
		CardMessageID:   211,
		Text:            "Проверить напоминание",
		Emoji:           "✨",
		Status:          "deferred",
		DeferredUntil:   &dueAt,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return store, task, dueAt, now
}
