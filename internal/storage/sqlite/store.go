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

type ModelCall struct {
	ID             int64
	RunID          int64
	Stage          string
	BatchIndex     int
	InputMessages  int
	InputChars     int
	DurationMillis int64
	Success        bool
	Fallbacks      int
	Error          string
	Model          string
	CreatedAt      time.Time
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

type WorkspaceTopic struct {
	SourceRef    string
	ChatID       int64
	TopicID      int
	TopMessageID int
	Title        string
	Pinned       bool
	Closed       bool
	Hidden       bool
	CreatedAt    time.Time
	DiscoveredAt time.Time
}

type WorkspaceAuditRun struct {
	ID          int64
	SourceRef   string
	Status      string
	DryRun      bool
	StartedAt   time.Time
	FinishedAt  *time.Time
	ArtifactDir string
	Summary     string
	Error       string
}

type WorkspaceSourceMessage struct {
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
	RawJSON     string
}

type WorkspaceAuditRecord struct {
	RunID           int64
	SourceRef       string
	ChatID          int64
	MessageID       int
	SourceTopic     string
	TopicID         int
	TopMessageID    int
	MessageDate     time.Time
	EditDate        *time.Time
	MessageLink     string
	ShortSummary    string
	DetectedType    string
	ModelDecision   string
	Confidence      string
	SuggestedTarget string
	Reason          string
	MediaType       string
	Pinned          bool
	LongMessage     bool
	Edited          bool
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
CREATE TABLE IF NOT EXISTS model_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES overview_runs(id),
    stage TEXT NOT NULL,
    batch_index INTEGER NOT NULL,
    input_messages INTEGER NOT NULL,
    input_chars INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    success INTEGER NOT NULL CHECK (success IN (0, 1)),
    fallbacks INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_calls_created
    ON model_calls(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_model_calls_run
    ON model_calls(run_id, stage, batch_index);
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
CREATE TABLE IF NOT EXISTS workspace_topics (
    source_ref TEXT NOT NULL,
    chat_id INTEGER NOT NULL,
    topic_id INTEGER NOT NULL,
    top_message_id INTEGER NOT NULL DEFAULT 0,
    title TEXT NOT NULL DEFAULT '',
    pinned INTEGER NOT NULL CHECK (pinned IN (0, 1)),
    closed INTEGER NOT NULL CHECK (closed IN (0, 1)),
    hidden INTEGER NOT NULL CHECK (hidden IN (0, 1)),
    created_at TEXT,
    discovered_at TEXT NOT NULL,
    PRIMARY KEY(source_ref, topic_id)
);
CREATE INDEX IF NOT EXISTS idx_workspace_topics_chat
    ON workspace_topics(chat_id, topic_id);
CREATE TABLE IF NOT EXISTS workspace_audit_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_ref TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'success', 'failed')),
    dry_run INTEGER NOT NULL CHECK (dry_run IN (0, 1)),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    artifact_dir TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_workspace_audit_runs_started
    ON workspace_audit_runs(started_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS workspace_audit_records (
    run_id INTEGER NOT NULL REFERENCES workspace_audit_runs(id),
    source_ref TEXT NOT NULL,
    chat_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    source_topic TEXT NOT NULL DEFAULT '',
    topic_id INTEGER NOT NULL DEFAULT 0,
    top_message_id INTEGER NOT NULL DEFAULT 0,
    message_date TEXT NOT NULL,
    edit_date TEXT,
    message_link TEXT NOT NULL DEFAULT '',
    short_summary TEXT NOT NULL DEFAULT '',
    detected_type TEXT NOT NULL,
    model_decision TEXT NOT NULL,
    confidence TEXT NOT NULL,
    suggested_target TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    pinned INTEGER NOT NULL CHECK (pinned IN (0, 1)),
    long_message INTEGER NOT NULL CHECK (long_message IN (0, 1)),
    edited INTEGER NOT NULL CHECK (edited IN (0, 1)),
    created_at TEXT NOT NULL,
    PRIMARY KEY(run_id, chat_id, message_id),
    FOREIGN KEY(chat_id, message_id) REFERENCES telegram_messages(chat_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_workspace_audit_records_decision
    ON workspace_audit_records(run_id, model_decision, detected_type);
CREATE TABLE IF NOT EXISTS workspace_messages (
    chat_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    topic_id INTEGER NOT NULL DEFAULT 0,
    from_user_id INTEGER NOT NULL DEFAULT 0,
    from_is_bot INTEGER NOT NULL CHECK (from_is_bot IN (0, 1)),
    date TEXT NOT NULL,
    edit_date TEXT,
    text TEXT NOT NULL DEFAULT '',
    caption TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    forwarded INTEGER NOT NULL CHECK (forwarded IN (0, 1)),
    forward_chat_id INTEGER NOT NULL DEFAULT 0,
    forward_message_id INTEGER NOT NULL DEFAULT 0,
    reply_to_message_id INTEGER NOT NULL DEFAULT 0,
    source_link TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(chat_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_workspace_messages_topic
    ON workspace_messages(chat_id, topic_id, message_id);
CREATE TABLE IF NOT EXISTS workspace_clusters (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id INTEGER NOT NULL,
    topic_id INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed', 'needs_review')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_workspace_clusters_topic
    ON workspace_clusters(chat_id, topic_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS workspace_cluster_messages (
    cluster_id INTEGER NOT NULL REFERENCES workspace_clusters(id) ON DELETE CASCADE,
    chat_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    position INTEGER NOT NULL,
    role TEXT NOT NULL DEFAULT 'part' CHECK (role IN ('primary', 'part', 'manual')),
    attached_at TEXT NOT NULL,
    PRIMARY KEY(cluster_id, chat_id, message_id),
    UNIQUE(chat_id, message_id),
    FOREIGN KEY(chat_id, message_id) REFERENCES workspace_messages(chat_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_workspace_cluster_messages_order
    ON workspace_cluster_messages(cluster_id, position, message_id);
CREATE TABLE IF NOT EXISTS workspace_tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_chat_id INTEGER NOT NULL,
    source_message_id INTEGER NOT NULL,
    source_link TEXT NOT NULL DEFAULT '',
    source_cluster_id INTEGER NOT NULL DEFAULT 0,
    card_chat_id INTEGER NOT NULL DEFAULT 0,
    card_topic_id INTEGER NOT NULL DEFAULT 0,
    card_message_id INTEGER NOT NULL DEFAULT 0,
    text TEXT NOT NULL,
    emoji TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('open', 'done', 'cancelled', 'deferred')),
    deferred_until TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT,
    cancelled_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_workspace_tasks_source
    ON workspace_tasks(source_chat_id, source_message_id, id);
CREATE INDEX IF NOT EXISTS idx_workspace_tasks_status
    ON workspace_tasks(status, updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_tasks_card
    ON workspace_tasks(card_chat_id, card_message_id)
    WHERE card_chat_id != 0 AND card_message_id != 0;
CREATE TABLE IF NOT EXISTS workspace_derived_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_chat_id INTEGER NOT NULL,
    source_message_id INTEGER NOT NULL,
    source_cluster_id INTEGER NOT NULL DEFAULT 0,
    derived_type TEXT NOT NULL,
    derived_chat_id INTEGER NOT NULL,
    derived_topic_id INTEGER NOT NULL DEFAULT 0,
    derived_message_id INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'published', 'needs_review', 'closed')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_workspace_derived_source
    ON workspace_derived_messages(source_chat_id, source_message_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_derived_message
    ON workspace_derived_messages(derived_chat_id, derived_message_id, derived_type);
CREATE TABLE IF NOT EXISTS workspace_topic_indexes (
    chat_id INTEGER NOT NULL,
    topic_id INTEGER NOT NULL,
    index_key TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(chat_id, topic_id, index_key)
);
CREATE TABLE IF NOT EXISTS workspace_documents (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    doc_type TEXT NOT NULL CHECK (doc_type IN ('note', 'template', 'collection')),
    status TEXT NOT NULL CHECK (status IN ('active', 'published', 'archived', 'needs_review')),
    title TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT '',
    source_chat_id INTEGER NOT NULL DEFAULT 0,
    source_message_id INTEGER NOT NULL DEFAULT 0,
    source_cluster_id INTEGER NOT NULL DEFAULT 0,
    source_link TEXT NOT NULL DEFAULT '',
    target_chat_id INTEGER NOT NULL DEFAULT 0,
    target_topic_id INTEGER NOT NULL DEFAULT 0,
    target_message_id INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    published_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_workspace_documents_type
    ON workspace_documents(doc_type, status, category, updated_at DESC);
CREATE TABLE IF NOT EXISTS workspace_document_parts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id INTEGER NOT NULL REFERENCES workspace_documents(id) ON DELETE CASCADE,
    part_no INTEGER NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    source_chat_id INTEGER NOT NULL DEFAULT 0,
    source_message_id INTEGER NOT NULL DEFAULT 0,
    source_cluster_id INTEGER NOT NULL DEFAULT 0,
    source_link TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE(document_id, part_no)
);
CREATE INDEX IF NOT EXISTS idx_workspace_document_parts_source
    ON workspace_document_parts(source_chat_id, source_message_id);
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

func (s *Store) RunByID(ctx context.Context, id int64) (Run, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, trigger, status, started_at, finished_at, summary, error
FROM overview_runs
WHERE id = ?`, id)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

func (s *Store) RecoverFailedOverview(ctx context.Context, id int64, summary string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE overview_runs
SET status = 'success', finished_at = ?, summary = ?, error = ''
WHERE id = ? AND status = 'failed'`,
		now.UTC().Format(time.RFC3339Nano), summary, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("failed overview %d not found", id)
	}
	return nil
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

func (s *Store) TelegramSources(ctx context.Context) ([]TelegramSource, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, ref, peer_kind, chat_id, access_hash, title, username, last_message_id
FROM telegram_sources
ORDER BY title, ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []TelegramSource
	for rows.Next() {
		var source TelegramSource
		if err := rows.Scan(
			&source.ID, &source.Ref, &source.PeerKind, &source.ChatID,
			&source.AccessHash, &source.Title, &source.Username, &source.LastMessageID,
		); err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sources, nil
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

func (s *Store) TelegramMessageIDBounds(ctx context.Context, sourceID int64) (minID int, maxID int, ok bool, err error) {
	if sourceID == 0 {
		return 0, 0, false, fmt.Errorf("source id is required")
	}
	var minRaw, maxRaw sql.NullInt64
	err = s.db.QueryRowContext(ctx, `
SELECT MIN(message_id), MAX(message_id)
FROM telegram_messages
WHERE source_id = ?`, sourceID).Scan(&minRaw, &maxRaw)
	if err != nil {
		return 0, 0, false, err
	}
	if !minRaw.Valid || !maxRaw.Valid {
		return 0, 0, false, nil
	}
	return int(minRaw.Int64), int(maxRaw.Int64), true, nil
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

func (s *Store) RecentTelegramMessagesBySourceRefs(ctx context.Context, sourceRefs []string, limit int) ([]TelegramRecentMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	seen := map[string]struct{}{}
	refs := make([]string, 0, len(sourceRefs))
	for _, ref := range sourceRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(refs))
	args := make([]any, 0, len(refs)+1)
	for i, ref := range refs {
		placeholders[i] = "?"
		args = append(args, ref)
	}
	args = append(args, limit)
	query := `
SELECT s.ref, s.title, s.username, m.chat_id, m.message_id, m.date, m.kind, m.text, m.media_type, m.source_link
FROM telegram_messages m
JOIN telegram_sources s ON s.id = m.source_id
WHERE s.ref IN (` + strings.Join(placeholders, ",") + `)
ORDER BY m.date DESC, m.chat_id DESC, m.message_id DESC
LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, args...)
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

func (s *Store) TelegramMessagesCreatedBetween(ctx context.Context, start, end time.Time) ([]TelegramRecentMessage, error) {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return nil, fmt.Errorf("valid Telegram message creation window is required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT s.ref, s.title, s.username, m.chat_id, m.message_id, m.date, m.kind, m.text, m.media_type, m.source_link
FROM telegram_messages m
JOIN telegram_sources s ON s.id = m.source_id
WHERE m.created_at >= ? AND m.created_at <= ?
ORDER BY m.date, m.chat_id, m.message_id`,
		start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano))
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
		message.Date, err = time.Parse(time.RFC3339Nano, dateRaw)
		if err != nil {
			return nil, fmt.Errorf("parse telegram message date: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Store) SampleTelegramTextMessages(ctx context.Context, limit int, seed int64) ([]TelegramRecentMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT s.ref, s.title, s.username, m.chat_id, m.message_id, m.date, m.kind, m.text, m.media_type, m.source_link
FROM telegram_messages m
JOIN telegram_sources s ON s.id = m.source_id
WHERE TRIM(m.text) != '' AND m.kind != 'service'
ORDER BY ABS(((m.chat_id * 1103515245) + (m.message_id * 12345) + ?) % 2147483647),
         m.date DESC, m.chat_id, m.message_id
LIMIT ?`, seed, limit)
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
		message.Date, err = time.Parse(time.RFC3339Nano, dateRaw)
		if err != nil {
			return nil, fmt.Errorf("parse telegram message date: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Store) WorkspaceMessagesBySourceRef(ctx context.Context, sourceRef string, limit int) ([]WorkspaceSourceMessage, error) {
	sourceRef = strings.TrimSpace(sourceRef)
	if sourceRef == "" {
		return nil, fmt.Errorf("source ref is required")
	}
	query := `
SELECT s.ref, s.title, s.username, m.chat_id, m.message_id, m.date, m.kind,
       m.text, m.media_type, m.source_link, m.raw_json
FROM telegram_messages m
JOIN telegram_sources s ON s.id = m.source_id
WHERE s.ref = ?
ORDER BY m.date, m.chat_id, m.message_id`
	args := []any{sourceRef}
	if limit > 0 {
		query += "\nLIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []WorkspaceSourceMessage
	for rows.Next() {
		var message WorkspaceSourceMessage
		var dateRaw string
		if err := rows.Scan(
			&message.SourceRef, &message.SourceTitle, &message.Username,
			&message.ChatID, &message.MessageID, &dateRaw, &message.Kind,
			&message.Text, &message.MediaType, &message.SourceLink, &message.RawJSON,
		); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, dateRaw)
		if err != nil {
			return nil, fmt.Errorf("parse workspace message date: %w", err)
		}
		message.Date = parsed
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Store) UpsertWorkspaceTopics(ctx context.Context, topics []WorkspaceTopic, now time.Time) error {
	if len(topics) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, topic := range topics {
		if strings.TrimSpace(topic.SourceRef) == "" || topic.ChatID == 0 || topic.TopicID == 0 {
			return fmt.Errorf("invalid workspace topic identity")
		}
		var created any
		if !topic.CreatedAt.IsZero() {
			created = topic.CreatedAt.UTC().Format(time.RFC3339Nano)
		}
		discoveredAt := topic.DiscoveredAt
		if discoveredAt.IsZero() {
			discoveredAt = now
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO workspace_topics(
    source_ref, chat_id, topic_id, top_message_id, title, pinned, closed,
    hidden, created_at, discovered_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source_ref, topic_id) DO UPDATE SET
    chat_id = excluded.chat_id,
    top_message_id = excluded.top_message_id,
    title = excluded.title,
    pinned = excluded.pinned,
    closed = excluded.closed,
    hidden = excluded.hidden,
    created_at = excluded.created_at,
    discovered_at = excluded.discovered_at`,
			topic.SourceRef, topic.ChatID, topic.TopicID, topic.TopMessageID,
			topic.Title, boolInt(topic.Pinned), boolInt(topic.Closed),
			boolInt(topic.Hidden), created, discoveredAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) WorkspaceTopicsBySource(ctx context.Context, sourceRef string) ([]WorkspaceTopic, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT source_ref, chat_id, topic_id, top_message_id, title, pinned, closed,
       hidden, created_at, discovered_at
FROM workspace_topics
WHERE source_ref = ?
ORDER BY pinned DESC, title, topic_id`, sourceRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var topics []WorkspaceTopic
	for rows.Next() {
		topic, err := scanWorkspaceTopic(rows)
		if err != nil {
			return nil, err
		}
		topics = append(topics, topic)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return topics, nil
}

func (s *Store) StartWorkspaceAudit(ctx context.Context, sourceRef string, dryRun bool, artifactDir string, now time.Time) (WorkspaceAuditRun, error) {
	sourceRef = strings.TrimSpace(sourceRef)
	if sourceRef == "" {
		return WorkspaceAuditRun{}, fmt.Errorf("source ref is required")
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_audit_runs(source_ref, status, dry_run, started_at, artifact_dir)
VALUES (?, 'running', ?, ?, ?)`,
		sourceRef, boolInt(dryRun), now.UTC().Format(time.RFC3339Nano), artifactDir)
	if err != nil {
		return WorkspaceAuditRun{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return WorkspaceAuditRun{}, err
	}
	return WorkspaceAuditRun{
		ID: id, SourceRef: sourceRef, Status: "running", DryRun: dryRun,
		StartedAt: now.UTC(), ArtifactDir: artifactDir,
	}, nil
}

func (s *Store) FinishWorkspaceAudit(ctx context.Context, id int64, status, summary, runErr string, now time.Time) error {
	if status != "success" && status != "failed" {
		return fmt.Errorf("invalid workspace audit status %q", status)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_audit_runs
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
		return fmt.Errorf("running workspace audit %d not found", id)
	}
	return nil
}

func (s *Store) WorkspaceAuditRunByID(ctx context.Context, id int64) (WorkspaceAuditRun, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, source_ref, status, dry_run, started_at, finished_at, artifact_dir, summary, error
FROM workspace_audit_runs
WHERE id = ?`, id)
	run, err := scanWorkspaceAuditRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceAuditRun{}, false, nil
	}
	if err != nil {
		return WorkspaceAuditRun{}, false, err
	}
	return run, true, nil
}

func (s *Store) LatestSuccessfulWorkspaceAuditRun(ctx context.Context) (WorkspaceAuditRun, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, source_ref, status, dry_run, started_at, finished_at, artifact_dir, summary, error
FROM workspace_audit_runs
WHERE status = 'success' AND dry_run = 0
ORDER BY started_at DESC, id DESC
LIMIT 1`)
	run, err := scanWorkspaceAuditRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceAuditRun{}, false, nil
	}
	if err != nil {
		return WorkspaceAuditRun{}, false, err
	}
	return run, true, nil
}

func (s *Store) WorkspaceAuditRecordsByRun(ctx context.Context, runID int64) ([]WorkspaceAuditRecord, error) {
	if runID <= 0 {
		return nil, fmt.Errorf("workspace audit run id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT run_id, source_ref, chat_id, message_id, source_topic, topic_id,
       top_message_id, message_date, edit_date, message_link, short_summary,
       detected_type, model_decision, confidence, suggested_target, reason,
       media_type, pinned, long_message, edited
FROM workspace_audit_records
WHERE run_id = ?
ORDER BY message_date, chat_id, message_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []WorkspaceAuditRecord
	for rows.Next() {
		record, err := scanWorkspaceAuditRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) InsertWorkspaceAuditRecords(ctx context.Context, records []WorkspaceAuditRecord, now time.Time) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, record := range records {
		if record.RunID == 0 || strings.TrimSpace(record.SourceRef) == "" ||
			record.ChatID == 0 || record.MessageID == 0 || record.MessageDate.IsZero() {
			return fmt.Errorf("invalid workspace audit record identity")
		}
		var editDate any
		if record.EditDate != nil && !record.EditDate.IsZero() {
			editDate = record.EditDate.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO workspace_audit_records(
    run_id, source_ref, chat_id, message_id, source_topic, topic_id,
    top_message_id, message_date, edit_date, message_link, short_summary,
    detected_type, model_decision, confidence, suggested_target, reason,
    media_type, pinned, long_message, edited, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.RunID, record.SourceRef, record.ChatID, record.MessageID,
			record.SourceTopic, record.TopicID, record.TopMessageID,
			record.MessageDate.UTC().Format(time.RFC3339Nano), editDate,
			record.MessageLink, record.ShortSummary, record.DetectedType,
			record.ModelDecision, record.Confidence, record.SuggestedTarget,
			record.Reason, record.MediaType, boolInt(record.Pinned),
			boolInt(record.LongMessage), boolInt(record.Edited),
			now.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
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
INSERT OR IGNORE INTO message_decisions(
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

func (s *Store) InsertModelCall(ctx context.Context, call ModelCall, now time.Time) error {
	if call.RunID == 0 {
		return fmt.Errorf("model call run id is required")
	}
	call.Stage = strings.TrimSpace(call.Stage)
	if call.Stage == "" {
		return fmt.Errorf("model call stage is required")
	}
	if call.BatchIndex <= 0 {
		return fmt.Errorf("model call batch index must be positive")
	}
	if call.InputMessages < 0 || call.InputChars < 0 || call.DurationMillis < 0 || call.Fallbacks < 0 {
		return fmt.Errorf("model call metrics cannot be negative")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO model_calls(
    run_id, stage, batch_index, input_messages, input_chars, duration_ms,
    success, fallbacks, error, model, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		call.RunID, call.Stage, call.BatchIndex, call.InputMessages, call.InputChars,
		call.DurationMillis, boolInt(call.Success), call.Fallbacks, call.Error, call.Model,
		now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) RecentModelCalls(ctx context.Context, limit int) ([]ModelCall, error) {
	if limit <= 0 {
		limit = 40
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, stage, batch_index, input_messages, input_chars, duration_ms,
       success, fallbacks, error, model, created_at
FROM model_calls
ORDER BY created_at DESC, id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var calls []ModelCall
	for rows.Next() {
		var call ModelCall
		var success int
		var createdRaw string
		if err := rows.Scan(
			&call.ID, &call.RunID, &call.Stage, &call.BatchIndex, &call.InputMessages,
			&call.InputChars, &call.DurationMillis, &success, &call.Fallbacks,
			&call.Error, &call.Model, &createdRaw,
		); err != nil {
			return nil, err
		}
		call.Success = success == 1
		call.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
		if err != nil {
			return nil, fmt.Errorf("parse model call time: %w", err)
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return calls, nil
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

func (s *Store) PendingCalendarCandidatesByRun(ctx context.Context, runID int64) ([]CalendarCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, chat_id, message_id, source_link, title, start_at, end_at,
       timezone, location, description, confidence, status, calendar_event_id,
       error, created_at, updated_at
FROM calendar_candidates
WHERE run_id = ? AND status = 'pending'
ORDER BY id`, runID)
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

func (s *Store) UpdateCalendarCandidateTime(ctx context.Context, id int64, startAt, endAt time.Time, now time.Time) error {
	if id <= 0 {
		return fmt.Errorf("calendar candidate id is required")
	}
	if startAt.IsZero() || endAt.IsZero() || !endAt.After(startAt) {
		return fmt.Errorf("calendar candidate requires a valid start/end")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE calendar_candidates
SET start_at = ?, end_at = ?, updated_at = ?
WHERE id = ? AND status NOT IN ('created', 'rejected')`,
		startAt.UTC().Format(time.RFC3339Nano),
		endAt.UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano),
		id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("editable calendar candidate %d not found", id)
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

func scanWorkspaceTopic(scanner interface{ Scan(dest ...any) error }) (WorkspaceTopic, error) {
	var topic WorkspaceTopic
	var pinned, closed, hidden int
	var createdRaw sql.NullString
	var discoveredRaw string
	if err := scanner.Scan(
		&topic.SourceRef, &topic.ChatID, &topic.TopicID, &topic.TopMessageID,
		&topic.Title, &pinned, &closed, &hidden, &createdRaw, &discoveredRaw,
	); err != nil {
		return WorkspaceTopic{}, err
	}
	topic.Pinned = pinned == 1
	topic.Closed = closed == 1
	topic.Hidden = hidden == 1
	if createdRaw.Valid && strings.TrimSpace(createdRaw.String) != "" {
		created, err := time.Parse(time.RFC3339Nano, createdRaw.String)
		if err != nil {
			return WorkspaceTopic{}, fmt.Errorf("parse workspace topic created_at: %w", err)
		}
		topic.CreatedAt = created
	}
	discovered, err := time.Parse(time.RFC3339Nano, discoveredRaw)
	if err != nil {
		return WorkspaceTopic{}, fmt.Errorf("parse workspace topic discovered_at: %w", err)
	}
	topic.DiscoveredAt = discovered
	return topic, nil
}

func scanWorkspaceAuditRun(scanner interface{ Scan(dest ...any) error }) (WorkspaceAuditRun, error) {
	var run WorkspaceAuditRun
	var dryRun int
	var startedRaw string
	var finishedRaw sql.NullString
	if err := scanner.Scan(
		&run.ID, &run.SourceRef, &run.Status, &dryRun, &startedRaw,
		&finishedRaw, &run.ArtifactDir, &run.Summary, &run.Error,
	); err != nil {
		return WorkspaceAuditRun{}, err
	}
	run.DryRun = dryRun == 1
	started, err := time.Parse(time.RFC3339Nano, startedRaw)
	if err != nil {
		return WorkspaceAuditRun{}, fmt.Errorf("parse workspace audit started_at: %w", err)
	}
	run.StartedAt = started
	if finishedRaw.Valid && strings.TrimSpace(finishedRaw.String) != "" {
		finished, err := time.Parse(time.RFC3339Nano, finishedRaw.String)
		if err != nil {
			return WorkspaceAuditRun{}, fmt.Errorf("parse workspace audit finished_at: %w", err)
		}
		run.FinishedAt = &finished
	}
	return run, nil
}

func scanWorkspaceAuditRecord(scanner interface{ Scan(dest ...any) error }) (WorkspaceAuditRecord, error) {
	var record WorkspaceAuditRecord
	var messageDateRaw string
	var editDateRaw sql.NullString
	var pinned, longMessage, edited int
	if err := scanner.Scan(
		&record.RunID, &record.SourceRef, &record.ChatID, &record.MessageID,
		&record.SourceTopic, &record.TopicID, &record.TopMessageID,
		&messageDateRaw, &editDateRaw, &record.MessageLink, &record.ShortSummary,
		&record.DetectedType, &record.ModelDecision, &record.Confidence,
		&record.SuggestedTarget, &record.Reason, &record.MediaType,
		&pinned, &longMessage, &edited,
	); err != nil {
		return WorkspaceAuditRecord{}, err
	}
	messageDate, err := time.Parse(time.RFC3339Nano, messageDateRaw)
	if err != nil {
		return WorkspaceAuditRecord{}, fmt.Errorf("parse workspace audit message_date: %w", err)
	}
	record.MessageDate = messageDate
	if editDateRaw.Valid && strings.TrimSpace(editDateRaw.String) != "" {
		editDate, err := time.Parse(time.RFC3339Nano, editDateRaw.String)
		if err != nil {
			return WorkspaceAuditRecord{}, fmt.Errorf("parse workspace audit edit_date: %w", err)
		}
		record.EditDate = &editDate
	}
	record.Pinned = pinned == 1
	record.LongMessage = longMessage == 1
	record.Edited = edited == 1
	return record, nil
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
