package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SearchDocument struct {
	ID            int64
	Scope         string
	SourceRef     string
	SourceTitle   string
	ChatID        int64
	MessageID     int
	TopicID       int
	MessageDate   time.Time
	Text          string
	MediaType     string
	SourceLink    string
	ContentHash   string
	Status        string
	IndexStatus   string
	Attempts      int
	NextAttemptAt *time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type SearchEmbeddingRow struct {
	Document      SearchDocument
	Model         string
	Dimensions    int
	Vector        []byte
	ProviderRoute string
	EmbeddedAt    time.Time
}

type SearchSyncState struct {
	Scope               string
	LastMessageID       int
	FullScanCompletedAt *time.Time
	LastSyncAt          *time.Time
	LastError           string
	ScanGeneration      int64
	ScanCursor          int
	ScanStartedAt       *time.Time
	AuditCursor         int
	UpdatedAt           time.Time
}

func (s *Store) UpsertTelegramMessages(ctx context.Context, messages []TelegramMessage, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, message := range messages {
		if message.SourceID == 0 || message.ChatID == 0 || message.MessageID == 0 || message.Date.IsZero() {
			return 0, fmt.Errorf("invalid telegram message identity")
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO telegram_messages(
    source_id, chat_id, message_id, topic_id, date, kind, sender, text, media_type,
    source_link, raw_json, created_at
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_id, message_id) DO UPDATE SET
    source_id = excluded.source_id, topic_id = excluded.topic_id, date = excluded.date,
    kind = excluded.kind, sender = excluded.sender, text = excluded.text, media_type = excluded.media_type,
    source_link = excluded.source_link, raw_json = excluded.raw_json`,
			message.SourceID, message.ChatID, message.MessageID, message.TopicID,
			message.Date.UTC().Format(time.RFC3339Nano), message.Kind, message.Sender,
			message.Text, message.MediaType, message.SourceLink, message.RawJSON,
			now.UTC().Format(time.RFC3339Nano)); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE telegram_sources
SET last_message_id = MAX(last_message_id, ?), updated_at = ?
WHERE id = ?`, message.MessageID, now.UTC().Format(time.RFC3339Nano), message.SourceID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(messages), nil
}

func (s *Store) UpsertSearchDocuments(ctx context.Context, documents []SearchDocument, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	changed := 0
	for _, document := range documents {
		if err := validateSearchDocument(document); err != nil {
			return 0, err
		}
		var id int64
		var oldHash string
		err := tx.QueryRowContext(ctx, `SELECT id, content_hash FROM search_documents WHERE scope = ? AND message_id = ?`, document.Scope, document.MessageID).Scan(&id, &oldHash)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `
INSERT INTO search_documents(
    scope, source_ref, source_title, chat_id, message_id, topic_id,
    message_date, text, media_type, source_link, content_hash, status,
    index_status, created_at, updated_at
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 'pending', ?, ?)`,
				document.Scope, document.SourceRef, document.SourceTitle, document.ChatID,
				document.MessageID, document.TopicID, document.MessageDate.UTC().Format(time.RFC3339Nano),
				document.Text, document.MediaType, document.SourceLink, document.ContentHash,
				now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
			if err != nil {
				return 0, err
			}
			changed++
		case err != nil:
			return 0, err
		case oldHash != document.ContentHash:
			if _, err = tx.ExecContext(ctx, `
UPDATE search_documents
SET source_ref = ?, source_title = ?, chat_id = ?, topic_id = ?, message_date = ?,
    text = ?, media_type = ?, source_link = ?, content_hash = ?, status = 'active',
    index_status = 'pending', attempts = 0, next_attempt_at = NULL,
    last_error = '', updated_at = ?
WHERE id = ?`, document.SourceRef, document.SourceTitle, document.ChatID, document.TopicID,
				document.MessageDate.UTC().Format(time.RFC3339Nano), document.Text, document.MediaType,
				document.SourceLink, document.ContentHash, now.UTC().Format(time.RFC3339Nano), id); err != nil {
				return 0, err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM search_embeddings WHERE document_id = ?`, id); err != nil {
				return 0, err
			}
			changed++
		default:
			if _, err = tx.ExecContext(ctx, `
UPDATE search_documents
SET source_ref = ?, source_title = ?, chat_id = ?, topic_id = ?, message_date = ?,
    media_type = ?, source_link = ?,
    index_status = CASE WHEN status = 'deleted' THEN 'pending' ELSE index_status END,
    attempts = CASE WHEN status = 'deleted' THEN 0 ELSE attempts END,
    next_attempt_at = CASE WHEN status = 'deleted' THEN NULL ELSE next_attempt_at END,
    last_error = CASE WHEN status = 'deleted' THEN '' ELSE last_error END,
    status = 'active', updated_at = ?
WHERE id = ?`, document.SourceRef, document.SourceTitle, document.ChatID, document.TopicID,
				document.MessageDate.UTC().Format(time.RFC3339Nano), document.MediaType, document.SourceLink,
				now.UTC().Format(time.RFC3339Nano), id); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}

func (s *Store) PendingSearchDocuments(ctx context.Context, now time.Time, limit int) ([]SearchDocument, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+searchDocumentColumns("d")+`
FROM search_documents d
WHERE d.status = 'active' AND d.index_status IN ('pending', 'retry')
  AND (d.next_attempt_at IS NULL OR julianday(d.next_attempt_at) <= julianday(?))
ORDER BY d.id LIMIT ?`, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchDocuments(rows)
}

func (s *Store) SaveSearchEmbedding(ctx context.Context, documentID int64, model string, dimensions int, vector []byte, route string, now time.Time) error {
	if documentID <= 0 || strings.TrimSpace(model) == "" || dimensions <= 0 || len(vector) != dimensions*4 {
		return fmt.Errorf("invalid search embedding metadata")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO search_embeddings(document_id, model, dimensions, vector_blob, provider_route, embedded_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(document_id) DO UPDATE SET
    model = excluded.model, dimensions = excluded.dimensions,
    vector_blob = excluded.vector_blob, provider_route = excluded.provider_route,
    embedded_at = excluded.embedded_at`, documentID, strings.TrimSpace(model), dimensions,
		vector, strings.TrimSpace(route), now.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE search_documents
SET index_status = 'ready', attempts = attempts + 1, next_attempt_at = NULL,
    last_error = '', updated_at = ?
WHERE id = ? AND status = 'active'`, now.UTC().Format(time.RFC3339Nano), documentID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return fmt.Errorf("active search document %d not found", documentID)
	}
	return tx.Commit()
}

func (s *Store) MarkSearchDocumentRetry(ctx context.Context, documentID int64, next time.Time, message string, terminal bool, now time.Time) error {
	status := "retry"
	var nextValue any = next.UTC().Format(time.RFC3339Nano)
	if terminal {
		status, nextValue = "error", nil
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE search_documents
SET index_status = ?, attempts = attempts + 1, next_attempt_at = ?,
    last_error = ?, updated_at = ?
WHERE id = ? AND status = 'active'`, status, nextValue, compactSearchError(message), now.UTC().Format(time.RFC3339Nano), documentID)
	return err
}

func (s *Store) MarkSearchDocumentDeleted(ctx context.Context, scope string, messageID int, now time.Time) error {
	if err := validateSearchScope(scope); err != nil {
		return err
	}
	if messageID <= 0 {
		return fmt.Errorf("search message ID must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
DELETE FROM search_embeddings
WHERE document_id IN (SELECT id FROM search_documents WHERE scope = ? AND message_id = ?)`, scope, messageID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE search_documents
SET status = 'deleted', index_status = 'error', next_attempt_at = NULL,
    last_error = 'source message deleted or excluded', updated_at = ?
WHERE scope = ? AND message_id = ?`, now.UTC().Format(time.RFC3339Nano), scope, messageID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReadySearchEmbeddings(ctx context.Context, model string, dimensions int) ([]SearchEmbeddingRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+searchDocumentColumns("d")+`,
       e.model, e.dimensions, e.vector_blob, e.provider_route, e.embedded_at
FROM search_documents d
JOIN search_embeddings e ON e.document_id = d.id
WHERE d.status = 'active' AND d.index_status = 'ready'
  AND e.model = ? AND e.dimensions = ?
ORDER BY d.id`, strings.TrimSpace(model), dimensions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SearchEmbeddingRow
	for rows.Next() {
		var row SearchEmbeddingRow
		var messageDate, createdAt, updatedAt, embeddedAt string
		var nullableNext sql.NullString
		if err := rows.Scan(
			&row.Document.ID, &row.Document.Scope, &row.Document.SourceRef, &row.Document.SourceTitle,
			&row.Document.ChatID, &row.Document.MessageID, &row.Document.TopicID, &messageDate,
			&row.Document.Text, &row.Document.MediaType, &row.Document.SourceLink, &row.Document.ContentHash,
			&row.Document.Status, &row.Document.IndexStatus, &row.Document.Attempts, &nullableNext,
			&row.Document.LastError, &createdAt, &updatedAt,
			&row.Model, &row.Dimensions, &row.Vector, &row.ProviderRoute, &embeddedAt,
		); err != nil {
			return nil, err
		}
		var err error
		row.Document.MessageDate, err = time.Parse(time.RFC3339Nano, messageDate)
		if err != nil {
			return nil, err
		}
		row.Document.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		row.Document.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, err
		}
		if nullableNext.Valid {
			parsed, parseErr := time.Parse(time.RFC3339Nano, nullableNext.String)
			if parseErr != nil {
				return nil, parseErr
			}
			row.Document.NextAttemptAt = &parsed
		}
		row.EmbeddedAt, err = time.Parse(time.RFC3339Nano, embeddedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) SearchCorpusFromTelegram(ctx context.Context, sourceRefs []string) ([]TelegramRecentMessage, error) {
	if len(sourceRefs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(sourceRefs))
	args := make([]any, len(sourceRefs))
	for index, ref := range sourceRefs {
		placeholders[index], args[index] = "?", strings.TrimSpace(ref)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT s.ref, s.title, s.username, m.chat_id, m.message_id, m.topic_id, m.date,
       m.kind, m.sender, m.text, m.media_type, m.source_link
FROM telegram_messages m
JOIN telegram_sources s ON s.id = m.source_id
WHERE s.ref IN (`+strings.Join(placeholders, ",")+`)
  AND m.kind != 'service' AND trim(m.text) != ''
ORDER BY s.ref, m.message_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []TelegramRecentMessage
	for rows.Next() {
		var message TelegramRecentMessage
		var date string
		if err := rows.Scan(&message.SourceRef, &message.SourceTitle, &message.Username,
			&message.ChatID, &message.MessageID, &message.TopicID, &date, &message.Kind, &message.Sender, &message.Text,
			&message.MediaType, &message.SourceLink); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, date)
		if err != nil {
			return nil, err
		}
		message.Date = parsed
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) ReconcileSearchScope(ctx context.Context, scope string, seenMessageIDs []int, now time.Time) error {
	return s.reconcileSearchScope(ctx, scope, 0, 0, seenMessageIDs, now)
}

func (s *Store) ReconcileSearchScopeRange(ctx context.Context, scope string, minMessageID, maxMessageID int, seenMessageIDs []int, now time.Time) error {
	if minMessageID <= 0 || maxMessageID < minMessageID {
		return fmt.Errorf("invalid search reconciliation range")
	}
	return s.reconcileSearchScope(ctx, scope, minMessageID, maxMessageID, seenMessageIDs, now)
}

func (s *Store) reconcileSearchScope(ctx context.Context, scope string, minMessageID, maxMessageID int, seenMessageIDs []int, now time.Time) error {
	if err := validateSearchScope(scope); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS search_seen_message_ids(message_id INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM search_seen_message_ids`); err != nil {
		return err
	}
	for _, messageID := range seenMessageIDs {
		if messageID <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO search_seen_message_ids(message_id) VALUES(?)`, messageID); err != nil {
			return err
		}
	}
	rangeSQL := ""
	rangeArgs := []any{scope}
	if minMessageID > 0 {
		rangeSQL = " AND d.message_id BETWEEN ? AND ?"
		rangeArgs = append(rangeArgs, minMessageID, maxMessageID)
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM search_embeddings
WHERE document_id IN (
    SELECT d.id FROM search_documents d
    WHERE d.scope = ?`+rangeSQL+` AND NOT EXISTS (
        SELECT 1 FROM search_seen_message_ids seen WHERE seen.message_id = d.message_id
    )
)`, rangeArgs...); err != nil {
		return err
	}
	updateRangeSQL := ""
	updateArgs := []any{now.UTC().Format(time.RFC3339Nano), scope}
	if minMessageID > 0 {
		updateRangeSQL = " AND message_id BETWEEN ? AND ?"
		updateArgs = append(updateArgs, minMessageID, maxMessageID)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE search_documents
SET status = 'deleted', index_status = 'error', next_attempt_at = NULL,
    last_error = 'source message deleted', updated_at = ?
WHERE scope = ?`+updateRangeSQL+` AND NOT EXISTS (
    SELECT 1 FROM search_seen_message_ids seen WHERE seen.message_id = search_documents.message_id
)`, updateArgs...); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SearchIndexPendingCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM search_documents
WHERE status = 'active' AND index_status != 'ready'`).Scan(&count)
	return count, err
}

func (s *Store) PrepareSearchEmbeddingModel(ctx context.Context, model string, dimensions int, now time.Time) error {
	model = strings.TrimSpace(model)
	if model == "" || dimensions <= 0 {
		return fmt.Errorf("search embedding model and dimensions are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM search_embeddings WHERE model != ? OR dimensions != ?`, model, dimensions); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE search_documents
SET index_status = 'pending', attempts = 0, next_attempt_at = NULL,
    last_error = '', updated_at = ?
WHERE status = 'active' AND NOT EXISTS (
    SELECT 1 FROM search_embeddings e
    WHERE e.document_id = search_documents.id AND e.model = ? AND e.dimensions = ?
)`, now.UTC().Format(time.RFC3339Nano), model, dimensions); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SearchIndexMissingEmbeddingCount(ctx context.Context, model string, dimensions int) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM search_documents d
LEFT JOIN search_embeddings e
  ON e.document_id = d.id AND e.model = ? AND e.dimensions = ?
WHERE d.status = 'active' AND (d.index_status != 'ready' OR e.document_id IS NULL)`,
		strings.TrimSpace(model), dimensions).Scan(&count)
	return count, err
}

func (s *Store) SetSearchSyncState(ctx context.Context, scope string, lastMessageID int, fullScan bool, syncErr string, now time.Time) error {
	if err := validateSearchScope(scope); err != nil {
		return err
	}
	var fullScanAt any
	if fullScan && strings.TrimSpace(syncErr) == "" {
		fullScanAt = now.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO search_sync_state(scope, last_message_id, full_scan_completed_at, last_sync_at, last_error, updated_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(scope) DO UPDATE SET
    last_message_id = MAX(search_sync_state.last_message_id, excluded.last_message_id),
    full_scan_completed_at = COALESCE(excluded.full_scan_completed_at, search_sync_state.full_scan_completed_at),
    last_sync_at = excluded.last_sync_at, last_error = excluded.last_error,
    updated_at = excluded.updated_at`, scope, lastMessageID, fullScanAt,
		now.UTC().Format(time.RFC3339Nano), compactSearchError(syncErr), now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) BeginOrResumeSearchFullScan(ctx context.Context, scope string, now time.Time) (generation int64, cursor int, err error) {
	if err := validateSearchScope(scope); err != nil {
		return 0, 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	nowRaw := now.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO search_sync_state(scope, updated_at)
VALUES(?, ?)
ON CONFLICT(scope) DO NOTHING`, scope, nowRaw); err != nil {
		return 0, 0, err
	}
	var started sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT scan_generation, scan_cursor, scan_started_at
FROM search_sync_state WHERE scope = ?`, scope).Scan(&generation, &cursor, &started); err != nil {
		return 0, 0, err
	}
	if !started.Valid {
		generation++
		cursor = 0
		if _, err := tx.ExecContext(ctx, `
UPDATE search_sync_state
SET scan_generation = ?, scan_cursor = 0, scan_started_at = ?,
    last_error = '', updated_at = ?
WHERE scope = ?`, generation, nowRaw, nowRaw, scope); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return generation, cursor, nil
}

func (s *Store) MarkSearchScanSeen(ctx context.Context, scope string, generation int64, messageIDs []int, now time.Time) error {
	if err := validateSearchScope(scope); err != nil {
		return err
	}
	if generation <= 0 {
		return fmt.Errorf("search scan generation must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, messageID := range messageIDs {
		if messageID <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE search_documents SET scan_generation = ?, updated_at = ?
WHERE scope = ? AND message_id = ?`, generation, now.UTC().Format(time.RFC3339Nano), scope, messageID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CheckpointSearchFullScan(ctx context.Context, scope string, generation int64, cursor, lastMessageID int, syncErr string, now time.Time) error {
	if err := validateSearchScope(scope); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE search_sync_state
SET scan_cursor = ?, last_message_id = MAX(last_message_id, ?),
    last_sync_at = ?, last_error = ?, updated_at = ?
WHERE scope = ? AND scan_generation = ? AND scan_started_at IS NOT NULL`,
		cursor, lastMessageID, now.UTC().Format(time.RFC3339Nano), compactSearchError(syncErr),
		now.UTC().Format(time.RFC3339Nano), scope, generation)
	if err != nil {
		return err
	}
	return requireOneRow(result, "active search full scan for %s generation %d not found", scope, generation)
}

func (s *Store) CompleteSearchFullScan(ctx context.Context, scope string, generation int64, lastMessageID int, now time.Time) error {
	if err := validateSearchScope(scope); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var startedRaw string
	if err := tx.QueryRowContext(ctx, `
SELECT scan_started_at FROM search_sync_state
WHERE scope = ? AND scan_generation = ? AND scan_started_at IS NOT NULL`, scope, generation).Scan(&startedRaw); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM search_embeddings WHERE document_id IN (
    SELECT id FROM search_documents
    WHERE scope = ? AND scan_generation != ? AND updated_at <= ?
)`, scope, generation, startedRaw); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE search_documents
SET status = 'deleted', index_status = 'error', next_attempt_at = NULL,
    last_error = 'source message deleted', updated_at = ?
WHERE scope = ? AND scan_generation != ? AND updated_at <= ?`,
		now.UTC().Format(time.RFC3339Nano), scope, generation, startedRaw); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE search_sync_state
SET last_message_id = MAX(last_message_id, ?), full_scan_completed_at = ?,
    last_sync_at = ?, last_error = '', scan_cursor = 0, scan_started_at = NULL,
    audit_cursor = 0, updated_at = ?
WHERE scope = ? AND scan_generation = ?`, lastMessageID,
		now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano), scope, generation)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "active search full scan for %s generation %d not found", scope, generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetSearchAuditCursor(ctx context.Context, scope string, cursor int, now time.Time) error {
	if err := validateSearchScope(scope); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE search_sync_state SET audit_cursor = ?, updated_at = ? WHERE scope = ?`,
		cursor, now.UTC().Format(time.RFC3339Nano), scope)
	return err
}

func (s *Store) SearchSyncStates(ctx context.Context) ([]SearchSyncState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT scope, last_message_id, full_scan_completed_at, last_sync_at, last_error,
       scan_generation, scan_cursor, scan_started_at, audit_cursor, updated_at
FROM search_sync_state ORDER BY scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var states []SearchSyncState
	for rows.Next() {
		var state SearchSyncState
		var fullScan, lastSync, scanStarted sql.NullString
		var updated string
		if err := rows.Scan(&state.Scope, &state.LastMessageID, &fullScan, &lastSync, &state.LastError,
			&state.ScanGeneration, &state.ScanCursor, &scanStarted, &state.AuditCursor, &updated); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, err
		}
		state.UpdatedAt = parsed
		if fullScan.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, fullScan.String)
			if err != nil {
				return nil, err
			}
			state.FullScanCompletedAt = &parsed
		}
		if lastSync.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, lastSync.String)
			if err != nil {
				return nil, err
			}
			state.LastSyncAt = &parsed
		}
		if scanStarted.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, scanStarted.String)
			if err != nil {
				return nil, err
			}
			state.ScanStartedAt = &parsed
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func validateSearchDocument(document SearchDocument) error {
	if err := validateSearchScope(document.Scope); err != nil {
		return err
	}
	if document.MessageID <= 0 || document.ChatID == 0 || document.MessageDate.IsZero() || strings.TrimSpace(document.Text) == "" || strings.TrimSpace(document.ContentHash) == "" {
		return fmt.Errorf("search document identity, date, text, and content hash are required")
	}
	return nil
}

func validateSearchScope(scope string) error {
	if scope != "workspace" && scope != "legacy" && scope != "nest" {
		return fmt.Errorf("invalid search scope %q", scope)
	}
	return nil
}

func searchDocumentColumns(alias string) string {
	prefix := ""
	if strings.TrimSpace(alias) != "" {
		prefix = strings.TrimSpace(alias) + "."
	}
	columns := []string{
		"id", "scope", "source_ref", "source_title", "chat_id", "message_id", "topic_id",
		"message_date", "text", "media_type", "source_link", "content_hash", "status",
		"index_status", "attempts", "next_attempt_at", "last_error", "created_at", "updated_at",
	}
	for index := range columns {
		columns[index] = prefix + columns[index]
	}
	return strings.Join(columns, ", ")
}

func scanSearchDocuments(rows *sql.Rows) ([]SearchDocument, error) {
	var documents []SearchDocument
	for rows.Next() {
		var document SearchDocument
		var messageDate, createdAt, updatedAt string
		var nextAttempt sql.NullString
		if err := rows.Scan(&document.ID, &document.Scope, &document.SourceRef, &document.SourceTitle,
			&document.ChatID, &document.MessageID, &document.TopicID, &messageDate, &document.Text,
			&document.MediaType, &document.SourceLink, &document.ContentHash, &document.Status,
			&document.IndexStatus, &document.Attempts, &nextAttempt, &document.LastError,
			&createdAt, &updatedAt); err != nil {
			return nil, err
		}
		var err error
		document.MessageDate, err = time.Parse(time.RFC3339Nano, messageDate)
		if err != nil {
			return nil, err
		}
		document.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		document.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, err
		}
		if nextAttempt.Valid {
			parsed, parseErr := time.Parse(time.RFC3339Nano, nextAttempt.String)
			if parseErr != nil {
				return nil, parseErr
			}
			document.NextAttemptAt = &parsed
		}
		documents = append(documents, document)
	}
	return documents, rows.Err()
}

func compactSearchError(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len([]rune(value)) <= 300 {
		return value
	}
	return string([]rune(value)[:299]) + "…"
}
