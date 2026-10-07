package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// WorkspaceTaskReminderMessage identifies a sent reminder independently of its
// schedule outbox row, which can be reused by a later deferral generation.
type WorkspaceTaskReminderMessage struct {
	Task      WorkspaceTask
	ChatID    int64
	TopicID   int
	MessageID int
	Attempts  int
}

// OldestActiveWorkspaceTask selects only open tasks in original card order.
// Deferred tasks become eligible again when their reminder reopens them.
func (s *Store) OldestActiveWorkspaceTask(ctx context.Context, chatID int64, topicID int) (WorkspaceTask, bool, error) {
	task, err := scanWorkspaceTask(s.db.QueryRowContext(ctx, workspaceTaskSelect()+`
WHERE status = 'open'
  AND card_chat_id = ? AND card_topic_id = ? AND card_message_id > 0
  AND card_chat_id != 0 AND card_topic_id > 0
ORDER BY card_message_id, id LIMIT 1`, chatID, topicID))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceTask{}, false, nil
	}
	if err != nil {
		return WorkspaceTask{}, false, err
	}
	return task, true, nil
}

// ReadyWorkspaceTaskReminderClosures returns every known reminder that still
// needs to reflect its task's terminal status. A zero taskID selects all tasks.
func (s *Store) ReadyWorkspaceTaskReminderClosures(ctx context.Context, chatID int64, topicID int, taskID int64, now time.Time, limit int) ([]WorkspaceTaskReminderMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.task_id, m.chat_id, m.topic_id, m.message_id, m.attempts
FROM workspace_task_reminder_messages m
JOIN workspace_tasks t ON t.id = m.task_id
WHERE m.chat_id = ? AND m.topic_id = ?
  AND (? = 0 OR m.task_id = ?)
  AND t.status IN ('done', 'cancelled') AND m.rendered_status != t.status
  AND (m.next_attempt_at IS NULL OR julianday(m.next_attempt_at) <= julianday(?))
ORDER BY m.message_id, m.task_id LIMIT ?`, chatID, topicID, taskID, taskID, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []WorkspaceTaskReminderMessage
	for rows.Next() {
		var message WorkspaceTaskReminderMessage
		if err := rows.Scan(&message.Task.ID, &message.ChatID, &message.TopicID, &message.MessageID, &message.Attempts); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The store has one database connection; release the result before loading
	// task snapshots to avoid blocking on our own active query.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range messages {
		messages[i].Task, err = s.WorkspaceTaskByID(ctx, messages[i].Task.ID)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *Store) MarkWorkspaceTaskReminderClosed(ctx context.Context, chatID int64, messageID int, status string, now time.Time) error {
	if status != "done" && status != "cancelled" {
		return fmt.Errorf("invalid workspace task reminder closure status %q", status)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_task_reminder_messages
SET rendered_status = ?, attempts = 0, next_attempt_at = NULL, last_error = '', updated_at = ?
WHERE chat_id = ? AND message_id = ?`, status, now.UTC().Format(time.RFC3339Nano), chatID, messageID)
	if err != nil {
		return err
	}
	return requireOneRow(result, "workspace task reminder message %d/%d not found", chatID, messageID)
}

func (s *Store) RetryWorkspaceTaskReminderClosure(ctx context.Context, chatID int64, messageID int, next time.Time, reason string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_task_reminder_messages
SET attempts = attempts + 1, next_attempt_at = ?, last_error = ?, updated_at = ?
WHERE chat_id = ? AND message_id = ?`, next.UTC().Format(time.RFC3339Nano),
		compactReminderError(reason), now.UTC().Format(time.RFC3339Nano), chatID, messageID)
	if err != nil {
		return err
	}
	return requireOneRow(result, "workspace task reminder message %d/%d not found", chatID, messageID)
}
