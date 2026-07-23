package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WorkspaceTaskReminder is the durable outbox entry for one deferred-task date.
// A sent entry is deliberately kept separate from completed so post-send task
// card/index updates can be resumed without sending the reminder twice.
type WorkspaceTaskReminder struct {
	ID                int64
	TaskID            int64
	ScheduledFor      time.Time
	Generation        int
	Status            string
	Attempts          int
	NextAttemptAt     *time.Time
	LastError         string
	ReminderChatID    int64
	ReminderTopicID   int
	ReminderMessageID int
	SentAt            *time.Time
	CompletedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Task              WorkspaceTask
}

func (s *Store) EnsureDueWorkspaceTaskReminders(ctx context.Context, now time.Time) (int64, error) {
	nowRaw := now.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_task_reminders(
    task_id, scheduled_for, generation, status, created_at, updated_at
)
SELECT id, deferred_until, deferred_generation, 'pending', ?, ?
FROM workspace_tasks
WHERE status = 'deferred'
  AND deferred_until IS NOT NULL
  AND julianday(deferred_until) <= julianday(?)
ON CONFLICT(task_id, scheduled_for) DO UPDATE SET
    generation = excluded.generation, status = 'pending', attempts = 0,
    next_attempt_at = NULL, last_error = '',
    reminder_chat_id = 0, reminder_topic_id = 0, reminder_message_id = 0,
    sent_at = NULL, completed_at = NULL, created_at = excluded.created_at,
    updated_at = excluded.updated_at
WHERE excluded.generation > workspace_task_reminders.generation`, nowRaw, nowRaw, nowRaw)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ReadyWorkspaceTaskReminders(ctx context.Context, now time.Time, limit int) ([]WorkspaceTaskReminder, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, workspaceTaskReminderSelect()+`
WHERE (
    r.status IN ('pending', 'retry', 'sent')
    AND (r.next_attempt_at IS NULL OR julianday(r.next_attempt_at) <= julianday(?))
)
ORDER BY r.scheduled_for, r.id
LIMIT ?`, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reminders []WorkspaceTaskReminder
	for rows.Next() {
		reminder, err := scanWorkspaceTaskReminder(rows)
		if err != nil {
			return nil, err
		}
		reminders = append(reminders, reminder)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return reminders, nil
}

func (s *Store) WorkspaceTaskReminderByTaskAndSchedule(ctx context.Context, taskID int64, scheduledFor time.Time) (WorkspaceTaskReminder, bool, error) {
	row := s.db.QueryRowContext(ctx, workspaceTaskReminderSelect()+`
WHERE r.task_id = ? AND r.scheduled_for = ?`, taskID, scheduledFor.UTC().Format(time.RFC3339Nano))
	reminder, err := scanWorkspaceTaskReminder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceTaskReminder{}, false, nil
	}
	if err != nil {
		return WorkspaceTaskReminder{}, false, err
	}
	return reminder, true, nil
}

func (s *Store) MarkWorkspaceTaskReminderRetry(ctx context.Context, id int64, next time.Time, lastError string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_task_reminders
SET status = 'retry', attempts = attempts + 1, next_attempt_at = ?,
    last_error = ?, updated_at = ?
WHERE id = ? AND status IN ('pending', 'retry')`,
		next.UTC().Format(time.RFC3339Nano), compactReminderError(lastError),
		now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "actionable workspace task reminder %d not found", id)
}

func (s *Store) MarkWorkspaceTaskReminderUnknown(ctx context.Context, id int64, lastError string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_task_reminders
SET status = 'unknown', attempts = attempts + 1, next_attempt_at = NULL,
    last_error = ?, updated_at = ?
WHERE id = ? AND status IN ('pending', 'retry')`,
		compactReminderError(lastError), now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "actionable workspace task reminder %d not found", id)
}

func (s *Store) MarkWorkspaceTaskReminderSent(ctx context.Context, id int64, chatID int64, topicID int, messageID int, now time.Time) error {
	if chatID == 0 || topicID == 0 || messageID == 0 {
		return fmt.Errorf("invalid workspace task reminder message identity")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_task_reminders
SET status = 'sent', attempts = attempts + 1, next_attempt_at = NULL,
    last_error = '', reminder_chat_id = ?, reminder_topic_id = ?,
    reminder_message_id = ?, sent_at = ?, updated_at = ?
WHERE id = ? AND status IN ('pending', 'retry')`,
		chatID, topicID, messageID, now.UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "actionable workspace task reminder %d not found", id)
}

