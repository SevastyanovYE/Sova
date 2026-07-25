package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type OverviewPublication struct {
	ID          int64
	RunID       int64
	Kind        string
	Position    int
	ContentHash string
	Status      string
	Attempts    int
	ChatID      int64
	TopicID     int
	MessageID   int
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	SentAt      *time.Time
}

func (s *Store) EnsureOverviewPublication(ctx context.Context, runID int64, kind string, position int, contentHash string, now time.Time) (OverviewPublication, error) {
	kind = strings.TrimSpace(kind)
	contentHash = strings.TrimSpace(contentHash)
	if runID <= 0 || (kind != "digest" && kind != "calendar") || position < 0 || contentHash == "" {
		return OverviewPublication{}, fmt.Errorf("invalid overview publication identity")
	}
	formattedNow := now.UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO overview_publications(
    run_id, kind, position, content_hash, status, created_at, updated_at
) VALUES (?, ?, ?, ?, 'pending', ?, ?)
ON CONFLICT(run_id, kind, position) DO NOTHING`,
		runID, kind, position, contentHash, formattedNow, formattedNow); err != nil {
		return OverviewPublication{}, err
	}
	publication, err := s.overviewPublicationByIdentity(ctx, runID, kind, position)
	if err != nil {
		return OverviewPublication{}, err
	}
	if publication.ContentHash != contentHash {
		return OverviewPublication{}, fmt.Errorf("overview publication %d/%s/%d content changed after it was queued", runID, kind, position)
	}
	return publication, nil
}

func (s *Store) ClaimOverviewPublication(ctx context.Context, id int64, now time.Time) (OverviewPublication, bool, error) {
	if id <= 0 {
		return OverviewPublication{}, false, fmt.Errorf("overview publication id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OverviewPublication{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE overview_publications
SET status = 'sending', attempts = attempts + 1, error = '', updated_at = ?
WHERE id = ? AND status IN ('pending', 'retry')`, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return OverviewPublication{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return OverviewPublication{}, false, err
	}
	publication, err := scanOverviewPublication(tx.QueryRowContext(ctx, overviewPublicationSelect+` WHERE id = ?`, id))
	if err != nil {
		return OverviewPublication{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return OverviewPublication{}, false, err
	}
	return publication, affected == 1, nil
}

func (s *Store) MarkOverviewPublicationSent(ctx context.Context, id int64, chatID int64, topicID, messageID int, now time.Time) error {
	if id <= 0 || chatID == 0 || topicID == 0 || messageID == 0 {
		return fmt.Errorf("overview publication Telegram identity is required")
	}
	formattedNow := now.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
UPDATE overview_publications
SET status = 'sent', chat_id = ?, topic_id = ?, message_id = ?, error = '', sent_at = ?, updated_at = ?
WHERE id = ? AND status = 'sending'`,
		chatID, topicID, messageID, formattedNow, formattedNow, id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sending overview publication %d not found", id)
}

func (s *Store) MarkOverviewPublicationRetry(ctx context.Context, id int64, sendErr string, now time.Time) error {
	return s.markOverviewPublicationFailure(ctx, id, "retry", sendErr, now)
}

func (s *Store) MarkOverviewPublicationUnknown(ctx context.Context, id int64, sendErr string, now time.Time) error {
	return s.markOverviewPublicationFailure(ctx, id, "unknown", sendErr, now)
}

func (s *Store) markOverviewPublicationFailure(ctx context.Context, id int64, status, sendErr string, now time.Time) error {
	if status != "retry" && status != "unknown" {
		return fmt.Errorf("invalid overview publication failure status %q", status)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE overview_publications
SET status = ?, error = ?, updated_at = ?
WHERE id = ? AND status = 'sending'`,
		status, compactReleaseError(sendErr), now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sending overview publication %d not found", id)
}

func (s *Store) RecoverInterruptedOverviewPublications(ctx context.Context, runID int64, now time.Time) (int64, error) {
	if runID <= 0 {
		return 0, fmt.Errorf("overview run id is required")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE overview_publications
SET status = 'unknown', error = 'process restarted while Telegram delivery was in progress', updated_at = ?
WHERE run_id = ? AND status = 'sending'`, now.UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ResolveUnknownOverviewPublication records an operator's explicit Telegram
// reconciliation. "retry" means the message was verified absent; "sent"
// requires the existing Telegram message identity. No unknown delivery is
// changed automatically.
func (s *Store) ResolveUnknownOverviewPublication(ctx context.Context, runID int64, kind string, position int, outcome string, chatID int64, topicID, messageID int, now time.Time) error {
	kind = strings.TrimSpace(kind)
	outcome = strings.TrimSpace(outcome)
	if runID <= 0 || (kind != "digest" && kind != "calendar") || position < 0 {
		return fmt.Errorf("invalid overview publication identity")
	}
	formattedNow := now.UTC().Format(time.RFC3339Nano)
	var (
		result sql.Result
		err    error
	)
	switch outcome {
	case "retry":
		result, err = s.db.ExecContext(ctx, `
UPDATE overview_publications
SET status = 'retry', error = '', updated_at = ?
WHERE run_id = ? AND kind = ? AND position = ? AND status = 'unknown'`,
			formattedNow, runID, kind, position)
	case "sent":
		if chatID == 0 || topicID == 0 || messageID == 0 {
			return fmt.Errorf("resolved Telegram message identity is required")
		}
		result, err = s.db.ExecContext(ctx, `
UPDATE overview_publications
SET status = 'sent', chat_id = ?, topic_id = ?, message_id = ?, error = '', sent_at = ?, updated_at = ?
WHERE run_id = ? AND kind = ? AND position = ? AND status = 'unknown'`,
			chatID, topicID, messageID, formattedNow, formattedNow, runID, kind, position)
	default:
		return fmt.Errorf("overview publication outcome must be sent or retry")
	}
	if err != nil {
		return err
	}
	return requireOneRow(result, "unknown overview publication %d/%s/%d not found", runID, kind, position)
}

func (s *Store) OverviewPublicationsByRun(ctx context.Context, runID int64) ([]OverviewPublication, error) {
	rows, err := s.db.QueryContext(ctx, overviewPublicationSelect+` WHERE run_id = ? ORDER BY kind, position`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var publications []OverviewPublication
	for rows.Next() {
		publication, scanErr := scanOverviewPublication(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		publications = append(publications, publication)
	}
	return publications, rows.Err()
}

func (s *Store) overviewPublicationByIdentity(ctx context.Context, runID int64, kind string, position int) (OverviewPublication, error) {
	return scanOverviewPublication(s.db.QueryRowContext(ctx, overviewPublicationSelect+`
WHERE run_id = ? AND kind = ? AND position = ?`, runID, kind, position))
}

const overviewPublicationSelect = `
SELECT id, run_id, kind, position, content_hash, status, attempts,
       chat_id, topic_id, message_id, error, created_at, updated_at, sent_at
FROM overview_publications`

type overviewPublicationScanner interface {
	Scan(dest ...any) error
}

func scanOverviewPublication(scanner overviewPublicationScanner) (OverviewPublication, error) {
	var publication OverviewPublication
	var createdRaw, updatedRaw string
	var sentRaw sql.NullString
	if err := scanner.Scan(
		&publication.ID, &publication.RunID, &publication.Kind, &publication.Position,
		&publication.ContentHash, &publication.Status, &publication.Attempts,
		&publication.ChatID, &publication.TopicID, &publication.MessageID, &publication.Error,
		&createdRaw, &updatedRaw, &sentRaw,
	); err != nil {
		return OverviewPublication{}, err
	}
	var err error
	publication.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return OverviewPublication{}, fmt.Errorf("parse overview publication created_at: %w", err)
	}
	publication.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return OverviewPublication{}, fmt.Errorf("parse overview publication updated_at: %w", err)
	}
	if sentRaw.Valid {
		sentAt, parseErr := time.Parse(time.RFC3339Nano, sentRaw.String)
		if parseErr != nil {
			return OverviewPublication{}, fmt.Errorf("parse overview publication sent_at: %w", parseErr)
		}
		publication.SentAt = &sentAt
	}
	return publication, nil
}

var _ overviewPublicationScanner = (*sql.Row)(nil)
var _ overviewPublicationScanner = (*sql.Rows)(nil)
