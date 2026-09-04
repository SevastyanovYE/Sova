package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WorkspacePublishRun is the durable state machine behind an explicitly
// approved Workspace note publication. Revision and message Text are durable
// user content and must never be copied into compact operational indexes.
type WorkspacePublishRun struct {
	ID                     int64
	DocumentID             int64
	Revision               string
	Model                  string
	RouteSummary           string
	Status                 string
	PreviewChatID          int64
	PreviewTopicID         int
	StatusMessageID        int
	ManualEditorUserID     int64
	ManualInputMessageID   int
	ManualReplacementRunID int64
	LastError              string
	ApprovedAt             *time.Time
	CompletedAt            *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
	Messages               []WorkspacePublishMessage
}

type WorkspacePublishMessage struct {
	ID        int64
	RunID     int64
	Kind      string
	Position  int
	Text      string
	Status    string
	ChatID    int64
	TopicID   int
	MessageID int
	LastError string
	SentAt    *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type WorkspacePublishRunSummary struct {
	ID           int64
	DocumentID   int64
	Model        string
	RouteSummary string
	Status       string
	LastError    string
	PreviewSent  int
	FinalSent    int
	Blocked      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (s *Store) CreateWorkspacePublishRun(ctx context.Context, documentID int64, revision string, previewChatID int64, previewTopicID, statusMessageID int, now time.Time) (WorkspacePublishRun, error) {
	if documentID <= 0 || previewChatID == 0 || previewTopicID == 0 {
		return WorkspacePublishRun{}, fmt.Errorf("workspace publish document and preview destination are required")
	}
	nowRaw := now.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_publish_runs(
    document_id, revision, model, status, preview_chat_id, preview_topic_id,
    status_message_id, last_error, created_at, updated_at
) VALUES(?, ?, '', 'generating', ?, ?, ?, '', ?, ?)`,
		documentID, strings.TrimSpace(revision), previewChatID, previewTopicID,
		statusMessageID, nowRaw, nowRaw)
	if err != nil {
		return WorkspacePublishRun{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return WorkspacePublishRun{}, err
	}
	return s.WorkspacePublishRunByID(ctx, id)
}

// SetWorkspacePublishPreview stores the formatter output before any preview is
// sent. That makes a process restart safe: pending sends are known-safe, while
// sending/unknown sends are never repeated automatically.
func (s *Store) SetWorkspacePublishPreview(ctx context.Context, runID int64, model string, texts []string, now time.Time) error {
	if runID <= 0 || len(texts) == 0 {
		return fmt.Errorf("workspace publish run and preview messages are required")
	}
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("workspace publish preview contains an empty message")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	nowRaw := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_runs
SET model = ?, status = 'preview_sending', last_error = '', updated_at = ?
WHERE id = ? AND status = 'generating'`, strings.TrimSpace(model), nowRaw, runID)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "generating workspace publish run %d not found", runID); err != nil {
		return err
	}
	for index, text := range texts {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO workspace_publish_messages(
    run_id, kind, position, text, status, created_at, updated_at
) VALUES(?, 'preview', ?, ?, 'pending', ?, ?)`, runID, index+1, text, nowRaw, nowRaw); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetWorkspacePublishRouteSummary(ctx context.Context, runID int64, summary string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_runs SET route_summary = ?, updated_at = ?
WHERE id = ? AND status IN ('preview_sending', 'awaiting_approval')`,
		compactPublishError(summary), now.UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return err
	}
	return requireOneRow(result, "workspace publish run %d not ready for route telemetry", runID)
}

// ActivateWorkspacePublishPreview switches the new preview into the only
// actionable preview for its document. It is called only after every new
// preview message has a confirmed Telegram ID. Previous previews are returned
// so callers can delete them after this transaction commits.
func (s *Store) ActivateWorkspacePublishPreview(ctx context.Context, runID int64, now time.Time) ([]WorkspacePublishRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var documentID int64
	if err := tx.QueryRowContext(ctx, `
SELECT document_id FROM workspace_publish_runs WHERE id = ? AND status = 'preview_sending'`, runID).Scan(&documentID); err != nil {
		return nil, err
	}
	var total, sent int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'sent' THEN 1 ELSE 0 END), 0)
