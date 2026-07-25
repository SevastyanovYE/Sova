package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceTaskReminderOutboxSelectsOnlyDueDatedTasks(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Hour)
	futureAt := now.Add(time.Hour)
	due := createReminderTestTask(t, store, 1, &dueAt, now)
	createReminderTestTask(t, store, 2, &futureAt, now)
	createReminderTestTask(t, store, 3, nil, now)

	created, err := store.EnsureDueWorkspaceTaskReminders(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("created reminders = %d, want 1", created)
	}
	if created, err = store.EnsureDueWorkspaceTaskReminders(ctx, now); err != nil || created != 0 {
		t.Fatalf("second ensure created=%d err=%v, want dedupe", created, err)
	}
	ready, err := store.ReadyWorkspaceTaskReminders(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].TaskID != due.ID || ready[0].ScheduledFor != dueAt {
		t.Fatalf("ready reminders = %+v", ready)
	}
}

func TestWorkspaceTaskReminderRetryAndReopenLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)
	task := createReminderTestTask(t, store, 11, &dueAt, now)
	if _, err := store.EnsureDueWorkspaceTaskReminders(ctx, now); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ReadyWorkspaceTaskReminders(ctx, now, 10)
	if err != nil || len(ready) != 1 {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	reminder := ready[0]
	next := now.Add(time.Minute)
	claimReminderForSend(t, store, reminder.ID, now)
	if err := store.MarkWorkspaceTaskReminderRetry(ctx, reminder.ID, next, "temporary failure", now); err != nil {
		t.Fatal(err)
	}
	if ready, err := store.ReadyWorkspaceTaskReminders(ctx, now.Add(30*time.Second), 10); err != nil || len(ready) != 0 {
		t.Fatalf("early retry ready=%+v err=%v", ready, err)
	}
	ready, err = store.ReadyWorkspaceTaskReminders(ctx, next, 10)
	if err != nil || len(ready) != 1 || ready[0].Attempts != 1 {
		t.Fatalf("due retry=%+v err=%v", ready, err)
	}
	claimReminderForSend(t, store, reminder.ID, next)
	if err := store.MarkWorkspaceTaskReminderSent(ctx, reminder.ID, -1001, 10, 90, next); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.ReopenWorkspaceTaskForReminder(ctx, reminder.ID, next)
	if err != nil || !reopened {
		t.Fatalf("reopened=%t err=%v", reopened, err)
	}
	storedTask, err := store.WorkspaceTaskByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.Status != "open" || storedTask.DeferredUntil != nil {
		t.Fatalf("task after reopen = %+v", storedTask)
	}
	if err := store.CompleteWorkspaceTaskReminder(ctx, reminder.ID, next); err != nil {
		t.Fatal(err)
	}
	stored, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, dueAt)
	if err != nil || !ok || stored.Status != "completed" || stored.Attempts != 2 {
		t.Fatalf("stored reminder=%+v ok=%t err=%v", stored, ok, err)
	}
	newSchedule := next.Add(2 * time.Hour)
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "deferred", &newSchedule, next); err != nil {
		t.Fatal(err)
	}
	if created, err := store.EnsureDueWorkspaceTaskReminders(ctx, newSchedule); err != nil || created != 1 {
		t.Fatalf("new deferral created=%d err=%v", created, err)
	}
	newReminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, newSchedule)
	if err != nil || !ok || newReminder.Status != "pending" {
		t.Fatalf("new generation=%+v ok=%t err=%v", newReminder, ok, err)
	}
	claimReminderForSend(t, store, newReminder.ID, newSchedule)
	if err := store.MarkWorkspaceTaskReminderSent(ctx, newReminder.ID, -1001, 10, 91, newSchedule); err != nil {
		t.Fatal(err)
	}
	if reopened, err := store.ReopenWorkspaceTaskForReminder(ctx, newReminder.ID, newSchedule); err != nil || !reopened {
		t.Fatalf("same-date generation reopen=%t err=%v", reopened, err)
	}
	if err := store.CompleteWorkspaceTaskReminder(ctx, newReminder.ID, newSchedule); err != nil {
		t.Fatal(err)
	}
	reassignedAt := newSchedule.Add(time.Minute)
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "deferred", &newSchedule, reassignedAt); err != nil {
		t.Fatal(err)
	}
	if created, err := store.EnsureDueWorkspaceTaskReminders(ctx, reassignedAt); err != nil || created != 1 {
		t.Fatalf("same-date re-deferral rearmed=%d err=%v", created, err)
	}
	rearmed, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, newSchedule)
	if err != nil || !ok || rearmed.Status != "pending" || rearmed.Attempts != 0 || rearmed.ReminderMessageID != 0 {
		t.Fatalf("rearmed reminder=%+v ok=%t err=%v", rearmed, ok, err)
	}
}

