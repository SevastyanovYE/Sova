package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Run struct {
	ID         int64
	Trigger    string
	Status     string
	StartedAt  time.Time
	FinishedAt *time.Time
	Summary    string
	Error      string
}

type TelegramSource struct {
	ID            int64
	Ref           string
	PeerKind      string
	ChatID        int64
	AccessHash    int64
	Title         string
	Username      string
	LastMessageID int
}

type TelegramMessage struct {
	SourceID   int64
	ChatID     int64
	MessageID  int
	Date       time.Time
	Kind       string
	Text       string
	MediaType  string
	SourceLink string
	RawJSON    string
}

type TelegramRecentMessage struct {
	SourceRef   string
	SourceTitle string
	Username    string
	ChatID      int64
	MessageID   int
	Date        time.Time
	Kind        string
	Text        string
	MediaType   string
	SourceLink  string
}

type MessageDecision struct {
	RunID      int64
	ChatID     int64
	MessageID  int
	Keep       bool
	Importance int
	Reason     string
	Tags       []string
	HasEvent   bool
	Model      string
}

type CalendarCandidate struct {
	ID              int64
	RunID           int64
	ChatID          int64
	MessageID       int
	SourceLink      string
	Title           string
	StartAt         time.Time
	EndAt           time.Time
	Timezone        string
	Location        string
	Description     string
	Confidence      string
	Status          string
	CalendarEventID string
	Error           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CooldownError struct {
	NextAllowedAt time.Time
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf("overview cooldown is active until %s", e.NextAllowedAt.Format(time.RFC3339))
}

var ErrRunActive = errors.New("another overview run is active")

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS overview_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger TEXT NOT NULL CHECK (trigger IN ('scheduled', 'nest_button', 'manual')),
    status TEXT NOT NULL CHECK (status IN ('running', 'success', 'failed')),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    summary TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_overview_runs_started_at
    ON overview_runs(started_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_one_running_overview
    ON overview_runs(status) WHERE status = 'running';
CREATE TABLE IF NOT EXISTS telegram_sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ref TEXT NOT NULL UNIQUE,
    peer_kind TEXT NOT NULL,
    chat_id INTEGER NOT NULL,
    access_hash INTEGER NOT NULL DEFAULT 0,
    title TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    last_message_id INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_sources_peer
    ON telegram_sources(peer_kind, chat_id);
CREATE TABLE IF NOT EXISTS telegram_messages (
    source_id INTEGER NOT NULL REFERENCES telegram_sources(id),
    chat_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    date TEXT NOT NULL,
    kind TEXT NOT NULL,
    text TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    source_link TEXT NOT NULL DEFAULT '',
    raw_json TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY(chat_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_telegram_messages_source_date
    ON telegram_messages(source_id, date DESC);
CREATE TABLE IF NOT EXISTS message_decisions (
    run_id INTEGER NOT NULL REFERENCES overview_runs(id),
    chat_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    keep INTEGER NOT NULL CHECK (keep IN (0, 1)),
    importance INTEGER NOT NULL CHECK (importance BETWEEN 0 AND 3),
    reason TEXT NOT NULL DEFAULT '',
    tags_json TEXT NOT NULL DEFAULT '[]',
    has_event INTEGER NOT NULL CHECK (has_event IN (0, 1)),
    model TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY(run_id, chat_id, message_id),
    FOREIGN KEY(chat_id, message_id) REFERENCES telegram_messages(chat_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_message_decisions_run
    ON message_decisions(run_id, importance DESC, keep DESC);
CREATE TABLE IF NOT EXISTS calendar_candidates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES overview_runs(id),
    chat_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    source_link TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    start_at TEXT NOT NULL,
    end_at TEXT NOT NULL,
    timezone TEXT NOT NULL,
    location TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    confidence TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'created', 'failed')),
    calendar_event_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(chat_id, message_id) REFERENCES telegram_messages(chat_id, message_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_calendar_candidates_run_message
    ON calendar_candidates(run_id, chat_id, message_id);
CREATE INDEX IF NOT EXISTS idx_calendar_candidates_status
    ON calendar_candidates(status, updated_at DESC);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate SQLite: %w", err)
	}
	return nil
}

func (s *Store) TryStartOverview(ctx context.Context, trigger string, now time.Time, cooldown time.Duration) (Run, error) {
	if trigger != "scheduled" && trigger != "nest_button" && trigger != "manual" {
		return Run{}, fmt.Errorf("unsupported trigger %q", trigger)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()

	var status, startedRaw string
	err = tx.QueryRowContext(ctx, `
SELECT status, started_at
FROM overview_runs
ORDER BY started_at DESC, id DESC
LIMIT 1`).Scan(&status, &startedRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Run{}, err
	}
	if err == nil {
		startedAt, parseErr := time.Parse(time.RFC3339Nano, startedRaw)
		if parseErr != nil {
			return Run{}, fmt.Errorf("parse latest run time: %w", parseErr)
		}
		if status == "running" {
			return Run{}, ErrRunActive
		}
		nextAllowed := startedAt.Add(cooldown)
		if now.Before(nextAllowed) {
			return Run{}, &CooldownError{NextAllowedAt: nextAllowed}
		}
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO overview_runs(trigger, status, started_at)
VALUES (?, 'running', ?)`, trigger, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if isConstraintError(err) {
			return Run{}, ErrRunActive
		}
		return Run{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return Run{ID: id, Trigger: trigger, Status: "running", StartedAt: now.UTC()}, nil
}

func (s *Store) FinishOverview(ctx context.Context, id int64, status, summary, runErr string, now time.Time) error {
	if status != "success" && status != "failed" {
		return fmt.Errorf("invalid final run status %q", status)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE overview_runs
SET status = ?, finished_at = ?, summary = ?, error = ?
WHERE id = ? AND status = 'running'`,
		status, now.UTC().Format(time.RFC3339Nano), summary, runErr, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("running overview %d not found", id)
	}
	return nil
}

func (s *Store) LatestRun(ctx context.Context) (Run, bool, error) {
	var run Run
	var startedRaw string
	var finishedRaw sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT id, trigger, status, started_at, finished_at, summary, error
FROM overview_runs
ORDER BY started_at DESC, id DESC
LIMIT 1`).Scan(&run.ID, &run.Trigger, &run.Status, &startedRaw, &finishedRaw, &run.Summary, &run.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	run.StartedAt, err = time.Parse(time.RFC3339Nano, startedRaw)
	if err != nil {
		return Run{}, false, err
	}
	if finishedRaw.Valid {
		finished, parseErr := time.Parse(time.RFC3339Nano, finishedRaw.String)
		if parseErr != nil {
			return Run{}, false, parseErr
		}
		run.FinishedAt = &finished
	}
	return run, true, nil
}

func (s *Store) RecentRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, trigger, status, started_at, finished_at, summary, error
FROM overview_runs
ORDER BY started_at DESC, id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Store) UpsertTelegramSource(ctx context.Context, source TelegramSource, now time.Time) (TelegramSource, error) {
	if source.Ref == "" {
		return TelegramSource{}, fmt.Errorf("telegram source ref is required")
	}
	if source.PeerKind == "" {
		return TelegramSource{}, fmt.Errorf("telegram source peer kind is required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO telegram_sources(ref, peer_kind, chat_id, access_hash, title, username, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(ref) DO UPDATE SET
    peer_kind = excluded.peer_kind,
    chat_id = excluded.chat_id,
    access_hash = excluded.access_hash,
    title = excluded.title,
    username = excluded.username,
    updated_at = excluded.updated_at`,
		source.Ref, source.PeerKind, source.ChatID, source.AccessHash, source.Title, source.Username,
		now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return TelegramSource{}, err
	}
	return s.TelegramSourceByRef(ctx, source.Ref)
}

func (s *Store) TelegramSourceByRef(ctx context.Context, ref string) (TelegramSource, error) {
	var source TelegramSource
	err := s.db.QueryRowContext(ctx, `
SELECT id, ref, peer_kind, chat_id, access_hash, title, username, last_message_id
FROM telegram_sources
WHERE ref = ?`, ref).Scan(
		&source.ID, &source.Ref, &source.PeerKind, &source.ChatID, &source.AccessHash,
		&source.Title, &source.Username, &source.LastMessageID,
	)
	if err != nil {
		return TelegramSource{}, err
	}
	return source, nil
}

func (s *Store) InsertTelegramMessages(ctx context.Context, messages []TelegramMessage) (int, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	inserted := 0
	maxMessageIDBySource := map[int64]int{}
	for _, message := range messages {
		if message.SourceID == 0 || message.ChatID == 0 || message.MessageID == 0 {
			return 0, 0, fmt.Errorf("invalid telegram message identity")
		}
		result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO telegram_messages(
    source_id, chat_id, message_id, date, kind, text, media_type, source_link, raw_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			message.SourceID, message.ChatID, message.MessageID, message.Date.UTC().Format(time.RFC3339Nano),
			message.Kind, message.Text, message.MediaType, message.SourceLink, message.RawJSON,
			time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return 0, 0, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
		if affected > 0 {
			inserted++
		}
		if message.MessageID > maxMessageIDBySource[message.SourceID] {
			maxMessageIDBySource[message.SourceID] = message.MessageID
		}
	}
	for sourceID, maxMessageID := range maxMessageIDBySource {
		if _, err := tx.ExecContext(ctx, `
UPDATE telegram_sources
SET last_message_id = MAX(last_message_id, ?), updated_at = ?
WHERE id = ?`, maxMessageID, time.Now().UTC().Format(time.RFC3339Nano), sourceID); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return inserted, len(messages), nil
}

func (s *Store) FilterNewTelegramMessages(ctx context.Context, messages []TelegramMessage) ([]TelegramMessage, error) {
	out := make([]TelegramMessage, 0, len(messages))
	for _, message := range messages {
		if message.ChatID == 0 || message.MessageID == 0 {
			return nil, fmt.Errorf("invalid telegram message identity")
		}
		var exists int
		err := s.db.QueryRowContext(ctx, `
SELECT 1
FROM telegram_messages
WHERE chat_id = ? AND message_id = ?`,
			message.ChatID, message.MessageID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			out = append(out, message)
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) RecentTelegramMessages(ctx context.Context, limit int) ([]TelegramRecentMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT s.ref, s.title, s.username, m.chat_id, m.message_id, m.date, m.kind, m.text, m.media_type, m.source_link
FROM telegram_messages m
JOIN telegram_sources s ON s.id = m.source_id
ORDER BY m.date DESC, m.chat_id DESC, m.message_id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []TelegramRecentMessage
	for rows.Next() {
		var message TelegramRecentMessage
		var dateRaw string
		if err := rows.Scan(
			&message.SourceRef, &message.SourceTitle, &message.Username, &message.ChatID,
			&message.MessageID, &dateRaw, &message.Kind, &message.Text, &message.MediaType,
			&message.SourceLink,
		); err != nil {
			return nil, err
		}
		date, err := time.Parse(time.RFC3339Nano, dateRaw)
		if err != nil {
			return nil, fmt.Errorf("parse telegram message date: %w", err)
		}
		message.Date = date
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Store) InsertMessageDecisions(ctx context.Context, decisions []MessageDecision, now time.Time) error {
	if len(decisions) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, decision := range decisions {
		if decision.RunID == 0 || decision.ChatID == 0 || decision.MessageID == 0 {
			return fmt.Errorf("invalid decision identity")
		}
		if decision.Importance < 0 || decision.Importance > 3 {
			return fmt.Errorf("decision importance out of range for %d/%d", decision.ChatID, decision.MessageID)
		}
		tags, err := json.Marshal(decision.Tags)
		if err != nil {
			return fmt.Errorf("marshal decision tags: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO message_decisions(
    run_id, chat_id, message_id, keep, importance, reason, tags_json, has_event, model, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			decision.RunID, decision.ChatID, decision.MessageID, boolInt(decision.Keep),
			decision.Importance, decision.Reason, string(tags), boolInt(decision.HasEvent),
			decision.Model, now.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) InsertCalendarCandidates(ctx context.Context, candidates []CalendarCandidate, now time.Time) ([]CalendarCandidate, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	inserted := make([]CalendarCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.RunID == 0 || candidate.ChatID == 0 || candidate.MessageID == 0 {
			return nil, fmt.Errorf("invalid calendar candidate identity")
		}
		if strings.TrimSpace(candidate.Title) == "" {
			return nil, fmt.Errorf("calendar candidate title is required")
		}
		if candidate.StartAt.IsZero() || candidate.EndAt.IsZero() || !candidate.EndAt.After(candidate.StartAt) {
			return nil, fmt.Errorf("calendar candidate requires a valid start/end")
		}
		if candidate.Status == "" {
			candidate.Status = "pending"
		}
		if candidate.Timezone == "" {
			candidate.Timezone = "Europe/Moscow"
		}
		nowRaw := now.UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO calendar_candidates(
    run_id, chat_id, message_id, source_link, title, start_at, end_at, timezone,
    location, description, confidence, status, calendar_event_id, error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			candidate.RunID, candidate.ChatID, candidate.MessageID, candidate.SourceLink,
			candidate.Title, candidate.StartAt.UTC().Format(time.RFC3339Nano),
			candidate.EndAt.UTC().Format(time.RFC3339Nano), candidate.Timezone,
			candidate.Location, candidate.Description, candidate.Confidence, candidate.Status,
			candidate.CalendarEventID, candidate.Error, nowRaw, nowRaw)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 0 {
			continue
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		candidate.ID = id
		candidate.CreatedAt = now.UTC()
		candidate.UpdatedAt = now.UTC()
		inserted = append(inserted, candidate)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inserted, nil
}

func (s *Store) CalendarCandidateByID(ctx context.Context, id int64) (CalendarCandidate, error) {
	return s.calendarCandidateByQuery(ctx, `
SELECT id, run_id, chat_id, message_id, source_link, title, start_at, end_at,
       timezone, location, description, confidence, status, calendar_event_id,
       error, created_at, updated_at
FROM calendar_candidates
WHERE id = ?`, id)
}

func (s *Store) RecentCalendarCandidates(ctx context.Context, limit int) ([]CalendarCandidate, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, chat_id, message_id, source_link, title, start_at, end_at,
       timezone, location, description, confidence, status, calendar_event_id,
       error, created_at, updated_at
FROM calendar_candidates
ORDER BY updated_at DESC, id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []CalendarCandidate
	for rows.Next() {
		candidate, err := scanCalendarCandidate(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func (s *Store) UpdateCalendarCandidateStatus(ctx context.Context, id int64, status, eventID, runErr string, now time.Time) error {
	if status != "approved" && status != "rejected" && status != "created" && status != "failed" {
		return fmt.Errorf("invalid calendar candidate status %q", status)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE calendar_candidates
SET status = ?, calendar_event_id = ?, error = ?, updated_at = ?
WHERE id = ?`,
		status, eventID, runErr, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("calendar candidate %d not found", id)
	}
	return nil
}

func isConstraintError(err error) bool {
	return err != nil && (contains(err.Error(), "constraint") || contains(err.Error(), "unique"))
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type calendarCandidateScanner interface {
	Scan(dest ...any) error
}

func (s *Store) calendarCandidateByQuery(ctx context.Context, query string, args ...any) (CalendarCandidate, error) {
	row := s.db.QueryRowContext(ctx, query, args...)
	return scanCalendarCandidate(row)
}

func scanCalendarCandidate(scanner calendarCandidateScanner) (CalendarCandidate, error) {
	var candidate CalendarCandidate
	var startRaw, endRaw, createdRaw, updatedRaw string
	if err := scanner.Scan(
		&candidate.ID, &candidate.RunID, &candidate.ChatID, &candidate.MessageID,
		&candidate.SourceLink, &candidate.Title, &startRaw, &endRaw, &candidate.Timezone,
		&candidate.Location, &candidate.Description, &candidate.Confidence,
		&candidate.Status, &candidate.CalendarEventID, &candidate.Error,
		&createdRaw, &updatedRaw,
	); err != nil {
		return CalendarCandidate{}, err
	}
	var err error
	candidate.StartAt, err = time.Parse(time.RFC3339Nano, startRaw)
	if err != nil {
		return CalendarCandidate{}, err
	}
	candidate.EndAt, err = time.Parse(time.RFC3339Nano, endRaw)
	if err != nil {
		return CalendarCandidate{}, err
	}
	candidate.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return CalendarCandidate{}, err
	}
	candidate.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return CalendarCandidate{}, err
	}
	return candidate, nil
}

type runScanner interface {
	Scan(dest ...any) error
}

func scanRun(scanner runScanner) (Run, error) {
	var run Run
	var startedRaw string
	var finishedRaw sql.NullString
	if err := scanner.Scan(&run.ID, &run.Trigger, &run.Status, &startedRaw, &finishedRaw, &run.Summary, &run.Error); err != nil {
		return Run{}, err
	}
	startedAt, err := time.Parse(time.RFC3339Nano, startedRaw)
	if err != nil {
		return Run{}, err
	}
	run.StartedAt = startedAt
	if finishedRaw.Valid {
		finished, err := time.Parse(time.RFC3339Nano, finishedRaw.String)
		if err != nil {
			return Run{}, err
		}
		run.FinishedAt = &finished
	}
	return run, nil
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		match := true
		for j := range fragment {
			a := value[i+j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if a != fragment[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