FROM workspace_publish_messages WHERE run_id = ? AND kind = 'preview'`, runID).Scan(&total, &sent); err != nil {
		return nil, err
	}
	if total == 0 || sent != total {
		return nil, fmt.Errorf("workspace publish preview %d is not fully sent", runID)
	}
	rows, err := tx.QueryContext(ctx, workspacePublishRunSelect()+`
WHERE r.document_id = ? AND r.status = 'awaiting_approval' AND r.id != ?
ORDER BY r.id`, documentID, runID)
	if err != nil {
		return nil, err
	}
	var old []WorkspacePublishRun
	for rows.Next() {
		run, err := scanWorkspacePublishRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		old = append(old, run)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_runs SET status = 'superseded', updated_at = ?
WHERE document_id = ? AND status = 'awaiting_approval' AND id != ?`,
		now.UTC().Format(time.RFC3339Nano), documentID, runID); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_runs SET status = 'awaiting_approval', last_error = '', updated_at = ?
WHERE id = ? AND status = 'preview_sending'`, now.UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return nil, err
	}
	if err := requireOneRow(result, "preview-sending workspace publish run %d not found", runID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for i := range old {
		old[i].Messages, _ = s.WorkspacePublishMessages(ctx, old[i].ID, "preview")
	}
	return old, nil
}

func (s *Store) WorkspacePublishRunByID(ctx context.Context, id int64) (WorkspacePublishRun, error) {
	run, err := scanWorkspacePublishRun(s.db.QueryRowContext(ctx, workspacePublishRunSelect()+` WHERE r.id = ?`, id))
	if err != nil {
		return WorkspacePublishRun{}, err
	}
	run.Messages, err = s.WorkspacePublishMessages(ctx, id, "")
	return run, err
}

// WorkspacePublishRunForCallback verifies both the document and the last
// preview message. This prevents a stale, undeleted button from approving a
// newer replacement preview that happens to belong to the same document.
func (s *Store) WorkspacePublishRunForCallback(ctx context.Context, documentID int64, callbackMessageID int) (WorkspacePublishRun, bool, error) {
	row := s.db.QueryRowContext(ctx, workspacePublishRunSelect()+`
WHERE r.document_id = ?
  AND r.status IN ('awaiting_approval', 'publishing', 'finalizing')
  AND EXISTS (
      SELECT 1 FROM workspace_publish_messages m
      WHERE m.run_id = r.id AND m.kind = 'preview' AND m.status = 'sent'
        AND m.message_id = ?
        AND m.position = (
            SELECT MAX(m2.position) FROM workspace_publish_messages m2
            WHERE m2.run_id = r.id AND m2.kind = 'preview'
        )
  )
ORDER BY r.id DESC LIMIT 1`, documentID, callbackMessageID)
	run, err := scanWorkspacePublishRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspacePublishRun{}, false, nil
	}
	if err != nil {
		return WorkspacePublishRun{}, false, err
	}
	run.Messages, err = s.WorkspacePublishMessages(ctx, run.ID, "")
	return run, err == nil, err
}

func (s *Store) WorkspacePublishRunByFinalMessage(ctx context.Context, chatID int64, topicID, messageID int) (WorkspacePublishRun, bool, error) {
	row := s.db.QueryRowContext(ctx, workspacePublishRunSelect()+`
JOIN workspace_publish_messages m ON m.run_id = r.id
WHERE m.kind = 'final' AND m.status = 'sent' AND m.chat_id = ?
  AND m.topic_id = ? AND m.message_id = ?
