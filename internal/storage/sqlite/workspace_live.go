package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

type WorkspaceMessage struct {
	ChatID           int64
	MessageID        int
	TopicID          int
	FromUserID       int64
	FromIsBot        bool
	Date             time.Time
	EditDate         *time.Time
	Text             string
	Caption          string
	MediaType        string
	Forwarded        bool
	ForwardChatID    int64
	ForwardMessageID int
	ReplyToMessageID int
	SourceLink       string
}

type WorkspaceCluster struct {
	ID        int64
	ChatID    int64
	TopicID   int
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type WorkspaceClusterMessage struct {
	ClusterID  int64
	Message    WorkspaceMessage
	Position   int
	Role       string
	AttachedAt time.Time
}

type WorkspaceClusterTail struct {
	Cluster WorkspaceCluster
	Message WorkspaceMessage
	Role    string
}

type WorkspaceTask struct {
	ID              int64
	SourceChatID    int64
	SourceMessageID int
	SourceLink      string
	SourceClusterID int64
	CardChatID      int64
	CardTopicID     int
	CardMessageID   int
	Text            string
	Emoji           string
	Status          string
	DeferredUntil   *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
	CancelledAt     *time.Time
}

type WorkspaceDerivedMessage struct {
	ID               int64
	SourceChatID     int64
	SourceMessageID  int
	SourceClusterID  int64
	DerivedType      string
	DerivedChatID    int64
	DerivedTopicID   int
	DerivedMessageID int
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (s *Store) UpsertWorkspaceMessage(ctx context.Context, message WorkspaceMessage, now time.Time) error {
	if message.ChatID == 0 || message.MessageID == 0 {
		return fmt.Errorf("invalid workspace message identity")
	}
	if message.Date.IsZero() {
		message.Date = now
	}
	var editDate any
	if message.EditDate != nil && !message.EditDate.IsZero() {
		editDate = message.EditDate.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_messages(
    chat_id, message_id, topic_id, from_user_id, from_is_bot, date, edit_date,
    text, caption, media_type, forwarded, forward_chat_id, forward_message_id,
    reply_to_message_id, source_link, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_id, message_id) DO UPDATE SET
    topic_id = excluded.topic_id,
    from_user_id = excluded.from_user_id,
    from_is_bot = excluded.from_is_bot,
    date = excluded.date,
    edit_date = excluded.edit_date,
    text = excluded.text,
    caption = excluded.caption,
    media_type = excluded.media_type,
    forwarded = excluded.forwarded,
    forward_chat_id = excluded.forward_chat_id,
    forward_message_id = excluded.forward_message_id,
    reply_to_message_id = excluded.reply_to_message_id,
    source_link = excluded.source_link,
    updated_at = excluded.updated_at`,
		message.ChatID, message.MessageID, message.TopicID, message.FromUserID,
		boolInt(message.FromIsBot), message.Date.UTC().Format(time.RFC3339Nano),
		editDate, message.Text, message.Caption, message.MediaType,
		boolInt(message.Forwarded), message.ForwardChatID, message.ForwardMessageID,
		message.ReplyToMessageID, message.SourceLink,
		now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) WorkspaceMessageByID(ctx context.Context, chatID int64, messageID int) (WorkspaceMessage, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT chat_id, message_id, topic_id, from_user_id, from_is_bot, date, edit_date,
       text, caption, media_type, forwarded, forward_chat_id, forward_message_id,
       reply_to_message_id, source_link
FROM workspace_messages
WHERE chat_id = ? AND message_id = ?`, chatID, messageID)
	message, err := scanWorkspaceMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceMessage{}, false, nil
	}
	if err != nil {
		return WorkspaceMessage{}, false, err
	}
	return message, true, nil
}

func (s *Store) LatestWorkspaceMessageInTopic(ctx context.Context, chatID int64, topicID int, fromUserID int64, beforeMessageID int) (WorkspaceMessage, bool, error) {
	if chatID == 0 || topicID == 0 {
		return WorkspaceMessage{}, false, fmt.Errorf("workspace message topic identity is required")
	}
	clauses := []string{"chat_id = ?", "topic_id = ?", "from_is_bot = 0", "LTRIM(text) NOT LIKE '/%'"}
	args := []any{chatID, topicID}
	if fromUserID != 0 {
		clauses = append(clauses, "from_user_id = ?")
		args = append(args, fromUserID)
	}
	if beforeMessageID > 0 {
		clauses = append(clauses, "message_id < ?")
		args = append(args, beforeMessageID)
	}
	row := s.db.QueryRowContext(ctx, `
SELECT chat_id, message_id, topic_id, from_user_id, from_is_bot, date, edit_date,
       text, caption, media_type, forwarded, forward_chat_id, forward_message_id,
       reply_to_message_id, source_link
FROM workspace_messages
WHERE `+strings.Join(clauses, " AND ")+`
ORDER BY message_id DESC
LIMIT 1`, args...)
	message, err := scanWorkspaceMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceMessage{}, false, nil
	}
	if err != nil {
		return WorkspaceMessage{}, false, err
	}
	return message, true, nil
}

