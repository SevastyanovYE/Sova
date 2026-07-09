package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type WorkspaceDocument struct {
	ID              int64
	Type            string
	Status          string
	Title           string
	Category        string
	SourceChatID    int64
	SourceMessageID int
	SourceClusterID int64
	SourceLink      string
	TargetChatID    int64
	TargetTopicID   int
	TargetMessageID int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	PublishedAt     *time.Time
}

type WorkspaceDocumentPart struct {
	ID              int64
	DocumentID      int64
	PartNo          int
	Title           string
	SourceChatID    int64
	SourceMessageID int
	SourceClusterID int64
	SourceLink      string
	Text            string
	CreatedAt       time.Time
}

func (s *Store) CreateWorkspaceDocument(ctx context.Context, doc WorkspaceDocument, first WorkspaceDocumentPart, now time.Time) (WorkspaceDocument, error) {
	doc.Type = normalizeWorkspaceDocumentType(doc.Type)
	doc.Status = normalizeWorkspaceDocumentStatus(doc.Status)
	doc.Title = strings.TrimSpace(doc.Title)
	doc.Category = strings.TrimSpace(doc.Category)
	if doc.Type == "" {
		return WorkspaceDocument{}, fmt.Errorf("workspace document type is required")
	}
	if doc.Status == "" {
		return WorkspaceDocument{}, fmt.Errorf("workspace document status is invalid")
	}
	if doc.Title == "" {
		return WorkspaceDocument{}, fmt.Errorf("workspace document title is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	first = normalizeWorkspaceDocumentPart(first)
	if doc.SourceChatID == 0 && first.SourceChatID != 0 {
		doc.SourceChatID = first.SourceChatID
		doc.SourceMessageID = first.SourceMessageID
		doc.SourceClusterID = first.SourceClusterID
		doc.SourceLink = first.SourceLink
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceDocument{}, err
	}
	defer tx.Rollback()
	var publishedAt any
	if doc.PublishedAt != nil && !doc.PublishedAt.IsZero() {
		publishedAt = doc.PublishedAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO workspace_documents(
    doc_type, status, title, category, source_chat_id, source_message_id,
    source_cluster_id, source_link, target_chat_id, target_topic_id,
    target_message_id, created_at, updated_at, published_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		doc.Type, doc.Status, doc.Title, doc.Category, doc.SourceChatID,
		doc.SourceMessageID, doc.SourceClusterID, doc.SourceLink, doc.TargetChatID,
		doc.TargetTopicID, doc.TargetMessageID, now.UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano), publishedAt)
	if err != nil {
		return WorkspaceDocument{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return WorkspaceDocument{}, err
	}
	if first.SourceChatID != 0 && first.SourceMessageID != 0 {
		first.DocumentID = id
		first.PartNo = 1
		if _, err := insertWorkspaceDocumentPart(ctx, tx, first, now); err != nil {
			return WorkspaceDocument{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceDocument{}, err
	}
	return s.WorkspaceDocumentByID(ctx, id)
}

func (s *Store) AddWorkspaceDocumentPart(ctx context.Context, part WorkspaceDocumentPart, now time.Time) (WorkspaceDocumentPart, error) {
	part = normalizeWorkspaceDocumentPart(part)
	if part.DocumentID <= 0 {
		return WorkspaceDocumentPart{}, fmt.Errorf("workspace document id is required")
	}
	if part.SourceChatID == 0 || part.SourceMessageID == 0 {
		return WorkspaceDocumentPart{}, fmt.Errorf("workspace document part source message is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceDocumentPart{}, err
	}
	defer tx.Rollback()
	var nextPartNo int
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(part_no), 0) + 1
FROM workspace_document_parts
WHERE document_id = ?`, part.DocumentID).Scan(&nextPartNo); err != nil {
		return WorkspaceDocumentPart{}, err
	}
	part.PartNo = nextPartNo
	inserted, err := insertWorkspaceDocumentPart(ctx, tx, part, now)
	if err != nil {
		return WorkspaceDocumentPart{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_documents
SET updated_at = ?
WHERE id = ?`, now.UTC().Format(time.RFC3339Nano), part.DocumentID); err != nil {
		return WorkspaceDocumentPart{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceDocumentPart{}, err
	}
	return inserted, nil
}

func (s *Store) UpdateWorkspaceDocumentTitle(ctx context.Context, id int64, title string, now time.Time) error {
	title = strings.TrimSpace(title)
	if id <= 0 || title == "" {
		return fmt.Errorf("workspace document title update requires id and title")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_documents
SET title = ?, updated_at = ?
WHERE id = ?`,
		title, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "workspace document %d not found", id)
}

func (s *Store) UpdateWorkspaceDocumentCategory(ctx context.Context, id int64, category string, now time.Time) error {
	if id <= 0 {
		return fmt.Errorf("workspace document category update requires id")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_documents
SET category = ?, updated_at = ?
WHERE id = ?`,
		strings.TrimSpace(category), now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "workspace document %d not found", id)
}

func (s *Store) UpdateWorkspaceDocumentStatus(ctx context.Context, id int64, status string, now time.Time) error {
	status = normalizeWorkspaceDocumentStatus(status)
	if id <= 0 || status == "" {
		return fmt.Errorf("workspace document status update requires id and valid status")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_documents
SET status = ?, updated_at = ?
WHERE id = ?`,
		status, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "workspace document %d not found", id)
}

func (s *Store) UpdateWorkspaceDocumentTarget(ctx context.Context, id int64, chatID int64, topicID int, messageID int, publishedAt *time.Time, now time.Time) error {
	if id <= 0 || chatID == 0 || topicID == 0 || messageID == 0 {
		return fmt.Errorf("workspace document target update requires document and message identity")
	}
	var published any
	if publishedAt != nil && !publishedAt.IsZero() {
		published = publishedAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_documents
SET target_chat_id = ?, target_topic_id = ?, target_message_id = ?,
    published_at = COALESCE(?, published_at), updated_at = ?
WHERE id = ?`,
		chatID, topicID, messageID, published, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "workspace document %d not found", id)
}

func (s *Store) UpdateWorkspaceDocumentPartTitle(ctx context.Context, id int64, title string, now time.Time) error {
	if id <= 0 {
		return fmt.Errorf("workspace document part title update requires id")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_document_parts
SET title = ?
WHERE id = ?`,
		strings.TrimSpace(title), id)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "workspace document part %d not found", id); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE workspace_documents
SET updated_at = ?
WHERE id = (SELECT document_id FROM workspace_document_parts WHERE id = ?)`,
		now.UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) WorkspaceDocumentPartByID(ctx context.Context, id int64) (WorkspaceDocumentPart, error) {
	row := s.db.QueryRowContext(ctx, workspaceDocumentPartSelect()+`
WHERE id = ?`, id)
	return scanWorkspaceDocumentPart(row)
}

func (s *Store) DeleteWorkspaceDocumentPart(ctx context.Context, id int64, now time.Time) error {
	if id <= 0 {
		return fmt.Errorf("workspace document part id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var documentID int64
	var partNo int
	if err := tx.QueryRowContext(ctx, `
SELECT document_id, part_no
FROM workspace_document_parts
WHERE id = ?`, id).Scan(&documentID, &partNo); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
DELETE FROM workspace_document_parts
WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "workspace document part %d not found", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_document_parts
SET part_no = part_no - 1
WHERE document_id = ? AND part_no > ?`, documentID, partNo); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_documents
SET updated_at = ?
WHERE id = ?`, now.UTC().Format(time.RFC3339Nano), documentID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) WorkspaceDocumentByID(ctx context.Context, id int64) (WorkspaceDocument, error) {
	row := s.db.QueryRowContext(ctx, workspaceDocumentSelect()+`
WHERE id = ?`, id)
	return scanWorkspaceDocument(row)
}

func (s *Store) WorkspaceDocumentByTargetMessage(ctx context.Context, chatID int64, messageID int) (WorkspaceDocument, bool, error) {
	row := s.db.QueryRowContext(ctx, workspaceDocumentSelect()+`
WHERE target_chat_id = ? AND target_message_id = ?
ORDER BY updated_at DESC, id DESC
LIMIT 1`, chatID, messageID)
	doc, err := scanWorkspaceDocument(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceDocument{}, false, nil
	}
	if err != nil {
		return WorkspaceDocument{}, false, err
	}
	return doc, true, nil
}

func (s *Store) WorkspaceDocumentsByType(ctx context.Context, docType string, statuses []string, limit int) ([]WorkspaceDocument, error) {
	docType = normalizeWorkspaceDocumentType(docType)
	if docType == "" {
		return nil, fmt.Errorf("workspace document type is required")
	}
	if limit <= 0 {
		limit = 100
	}
	var normalized []string
	for _, status := range statuses {
		if value := normalizeWorkspaceDocumentStatus(status); value != "" {
			normalized = append(normalized, value)
		}
	}
	if len(normalized) == 0 {
		normalized = []string{"active"}
	}
	placeholders := make([]string, 0, len(normalized))
	args := []any{docType}
	for _, status := range normalized {
		placeholders = append(placeholders, "?")
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, workspaceDocumentSelect()+`
WHERE doc_type = ? AND status IN (`+strings.Join(placeholders, ", ")+`)
ORDER BY category, updated_at DESC, id
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []WorkspaceDocument
	for rows.Next() {
		doc, err := scanWorkspaceDocument(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *Store) PublishedWorkspaceDocuments(ctx context.Context, docType string, limit int) ([]WorkspaceDocument, error) {
	docType = normalizeWorkspaceDocumentType(docType)
	if docType == "" {
		return nil, fmt.Errorf("workspace document type is required")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, workspaceDocumentSelect()+`
WHERE doc_type = ? AND target_chat_id != 0 AND target_message_id != 0
ORDER BY published_at DESC, updated_at DESC, id DESC
LIMIT ?`, docType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []WorkspaceDocument
	for rows.Next() {
		doc, err := scanWorkspaceDocument(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *Store) WorkspaceDocumentParts(ctx context.Context, documentID int64) ([]WorkspaceDocumentPart, error) {
	rows, err := s.db.QueryContext(ctx, workspaceDocumentPartSelect()+`
WHERE document_id = ?
ORDER BY part_no, id`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var parts []WorkspaceDocumentPart
	for rows.Next() {
		part, err := scanWorkspaceDocumentPart(rows)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return parts, nil
}

func (s *Store) WorkspaceDocumentPartsBySource(ctx context.Context, chatID int64, messageID int) ([]WorkspaceDocumentPart, error) {
	rows, err := s.db.QueryContext(ctx, workspaceDocumentPartSelect()+`
WHERE source_chat_id = ? AND source_message_id = ?
ORDER BY document_id, part_no`, chatID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var parts []WorkspaceDocumentPart
	for rows.Next() {
		part, err := scanWorkspaceDocumentPart(rows)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return parts, nil
}

type workspaceDocumentPartInserter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertWorkspaceDocumentPart(ctx context.Context, tx workspaceDocumentPartInserter, part WorkspaceDocumentPart, now time.Time) (WorkspaceDocumentPart, error) {
	part = normalizeWorkspaceDocumentPart(part)
	if part.DocumentID <= 0 {
		return WorkspaceDocumentPart{}, fmt.Errorf("workspace document id is required")
	}
	if part.SourceChatID == 0 || part.SourceMessageID == 0 {
		return WorkspaceDocumentPart{}, fmt.Errorf("workspace document part source message is required")
	}
	if part.PartNo <= 0 {
		part.PartNo = 1
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO workspace_document_parts(
    document_id, part_no, title, source_chat_id, source_message_id,
    source_cluster_id, source_link, text, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		part.DocumentID, part.PartNo, part.Title, part.SourceChatID,
		part.SourceMessageID, part.SourceClusterID, part.SourceLink, part.Text,
		now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return WorkspaceDocumentPart{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return WorkspaceDocumentPart{}, err
	}
	part.ID = id
	part.CreatedAt = now.UTC()
	return part, nil
}

func normalizeWorkspaceDocumentPart(part WorkspaceDocumentPart) WorkspaceDocumentPart {
	part.Title = strings.TrimSpace(part.Title)
	part.SourceLink = strings.TrimSpace(part.SourceLink)
	part.Text = strings.TrimSpace(part.Text)
	return part
}

func normalizeWorkspaceDocumentType(docType string) string {
	switch strings.ToLower(strings.TrimSpace(docType)) {
	case "note", "template", "collection":
		return strings.ToLower(strings.TrimSpace(docType))
	default:
		return ""
	}
}

func normalizeWorkspaceDocumentStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "active":
		return "active"
	case "published", "archived", "needs_review":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return ""
	}
}

func workspaceDocumentSelect() string {
	return `SELECT id, doc_type, status, title, category, source_chat_id,
       source_message_id, source_cluster_id, source_link, target_chat_id,
       target_topic_id, target_message_id, created_at, updated_at, published_at
FROM workspace_documents`
}

func workspaceDocumentPartSelect() string {
	return `SELECT id, document_id, part_no, title, source_chat_id,
       source_message_id, source_cluster_id, source_link, text, created_at
FROM workspace_document_parts`
}

func scanWorkspaceDocument(scanner interface{ Scan(dest ...any) error }) (WorkspaceDocument, error) {
	var doc WorkspaceDocument
	var createdRaw, updatedRaw string
	var publishedRaw sql.NullString
	if err := scanner.Scan(
		&doc.ID, &doc.Type, &doc.Status, &doc.Title, &doc.Category,
		&doc.SourceChatID, &doc.SourceMessageID, &doc.SourceClusterID,
		&doc.SourceLink, &doc.TargetChatID, &doc.TargetTopicID,
		&doc.TargetMessageID, &createdRaw, &updatedRaw, &publishedRaw,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkspaceDocument{}, err
		}
		return WorkspaceDocument{}, err
	}
	var err error
	doc.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return WorkspaceDocument{}, fmt.Errorf("parse workspace document created_at: %w", err)
	}
	doc.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return WorkspaceDocument{}, fmt.Errorf("parse workspace document updated_at: %w", err)
	}
	if publishedRaw.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, publishedRaw.String)
		if err != nil {
			return WorkspaceDocument{}, fmt.Errorf("parse workspace document published_at: %w", err)
		}
		doc.PublishedAt = &parsed
	}
	return doc, nil
}

func scanWorkspaceDocumentPart(scanner interface{ Scan(dest ...any) error }) (WorkspaceDocumentPart, error) {
	var part WorkspaceDocumentPart
	var createdRaw string
	if err := scanner.Scan(
		&part.ID, &part.DocumentID, &part.PartNo, &part.Title,
		&part.SourceChatID, &part.SourceMessageID, &part.SourceClusterID,
		&part.SourceLink, &part.Text, &createdRaw,
	); err != nil {
		return WorkspaceDocumentPart{}, err
	}
	var err error
	part.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return WorkspaceDocumentPart{}, fmt.Errorf("parse workspace document part created_at: %w", err)
	}
	return part, nil
}
