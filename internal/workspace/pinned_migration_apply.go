package workspace

import (
	"context"
	"encoding/csv"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type PinnedMigrationApplyOptions struct {
	ReviewCSVPath string
	OutputDir     string
	Execute       bool
	Now           time.Time
}

type PinnedMigrationApplyResult struct {
	RunID       string
	ArtifactDir string
	PlanMD      string
	PlanCSV     string
	Rows        int
	Pending     int
	Archived    int
	Planned     int
	Executed    int
	Errors      int
	DryRun      bool
}

type pinnedMigrationReviewRow struct {
	LegacyTopic          string
	SourceMessageLink    string
	SourceMessageIDsRaw  string
	SourceMessageIDs     []int
	ClusterID            string
	ShortTitle           string
	ShortSummary         string
	DetectedType         string
	SuggestedTargetTopic string
	Confidence           string
	Reason               string
	NeedsUserReview      string
	ReviewStatus         string
	UserComment          string
}

type pinnedMigrationDecision struct {
	Action string
	Target string
}

type pinnedMigrationApplyItem struct {
	RowIndex     int
	SourceIDs    []int
	ShortTitle   string
	UserComment  string
	Action       string
	Target       string
	Status       string
	DerivedLinks []string
	Error        string
}

func ApplyPinnedMigrationReview(ctx context.Context, cfg config.Config, store *sqlitestore.Store, opts PinnedMigrationApplyOptions) (PinnedMigrationApplyResult, error) {
	if store == nil {
		return PinnedMigrationApplyResult{}, fmt.Errorf("store is required")
	}
	reviewCSV := strings.TrimSpace(opts.ReviewCSVPath)
	if reviewCSV == "" {
		return PinnedMigrationApplyResult{}, fmt.Errorf("--review-csv is required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	source, err := ResolveLegacySource(ctx, cfg, store)
	if err != nil {
		return PinnedMigrationApplyResult{}, err
	}
	sourceMessages, err := store.WorkspaceMessagesBySourceRef(ctx, source.Ref, 0)
	if err != nil {
		return PinnedMigrationApplyResult{}, err
	}
	byID := map[int]sqlitestore.WorkspaceSourceMessage{}
	for _, message := range sourceMessages {
		byID[message.MessageID] = message
	}
	rows, err := readPinnedMigrationReviewRows(reviewCSV)
	if err != nil {
		return PinnedMigrationApplyResult{}, err
	}
	runID := now.UTC().Format("20060102T150405Z")
	outputDir := strings.TrimSpace(opts.OutputDir)
	if outputDir == "" {
		outputDir = filepath.Join(cfg.StateDir, "artifacts", "workspace", "migration_apply", runID)
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return PinnedMigrationApplyResult{}, err
	}
	client := nest.New(cfg.Workspace.BotToken)
	var items []pinnedMigrationApplyItem
	result := PinnedMigrationApplyResult{
		RunID:       runID,
		ArtifactDir: outputDir,
		PlanMD:      filepath.Join(outputDir, "pinned_migration_apply.md"),
		PlanCSV:     filepath.Join(outputDir, "pinned_migration_apply.csv"),
		Rows:        len(rows),
		DryRun:      !opts.Execute,
	}
	for i, row := range rows {
		item := pinnedMigrationApplyItem{
			RowIndex:    i + 2,
			SourceIDs:   row.SourceMessageIDs,
			ShortTitle:  row.ShortTitle,
			UserComment: row.UserComment,
		}
		decision := parsePinnedMigrationDecision(row.UserComment, row.SuggestedTargetTopic)
		item.Action = decision.Action
		item.Target = decision.Target
		switch decision.Action {
		case "pending":
			item.Status = "pending"
			result.Pending++
		case "archive":
			item.Status = "archived"
			result.Archived++
		case "migrate", "publish":
			messages, missing := migrationSourceMessages(row.SourceMessageIDs, byID)
			if len(missing) > 0 {
				item.Status = "error"
				item.Error = "missing source messages: " + joinInts(missing, "+")
				result.Errors++
				break
			}
			if !opts.Execute {
				item.Status = "planned"
				result.Planned++
				break
			}
			links, err := executePinnedMigrationItem(ctx, cfg, store, client, row, decision, messages, now)
			if err != nil {
				item.Status = "error"
				item.Error = err.Error()
				result.Errors++
				break
			}
			item.DerivedLinks = links
			item.Status = "executed"
			result.Executed++
		default:
			item.Status = "error"
			item.Error = "unknown user_comment decision"
			result.Errors++
		}
		items = append(items, item)
	}
	if err := writePinnedMigrationApplyCSV(result.PlanCSV, items); err != nil {
		return PinnedMigrationApplyResult{}, err
	}
	if err := os.WriteFile(result.PlanMD, []byte(renderPinnedMigrationApplyMarkdown(now, reviewCSV, result, items)), 0o600); err != nil {
		return PinnedMigrationApplyResult{}, err
	}
	return result, nil
}

func readPinnedMigrationReviewRows(path string) ([]pinnedMigrationReviewRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open review csv %s: %w", path, err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	rawRows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read review csv %s: %w", path, err)
	}
	if len(rawRows) == 0 {
		return nil, fmt.Errorf("review csv %s is empty", path)
	}
	header := map[string]int{}
	for i, name := range rawRows[0] {
		header[strings.TrimSpace(name)] = i
	}
	required := []string{"legacy_topic", "source_message_link", "source_message_ids", "cluster_id", "short_title", "short_summary", "detected_type", "suggested_target_topic", "confidence", "reason", "needs_user_review", "review_status", "user_comment"}
	for _, name := range required {
		if _, ok := header[name]; !ok {
			return nil, fmt.Errorf("review csv %s is missing column %q", path, name)
		}
	}
	rows := make([]pinnedMigrationReviewRow, 0, len(rawRows)-1)
	for i, raw := range rawRows[1:] {
		row := pinnedMigrationReviewRow{
			LegacyTopic:          csvRowValue(raw, header, "legacy_topic"),
			SourceMessageLink:    csvRowValue(raw, header, "source_message_link"),
			SourceMessageIDsRaw:  csvRowValue(raw, header, "source_message_ids"),
			ClusterID:            csvRowValue(raw, header, "cluster_id"),
			ShortTitle:           csvRowValue(raw, header, "short_title"),
			ShortSummary:         csvRowValue(raw, header, "short_summary"),
			DetectedType:         csvRowValue(raw, header, "detected_type"),
			SuggestedTargetTopic: csvRowValue(raw, header, "suggested_target_topic"),
			Confidence:           csvRowValue(raw, header, "confidence"),
			Reason:               csvRowValue(raw, header, "reason"),
			NeedsUserReview:      csvRowValue(raw, header, "needs_user_review"),
			ReviewStatus:         csvRowValue(raw, header, "review_status"),
			UserComment:          csvRowValue(raw, header, "user_comment"),
		}
		ids, err := parseMigrationSourceIDs(row.SourceMessageIDsRaw)
		if err != nil {
			return nil, fmt.Errorf("review csv row %d: %w", i+2, err)
		}
		row.SourceMessageIDs = ids
		rows = append(rows, row)
	}
	return rows, nil
}

func csvRowValue(row []string, header map[string]int, name string) string {
	idx, ok := header[name]
	if !ok || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

func parseMigrationSourceIDs(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("source_message_ids is empty")
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '+' || r == ',' || r == ';' || r == ' '
	})
	out := make([]int, 0, len(fields))
	seen := map[int]struct{}{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		id, err := strconv.Atoi(field)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid source message id %q", field)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source_message_ids is empty")
	}
	return out, nil
}