func (s *Store) CreateWorkspaceCluster(ctx context.Context, chatID int64, topicID int, now time.Time) (WorkspaceCluster, error) {
	if chatID == 0 {
		return WorkspaceCluster{}, fmt.Errorf("workspace cluster chat id is required")
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_clusters(chat_id, topic_id, status, created_at, updated_at)
VALUES (?, ?, 'active', ?, ?)`,
		chatID, topicID, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return WorkspaceCluster{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return WorkspaceCluster{}, err
	}
	return WorkspaceCluster{ID: id, ChatID: chatID, TopicID: topicID, Status: "active", CreatedAt: now.UTC(), UpdatedAt: now.UTC()}, nil
}

func (s *Store) CreateWorkspaceClusterWithMessage(ctx context.Context, message WorkspaceMessage, role string, now time.Time) (WorkspaceCluster, error) {
	cluster, err := s.CreateWorkspaceCluster(ctx, message.ChatID, message.TopicID, now)
	if err != nil {
		return WorkspaceCluster{}, err
	}
	if err := s.AddWorkspaceMessageToCluster(ctx, cluster.ID, message.ChatID, message.MessageID, role, now); err != nil {
		return WorkspaceCluster{}, err
	}
	return cluster, nil
}

func (s *Store) AddWorkspaceMessageToCluster(ctx context.Context, clusterID int64, chatID int64, messageID int, role string, now time.Time) error {
	if clusterID <= 0 || chatID == 0 || messageID == 0 {
		return fmt.Errorf("invalid workspace cluster attachment")
	}
	role = normalizeClusterRole(role)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO workspace_cluster_messages(cluster_id, chat_id, message_id, position, role, attached_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_id, message_id) DO UPDATE SET
    cluster_id = excluded.cluster_id,
    position = excluded.position,
    role = excluded.role,
    attached_at = excluded.attached_at`,
		clusterID, chatID, messageID, messageID, role, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE workspace_clusters
SET updated_at = ?
WHERE id = ?`, now.UTC().Format(time.RFC3339Nano), clusterID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) WorkspaceClusterByID(ctx context.Context, id int64) (WorkspaceCluster, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, chat_id, topic_id, status, created_at, updated_at
FROM workspace_clusters
WHERE id = ?`, id)
	cluster, err := scanWorkspaceCluster(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceCluster{}, false, nil
	}
	if err != nil {
		return WorkspaceCluster{}, false, err
	}
	return cluster, true, nil
}