ORDER BY r.id DESC LIMIT 1`, chatID, topicID, messageID)
	run, err := scanWorkspacePublishRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspacePublishRun{}, false, nil
	}
	if err != nil {
		return WorkspacePublishRun{}, false, err
	}
	run.Messages, err = s.WorkspacePublishMessages(ctx, run.ID, "")
	return run, err == nil, err
}

func (s *Store) LatestPublishedWorkspacePublishRun(ctx context.Context, documentID int64) (WorkspacePublishRun, bool, error) {
	run, err := scanWorkspacePublishRun(s.db.QueryRowContext(ctx, workspacePublishRunSelect()+`
WHERE r.document_id = ? AND r.status IN ('finalizing', 'completed')
ORDER BY r.id DESC LIMIT 1`, documentID))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspacePublishRun{}, false, nil
	}
	if err != nil {
		return WorkspacePublishRun{}, false, err
	}
	run.Messages, err = s.WorkspacePublishMessages(ctx, run.ID, "final")
	return run, err == nil, err
}

func (s *Store) WorkspaceUsefulLegacyMessageIDsForDocument(ctx context.Context, documentID int64, chatID int64, topicID int) ([]int, error) {
	if documentID <= 0 || chatID == 0 || topicID <= 0 {
		return nil, fmt.Errorf("legacy useful publication identity is incomplete")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT d.derived_message_id
FROM workspace_document_parts p
JOIN workspace_derived_messages d
  ON d.source_chat_id = p.source_chat_id AND d.source_message_id = p.source_message_id
WHERE p.document_id = ? AND d.derived_chat_id = ? AND d.derived_topic_id = ?
  AND d.status IN ('published', 'needs_review')
  AND d.derived_type LIKE 'legacy_migration_%'
ORDER BY d.derived_message_id`, documentID, chatID, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id > 0 {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func (s *Store) ArchiveWorkspaceUsefulPublication(ctx context.Context, documentID int64, chatID int64, topicID int, messageIDs []int, now time.Time) error {
	if documentID <= 0 || chatID == 0 || topicID <= 0 || len(messageIDs) == 0 {
		return fmt.Errorf("useful publication identity is incomplete")
	}
	seen := map[int]struct{}{}
	ids := make([]int, 0, len(messageIDs))
	for _, id := range messageIDs {
		if id <= 0 {
			return fmt.Errorf("useful publication message ID must be positive")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var targetChatID int64
	var targetTopicID int
	if err := tx.QueryRowContext(ctx, `
SELECT status, target_chat_id, target_topic_id FROM workspace_documents WHERE id = ?`, documentID).Scan(&status, &targetChatID, &targetTopicID); err != nil {
		return err
	}
	if status != "published" && status != "needs_review" {
		return fmt.Errorf("workspace document %d is %s, not published useful", documentID, status)
	}
	if targetChatID != chatID || targetTopicID != topicID {
		return fmt.Errorf("workspace document %d is not in the configured Useful topic", documentID)
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+3)
	for index, id := range ids {
		placeholders[index] = "?"
		args = append(args, id)
	}
	nowRaw := now.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_documents SET status = 'archived', updated_at = ? WHERE id = ?`, nowRaw, documentID); err != nil {
		return err
	}
	derivedArgs := append([]any{nowRaw, chatID, topicID}, args...)
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_derived_messages SET status = 'closed', updated_at = ?
WHERE derived_chat_id = ? AND derived_topic_id = ? AND derived_message_id IN (`+strings.Join(placeholders, ",")+`)`, derivedArgs...); err != nil {
		return err
	}
	publishArgs := append([]any{nowRaw, chatID, topicID}, args...)
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_messages SET status = 'deleted', updated_at = ?
WHERE kind = 'final' AND status = 'sent' AND chat_id = ? AND topic_id = ?
  AND message_id IN (`+strings.Join(placeholders, ",")+`)`, publishArgs...); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
DELETE FROM search_embeddings WHERE document_id IN (
  SELECT id FROM search_documents WHERE scope = 'workspace' AND message_id = ?
)`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE search_documents SET status = 'deleted', index_status = 'error', next_attempt_at = NULL,
    last_error = 'source message deleted or excluded', updated_at = ?
WHERE scope = 'workspace' AND message_id = ?`, nowRaw, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) BeginWorkspaceManualPublishEdit(ctx context.Context, runID, userID int64, now time.Time) error {
	if runID <= 0 || userID <= 0 {
		return fmt.Errorf("workspace publish run and editor are required")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_runs
SET manual_editor_user_id = ?, updated_at = ?
WHERE id = ? AND status = 'awaiting_approval'
  AND manual_input_message_id = 0 AND manual_replacement_run_id = 0
  AND (manual_editor_user_id = 0 OR manual_editor_user_id = ?)`,
		userID, now.UTC().Format(time.RFC3339Nano), runID, userID)
	if err != nil {
		return err
	}
	return requireOneRow(result, "awaiting workspace publish run %d is not available for manual edit", runID)
}

func (s *Store) CancelWorkspaceManualPublishEdit(ctx context.Context, runID, userID int64, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_runs
SET manual_editor_user_id = 0, manual_input_message_id = 0,
    manual_replacement_run_id = 0, updated_at = ?
WHERE id = ? AND status = 'awaiting_approval' AND manual_editor_user_id = ?
  AND manual_replacement_run_id = 0`, now.UTC().Format(time.RFC3339Nano), runID, userID)
	if err != nil {
		return err
	}
	return requireOneRow(result, "manual edit for workspace publish run %d is not awaiting input", runID)
}

func (s *Store) AwaitingWorkspaceManualPublishInputs(ctx context.Context, limit int) ([]WorkspacePublishRun, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, workspacePublishRunSelect()+`
WHERE r.status = 'awaiting_approval' AND r.manual_editor_user_id != 0
  AND r.manual_input_message_id = 0 AND r.manual_replacement_run_id = 0
ORDER BY r.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []WorkspacePublishRun
	for rows.Next() {
		run, err := scanWorkspacePublishRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) CreateWorkspaceManualPublishPreview(ctx context.Context, baseRunID, editorUserID int64, inputMessageID int, texts []string, now time.Time) (WorkspacePublishRun, error) {
	if baseRunID <= 0 || editorUserID <= 0 || inputMessageID <= 0 || len(texts) == 0 {
		return WorkspacePublishRun{}, fmt.Errorf("manual workspace publish input is incomplete")
	}
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			return WorkspacePublishRun{}, fmt.Errorf("manual workspace publish preview contains an empty message")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspacePublishRun{}, err
	}
	defer tx.Rollback()
	var documentID int64
	var status string
	var previewChatID int64
	var previewTopicID int
	var inputID int
	var replacementID int64
	if err := tx.QueryRowContext(ctx, `
SELECT document_id, status, preview_chat_id, preview_topic_id,
       manual_input_message_id, manual_replacement_run_id
FROM workspace_publish_runs
WHERE id = ? AND manual_editor_user_id = ?`, baseRunID, editorUserID).Scan(
		&documentID, &status, &previewChatID, &previewTopicID, &inputID, &replacementID); err != nil {
		return WorkspacePublishRun{}, err
	}
	if status != "awaiting_approval" {
		return WorkspacePublishRun{}, fmt.Errorf("workspace publish preview is %s, not awaiting manual input", status)
	}
	if inputID == inputMessageID && replacementID > 0 {
		if err := tx.Commit(); err != nil {
			return WorkspacePublishRun{}, err
		}
		return s.WorkspacePublishRunByID(ctx, replacementID)
	}
	if inputID != 0 || replacementID != 0 {
		return WorkspacePublishRun{}, fmt.Errorf("workspace publish preview already captured another manual input")
	}
	nowRaw := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
INSERT INTO workspace_publish_runs(
    document_id, revision, model, route_summary, status, preview_chat_id,
    preview_topic_id, status_message_id, last_error, created_at, updated_at
) VALUES(?, '', 'manual', 'manual final edit', 'preview_sending', ?, ?, 0, '', ?, ?)`,
		documentID, previewChatID, previewTopicID, nowRaw, nowRaw)
	if err != nil {
		return WorkspacePublishRun{}, err
	}
	childID, err := result.LastInsertId()
	if err != nil {
		return WorkspacePublishRun{}, err
	}
	for index, text := range texts {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO workspace_publish_messages(
    run_id, kind, position, text, status, created_at, updated_at
) VALUES(?, 'preview', ?, ?, 'pending', ?, ?)`, childID, index+1, text, nowRaw, nowRaw); err != nil {
			return WorkspacePublishRun{}, err
		}
	}
	result, err = tx.ExecContext(ctx, `
UPDATE workspace_publish_runs
SET manual_input_message_id = ?, manual_replacement_run_id = ?, updated_at = ?
WHERE id = ? AND status = 'awaiting_approval' AND manual_editor_user_id = ?
  AND manual_input_message_id = 0 AND manual_replacement_run_id = 0`,
		inputMessageID, childID, nowRaw, baseRunID, editorUserID)
	if err != nil {
		return WorkspacePublishRun{}, err
	}
	if err := requireOneRow(result, "manual input for workspace publish run %d was already captured", baseRunID); err != nil {
		return WorkspacePublishRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkspacePublishRun{}, err
	}
	return s.WorkspacePublishRunByID(ctx, childID)
}