func (s *Store) CancelWorkspaceTaskReminder(ctx context.Context, id int64, reason string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_task_reminders
SET status = 'cancelled', next_attempt_at = NULL, last_error = ?,
    completed_at = ?, updated_at = ?
WHERE id = ? AND status IN ('pending', 'retry', 'sent')`, compactReminderError(reason),
		now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "actionable workspace task reminder %d not found", id)
}

// ReopenWorkspaceTaskForReminder atomically verifies that the task still has
// the reminder's date and reopens it. A stale outbox row is cancelled instead.
func (s *Store) ReopenWorkspaceTaskForReminder(ctx context.Context, id int64, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var reminderStatus, scheduledRaw, taskStatus string
	var reminderGeneration, taskGeneration int
	var deferredRaw sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT r.status, r.scheduled_for, r.generation,
       t.status, t.deferred_until, t.deferred_generation
FROM workspace_task_reminders r
JOIN workspace_tasks t ON t.id = r.task_id
WHERE r.id = ?`, id).Scan(
		&reminderStatus, &scheduledRaw, &reminderGeneration,
		&taskStatus, &deferredRaw, &taskGeneration,
	)
	if err != nil {
		return false, err
	}
	if reminderStatus != "sent" {
		return false, fmt.Errorf("workspace task reminder %d is %s, not sent", id, reminderStatus)
	}
	sameGeneration := reminderGeneration == taskGeneration
	currentDeferred := sameGeneration && taskStatus == "deferred" && deferredRaw.Valid && sameReminderInstant(scheduledRaw, deferredRaw.String)
	alreadyReopened := sameGeneration && taskStatus == "open" && !deferredRaw.Valid
	if currentDeferred {
		if _, err := tx.ExecContext(ctx, `
UPDATE workspace_tasks
SET status = 'open', deferred_until = NULL, updated_at = ?
WHERE id = (SELECT task_id FROM workspace_task_reminders WHERE id = ?)`,
			now.UTC().Format(time.RFC3339Nano), id); err != nil {
			return false, err
		}
	} else if !alreadyReopened {
		if _, err := tx.ExecContext(ctx, `
UPDATE workspace_task_reminders
SET status = 'cancelled', next_attempt_at = NULL,
    last_error = 'task changed before reminder finalization',
    completed_at = ?, updated_at = ?
WHERE id = ?`, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), id); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) RescheduleWorkspaceTaskReminderFinalization(ctx context.Context, id int64, next time.Time, lastError string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_task_reminders
SET next_attempt_at = ?, last_error = ?, updated_at = ?
WHERE id = ? AND status = 'sent'`, next.UTC().Format(time.RFC3339Nano),
		compactReminderError(lastError), now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sent workspace task reminder %d not found", id)
}

func (s *Store) CompleteWorkspaceTaskReminder(ctx context.Context, id int64, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_task_reminders
SET status = 'completed', next_attempt_at = NULL, last_error = '',
    completed_at = ?, updated_at = ?
WHERE id = ? AND status = 'sent'`, now.UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sent workspace task reminder %d not found", id)
}

func workspaceTaskReminderSelect() string {
	return `
SELECT r.id, r.task_id, r.scheduled_for, r.generation, r.status, r.attempts,
       r.next_attempt_at, r.last_error, r.reminder_chat_id,
       r.reminder_topic_id, r.reminder_message_id, r.sent_at,
       r.completed_at, r.created_at, r.updated_at,
       ` + workspaceTaskColumns("t") + `
FROM workspace_task_reminders r
JOIN workspace_tasks t ON t.id = r.task_id`
}