func TestWorkspaceTaskReminderUnknownIsNotRearmedByTextEdit(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)
	task := createReminderTestTask(t, store, 21, &dueAt, now)
	if _, err := store.EnsureDueWorkspaceTaskReminders(ctx, now); err != nil {
		t.Fatal(err)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, dueAt)
	if err != nil || !ok {
		t.Fatalf("reminder=%+v ok=%t err=%v", reminder, ok, err)
	}
	claimReminderForSend(t, store, reminder.ID, now)
	if err := store.MarkWorkspaceTaskReminderUnknown(ctx, reminder.ID, "ambiguous Telegram result", now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceTaskText(ctx, task.ID, "Edited task", "✨", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if created, err := store.EnsureDueWorkspaceTaskReminders(ctx, now.Add(time.Minute)); err != nil || created != 0 {
		t.Fatalf("text edit rearmed reminders=%d err=%v", created, err)
	}
	ready, err := store.ReadyWorkspaceTaskReminders(ctx, now.Add(time.Minute), 10)
	if err != nil || len(ready) != 0 {
		t.Fatalf("unknown reminder became ready=%+v err=%v", ready, err)
	}
	stored, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, dueAt)
	if err != nil || !ok || stored.Status != "unknown" || stored.Generation != reminder.Generation {
		t.Fatalf("stored reminder=%+v ok=%t err=%v", stored, ok, err)
	}
	reassignedAt := now.Add(2 * time.Minute)
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "deferred", &dueAt, reassignedAt); err != nil {
		t.Fatal(err)
	}
	if created, err := store.EnsureDueWorkspaceTaskReminders(ctx, reassignedAt); err != nil || created != 1 {
		t.Fatalf("explicit same-date deferral rearmed=%d err=%v", created, err)
	}
	rearmed, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, dueAt)
	if err != nil || !ok || rearmed.Status != "pending" || rearmed.Generation != reminder.Generation+1 {
		t.Fatalf("rearmed reminder=%+v ok=%t err=%v", rearmed, ok, err)
	}
}

func TestWorkspaceTaskReminderExplicitSameDateDeferralSupersedesRetry(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)
	task := createReminderTestTask(t, store, 31, &dueAt, now)
	if _, err := store.EnsureDueWorkspaceTaskReminders(ctx, now); err != nil {
		t.Fatal(err)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, dueAt)
	if err != nil || !ok {
		t.Fatalf("reminder=%+v ok=%t err=%v", reminder, ok, err)
	}
	claimReminderForSend(t, store, reminder.ID, now)
	if err := store.MarkWorkspaceTaskReminderRetry(ctx, reminder.ID, now.Add(time.Hour), "rate limited", now); err != nil {
		t.Fatal(err)
	}
	reassignedAt := now.Add(time.Minute)
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "deferred", &dueAt, reassignedAt); err != nil {
		t.Fatal(err)
	}
	if created, err := store.EnsureDueWorkspaceTaskReminders(ctx, reassignedAt); err != nil || created != 1 {
		t.Fatalf("same-date deferral superseded retry=%d err=%v", created, err)
	}
	rearmed, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, dueAt)
	if err != nil || !ok || rearmed.Status != "pending" || rearmed.Generation != reminder.Generation+1 || rearmed.Attempts != 0 || rearmed.NextAttemptAt != nil {
		t.Fatalf("rearmed reminder=%+v ok=%t err=%v", rearmed, ok, err)
	}
}