func (s *Store) WorkspaceClusterByMessage(ctx context.Context, chatID int64, messageID int) (WorkspaceCluster, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT c.id, c.chat_id, c.topic_id, c.status, c.created_at, c.updated_at
FROM workspace_clusters c
JOIN workspace_cluster_messages cm ON cm.cluster_id = c.id
WHERE cm.chat_id = ? AND cm.message_id = ?`, chatID, messageID)
	cluster, err := scanWorkspaceCluster(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceCluster{}, false, nil
	}
	if err != nil {
		return WorkspaceCluster{}, false, err
	}
	return cluster, true, nil
}

func (s *Store) WorkspaceClusterMessages(ctx context.Context, clusterID int64) ([]WorkspaceClusterMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT cm.cluster_id, cm.position, cm.role, cm.attached_at,
       m.chat_id, m.message_id, m.topic_id, m.from_user_id, m.from_is_bot,
       m.date, m.edit_date, m.text, m.caption, m.media_type, m.forwarded,
       m.forward_chat_id, m.forward_message_id, m.reply_to_message_id, m.source_link
FROM workspace_cluster_messages cm
JOIN workspace_messages m ON m.chat_id = cm.chat_id AND m.message_id = cm.message_id
WHERE cm.cluster_id = ?
ORDER BY cm.position, cm.message_id`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []WorkspaceClusterMessage
	for rows.Next() {
		item, err := scanWorkspaceClusterMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Store) LatestWorkspaceClusterTail(ctx context.Context, chatID int64, topicID int, fromUserID int64) (WorkspaceClusterTail, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT c.id, c.chat_id, c.topic_id, c.status, c.created_at, c.updated_at,
       cm.role,
       m.chat_id, m.message_id, m.topic_id, m.from_user_id, m.from_is_bot,
       m.date, m.edit_date, m.text, m.caption, m.media_type, m.forwarded,
       m.forward_chat_id, m.forward_message_id, m.reply_to_message_id, m.source_link
FROM workspace_clusters c
JOIN workspace_cluster_messages cm ON cm.cluster_id = c.id
JOIN workspace_messages m ON m.chat_id = cm.chat_id AND m.message_id = cm.message_id
WHERE c.chat_id = ? AND c.topic_id = ? AND c.status = 'active' AND m.from_user_id = ?
ORDER BY m.message_id DESC
LIMIT 1`, chatID, topicID, fromUserID)
	tail, err := scanWorkspaceClusterTail(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceClusterTail{}, false, nil
	}
	if err != nil {
		return WorkspaceClusterTail{}, false, err
	}
	return tail, true, nil
}

func (s *Store) AttachWorkspaceMessagesToCluster(ctx context.Context, clusterID int64, chatID int64, messageIDs []int, now time.Time) error {
	if len(messageIDs) == 0 {
		return nil
	}
	for _, messageID := range uniquePositiveInts(messageIDs) {
		if _, ok, err := s.WorkspaceMessageByID(ctx, chatID, messageID); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("workspace message %d is not tracked; bot messages and command messages cannot be attached", messageID)
		}
		if err := s.AddWorkspaceMessageToCluster(ctx, clusterID, chatID, messageID, "manual", now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) MergeWorkspaceClusters(ctx context.Context, targetClusterID int64, sourceClusterIDs []int64, now time.Time) error {
	if targetClusterID <= 0 {
		return fmt.Errorf("target cluster id is required")
	}
	seen := map[int64]struct{}{targetClusterID: {}}
	var ids []int64
	for _, id := range sourceClusterIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		messages, err := s.WorkspaceClusterMessages(ctx, id)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if err := s.AddWorkspaceMessageToCluster(ctx, targetClusterID, message.Message.ChatID, message.Message.MessageID, "manual", now); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM workspace_clusters WHERE id = ?`, id); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE workspace_clusters
SET updated_at = ?
WHERE id = ?`, now.UTC().Format(time.RFC3339Nano), targetClusterID)
	return err
}

func (s *Store) DetachWorkspaceMessages(ctx context.Context, chatID int64, messageIDs []int, now time.Time) ([]WorkspaceCluster, error) {
	var clusters []WorkspaceCluster
	for _, messageID := range uniquePositiveInts(messageIDs) {
		message, ok, err := s.WorkspaceMessageByID(ctx, chatID, messageID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("workspace message %d not found", messageID)
		}
		cluster, err := s.CreateWorkspaceClusterWithMessage(ctx, message, "manual", now)
		if err != nil {
			return nil, err
		}
		clusters = append(clusters, cluster)
	}
	return clusters, nil
}

func (s *Store) CreateWorkspaceTask(ctx context.Context, task WorkspaceTask, now time.Time) (WorkspaceTask, error) {
	if task.SourceChatID == 0 || task.SourceMessageID == 0 {
		return WorkspaceTask{}, fmt.Errorf("task source message identity is required")
	}
	task.Text = strings.TrimSpace(task.Text)
	if task.Text == "" {
		return WorkspaceTask{}, fmt.Errorf("task text is required")
	}
	if task.Status == "" {
		task.Status = "open"
	}
	if task.Status != "open" && task.Status != "deferred" {
		return WorkspaceTask{}, fmt.Errorf("new task status must be open or deferred")
	}
	var deferred any
	if task.DeferredUntil != nil && !task.DeferredUntil.IsZero() {
		deferred = task.DeferredUntil.UTC().Format(time.RFC3339Nano)
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_tasks(
    source_chat_id, source_message_id, source_link, source_cluster_id,
    card_chat_id, card_topic_id, card_message_id, text, emoji, status,
    deferred_until, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.SourceChatID, task.SourceMessageID, task.SourceLink, task.SourceClusterID,
		task.CardChatID, task.CardTopicID, task.CardMessageID, task.Text, task.Emoji,
		task.Status, deferred, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return WorkspaceTask{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return WorkspaceTask{}, err
	}
	return s.WorkspaceTaskByID(ctx, id)
}

func (s *Store) SetWorkspaceTaskCard(ctx context.Context, id int64, chatID int64, topicID int, messageID int, now time.Time) error {
	if id <= 0 || chatID == 0 || messageID == 0 {
		return fmt.Errorf("invalid task card identity")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_tasks
SET card_chat_id = ?, card_topic_id = ?, card_message_id = ?, updated_at = ?
WHERE id = ?`,
		chatID, topicID, messageID, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "workspace task %d not found", id)
}