func (s *Store) ResetWorkspaceManualPublishCapture(ctx context.Context, baseRunID, childRunID int64, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_runs
SET manual_input_message_id = 0, manual_replacement_run_id = 0, updated_at = ?
WHERE id = ? AND status = 'awaiting_approval' AND manual_replacement_run_id = ?`,
		now.UTC().Format(time.RFC3339Nano), baseRunID, childRunID)
	if err != nil {
		return err
	}
	return requireOneRow(result, "manual capture for workspace publish run %d not found", baseRunID)
}

func (s *Store) RecoverWorkspaceManualPublishInputs(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_runs
SET manual_input_message_id = 0, manual_replacement_run_id = 0, updated_at = ?
WHERE status = 'awaiting_approval' AND manual_editor_user_id != 0
  AND manual_replacement_run_id != 0
  AND manual_replacement_run_id IN (
      SELECT id FROM workspace_publish_runs WHERE status IN ('failed', 'cancelled')
  )`, now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) WorkspacePublishMessages(ctx context.Context, runID int64, kind string) ([]WorkspacePublishMessage, error) {
	query := workspacePublishMessageSelect() + ` WHERE run_id = ?`
	args := []any{runID}
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY CASE kind WHEN 'preview' THEN 0 ELSE 1 END, position`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []WorkspacePublishMessage
	for rows.Next() {
		message, err := scanWorkspacePublishMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) MarkWorkspacePublishMessageSending(ctx context.Context, id int64, chatID int64, topicID int, now time.Time) error {
	if chatID == 0 || topicID == 0 {
		return fmt.Errorf("workspace publish message destination is required")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_messages
SET status = 'sending', chat_id = ?, topic_id = ?, last_error = '', updated_at = ?
WHERE id = ? AND status = 'pending'`, chatID, topicID, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "pending workspace publish message %d not found", id)
}