func parsePinnedMigrationDecision(comment, suggested string) pinnedMigrationDecision {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return pinnedMigrationDecision{Action: "pending", Target: strings.TrimSpace(suggested)}
	}
	normalized := strings.ToLower(comment)
	normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "decision:"))
	normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "target:"))
	normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "topic:"))
	if strings.Contains(normalized, "archive") || strings.Contains(normalized, "архив") || strings.Contains(normalized, "skip") {
		return pinnedMigrationDecision{Action: "archive", Target: "Legacy archive"}
	}
	if strings.Contains(normalized, "publish") || strings.Contains(normalized, "опубликов") {
		return pinnedMigrationDecision{Action: "publish", Target: "Полезное"}
	}
	for _, target := range []string{"Inbox", "Задачи", "Заметки", "Опыт", "Полезное", "Заготовки", "Коллекции"} {
		if commentMatchesTopic(normalized, target) {
			return pinnedMigrationDecision{Action: "migrate", Target: target}
		}
	}
	return pinnedMigrationDecision{Action: "unknown", Target: strings.TrimSpace(suggested)}
}

func commentMatchesTopic(normalized, topic string) bool {
	topicNorm := strings.ToLower(topic)
	aliases := map[string][]string{
		"inbox":     {"inbox", "инбокс"},
		"задачи":    {"задачи", "tasks", "task"},
		"заметки":   {"заметки", "notes", "note", "doc"},
		"опыт":      {"опыт", "experience"},
		"полезное":  {"полезное", "useful"},
		"заготовки": {"заготовки", "templates", "template"},
		"коллекции": {"коллекции", "collections", "collection"},
	}
	for _, alias := range aliases[topicNorm] {
		if normalized == alias || strings.HasPrefix(normalized, alias+" ") || strings.HasPrefix(normalized, alias+":") {
			return true
		}
	}
	return false
}