func TestWorkspaceTaskReminderStaleSentGenerationCannotReopenTask(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)
	task := createReminderTestTask(t, store, 41, &dueAt, now)
	if _, err := store.EnsureDueWorkspaceTaskReminders(ctx, now); err != nil {
		t.Fatal(err)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, dueAt)
	if err != nil || !ok {
		t.Fatalf("reminder=%+v ok=%t err=%v", reminder, ok, err)
	}
	claimReminderForSend(t, store, reminder.ID, now)
	if err := store.MarkWorkspaceTaskReminderSent(ctx, reminder.ID, -1001, 10, 99, now); err != nil {
		t.Fatal(err)
	}
	reassignedAt := now.Add(time.Minute)
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "deferred", &dueAt, reassignedAt); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.ReopenWorkspaceTaskForReminder(ctx, reminder.ID, reassignedAt)
	if err != nil || reopened {
		t.Fatalf("stale generation reopened=%t err=%v", reopened, err)
	}
	storedTask, err := store.WorkspaceTaskByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.Status != "deferred" || storedTask.DeferredUntil == nil || !storedTask.DeferredUntil.Equal(dueAt) || storedTask.DeferredGeneration != reminder.Generation+1 {
		t.Fatalf("task was clobbered by stale reminder: %+v", storedTask)
	}
	stale, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, dueAt)
	if err != nil || !ok || stale.Status != "cancelled" || stale.Generation != reminder.Generation {
		t.Fatalf("stale reminder=%+v ok=%t err=%v", stale, ok, err)
	}
	if created, err := store.EnsureDueWorkspaceTaskReminders(ctx, reassignedAt); err != nil || created != 1 {
		t.Fatalf("current generation rearmed=%d err=%v", created, err)
	}
}

func TestWorkspaceTaskReminderInterruptedSendRecoversAsUnknown(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "sova.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)
	task := createReminderTestTask(t, store, 51, &dueAt, now)
	if _, err := store.EnsureDueWorkspaceTaskReminders(ctx, now); err != nil {
		t.Fatal(err)
	}
	reminder, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, dueAt)
	if err != nil || !ok {
		t.Fatalf("reminder=%+v ok=%t err=%v", reminder, ok, err)
	}
	claimReminderForSend(t, store, reminder.ID, now)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recovered, err := store.RecoverInterruptedWorkspaceTaskReminders(ctx, now.Add(time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	ready, err := store.ReadyWorkspaceTaskReminders(ctx, now.Add(24*time.Hour), 10)
	if err != nil || len(ready) != 0 {
		t.Fatalf("interrupted reminder became ready=%+v err=%v", ready, err)
	}
	stored, ok, err := store.WorkspaceTaskReminderByTaskAndSchedule(ctx, task.ID, dueAt)
	if err != nil || !ok || stored.Status != "unknown" || stored.Attempts != 1 {
		t.Fatalf("stored=%+v ok=%t err=%v", stored, ok, err)
	}
}

func claimReminderForSend(t *testing.T, store *Store, reminderID int64, now time.Time) {
	t.Helper()
	claimed, err := store.ClaimWorkspaceTaskReminderForSend(context.Background(), reminderID, now)
	if err != nil || !claimed {
		t.Fatalf("claim reminder %d: claimed=%t err=%v", reminderID, claimed, err)
	}
}

func createReminderTestTask(t *testing.T, store *Store, messageID int, deferredUntil *time.Time, now time.Time) WorkspaceTask {
	t.Helper()
	task, err := store.CreateWorkspaceTask(context.Background(), WorkspaceTask{
		SourceChatID:    -1001,
		SourceMessageID: messageID,
		SourceLink:      "https://t.me/c/1/8/" + time.Unix(int64(messageID), 0).Format("05"),
		CardChatID:      -1001,
		CardTopicID:     10,
		CardMessageID:   100 + messageID,
		Text:            "Task",
		Emoji:           "✨",
		Status:          "deferred",
		DeferredUntil:   deferredUntil,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return task
}