func (s *Store) MarkWorkspacePublishMessagePending(ctx context.Context, id int64, lastError string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_messages
SET status = 'pending', last_error = ?, updated_at = ?
WHERE id = ? AND status = 'sending'`, compactPublishError(lastError), now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sending workspace publish message %d not found", id)
}

func (s *Store) MarkWorkspacePublishMessageUnknown(ctx context.Context, id int64, lastError string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_messages
SET status = 'unknown', last_error = ?, updated_at = ?
WHERE id = ? AND status = 'sending'`, compactPublishError(lastError), now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sending workspace publish message %d not found", id)
}

func (s *Store) MarkWorkspacePublishMessageSent(ctx context.Context, id int64, messageID int, now time.Time) error {
	if messageID <= 0 {
		return fmt.Errorf("workspace publish Telegram message id is required")
	}
	nowRaw := now.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_messages
SET status = 'sent', message_id = ?, last_error = '', sent_at = ?, updated_at = ?
WHERE id = ? AND status = 'sending'`, messageID, nowRaw, nowRaw, id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sending workspace publish message %d not found", id)
}

func (s *Store) MarkWorkspacePublishMessagesDeleted(ctx context.Context, runID int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_messages SET status = 'deleted', updated_at = ?
WHERE run_id = ? AND kind = 'preview' AND status = 'sent'`, now.UTC().Format(time.RFC3339Nano), runID)
	return err
}

