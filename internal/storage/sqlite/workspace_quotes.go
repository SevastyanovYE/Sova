package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type WorkspaceQuote struct {
	ID              int64
	Title           string
	Text            string
	Author          string
	Status          string
	WizardUserID    int64
	WizardStage     string
	DeliveryStatus  string
	DeliveryError   string
	SourceChatID    int64
	SourceMessageID int
	SourceLink      string
	TargetChatID    int64
	TargetTopicID   int
	TargetMessageID int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	PublishedAt     *time.Time
}

func (s *Store) CreateWorkspaceQuote(ctx context.Context, quote WorkspaceQuote, now time.Time) (WorkspaceQuote, error) {
	quote.Title = strings.TrimSpace(quote.Title)
	quote.Text = strings.TrimSpace(quote.Text)
	quote.Author = strings.TrimSpace(quote.Author)
	if quote.Text == "" {
		return WorkspaceQuote{}, fmt.Errorf("workspace quote text is required")
	}
	if quote.SourceChatID == 0 || quote.SourceMessageID == 0 {
		return WorkspaceQuote{}, fmt.Errorf("workspace quote source identity is required")
	}
	if quote.Status == "" {
		quote.Status = "draft"
	}
	if !validWorkspaceQuoteStatus(quote.Status) {
		return WorkspaceQuote{}, fmt.Errorf("invalid workspace quote status %q", quote.Status)
	}
	if quote.DeliveryStatus == "" {
		quote.DeliveryStatus = "draft"
	}
	now = now.UTC()
	result, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_quotes(
    title, text, author, status, wizard_user_id, wizard_stage, delivery_status, delivery_error,
    source_chat_id, source_message_id, source_link,
    target_chat_id, target_topic_id, target_message_id, created_at, updated_at, published_at
) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		quote.Title, quote.Text, quote.Author, quote.Status,
		quote.WizardUserID, strings.TrimSpace(quote.WizardStage), quote.DeliveryStatus,
		quote.SourceChatID, quote.SourceMessageID, strings.TrimSpace(quote.SourceLink),
		quote.TargetChatID, quote.TargetTopicID, quote.TargetMessageID,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return WorkspaceQuote{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return WorkspaceQuote{}, err
	}
	return s.WorkspaceQuoteByID(ctx, id)
}

func (s *Store) PublishWorkspaceQuote(ctx context.Context, id int64, targetChatID int64, targetTopicID, targetMessageID int, now time.Time) error {
	if id <= 0 || targetChatID == 0 || targetTopicID == 0 || targetMessageID == 0 {
		return fmt.Errorf("workspace quote target identity is required")
	}
	now = now.UTC()
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_quotes
SET status = 'active', target_chat_id = ?, target_topic_id = ?, target_message_id = ?,
    delivery_status = 'sent', delivery_error = '', wizard_stage = '',
    published_at = COALESCE(published_at, ?), updated_at = ?
WHERE id = ? AND status IN ('draft', 'active') AND delivery_status = 'sending'`,
		targetChatID, targetTopicID, targetMessageID,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("publishable workspace quote %d not found", id)
	}
	return nil
}

func (s *Store) WorkspaceQuoteByID(ctx context.Context, id int64) (WorkspaceQuote, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, title, text, author, status, source_chat_id, source_message_id, source_link,
       wizard_user_id, wizard_stage, delivery_status, delivery_error,
       target_chat_id, target_topic_id, target_message_id, created_at, updated_at, published_at
FROM workspace_quotes
WHERE id = ?`, id)
	return scanWorkspaceQuote(row)
}

func (s *Store) UpdateWorkspaceQuoteDraft(ctx context.Context, id int64, title, author, stage string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_quotes
SET title = ?, author = ?, wizard_stage = ?, updated_at = ?
WHERE id = ? AND status = 'draft' AND delivery_status = 'draft'`,
		strings.TrimSpace(title), strings.TrimSpace(author), strings.TrimSpace(stage),
		now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "editable workspace quote draft %d not found", id)
}

func (s *Store) WorkspaceQuoteDrafts(ctx context.Context) ([]WorkspaceQuote, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, title, text, author, status, source_chat_id, source_message_id, source_link,
       wizard_user_id, wizard_stage, delivery_status, delivery_error,
       target_chat_id, target_topic_id, target_message_id, created_at, updated_at, published_at
FROM workspace_quotes
WHERE status = 'draft' AND wizard_user_id != 0 AND wizard_stage != ''
ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var quotes []WorkspaceQuote
	for rows.Next() {
		quote, err := scanWorkspaceQuote(rows)
		if err != nil {
			return nil, err
		}
		quotes = append(quotes, quote)
	}
	return quotes, rows.Err()
}

func (s *Store) MarkWorkspaceQuoteSending(ctx context.Context, id int64, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_quotes
SET delivery_status = 'sending', delivery_error = '', updated_at = ?
WHERE id = ? AND status = 'draft' AND delivery_status = 'draft'`,
		now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sendable workspace quote %d not found", id)
}

