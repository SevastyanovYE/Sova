package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceReminderHistoryPreservesRepeatedScheduleAndClosures(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	due := now.Add(-time.Hour)
	task := createReminderTestTask(t, store, 71, &due, now)

	for _, messageID := range []int{201, 202} {
		if messageID == 202 {
			if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "deferred", &due, now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.EnsureDueWorkspaceTaskReminders(ctx, now); err != nil {
			t.Fatal(err)
		}
		reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, due)
		if err != nil || !ok {
			t.Fatalf("reminder=%+v ok=%t err=%v", reminder, ok, err)
		}
		claimReminderForSend(t, store, reminder.ID, now)
		if err := store.MarkWorkspaceTaskReminderSent(ctx, reminder.ID, -1001, 10, messageID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReopenWorkspaceTaskForReminder(ctx, reminder.ID, now); err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteWorkspaceTaskReminder(ctx, reminder.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	if ready, err := store.ReadyWorkspaceTaskReminderClosures(ctx, -1001, 10, 0, now, 10); err != nil || len(ready) != 0 {
		t.Fatalf("open task closures=%+v err=%v", ready, err)
	}
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "done", nil, now); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ReadyWorkspaceTaskReminderClosures(ctx, -1001, 10, task.ID, now, 10)
	if err != nil || len(ready) != 2 || ready[0].MessageID != 201 || ready[1].MessageID != 202 || ready[0].Task.Status != "done" {
		t.Fatalf("terminal reminder history=%+v err=%v", ready, err)
	}
	for _, identity := range []struct {
		chatID  int64
		topicID int
		taskID  int64
	}{{-1002, 10, 0}, {-1001, 11, 0}, {-1001, 10, task.ID + 1}} {
		if ready, err := store.ReadyWorkspaceTaskReminderClosures(ctx, identity.chatID, identity.topicID, identity.taskID, now, 10); err != nil || len(ready) != 0 {
			t.Fatalf("other scope selected=%+v err=%v", ready, err)
		}
	}
	if err := store.MarkWorkspaceTaskReminderClosed(ctx, -1001, 201, "done", now); err != nil {
		t.Fatal(err)
	}
	retryAt := now.Add(time.Minute)
	if err := store.RetryWorkspaceTaskReminderClosure(ctx, -1001, 202, retryAt, "temporary failure", now); err != nil {
		t.Fatal(err)
	}
	if ready, err := store.ReadyWorkspaceTaskReminderClosures(ctx, -1001, 10, 0, now, 10); err != nil || len(ready) != 0 {
		t.Fatalf("premature retry=%+v err=%v", ready, err)
	}
	ready, err = store.ReadyWorkspaceTaskReminderClosures(ctx, -1001, 10, 0, retryAt, 10)
	if err != nil || len(ready) != 1 || ready[0].MessageID != 202 || ready[0].Attempts != 1 {
		t.Fatalf("due retry=%+v err=%v", ready, err)
	}
	if err := store.MarkWorkspaceTaskReminderClosed(ctx, -1001, 202, "done", retryAt); err != nil {
		t.Fatal(err)
	}
	if ready, err := store.ReadyWorkspaceTaskReminderClosures(ctx, -1001, 10, 0, retryAt, 10); err != nil || len(ready) != 0 {
		t.Fatalf("completed closures=%+v err=%v", ready, err)
	}
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "cancelled", nil, retryAt); err != nil {
		t.Fatal(err)
	}
	ready, err = store.ReadyWorkspaceTaskReminderClosures(ctx, -1001, 10, 0, retryAt, 1)
	if err != nil || len(ready) != 1 || ready[0].Task.Status != "cancelled" || ready[0].Attempts != 0 {
		t.Fatalf("changed terminal status=%+v err=%v", ready, err)
	}
	if err := store.MarkWorkspaceTaskReminderClosed(ctx, -1001, 201, "open", retryAt); err == nil {
		t.Fatal("non-terminal rendered status accepted")
	}
	ready, err = store.ReadyWorkspaceTaskReminderClosures(ctx, -1001, 10, 0, retryAt, 10)
	if err != nil || len(ready) != 2 {
		t.Fatalf("cancelled task closures=%+v err=%v", ready, err)
	}
	for _, message := range ready {
		if err := store.MarkWorkspaceTaskReminderClosed(ctx, message.ChatID, message.MessageID, "cancelled", retryAt); err != nil {
			t.Fatal(err)
		}
	}
	if ready, err := store.ReadyWorkspaceTaskReminderClosures(ctx, -1001, 10, 0, retryAt, 10); err != nil || len(ready) != 0 {
		t.Fatalf("completed cancellation closures=%+v err=%v", ready, err)
	}
}

func TestWorkspaceReminderMessageMigrationBackfillsAndKeepsCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sova.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	due := now.Add(-time.Hour)
	task := createReminderTestTask(t, store, 81, &due, now)
	// Recreate the pre-sending-status schema and omit the new ledger to exercise
	// both legacy outbox migration and historical identity backfill on Open.
	if _, err := store.db.Exec(`
DROP TABLE workspace_task_reminder_messages;
DROP TABLE workspace_task_reminders;
CREATE TABLE workspace_task_reminders (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 task_id INTEGER NOT NULL REFERENCES workspace_tasks(id) ON DELETE CASCADE,
 scheduled_for TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending'
   CHECK (status IN ('pending','retry','sent','completed','unknown','cancelled')),
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TEXT,
 last_error TEXT NOT NULL DEFAULT '',
 reminder_chat_id INTEGER NOT NULL DEFAULT 0,
 reminder_topic_id INTEGER NOT NULL DEFAULT 0,
 reminder_message_id INTEGER NOT NULL DEFAULT 0,
 sent_at TEXT, completed_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(task_id,scheduled_for)
);
INSERT INTO workspace_task_reminders(task_id,scheduled_for,status,reminder_chat_id,
 reminder_topic_id,reminder_message_id,sent_at,created_at,updated_at)
VALUES (?,?,'completed',-1001,10,301,?,?,?)`, task.ID, due.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "cancelled", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := store.ReadyWorkspaceTaskReminderClosures(ctx, -1001, 10, 0, now, 10)
	if err != nil || len(ready) != 1 || ready[0].MessageID != 301 || ready[0].Task.ID != task.ID {
		t.Fatalf("backfilled closures=%+v err=%v", ready, err)
	}
	if err := store.MarkWorkspaceTaskReminderClosed(ctx, -1001, 301, "cancelled", now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if ready, err := store.ReadyWorkspaceTaskReminderClosures(ctx, -1001, 10, 0, now, 10); err != nil || len(ready) != 0 {
		t.Fatalf("re-init lost completion=%+v err=%v", ready, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM workspace_task_reminder_messages`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("backfill duplicated history count=%d err=%v", count, err)
	}
}

func TestWorkspaceReminderSendAndHistoryAreAtomic(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	due := now.Add(-time.Hour)
	task := createReminderTestTask(t, store, 91, &due, now)
	if _, err := store.EnsureDueWorkspaceTaskReminders(ctx, now); err != nil {
		t.Fatal(err)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, due)
	if err != nil || !ok {
		t.Fatalf("reminder=%+v ok=%t err=%v", reminder, ok, err)
	}
	claimReminderForSend(t, store, reminder.ID, now)
	if _, err := store.db.Exec(`CREATE TRIGGER fail_history_insert
BEFORE INSERT ON workspace_task_reminder_messages BEGIN
SELECT RAISE(ABORT, 'simulated history failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspaceTaskReminderSent(ctx, reminder.ID, -1001, 10, 401, now); err == nil {
		t.Fatal("history failure was ignored")
	}
	stored, _, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, due)
	if err != nil || stored.Status != "sending" || stored.ReminderMessageID != 0 || stored.Attempts != 0 {
		t.Fatalf("partial sent state committed=%+v err=%v", stored, err)
	}
}

func TestOldestActiveWorkspaceTaskUsesOriginalCardOrder(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if _, found, err := store.OldestActiveWorkspaceTask(ctx, -1001, 10); err != nil || found {
		t.Fatalf("empty found=%t err=%v", found, err)
	}
	var expected WorkspaceTask
	for i, spec := range []struct {
		chatID    int64
		topicID   int
		messageID int
		status    string
	}{
		{-1001, 10, 30, "open"},
		{-1002, 10, 1, "open"},
		{-1001, 11, 2, "open"},
		{-1001, 10, 0, "open"},
		{-1001, 10, 3, "done"},
		{-1001, 10, 4, "cancelled"},
		{-1001, 10, 20, "deferred"},
		{-1001, 10, 40, "open"},
		{0, 10, 5, "open"},
		{-1001, 0, 6, "open"},
	} {
		task, err := store.CreateWorkspaceTask(ctx, WorkspaceTask{
			SourceChatID: -1001, SourceMessageID: i + 1, Text: "Task",
			CardChatID: spec.chatID, CardTopicID: spec.topicID, CardMessageID: spec.messageID,
		}, now.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if spec.status != "open" {
			future := now.Add(365 * 24 * time.Hour)
			if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, spec.status, &future, future); err != nil {
				t.Fatal(err)
			}
		}
		if spec.messageID == 30 {
			expected = task
		}
	}
	oldest, found, err := store.OldestActiveWorkspaceTask(ctx, -1001, 10)
	if err != nil || !found || oldest.ID != expected.ID || oldest.Status != "open" || oldest.CardMessageID != 30 {
		t.Fatalf("oldest=%+v found=%t err=%v", oldest, found, err)
	}
	for _, scope := range []struct {
		chatID  int64
		topicID int
	}{{0, 10}, {-1001, 0}} {
		if _, found, err := store.OldestActiveWorkspaceTask(ctx, scope.chatID, scope.topicID); err != nil || found {
			t.Fatalf("invalid card scope selected found=%t err=%v", found, err)
		}
	}
	if err := store.UpdateWorkspaceTaskStatus(ctx, oldest.ID, "done", nil, now); err != nil {
		t.Fatal(err)
	}
	oldest, found, err = store.OldestActiveWorkspaceTask(ctx, -1001, 10)
	if err != nil || !found || oldest.CardMessageID != 40 {
		t.Fatalf("next oldest=%+v found=%t err=%v", oldest, found, err)
	}
	if err := store.UpdateWorkspaceTaskStatus(ctx, oldest.ID, "deferred", nil, now); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.OldestActiveWorkspaceTask(ctx, -1001, 10); err != nil || found {
		t.Fatalf("deferred-only found=%t err=%v", found, err)
	}
}