// InterruptedWorkspacePublishRuns returns formatter/preview runs which have
// durable state but are not part of the normal approved-publication resume
// path. The caller can safely repeat generation and pending sends, but must
// reconcile a persisted sending marker as an ambiguous Telegram outcome.
func (s *Store) InterruptedWorkspacePublishRuns(ctx context.Context, limit int) ([]WorkspacePublishRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, workspacePublishRunSelect()+`
WHERE r.status IN ('generating', 'preview_sending') ORDER BY r.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []WorkspacePublishRun
	for rows.Next() {
		run, err := scanWorkspacePublishRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range runs {
		runs[index].Messages, err = s.WorkspacePublishMessages(ctx, runs[index].ID, "preview")
		if err != nil {
			return nil, err
		}
	}
	return runs, nil
}

// MarkInterruptedWorkspacePublishSendsUnknown closes the crash window between
// committing the outbox claim and persisting Telegram's response. Repeating
// such a send could create a duplicate, so it is deliberately never returned
// to pending.
func (s *Store) MarkInterruptedWorkspacePublishSendsUnknown(ctx context.Context, runID int64, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_messages
SET status = 'unknown', last_error = 'interrupted while awaiting Telegram response', updated_at = ?
WHERE run_id = ? AND kind = 'preview' AND status = 'sending'`, now.UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) FailWorkspacePublishRun(ctx context.Context, runID int64, lastError string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	nowRaw := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_runs
SET status = 'failed', last_error = ?, completed_at = ?, updated_at = ?
WHERE id = ? AND status IN ('generating', 'preview_sending')`, compactPublishError(lastError),
		nowRaw, nowRaw, runID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		// Failing an already-terminal run is intentionally idempotent. This is
		// useful when recovery persisted the terminal state before reporting a
		// secondary cleanup error.
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_messages
SET status = 'unknown', last_error = CASE WHEN last_error = '' THEN 'interrupted while awaiting Telegram response' ELSE last_error END, updated_at = ?
WHERE run_id = ? AND status = 'sending'`, nowRaw, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_messages SET status = 'cancelled', updated_at = ?
WHERE run_id = ? AND status = 'pending'`, nowRaw, runID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CancelWorkspacePublishRun(ctx context.Context, runID int64, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	nowRaw := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_runs
SET status = 'cancelled', last_error = '', completed_at = ?, updated_at = ?
WHERE id = ? AND status = 'awaiting_approval'`, nowRaw, nowRaw, runID)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "awaiting workspace publish run %d not found", runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_messages SET status = 'cancelled', updated_at = ?
WHERE run_id = ? AND status = 'pending'`, nowRaw, runID); err != nil {
		return err
	}
	return tx.Commit()
}

// ApproveWorkspacePublishRun persists explicit approval and creates durable
// final outbox rows exactly once. Calling it for publishing/finalizing runs is
// an idempotent resume operation.
func (s *Store) ApproveWorkspacePublishRun(ctx context.Context, runID int64, now time.Time) (WorkspacePublishRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspacePublishRun{}, err
	}
	defer tx.Rollback()
	var status string
	var manualEditorUserID int64
	if err := tx.QueryRowContext(ctx, `SELECT status, manual_editor_user_id FROM workspace_publish_runs WHERE id = ?`, runID).Scan(&status, &manualEditorUserID); err != nil {
		return WorkspacePublishRun{}, err
	}
	if status == "awaiting_approval" {
		if manualEditorUserID != 0 {
			return WorkspacePublishRun{}, fmt.Errorf("workspace publish run %d is awaiting manual text", runID)
		}
		nowRaw := now.UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO workspace_publish_messages(run_id, kind, position, text, status, created_at, updated_at)
SELECT run_id, 'final', position, text, 'pending', ?, ?
FROM workspace_publish_messages
WHERE run_id = ? AND kind = 'preview'
ORDER BY position
ON CONFLICT(run_id, kind, position) DO NOTHING`, nowRaw, nowRaw, runID); err != nil {
			return WorkspacePublishRun{}, err
		}
		result, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_runs