func migrationSourceMessages(ids []int, byID map[int]sqlitestore.WorkspaceSourceMessage) ([]sqlitestore.WorkspaceSourceMessage, []int) {
	messages := make([]sqlitestore.WorkspaceSourceMessage, 0, len(ids))
	var missing []int
	for _, id := range ids {
		message, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		messages = append(messages, message)
	}
	return messages, missing
}

func executePinnedMigrationItem(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, row pinnedMigrationReviewRow, decision pinnedMigrationDecision, messages []sqlitestore.WorkspaceSourceMessage, now time.Time) ([]string, error) {
	topicID, err := pinnedMigrationTargetTopicID(cfg, decision.Target)
	if err != nil {
		return nil, err
	}
	var derivedLinks []string
	var firstTargetMessageID int
	for i, message := range messages {
		texts := renderPinnedMigrationTelegramMessages(row, decision, message, i+1, len(messages))
		for j, text := range texts {
			sent, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
				ChatID:          cfg.Workspace.ChatID,
				MessageThreadID: topicID,
				Text:            text,
				ParseMode:       "HTML",
			})
			if err != nil {
				return derivedLinks, err
			}
			if firstTargetMessageID == 0 {
				firstTargetMessageID = sent.MessageID
			}
			link := workspaceMessageLink(cfg.Workspace.ChatID, topicID, sent.MessageID)
			derivedLinks = append(derivedLinks, link)
			status := "active"
			if decision.Action == "publish" || decision.Target == "Полезное" {
				status = "published"
			}
			if err := store.UpsertWorkspaceDerivedMessage(ctx, sqlitestore.WorkspaceDerivedMessage{
				SourceChatID:     message.ChatID,
				SourceMessageID:  message.MessageID,
				DerivedType:      fmt.Sprintf("legacy_migration_%s_row_%s_part_%d_msg_%d", decision.Action, row.ClusterID, i+1, j+1),
				DerivedChatID:    cfg.Workspace.ChatID,
				DerivedTopicID:   topicID,
				DerivedMessageID: sent.MessageID,
				Status:           status,
			}, now); err != nil {
				return derivedLinks, err
			}
		}
	}
	if decision.Action == "publish" && firstTargetMessageID > 0 && len(messages) > 0 {
		if err := createPublishedLegacyDocument(ctx, cfg, store, row, messages, firstTargetMessageID, now); err != nil {
			return derivedLinks, err
		}
		if err := updateUsefulIndex(ctx, cfg, store, client, now); err != nil {
			return derivedLinks, err
		}
	}
	return derivedLinks, nil
}