func (s *Store) MarkWorkspaceQuoteDeliveryUnknown(ctx context.Context, id int64, sendErr string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_quotes
SET delivery_status = 'unknown', delivery_error = ?, wizard_stage = '', updated_at = ?
WHERE id = ? AND status = 'draft' AND delivery_status = 'sending'`,
		compactReleaseError(sendErr), now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sending workspace quote %d not found", id)
}

func (s *Store) ArchiveWorkspaceQuoteDraft(ctx context.Context, id int64, now time.Time) error {
	if id <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE workspace_quotes
SET status = 'archived', updated_at = ?
WHERE id = ? AND status = 'draft'`, now.UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) WorkspaceQuotes(ctx context.Context, statuses []string, limit int) ([]WorkspaceQuote, error) {
	if limit <= 0 {
		limit = 100
	}
	clauses := make([]string, 0, 1)
	args := make([]any, 0, len(statuses)+1)
	if len(statuses) > 0 {
		placeholders := make([]string, 0, len(statuses))
		for _, status := range statuses {
			status = strings.TrimSpace(status)
			if !validWorkspaceQuoteStatus(status) {
				return nil, fmt.Errorf("invalid workspace quote status %q", status)
			}
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		clauses = append(clauses, "status IN ("+strings.Join(placeholders, ", ")+")")
	}
	query := `
SELECT id, title, text, author, status, source_chat_id, source_message_id, source_link,
       wizard_user_id, wizard_stage, delivery_status, delivery_error,
       target_chat_id, target_topic_id, target_message_id, created_at, updated_at, published_at
FROM workspace_quotes`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY COALESCE(published_at, created_at) DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var quotes []WorkspaceQuote
	for rows.Next() {
		quote, err := scanWorkspaceQuote(rows)
		if err != nil {
			return nil, err
		}
		quotes = append(quotes, quote)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return quotes, nil
}

// MarkWorkspaceQuoteSourceNeedsReview returns only quotes changed by this call,
// allowing the caller to send a single notification for the first source edit.
func (s *Store) MarkWorkspaceQuoteSourceNeedsReview(ctx context.Context, chatID int64, messageID int, now time.Time) ([]WorkspaceQuote, error) {
	if chatID == 0 || messageID == 0 {
		return nil, fmt.Errorf("workspace quote source identity is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
SELECT id, title, text, author, status, source_chat_id, source_message_id, source_link,
       wizard_user_id, wizard_stage, delivery_status, delivery_error,
       target_chat_id, target_topic_id, target_message_id, created_at, updated_at, published_at
FROM workspace_quotes
WHERE source_chat_id = ? AND source_message_id = ? AND status = 'active'
ORDER BY id`, chatID, messageID)
	if err != nil {
		return nil, err
	}
	var changed []WorkspaceQuote
	for rows.Next() {
		quote, scanErr := scanWorkspaceQuote(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		changed = append(changed, quote)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(changed) == 0 {
		return nil, tx.Commit()
	}
	formattedNow := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE workspace_quotes
SET status = 'needs_review', updated_at = ?
WHERE source_chat_id = ? AND source_message_id = ? AND status = 'active'`,
		formattedNow, chatID, messageID)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != int64(len(changed)) {
		return nil, fmt.Errorf("workspace quote review update changed %d rows, expected %d", affected, len(changed))
	}
	for i := range changed {
		changed[i].Status = "needs_review"
		changed[i].UpdatedAt = now.UTC()
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changed, nil
}

func validWorkspaceQuoteStatus(status string) bool {
	switch status {
	case "draft", "active", "needs_review", "archived":
		return true
	default:
		return false
	}
}

type workspaceQuoteScanner interface {
	Scan(dest ...any) error
}

func scanWorkspaceQuote(scanner workspaceQuoteScanner) (WorkspaceQuote, error) {
	var quote WorkspaceQuote
	var createdRaw, updatedRaw string
	var publishedRaw sql.NullString
	err := scanner.Scan(
		&quote.ID, &quote.Title, &quote.Text, &quote.Author, &quote.Status,
		&quote.SourceChatID, &quote.SourceMessageID, &quote.SourceLink,
		&quote.WizardUserID, &quote.WizardStage, &quote.DeliveryStatus, &quote.DeliveryError,
		&quote.TargetChatID, &quote.TargetTopicID, &quote.TargetMessageID,
		&createdRaw, &updatedRaw, &publishedRaw,
	)
	if err != nil {
		return WorkspaceQuote{}, err
	}
	quote.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return WorkspaceQuote{}, fmt.Errorf("parse workspace quote created_at: %w", err)
	}
	quote.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return WorkspaceQuote{}, fmt.Errorf("parse workspace quote updated_at: %w", err)
	}
	if publishedRaw.Valid {
		publishedAt, parseErr := time.Parse(time.RFC3339Nano, publishedRaw.String)
		if parseErr != nil {
			return WorkspaceQuote{}, fmt.Errorf("parse workspace quote published_at: %w", parseErr)
		}
		quote.PublishedAt = &publishedAt
	}
	return quote, nil
}

var _ workspaceQuoteScanner = (*sql.Row)(nil)
var _ workspaceQuoteScanner = (*sql.Rows)(nil)