SET status = 'publishing', approved_at = COALESCE(approved_at, ?), last_error = '', updated_at = ?
WHERE id = ? AND status = 'awaiting_approval'`, nowRaw, nowRaw, runID)
		if err != nil {
			return WorkspacePublishRun{}, err
		}
		if err := requireOneRow(result, "awaiting workspace publish run %d not found", runID); err != nil {
			return WorkspacePublishRun{}, err
		}
	} else if status != "publishing" && status != "finalizing" {
		return WorkspacePublishRun{}, fmt.Errorf("workspace publish run %d is %s, not approvable", runID, status)
	}
	if err := tx.Commit(); err != nil {
		return WorkspacePublishRun{}, err
	}
	return s.WorkspacePublishRunByID(ctx, runID)
}

func (s *Store) SetWorkspacePublishRunError(ctx context.Context, runID int64, lastError string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_runs SET last_error = ?, updated_at = ?
WHERE id = ? AND status IN ('publishing', 'finalizing')`, compactPublishError(lastError), now.UTC().Format(time.RFC3339Nano), runID)
	return err
}

func (s *Store) MarkWorkspacePublishRunFinalizing(ctx context.Context, runID int64, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var total, sent int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'sent' THEN 1 ELSE 0 END), 0)
FROM workspace_publish_messages WHERE run_id = ? AND kind = 'final'`, runID).Scan(&total, &sent); err != nil {
		return err
	}
	if total == 0 || sent != total {
		return fmt.Errorf("workspace publish run %d has %d/%d confirmed final messages", runID, sent, total)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE workspace_publish_runs SET status = 'finalizing', last_error = '', updated_at = ?
WHERE id = ? AND status IN ('publishing', 'finalizing')`, now.UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "publishing workspace publish run %d not found", runID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteWorkspacePublishRun(ctx context.Context, runID int64, now time.Time) error {
	nowRaw := now.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_publish_runs
SET status = 'completed', last_error = '', completed_at = ?, updated_at = ?
WHERE id = ? AND status = 'finalizing'`, nowRaw, nowRaw, runID)
	if err != nil {
		return err
	}
	return requireOneRow(result, "finalizing workspace publish run %d not found", runID)
}

func (s *Store) ResumableWorkspacePublishRuns(ctx context.Context, limit int) ([]WorkspacePublishRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, workspacePublishRunSelect()+`
WHERE r.status IN ('publishing', 'finalizing') ORDER BY r.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []WorkspacePublishRun
	for rows.Next() {
		run, err := scanWorkspacePublishRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range runs {
		runs[i].Messages, err = s.WorkspacePublishMessages(ctx, runs[i].ID, "")
		if err != nil {
			return nil, err
		}
	}
	return runs, nil
}

func (s *Store) AwaitingWorkspacePublishRuns(ctx context.Context, limit int) ([]WorkspacePublishRun, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, workspacePublishRunSelect()+`
WHERE r.status = 'awaiting_approval' ORDER BY r.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []WorkspacePublishRun
	for rows.Next() {
		run, err := scanWorkspacePublishRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range runs {
		runs[i].Messages, err = s.WorkspacePublishMessages(ctx, runs[i].ID, "preview")
		if err != nil {
			return nil, err
		}
	}
	return runs, nil
}

func (s *Store) WorkspacePublishRunSummaries(ctx context.Context, limit int) ([]WorkspacePublishRunSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT r.id, r.document_id, r.model, r.route_summary, r.status, r.last_error,
       COALESCE(SUM(CASE WHEN m.kind = 'preview' AND m.status = 'sent' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN m.kind = 'final' AND m.status = 'sent' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN m.status IN ('sending', 'unknown') THEN 1 ELSE 0 END), 0),
       r.created_at, r.updated_at
FROM workspace_publish_runs r
LEFT JOIN workspace_publish_messages m ON m.run_id = r.id
GROUP BY r.id
ORDER BY r.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summaries []WorkspacePublishRunSummary
	for rows.Next() {
		var summary WorkspacePublishRunSummary
		var createdRaw, updatedRaw string
		if err := rows.Scan(&summary.ID, &summary.DocumentID, &summary.Model, &summary.RouteSummary, &summary.Status, &summary.LastError,
			&summary.PreviewSent, &summary.FinalSent, &summary.Blocked, &createdRaw, &updatedRaw); err != nil {
			return nil, err
		}
		var err error
		summary.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
		if err != nil {
			return nil, err
		}
		summary.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func workspacePublishRunSelect() string {
	return `
SELECT r.id, r.document_id, r.revision, r.model, r.route_summary, r.status, r.preview_chat_id,
       r.preview_topic_id, r.status_message_id, r.manual_editor_user_id,
       r.manual_input_message_id, r.manual_replacement_run_id, r.last_error, r.approved_at,
       r.completed_at, r.created_at, r.updated_at
FROM workspace_publish_runs r `
}

func workspacePublishMessageSelect() string {
	return `
SELECT id, run_id, kind, position, text, status, chat_id, topic_id, message_id,
       last_error, sent_at, created_at, updated_at
FROM workspace_publish_messages`
}

func scanWorkspacePublishRun(scanner interface{ Scan(...any) error }) (WorkspacePublishRun, error) {
	var run WorkspacePublishRun
	var approvedRaw, completedRaw sql.NullString
	var createdRaw, updatedRaw string
	if err := scanner.Scan(&run.ID, &run.DocumentID, &run.Revision, &run.Model, &run.RouteSummary, &run.Status,
		&run.PreviewChatID, &run.PreviewTopicID, &run.StatusMessageID,
		&run.ManualEditorUserID, &run.ManualInputMessageID, &run.ManualReplacementRunID, &run.LastError,
		&approvedRaw, &completedRaw, &createdRaw, &updatedRaw); err != nil {
		return WorkspacePublishRun{}, err
	}
	var err error
	run.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return WorkspacePublishRun{}, err
	}
	run.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return WorkspacePublishRun{}, err
	}
	if approvedRaw.Valid {
		value, err := time.Parse(time.RFC3339Nano, approvedRaw.String)
		if err != nil {
			return WorkspacePublishRun{}, err
		}
		run.ApprovedAt = &value
	}
	if completedRaw.Valid {
		value, err := time.Parse(time.RFC3339Nano, completedRaw.String)
		if err != nil {
			return WorkspacePublishRun{}, err
		}
		run.CompletedAt = &value
	}
	return run, nil
}

func scanWorkspacePublishMessage(scanner interface{ Scan(...any) error }) (WorkspacePublishMessage, error) {
	var message WorkspacePublishMessage
	var sentRaw sql.NullString
	var createdRaw, updatedRaw string
	if err := scanner.Scan(&message.ID, &message.RunID, &message.Kind, &message.Position,
		&message.Text, &message.Status, &message.ChatID, &message.TopicID,
		&message.MessageID, &message.LastError, &sentRaw, &createdRaw, &updatedRaw); err != nil {
		return WorkspacePublishMessage{}, err
	}
	var err error
	message.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return WorkspacePublishMessage{}, err
	}
	message.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return WorkspacePublishMessage{}, err
	}
	if sentRaw.Valid {
		value, err := time.Parse(time.RFC3339Nano, sentRaw.String)
		if err != nil {
			return WorkspacePublishMessage{}, err
		}
		message.SentAt = &value
	}
	return message, nil
}

func compactPublishError(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len([]rune(value)) > 500 {
		value = string([]rune(value)[:500])
	}
	return value
}