func pinnedMigrationTargetTopicID(cfg config.Config, target string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "inbox":
		return cfg.Workspace.Topics.Inbox, nil
	case "задачи":
		return cfg.Workspace.Topics.Tasks, nil
	case "заметки":
		return cfg.Workspace.Topics.Notes, nil
	case "опыт":
		return cfg.Workspace.Topics.Experience, nil
	case "полезное":
		return cfg.Workspace.Topics.Useful, nil
	case "заготовки":
		return cfg.Workspace.Topics.Templates, nil
	case "коллекции":
		return cfg.Workspace.Topics.Collections, nil
	default:
		return 0, fmt.Errorf("unknown migration target topic %q", target)
	}
}

func renderPinnedMigrationTelegramMessages(row pinnedMigrationReviewRow, decision pinnedMigrationDecision, message sqlitestore.WorkspaceSourceMessage, index, total int) []string {
	title := cleanMigrationTitle(row.ShortTitle)
	if title == "" {
		title = cleanMigrationTitle(firstNonEmptyLine(message.Text))
	}
	if title == "" {
		title = "Материал " + strconv.Itoa(message.MessageID)
	}
	body := strings.TrimSpace(message.Text)
	if body == "" && strings.TrimSpace(message.MediaType) != "" {
		body = "[" + strings.TrimSpace(message.MediaType) + "]"
	}
	if decision.Action == "publish" {
		body = formatLegacyPublishBody(title, body)
	}
	sourceLink := firstNonEmpty(message.SourceLink, row.SourceMessageLink, workspaceMessageLink(message.ChatID, 0, message.MessageID))
	chunks := splitMigrationBody(body, 3200)
	out := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		var b strings.Builder
		b.WriteString("<b>")
		b.WriteString(html.EscapeString(title))
		if total > 1 {
			fmt.Fprintf(&b, " [%d/%d]", index, total)
		}
		if len(chunks) > 1 {
			fmt.Fprintf(&b, " · %d/%d", i+1, len(chunks))
		}
		b.WriteString("</b>\n\n")
		b.WriteString(html.EscapeString(strings.TrimSpace(chunk)))
		b.WriteString("\n\n<blockquote>Источник: ")
		if sourceLink != "" {
			b.WriteString("<a href=\"")
			b.WriteString(html.EscapeString(sourceLink))
			b.WriteString("\">")
			b.WriteString(html.EscapeString(strconv.Itoa(message.MessageID)))
			b.WriteString("</a>")
		} else {
			b.WriteString(html.EscapeString(strconv.Itoa(message.MessageID)))
		}
		b.WriteString("</blockquote>")
		out = append(out, b.String())
	}
	if len(out) == 0 {
		out = append(out, "<b>"+html.EscapeString(title)+"</b>\n\n<blockquote>Источник: "+html.EscapeString(sourceLink)+"</blockquote>")
	}
	return out
}

func formatLegacyPublishBody(title, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	for len(lines) > 0 && strings.EqualFold(strings.TrimSpace(lines[0]), strings.TrimSpace(title)) {
		lines = lines[1:]
	}
	body = strings.TrimSpace(strings.Join(lines, "\n"))
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	paragraphs := strings.Split(body, "\n\n")
	for i, paragraph := range paragraphs {
		paragraphs[i] = strings.Join(strings.Fields(strings.TrimSpace(paragraph)), " ")
	}
	return strings.TrimSpace(strings.Join(nonEmptyStrings(paragraphs), "\n\n"))
}