func workspaceTaskColumns(alias string) string {
	if alias != "" {
		alias += "."
	}
	return alias + `id, ` + alias + `source_chat_id, ` + alias + `source_message_id,
       ` + alias + `source_link, ` + alias + `source_cluster_id,
       ` + alias + `card_chat_id, ` + alias + `card_topic_id, ` + alias + `card_message_id,
       ` + alias + `text, ` + alias + `emoji, ` + alias + `status,
       ` + alias + `deferred_until, ` + alias + `deferred_generation,
       ` + alias + `created_at, ` + alias + `updated_at,
       ` + alias + `completed_at, ` + alias + `cancelled_at`
}

func scanWorkspaceTaskReminder(scanner interface{ Scan(dest ...any) error }) (WorkspaceTaskReminder, error) {
	var reminder WorkspaceTaskReminder
	var scheduledRaw, createdRaw, updatedRaw string
	var nextRaw, sentRaw, reminderCompletedRaw sql.NullString
	var taskDeferredRaw, taskCompletedRaw, taskCancelledRaw sql.NullString
	var taskCreatedRaw, taskUpdatedRaw string
	err := scanner.Scan(
		&reminder.ID, &reminder.TaskID, &scheduledRaw, &reminder.Generation, &reminder.Status,
		&reminder.Attempts, &nextRaw, &reminder.LastError, &reminder.ReminderChatID,
		&reminder.ReminderTopicID, &reminder.ReminderMessageID, &sentRaw,
		&reminderCompletedRaw, &createdRaw, &updatedRaw,
		&reminder.Task.ID, &reminder.Task.SourceChatID, &reminder.Task.SourceMessageID,
		&reminder.Task.SourceLink, &reminder.Task.SourceClusterID,
		&reminder.Task.CardChatID, &reminder.Task.CardTopicID, &reminder.Task.CardMessageID,
		&reminder.Task.Text, &reminder.Task.Emoji, &reminder.Task.Status,
		&taskDeferredRaw, &reminder.Task.DeferredGeneration,
		&taskCreatedRaw, &taskUpdatedRaw, &taskCompletedRaw, &taskCancelledRaw,
	)
	if err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.ScheduledFor, err = parseReminderTime("scheduled_for", scheduledRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.CreatedAt, err = parseReminderTime("created_at", createdRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.UpdatedAt, err = parseReminderTime("updated_at", updatedRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.NextAttemptAt, err = parseNullableReminderTime("next_attempt_at", nextRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.SentAt, err = parseNullableReminderTime("sent_at", sentRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.CompletedAt, err = parseNullableReminderTime("completed_at", reminderCompletedRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.Task.CreatedAt, err = parseReminderTime("task created_at", taskCreatedRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.Task.UpdatedAt, err = parseReminderTime("task updated_at", taskUpdatedRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.Task.DeferredUntil, err = parseNullableReminderTime("task deferred_until", taskDeferredRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.Task.CompletedAt, err = parseNullableReminderTime("task completed_at", taskCompletedRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	if reminder.Task.CancelledAt, err = parseNullableReminderTime("task cancelled_at", taskCancelledRaw); err != nil {
		return WorkspaceTaskReminder{}, err
	}
	return reminder, nil
}

func parseReminderTime(field, raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse workspace task reminder %s: %w", field, err)
	}
	return parsed, nil
}

func parseNullableReminderTime(field string, raw sql.NullString) (*time.Time, error) {
	if !raw.Valid {
		return nil, nil
	}
	parsed, err := parseReminderTime(field, raw.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func sameReminderInstant(a, b string) bool {
	left, err := time.Parse(time.RFC3339Nano, a)
	if err != nil {
		return false
	}
	right, err := time.Parse(time.RFC3339Nano, b)
	return err == nil && left.Equal(right)
}

func compactReminderError(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 500 {
		return string(runes[:497]) + "..."
	}
	return value
}
