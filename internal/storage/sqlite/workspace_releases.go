package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type WorkspaceDeploymentReceipt struct {
	Environment string
	Version     string
	Commit      string
	Checks      string
	VerifiedAt  time.Time
	CreatedAt   time.Time
}

type WorkspaceReleaseAnnouncement struct {
	ID          int64
	Environment string
	Version     string
	Commit      string
	Status      string
	MessageID   int
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (s *Store) RecordWorkspaceDeploymentReceipt(ctx context.Context, receipt WorkspaceDeploymentReceipt, now time.Time) error {
	receipt.Environment = strings.TrimSpace(receipt.Environment)
	receipt.Version = strings.TrimSpace(receipt.Version)
	receipt.Commit = strings.TrimSpace(receipt.Commit)
	receipt.Checks = strings.TrimSpace(receipt.Checks)
	if receipt.Environment == "" || receipt.Version == "" || receipt.Commit == "" {
		return fmt.Errorf("deployment receipt environment, version, and commit are required")
	}
	if receipt.VerifiedAt.IsZero() {
		receipt.VerifiedAt = now
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_deployment_receipts(environment, version, commit_sha, checks, verified_at, created_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(environment, version, commit_sha) DO UPDATE SET
    checks = excluded.checks,
    verified_at = excluded.verified_at`,
		receipt.Environment, receipt.Version, receipt.Commit, receipt.Checks,
		receipt.VerifiedAt.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) WorkspaceDeploymentReceipt(ctx context.Context, environment, version, commit string) (WorkspaceDeploymentReceipt, bool, error) {
	var receipt WorkspaceDeploymentReceipt
	var verifiedAt, createdAt string
	err := s.db.QueryRowContext(ctx, `
SELECT environment, version, commit_sha, checks, verified_at, created_at
FROM workspace_deployment_receipts
WHERE environment = ? AND version = ? AND commit_sha = ?`,
		strings.TrimSpace(environment), strings.TrimSpace(version), strings.TrimSpace(commit)).Scan(
		&receipt.Environment, &receipt.Version, &receipt.Commit, &receipt.Checks, &verifiedAt, &createdAt)
	if err == sql.ErrNoRows {
		return WorkspaceDeploymentReceipt{}, false, nil
	}
	if err != nil {
		return WorkspaceDeploymentReceipt{}, false, err
	}
	parsedVerified, err := time.Parse(time.RFC3339Nano, verifiedAt)
	if err != nil {
		return WorkspaceDeploymentReceipt{}, false, err
	}
	parsedCreated, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return WorkspaceDeploymentReceipt{}, false, err
	}
	receipt.VerifiedAt, receipt.CreatedAt = parsedVerified, parsedCreated
	return receipt, true, nil
}

// ReserveWorkspaceReleaseAnnouncement makes the external send a two-phase
// operation. A pre-existing row is returned and must never be sent again
// automatically, including the ambiguous "unknown" state.
func (s *Store) ReserveWorkspaceReleaseAnnouncement(ctx context.Context, environment, version, commit string, now time.Time) (WorkspaceReleaseAnnouncement, bool, error) {
	environment, version, commit = strings.TrimSpace(environment), strings.TrimSpace(version), strings.TrimSpace(commit)
	if environment == "" || version == "" || commit == "" {
		return WorkspaceReleaseAnnouncement{}, false, fmt.Errorf("announcement environment, version, and commit are required")
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_release_announcements(
    environment, version, commit_sha, status, created_at, updated_at
) SELECT ?, ?, ?, 'sending', ?, ?
WHERE NOT EXISTS (
    SELECT 1 FROM workspace_release_announcements
    WHERE environment = ? AND version = ?
)`, environment, version, commit, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), environment, version)
	if err != nil {
		return WorkspaceReleaseAnnouncement{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return WorkspaceReleaseAnnouncement{}, false, err
	}
	announcement, err := s.workspaceReleaseAnnouncement(ctx, environment, version)
	return announcement, rows == 1, err
}

func (s *Store) MarkWorkspaceReleaseAnnouncementSent(ctx context.Context, id int64, messageID int, now time.Time) error {
	if id <= 0 || messageID <= 0 {
		return fmt.Errorf("announcement and Telegram message IDs must be positive")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_release_announcements
SET status = 'sent', message_id = ?, error = '', updated_at = ?
WHERE id = ? AND status = 'sending'`, messageID, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sending workspace release announcement %d not found", id)
}

func (s *Store) MarkWorkspaceReleaseAnnouncementUnknown(ctx context.Context, id int64, sendErr string, now time.Time) error {
	if id <= 0 {
		return fmt.Errorf("announcement ID must be positive")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_release_announcements
SET status = 'unknown', error = ?, updated_at = ?
WHERE id = ? AND status = 'sending'`, compactReleaseError(sendErr), now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "sending workspace release announcement %d not found", id)
}

func (s *Store) workspaceReleaseAnnouncement(ctx context.Context, environment, version string) (WorkspaceReleaseAnnouncement, error) {
	var item WorkspaceReleaseAnnouncement
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT id, environment, version, commit_sha, status, message_id, error, created_at, updated_at
FROM workspace_release_announcements
WHERE environment = ? AND version = ?`, environment, version).Scan(
		&item.ID, &item.Environment, &item.Version, &item.Commit, &item.Status,
		&item.MessageID, &item.Error, &createdAt, &updatedAt)
	if err != nil {
		return WorkspaceReleaseAnnouncement{}, err
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return WorkspaceReleaseAnnouncement{}, err
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	return item, err
}

func compactReleaseError(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	const limit = 300
	if len([]rune(value)) <= limit {
		return value
	}
	return string([]rune(value)[:limit-1]) + "…"
}