func (s *Store) WorkspaceTaskByID(ctx context.Context, id int64) (WorkspaceTask, error) {
	row := s.db.QueryRowContext(ctx, workspaceTaskSelect()+` WHERE id = ?`, id)
	return scanWorkspaceTask(row)
}

func (s *Store) WorkspaceTasksBySource(ctx context.Context, chatID int64, messageID int) ([]WorkspaceTask, error) {
	rows, err := s.db.QueryContext(ctx, workspaceTaskSelect()+`
WHERE source_chat_id = ? AND source_message_id = ?
ORDER BY id`, chatID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []WorkspaceTask
	for rows.Next() {
		task, err := scanWorkspaceTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *Store) OpenWorkspaceTasks(ctx context.Context, limit int) ([]WorkspaceTask, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, workspaceTaskSelect()+`
WHERE status IN ('open', 'deferred')
ORDER BY COALESCE(deferred_until, updated_at), id
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []WorkspaceTask
	for rows.Next() {
		task, err := scanWorkspaceTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *Store) DeferredWorkspaceTasks(ctx context.Context, limit int) ([]WorkspaceTask, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, workspaceTaskSelect()+`
WHERE status = 'deferred'
ORDER BY deferred_until IS NULL, deferred_until, updated_at, id
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []WorkspaceTask
	for rows.Next() {
		task, err := scanWorkspaceTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *Store) WorkspaceTasksContaining(ctx context.Context, terms []string, limit int) ([]WorkspaceTask, error) {
	if limit <= 0 {
		limit = 100
	}
	var normalized []string
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term != "" {
			normalized = append(normalized, workspaceTaskSearchVariants(term)...)
		}
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("at least one search term is required")
	}
	clauses := make([]string, 0, len(normalized))
	args := make([]any, 0, len(normalized)+1)
	for _, term := range normalized {
		clauses = append(clauses, "text LIKE ?")
		args = append(args, "%"+term+"%")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, workspaceTaskSelect()+`
WHERE (`+strings.Join(clauses, " OR ")+`)
ORDER BY id
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []WorkspaceTask
	for rows.Next() {
		task, err := scanWorkspaceTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

func workspaceTaskSearchVariants(term string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, variant := range []string{
		term,
		strings.ToLower(term),
		strings.ToUpper(term),
		titleFirstRune(term),
	} {
		variant = strings.TrimSpace(variant)
		if variant == "" {
			continue
		}
		if _, ok := seen[variant]; ok {
			continue
		}
		seen[variant] = struct{}{}
		out = append(out, variant)
	}
	return out
}

func titleFirstRune(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func (s *Store) UpdateWorkspaceTaskText(ctx context.Context, id int64, text, emoji string, now time.Time) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("task text is required")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_tasks
SET text = ?, emoji = ?, updated_at = ?
WHERE id = ? AND status IN ('open', 'deferred')`,
		text, emoji, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "open workspace task %d not found", id)
}

func (s *Store) UpdateWorkspaceTaskStatus(ctx context.Context, id int64, status string, deferredUntil *time.Time, now time.Time) error {
	if status != "open" && status != "done" && status != "cancelled" && status != "deferred" {
		return fmt.Errorf("invalid workspace task status %q", status)
	}
	var deferred, completed, cancelled any
	if deferredUntil != nil && !deferredUntil.IsZero() {
		deferred = deferredUntil.UTC().Format(time.RFC3339Nano)
	}
	if status == "done" {
		completed = now.UTC().Format(time.RFC3339Nano)
	}
	if status == "cancelled" {
		cancelled = now.UTC().Format(time.RFC3339Nano)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_tasks
SET status = ?, deferred_until = ?, completed_at = COALESCE(?, completed_at),
    cancelled_at = COALESCE(?, cancelled_at), updated_at = ?
WHERE id = ?`,
		status, deferred, completed, cancelled, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return requireOneRow(result, "workspace task %d not found", id)
}

func (s *Store) UpsertWorkspaceDerivedMessage(ctx context.Context, derived WorkspaceDerivedMessage, now time.Time) error {
	if derived.SourceChatID == 0 || derived.SourceMessageID == 0 ||
		derived.DerivedChatID == 0 || derived.DerivedMessageID == 0 ||
		strings.TrimSpace(derived.DerivedType) == "" {
		return fmt.Errorf("invalid workspace derived message identity")
	}
	if derived.Status == "" {
		derived.Status = "active"
	}
	if derived.Status != "active" && derived.Status != "published" && derived.Status != "needs_review" && derived.Status != "closed" {
		return fmt.Errorf("invalid workspace derived status %q", derived.Status)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_derived_messages(
    source_chat_id, source_message_id, source_cluster_id, derived_type,
    derived_chat_id, derived_topic_id, derived_message_id, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(derived_chat_id, derived_message_id, derived_type) DO UPDATE SET
    source_chat_id = excluded.source_chat_id,
    source_message_id = excluded.source_message_id,
    source_cluster_id = excluded.source_cluster_id,
    derived_topic_id = excluded.derived_topic_id,
    status = excluded.status,
    updated_at = excluded.updated_at`,
		derived.SourceChatID, derived.SourceMessageID, derived.SourceClusterID,
		strings.TrimSpace(derived.DerivedType), derived.DerivedChatID, derived.DerivedTopicID,
		derived.DerivedMessageID, derived.Status, now.UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) WorkspaceDerivedMessagesBySource(ctx context.Context, chatID int64, messageID int, derivedTypePrefix string, statuses []string, limit int) ([]WorkspaceDerivedMessage, error) {
	if chatID == 0 || messageID == 0 {
		return nil, fmt.Errorf("workspace derived source identity is required")
	}
	if limit <= 0 {
		limit = 100
	}
	clauses := []string{"source_chat_id = ?", "source_message_id = ?"}
	args := []any{chatID, messageID}
	derivedTypePrefix = strings.TrimSpace(derivedTypePrefix)
	if derivedTypePrefix != "" {
		clauses = append(clauses, "derived_type LIKE ?")
		args = append(args, derivedTypePrefix+"%")
	}
	var normalized []string
	for _, status := range statuses {
		status = strings.TrimSpace(status)
		if status != "" {
			normalized = append(normalized, status)
		}
	}
	if len(normalized) > 0 {
		placeholders := make([]string, 0, len(normalized))
		for _, status := range normalized {
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		clauses = append(clauses, "status IN ("+strings.Join(placeholders, ", ")+")")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, source_chat_id, source_message_id, source_cluster_id, derived_type,
       derived_chat_id, derived_topic_id, derived_message_id, status, created_at, updated_at
FROM workspace_derived_messages
WHERE `+strings.Join(clauses, " AND ")+`
ORDER BY updated_at DESC, id DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []WorkspaceDerivedMessage
	for rows.Next() {
		message, err := scanWorkspaceDerivedMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Store) MarkWorkspacePublishedSourceNeedsReview(ctx context.Context, chatID int64, messageID int, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE workspace_derived_messages
SET status = 'needs_review', updated_at = ?
WHERE source_chat_id = ? AND source_message_id = ? AND status = 'published'`,
		now.UTC().Format(time.RFC3339Nano), chatID, messageID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) UpsertWorkspaceTopicIndex(ctx context.Context, chatID int64, topicID int, key string, messageID int, now time.Time) error {
	key = strings.TrimSpace(key)
	if chatID == 0 || topicID == 0 || key == "" || messageID == 0 {
		return fmt.Errorf("invalid workspace topic index identity")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO workspace_topic_indexes(chat_id, topic_id, index_key, message_id, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(chat_id, topic_id, index_key) DO UPDATE SET
    message_id = excluded.message_id,
    updated_at = excluded.updated_at`,
		chatID, topicID, key, messageID, now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) DeleteWorkspaceTopicIndex(ctx context.Context, chatID int64, topicID int, key string) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM workspace_topic_indexes
WHERE chat_id = ? AND topic_id = ? AND index_key = ?`, chatID, topicID, strings.TrimSpace(key))
	return err
}

func (s *Store) WorkspaceTopicIndexMessage(ctx context.Context, chatID int64, topicID int, key string) (int, bool, error) {
	var messageID int
	err := s.db.QueryRowContext(ctx, `
SELECT message_id
FROM workspace_topic_indexes
WHERE chat_id = ? AND topic_id = ? AND index_key = ?`, chatID, topicID, strings.TrimSpace(key)).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return messageID, true, nil
}

func normalizeClusterRole(role string) string {
	switch strings.TrimSpace(role) {
	case "primary", "manual":
		return strings.TrimSpace(role)
	default:
		return "part"
	}
}

func uniquePositiveInts(values []int) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func requireOneRow(result sql.Result, format string, args ...any) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf(format, args...)
	}
	return nil
}

type workspaceMessageScanner interface {
	Scan(dest ...any) error
}

func scanWorkspaceMessage(scanner workspaceMessageScanner) (WorkspaceMessage, error) {
	var message WorkspaceMessage
	var fromIsBot, forwarded int
	var dateRaw string
	var editRaw sql.NullString
	if err := scanner.Scan(
		&message.ChatID, &message.MessageID, &message.TopicID, &message.FromUserID,
		&fromIsBot, &dateRaw, &editRaw, &message.Text, &message.Caption,
		&message.MediaType, &forwarded, &message.ForwardChatID,
		&message.ForwardMessageID, &message.ReplyToMessageID, &message.SourceLink,
	); err != nil {
		return WorkspaceMessage{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, dateRaw)
	if err != nil {
		return WorkspaceMessage{}, fmt.Errorf("parse workspace message date: %w", err)
	}
	message.Date = parsed
	if editRaw.Valid {
		editDate, err := time.Parse(time.RFC3339Nano, editRaw.String)
		if err != nil {
			return WorkspaceMessage{}, fmt.Errorf("parse workspace message edit date: %w", err)
		}
		message.EditDate = &editDate
	}
	message.FromIsBot = fromIsBot == 1
	message.Forwarded = forwarded == 1
	return message, nil
}

func scanWorkspaceCluster(scanner interface{ Scan(dest ...any) error }) (WorkspaceCluster, error) {
	var cluster WorkspaceCluster
	var createdRaw, updatedRaw string
	if err := scanner.Scan(&cluster.ID, &cluster.ChatID, &cluster.TopicID, &cluster.Status, &createdRaw, &updatedRaw); err != nil {
		return WorkspaceCluster{}, err
	}
	var err error
	cluster.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return WorkspaceCluster{}, fmt.Errorf("parse workspace cluster created_at: %w", err)
	}
	cluster.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return WorkspaceCluster{}, fmt.Errorf("parse workspace cluster updated_at: %w", err)
	}
	return cluster, nil
}

func scanWorkspaceClusterMessage(scanner interface{ Scan(dest ...any) error }) (WorkspaceClusterMessage, error) {
	var item WorkspaceClusterMessage
	var attachedRaw string
	var fromIsBot, forwarded int
	var dateRaw string
	var editRaw sql.NullString
	if err := scanner.Scan(
		&item.ClusterID, &item.Position, &item.Role, &attachedRaw,
		&item.Message.ChatID, &item.Message.MessageID, &item.Message.TopicID,
		&item.Message.FromUserID, &fromIsBot, &dateRaw, &editRaw,
		&item.Message.Text, &item.Message.Caption, &item.Message.MediaType,
		&forwarded, &item.Message.ForwardChatID, &item.Message.ForwardMessageID,
		&item.Message.ReplyToMessageID, &item.Message.SourceLink,
	); err != nil {
		return WorkspaceClusterMessage{}, err
	}
	var err error
	item.AttachedAt, err = time.Parse(time.RFC3339Nano, attachedRaw)
	if err != nil {
		return WorkspaceClusterMessage{}, fmt.Errorf("parse workspace cluster attachment: %w", err)
	}
	item.Message.Date, err = time.Parse(time.RFC3339Nano, dateRaw)
	if err != nil {
		return WorkspaceClusterMessage{}, fmt.Errorf("parse workspace message date: %w", err)
	}
	if editRaw.Valid {
		editDate, err := time.Parse(time.RFC3339Nano, editRaw.String)
		if err != nil {
			return WorkspaceClusterMessage{}, fmt.Errorf("parse workspace message edit date: %w", err)
		}
		item.Message.EditDate = &editDate
	}
	item.Message.FromIsBot = fromIsBot == 1
	item.Message.Forwarded = forwarded == 1
	return item, nil
}

func scanWorkspaceClusterTail(scanner interface{ Scan(dest ...any) error }) (WorkspaceClusterTail, error) {
	var tail WorkspaceClusterTail
	var createdRaw, updatedRaw, dateRaw string
	var editRaw sql.NullString
	var fromIsBot, forwarded int
	if err := scanner.Scan(
		&tail.Cluster.ID, &tail.Cluster.ChatID, &tail.Cluster.TopicID,
		&tail.Cluster.Status, &createdRaw, &updatedRaw, &tail.Role,
		&tail.Message.ChatID, &tail.Message.MessageID, &tail.Message.TopicID,
		&tail.Message.FromUserID, &fromIsBot, &dateRaw, &editRaw,
		&tail.Message.Text, &tail.Message.Caption, &tail.Message.MediaType,
		&forwarded, &tail.Message.ForwardChatID, &tail.Message.ForwardMessageID,
		&tail.Message.ReplyToMessageID, &tail.Message.SourceLink,
	); err != nil {
		return WorkspaceClusterTail{}, err
	}
	var err error
	tail.Cluster.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return WorkspaceClusterTail{}, fmt.Errorf("parse workspace cluster created_at: %w", err)
	}
	tail.Cluster.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return WorkspaceClusterTail{}, fmt.Errorf("parse workspace cluster updated_at: %w", err)
	}
	tail.Message.Date, err = time.Parse(time.RFC3339Nano, dateRaw)
	if err != nil {
		return WorkspaceClusterTail{}, fmt.Errorf("parse workspace message date: %w", err)
	}
	if editRaw.Valid {
		editDate, err := time.Parse(time.RFC3339Nano, editRaw.String)
		if err != nil {
			return WorkspaceClusterTail{}, fmt.Errorf("parse workspace message edit date: %w", err)
		}
		tail.Message.EditDate = &editDate
	}
	tail.Message.FromIsBot = fromIsBot == 1
	tail.Message.Forwarded = forwarded == 1
	return tail, nil
}

func workspaceTaskSelect() string {
	return `SELECT id, source_chat_id, source_message_id, source_link, source_cluster_id,
       card_chat_id, card_topic_id, card_message_id, text, emoji, status,
       deferred_until, created_at, updated_at, completed_at, cancelled_at
FROM workspace_tasks`
}

func scanWorkspaceTask(scanner interface{ Scan(dest ...any) error }) (WorkspaceTask, error) {
	var task WorkspaceTask
	var deferredRaw, completedRaw, cancelledRaw sql.NullString
	var createdRaw, updatedRaw string
	if err := scanner.Scan(
		&task.ID, &task.SourceChatID, &task.SourceMessageID, &task.SourceLink,
		&task.SourceClusterID, &task.CardChatID, &task.CardTopicID,
		&task.CardMessageID, &task.Text, &task.Emoji, &task.Status,
		&deferredRaw, &createdRaw, &updatedRaw, &completedRaw, &cancelledRaw,
	); err != nil {
		return WorkspaceTask{}, err
	}
	var err error
	task.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return WorkspaceTask{}, fmt.Errorf("parse workspace task created_at: %w", err)
	}
	task.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return WorkspaceTask{}, fmt.Errorf("parse workspace task updated_at: %w", err)
	}
	if deferredRaw.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, deferredRaw.String)
		if err != nil {
			return WorkspaceTask{}, fmt.Errorf("parse workspace task deferred_until: %w", err)
		}
		task.DeferredUntil = &parsed
	}
	if completedRaw.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, completedRaw.String)
		if err != nil {
			return WorkspaceTask{}, fmt.Errorf("parse workspace task completed_at: %w", err)
		}
		task.CompletedAt = &parsed
	}
	if cancelledRaw.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, cancelledRaw.String)
		if err != nil {
			return WorkspaceTask{}, fmt.Errorf("parse workspace task cancelled_at: %w", err)
		}
		task.CancelledAt = &parsed
	}
	return task, nil
}

func scanWorkspaceDerivedMessage(scanner interface{ Scan(dest ...any) error }) (WorkspaceDerivedMessage, error) {
	var message WorkspaceDerivedMessage
	var createdRaw, updatedRaw string
	if err := scanner.Scan(
		&message.ID, &message.SourceChatID, &message.SourceMessageID,
		&message.SourceClusterID, &message.DerivedType, &message.DerivedChatID,
		&message.DerivedTopicID, &message.DerivedMessageID, &message.Status,
		&createdRaw, &updatedRaw,
	); err != nil {
		return WorkspaceDerivedMessage{}, err
	}
	var err error
	message.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return WorkspaceDerivedMessage{}, fmt.Errorf("parse workspace derived created_at: %w", err)
	}
	message.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return WorkspaceDerivedMessage{}, fmt.Errorf("parse workspace derived updated_at: %w", err)
	}
	return message, nil
}