func splitMigrationBody(body string, limit int) []string {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	if len([]rune(body)) <= limit {
		return []string{body}
	}
	return nest.SplitMessageText(body, limit)
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func createPublishedLegacyDocument(ctx context.Context, cfg config.Config, store *sqlitestore.Store, row pinnedMigrationReviewRow, messages []sqlitestore.WorkspaceSourceMessage, targetMessageID int, now time.Time) error {
	first := messages[0]
	title := cleanMigrationTitle(row.ShortTitle)
	if title == "" {
		title = cleanMigrationTitle(firstNonEmptyLine(first.Text))
	}
	if title == "" {
		title = "Материал " + joinInts(row.SourceMessageIDs, "+")
	}
	publishedAt := now
	doc, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{
		Type:            "note",
		Status:          "published",
		Title:           title,
		SourceChatID:    first.ChatID,
		SourceMessageID: first.MessageID,
		SourceLink:      firstNonEmpty(first.SourceLink, row.SourceMessageLink),
		TargetChatID:    cfg.Workspace.ChatID,
		TargetTopicID:   cfg.Workspace.Topics.Useful,
		TargetMessageID: targetMessageID,
		PublishedAt:     &publishedAt,
	}, sqlitestore.WorkspaceDocumentPart{
		Title:           title,
		SourceChatID:    first.ChatID,
		SourceMessageID: first.MessageID,
		SourceLink:      firstNonEmpty(first.SourceLink, row.SourceMessageLink),
		Text:            first.Text,
	}, now)
	if err != nil {
		return err
	}
	for i, message := range messages[1:] {
		if _, err := store.AddWorkspaceDocumentPart(ctx, sqlitestore.WorkspaceDocumentPart{
			DocumentID:      doc.ID,
			PartNo:          i + 2,
			Title:           "Часть " + strconv.Itoa(i+2),
			SourceChatID:    message.ChatID,
			SourceMessageID: message.MessageID,
			SourceLink:      firstNonEmpty(message.SourceLink, row.SourceMessageLink),
			Text:            message.Text,
		}, now); err != nil {
			return err
		}
	}
	return nil
}

func writePinnedMigrationApplyCSV(path string, items []pinnedMigrationApplyItem) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"row", "source_message_ids", "short_title", "user_comment", "action", "target", "status", "derived_links", "error"}); err != nil {
		return err
	}
	for _, item := range items {
		if err := writer.Write([]string{
			strconv.Itoa(item.RowIndex),
			joinInts(item.SourceIDs, "+"),
			item.ShortTitle,
			item.UserComment,
			item.Action,
			item.Target,
			item.Status,
			strings.Join(item.DerivedLinks, " "),
			item.Error,
		}); err != nil {
			return err
		}
	}
	return writer.Error()
}

func renderPinnedMigrationApplyMarkdown(now time.Time, reviewCSV string, result PinnedMigrationApplyResult, items []pinnedMigrationApplyItem) string {
	var b strings.Builder
	b.WriteString("# Pinned Migration Apply Plan\n\n")
	fmt.Fprintf(&b, "Generated: `%s`\n\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Review CSV: `%s`\n\n", reviewCSV)
	if result.DryRun {
		b.WriteString("Mode: `dry-run` (no Telegram messages were sent)\n\n")
	} else {
		b.WriteString("Mode: `execute`\n\n")
	}
	fmt.Fprintf(&b, "- Rows: `%d`\n", result.Rows)
	fmt.Fprintf(&b, "- Planned: `%d`\n", result.Planned)
	fmt.Fprintf(&b, "- Executed: `%d`\n", result.Executed)
	fmt.Fprintf(&b, "- Archived/skipped: `%d`\n", result.Archived)
	fmt.Fprintf(&b, "- Pending/no decision: `%d`\n", result.Pending)
	fmt.Fprintf(&b, "- Errors: `%d`\n\n", result.Errors)
	b.WriteString("| row | source ids | action | target | status | title | links/error |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, item := range items {
		detail := item.Error
		if detail == "" {
			detail = strings.Join(item.DerivedLinks, " ")
		}
		fmt.Fprintf(&b, "| `%d` | `%s` | `%s` | %s | `%s` | %s | %s |\n",
			item.RowIndex,
			joinInts(item.SourceIDs, "+"),
			item.Action,
			mdCell(item.Target),
			item.Status,
			mdCell(item.ShortTitle),
			mdCell(detail),
		)
	}
	return b.String()
}
