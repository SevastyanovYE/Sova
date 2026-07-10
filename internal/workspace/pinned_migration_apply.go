package workspace

import (
	"context"
	"encoding/csv"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
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
	PinnedReview         bool
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
	UserDecision         string
	UserComment          string
}

type pinnedMigrationDecision struct {
	Action       string
	Target       string
	SourceIDs    []int
	AppendTags   []string
	TextPrefix   string
	Note         string
	UseLinkedIDs bool
}

type pinnedMigrationDestination struct {
	Client  *nest.Client
	ChatID  int64
	TopicID int
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
	reviewCSVPaths := splitReviewCSVPaths(reviewCSV)
	rows, err := readMigrationApplyRows(reviewCSVPaths)
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
	var items []pinnedMigrationApplyItem
	result := PinnedMigrationApplyResult{
		RunID:       runID,
		ArtifactDir: outputDir,
		PlanMD:      filepath.Join(outputDir, "pinned_migration_apply.md"),
		PlanCSV:     filepath.Join(outputDir, "pinned_migration_apply.csv"),
		Rows:        len(rows),
		DryRun:      !opts.Execute,
	}
	seenTransfers := map[string]struct{}{}
	for i, row := range rows {
		item := pinnedMigrationApplyItem{
			RowIndex:    i + 2,
			SourceIDs:   row.SourceMessageIDs,
			ShortTitle:  row.ShortTitle,
			UserComment: row.UserComment,
		}
		decision := parsePinnedMigrationDecision(row)
		if len(decision.SourceIDs) > 0 {
			row.SourceMessageIDs = decision.SourceIDs
			item.SourceIDs = decision.SourceIDs
		}
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
			if decision.UseLinkedIDs {
				containerMessages, missing := migrationSourceMessages(row.SourceMessageIDs, byID)
				if len(missing) > 0 {
					item.Status = "error"
					item.Error = "missing source messages for linked ids: " + joinInts(missing, "+")
					result.Errors++
					break
				}
				linkedIDs := migrationLinkedMessageIDs(containerMessages, byID)
				if len(linkedIDs) == 0 {
					item.Status = "error"
					item.Error = "no linked source messages found"
					result.Errors++
					break
				}
				row.SourceMessageIDs = linkedIDs
				item.SourceIDs = linkedIDs
			}
			dedupeKey := migrationApplyDedupeKey(decision.Action, decision.Target, row.SourceMessageIDs)
			if _, ok := seenTransfers[dedupeKey]; ok {
				item.Status = "duplicate_skipped"
				result.Archived++
				break
			}
			seenTransfers[dedupeKey] = struct{}{}
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
			links, err := executePinnedMigrationItem(ctx, cfg, store, row, decision, messages, now)
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

func splitReviewCSVPaths(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func readMigrationApplyRows(paths []string) ([]pinnedMigrationReviewRow, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("--review-csv is required")
	}
	var out []pinnedMigrationReviewRow
	for _, path := range paths {
		rows, err := readMigrationApplyRowsFromCSV(path)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

func readMigrationApplyRowsFromCSV(path string) ([]pinnedMigrationReviewRow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read review csv %s: %w", path, err)
	}
	reader := csv.NewReader(strings.NewReader(string(data)))
	reader.Comma = detectReviewCSVDelimiter(string(data))
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
	if _, ok := header["source_message_ids"]; ok {
		return readPinnedMigrationApplyRows(path, rawRows[1:], header)
	}
	if _, ok := header["message_link"]; ok {
		return readAuditMigrationApplyRows(path, rawRows[1:], header)
	}
	return nil, fmt.Errorf("review csv %s has unsupported schema", path)
}

func readPinnedMigrationApplyRows(path string, rawRows [][]string, header map[string]int) ([]pinnedMigrationReviewRow, error) {
	required := []string{"legacy_topic", "source_message_link", "source_message_ids", "cluster_id", "short_title", "short_summary", "detected_type", "suggested_target_topic", "confidence", "reason", "needs_user_review", "review_status", "user_comment"}
	if err := requireCSVColumns(path, header, required); err != nil {
		return nil, err
	}
	rows := make([]pinnedMigrationReviewRow, 0, len(rawRows))
	for i, raw := range rawRows {
		row := pinnedMigrationReviewRow{
			PinnedReview:         true,
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

func readAuditMigrationApplyRows(path string, rawRows [][]string, header map[string]int) ([]pinnedMigrationReviewRow, error) {
	required := []string{"source_topic", "message_link", "short_summary", "detected_type", "confidence", "reason", "user_decision", "user_comment"}
	if err := requireCSVColumns(path, header, required); err != nil {
		return nil, err
	}
	rows := make([]pinnedMigrationReviewRow, 0, len(rawRows))
	for i, raw := range rawRows {
		link := csvRowValue(raw, header, "message_link")
		messageID, ok := telegramLinkLastMessageID(link)
		if !ok {
			return nil, fmt.Errorf("review csv row %d has invalid message_link %q", i+2, link)
		}
		userDecision := strings.ToLower(csvRowValue(raw, header, "user_decision"))
		row := pinnedMigrationReviewRow{
			LegacyTopic:          csvRowValue(raw, header, "source_topic"),
			SourceMessageLink:    link,
			SourceMessageIDsRaw:  strconv.Itoa(messageID),
			SourceMessageIDs:     []int{messageID},
			ClusterID:            "audit-" + strconv.Itoa(messageID),
			ShortTitle:           cleanMigrationTitle(csvRowValue(raw, header, "short_summary")),
			ShortSummary:         csvRowValue(raw, header, "short_summary"),
			DetectedType:         csvRowValue(raw, header, "detected_type"),
			SuggestedTargetTopic: auditApplySuggestedTarget(raw, header),
			Confidence:           csvRowValue(raw, header, "confidence"),
			Reason:               csvRowValue(raw, header, "reason"),
			UserDecision:         userDecision,
			UserComment:          csvRowValue(raw, header, "user_comment"),
		}
		if row.UserDecision == "" && row.UserComment == "" {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func requireCSVColumns(path string, header map[string]int, required []string) error {
	for _, name := range required {
		if _, ok := header[name]; !ok {
			return fmt.Errorf("review csv %s is missing column %q", path, name)
		}
	}
	return nil
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

func auditApplySuggestedTarget(raw []string, header map[string]int) string {
	target := csvRowValue(raw, header, "target")
	if target == "" {
		target = csvRowValue(raw, header, "suggested_target")
	}
	if target == "" || target == "Review" {
		target = targetFromDetectedType(csvRowValue(raw, header, "detected_type"))
	}
	return target
}

func targetFromDetectedType(detectedType string) string {
	switch detectedType {
	case TypeTask, TypeDeferredTask:
		return "Задачи"
	case TypeTemplateDocument:
		return "Заготовки"
	case TypeCollectionItem:
		return "Коллекции"
	case TypeUsefulMaterial:
		return "Полезное"
	case TypeExperience:
		return "Опыт"
	default:
		return "Заметки"
	}
}

func parsePinnedMigrationDecision(row pinnedMigrationReviewRow) pinnedMigrationDecision {
	userDecision := strings.ToLower(strings.TrimSpace(row.UserDecision))
	comment := strings.TrimSpace(row.UserComment)
	normalized := strings.ToLower(comment)
	normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "decision:"))
	normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "target:"))
	normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "topic:"))

	if migrationCommentNeedsLinkedIDs(normalized) {
		target := strings.TrimSpace(row.SuggestedTargetTopic)
		if strings.Contains(normalized, "промпт") || strings.Contains(normalized, "заготов") {
			target = "Заготовки"
		}
		if target == "" || target == "Review" {
			target = "Заметки"
		}
		return pinnedMigrationDecision{Action: "migrate", Target: target, UseLinkedIDs: true, Note: comment}
	}
	if userDecision == "archive" || userDecision == "trash" || userDecision == "later" ||
		strings.Contains(normalized, "archive") || strings.Contains(normalized, "архив") || strings.Contains(normalized, "skip") {
		return pinnedMigrationDecision{Action: "archive", Target: "Legacy archive"}
	}
	if userDecision == "study" || normalized == "study" {
		return pinnedMigrationDecision{Action: "migrate", Target: "Study branch"}
	}
	if userDecision == "control" || normalized == "control" {
		return pinnedMigrationDecision{Action: "migrate", Target: "Sova.Control"}
	}
	if userDecision == "collection" {
		return pinnedMigrationDecision{Action: "migrate", Target: "Коллекции"}
	}
	if userDecision == "take" && strings.Contains(normalized, "полезн") {
		return pinnedMigrationDecision{Action: "publish", Target: "Полезное", AppendTags: migrationTagsFromComment(comment)}
	}
	if strings.Contains(normalized, "publish") || strings.Contains(normalized, "опубликов") ||
		normalized == "полезное" || strings.HasPrefix(normalized, "в полезн") {
		return pinnedMigrationDecision{Action: "publish", Target: "Полезное", SourceIDs: migrationBundleIDs(comment)}
	}
	for _, target := range []string{"Inbox", "Задачи", "Заметки", "Опыт", "Полезное", "Заготовки", "Коллекции"} {
		if commentMatchesTopic(normalized, target) {
			return pinnedMigrationDecision{Action: "migrate", Target: target, SourceIDs: migrationBundleIDs(comment), AppendTags: migrationTagsFromComment(comment)}
		}
	}
	if bundle := migrationBundleIDs(comment); len(bundle) > 0 {
		return pinnedMigrationDecision{Action: "migrate", Target: strings.TrimSpace(row.SuggestedTargetTopic), SourceIDs: bundle}
	}
	if userDecision == "take" {
		return pinnedMigrationDecision{Action: "migrate", Target: strings.TrimSpace(row.SuggestedTargetTopic), AppendTags: migrationTagsFromComment(comment)}
	}
	if comment != "" {
		return pinnedMigrationDecision{Action: "migrate", Target: strings.TrimSpace(row.SuggestedTargetTopic), TextPrefix: migrationTextPrefixFromComment(comment), Note: comment}
	}
	if row.PinnedReview && strings.TrimSpace(row.SuggestedTargetTopic) != "" {
		return pinnedMigrationDecision{Action: "migrate", Target: strings.TrimSpace(row.SuggestedTargetTopic)}
	}
	return pinnedMigrationDecision{Action: "pending", Target: strings.TrimSpace(row.SuggestedTargetTopic)}
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

func migrationCommentNeedsLinkedIDs(normalized string) bool {
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "ссыл") &&
		(strings.Contains(normalized, "само сообщение") || strings.Contains(normalized, "ссылает") || strings.Contains(normalized, "ссылается"))
}

func migrationBundleIDs(comment string) []int {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return nil
	}
	for _, r := range comment {
		if !(r >= '0' && r <= '9') && r != '+' && r != ' ' {
			return nil
		}
	}
	ids, err := parseMigrationSourceIDs(comment)
	if err != nil || len(ids) <= 1 {
		return nil
	}
	return ids
}

func migrationTagsFromComment(comment string) []string {
	lower := strings.ToLower(comment)
	var tags []string
	for _, tag := range []string{"#мюсли", "#идеи", "#опыт"} {
		if strings.Contains(lower, tag) {
			tags = append(tags, tag)
		}
	}
	return tags
}

func migrationTextPrefixFromComment(comment string) string {
	lower := strings.ToLower(comment)
	if !strings.Contains(lower, "начинай со слов") {
		return ""
	}
	for _, pair := range [][2]string{{"“", "”"}, {"\"", "\""}, {"«", "»"}} {
		start := strings.Index(comment, pair[0])
		if start < 0 {
			continue
		}
		rest := comment[start+len(pair[0]):]
		end := strings.Index(rest, pair[1])
		if end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return ""
}

func telegramLinkLastMessageID(link string) (int, bool) {
	link = cleanTelegramArg(link)
	parts := strings.Split(link, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		id, err := strconv.Atoi(parts[i])
		if err == nil && id > 0 {
			return id, true
		}
	}
	return 0, false
}

func migrationApplyDedupeKey(action, target string, ids []int) string {
	return action + "|" + target + "|" + joinInts(ids, "+")
}

var telegramLinkPattern = regexp.MustCompile(`https?://t\.me/[^\s"')<>]+`)
var telegramRetryAfterPattern = regexp.MustCompile(`retry after (\d+)`)

func migrationLinkedMessageIDs(messages []sqlitestore.WorkspaceSourceMessage, byID map[int]sqlitestore.WorkspaceSourceMessage) []int {
	self := map[int]struct{}{}
	var corpus strings.Builder
	for _, message := range messages {
		self[message.MessageID] = struct{}{}
		corpus.WriteString(message.Text)
		corpus.WriteString("\n")
		corpus.WriteString(message.RawJSON)
		corpus.WriteString("\n")
	}
	seen := map[int]struct{}{}
	var out []int
	for _, link := range telegramLinkPattern.FindAllString(corpus.String(), -1) {
		id, ok := telegramLinkLastMessageID(link)
		if !ok {
			continue
		}
		if _, ok := self[id]; ok {
			continue
		}
		if _, ok := byID[id]; !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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

func executePinnedMigrationItem(ctx context.Context, cfg config.Config, store *sqlitestore.Store, row pinnedMigrationReviewRow, decision pinnedMigrationDecision, messages []sqlitestore.WorkspaceSourceMessage, now time.Time) ([]string, error) {
	dest, err := pinnedMigrationDestinationFor(cfg, decision)
	if err != nil {
		return nil, err
	}
	var derivedLinks []string
	var firstTargetMessageID int
	for i, message := range messages {
		existing, err := existingPinnedMigrationDerivedLinks(ctx, store, message, decision, row, dest, i+1)
		if err != nil {
			return derivedLinks, err
		}
		if len(existing) > 0 {
			if firstTargetMessageID == 0 {
				firstTargetMessageID = existing[0].MessageID
			}
			for _, link := range existing {
				derivedLinks = append(derivedLinks, workspaceMessageLink(link.ChatID, link.TopicID, link.MessageID))
			}
			continue
		}
		if shouldCopyPinnedMigrationMessage(decision) {
			sent, err := copyPinnedMigrationMessage(ctx, dest.Client, nest.CopyMessageRequest{
				ChatID:          dest.ChatID,
				MessageThreadID: dest.TopicID,
				FromChatID:      botAPISupergroupChatID(message.ChatID),
				MessageID:       message.MessageID,
			})
			if err == nil && sent.MessageID > 0 {
				if firstTargetMessageID == 0 {
					firstTargetMessageID = sent.MessageID
				}
				link := workspaceMessageLink(dest.ChatID, dest.TopicID, sent.MessageID)
				derivedLinks = append(derivedLinks, link)
				if err := upsertPinnedMigrationDerivedMessage(ctx, store, message, decision, row, dest, sent.MessageID, now, i+1, 1); err != nil {
					return derivedLinks, err
				}
				continue
			}
		}
		texts := renderPinnedMigrationTelegramMessages(row, decision, message, i+1, len(messages))
		for j, text := range texts {
			sent, err := sendPinnedMigrationMessage(ctx, dest.Client, nest.SendMessageRequest{
				ChatID:          dest.ChatID,
				MessageThreadID: dest.TopicID,
				Text:            text,
				ParseMode:       "HTML",
			})
			if err != nil {
				return derivedLinks, err
			}
			if firstTargetMessageID == 0 {
				firstTargetMessageID = sent.MessageID
			}
			link := workspaceMessageLink(dest.ChatID, dest.TopicID, sent.MessageID)
			derivedLinks = append(derivedLinks, link)
			if err := upsertPinnedMigrationDerivedMessage(ctx, store, message, decision, row, dest, sent.MessageID, now, i+1, j+1); err != nil {
				return derivedLinks, err
			}
		}
	}
	if (decision.Action == "publish" || decision.Target == "Полезное") && firstTargetMessageID > 0 && len(messages) > 0 {
		if err := createPublishedLegacyDocument(ctx, cfg, store, row, messages, firstTargetMessageID, now); err != nil {
			return derivedLinks, err
		}
		if err := updateUsefulIndex(ctx, cfg, store, dest.Client, now); err != nil {
			return derivedLinks, err
		}
	}
	return derivedLinks, nil
}

type pinnedMigrationDerivedLink struct {
	ChatID    int64
	TopicID   int
	MessageID int
}

func existingPinnedMigrationDerivedLinks(ctx context.Context, store *sqlitestore.Store, source sqlitestore.WorkspaceSourceMessage, decision pinnedMigrationDecision, row pinnedMigrationReviewRow, dest pinnedMigrationDestination, sourcePart int) ([]pinnedMigrationDerivedLink, error) {
	prefix := fmt.Sprintf("legacy_migration_%s_row_%s_part_%d_", decision.Action, row.ClusterID, sourcePart)
	statuses := []string{"active", "published"}
	records, err := store.WorkspaceDerivedMessagesBySource(ctx, source.ChatID, source.MessageID, prefix, statuses, 50)
	if err != nil {
		return nil, err
	}
	if out := matchingPinnedMigrationDerivedLinks(records, dest); len(out) > 0 {
		return out, nil
	}
	records, err = store.WorkspaceDerivedMessagesBySource(ctx, source.ChatID, source.MessageID, "legacy_migration_", statuses, 50)
	if err != nil {
		return nil, err
	}
	return matchingPinnedMigrationDerivedLinks(records, dest), nil
}

func matchingPinnedMigrationDerivedLinks(records []sqlitestore.WorkspaceDerivedMessage, dest pinnedMigrationDestination) []pinnedMigrationDerivedLink {
	out := make([]pinnedMigrationDerivedLink, 0, len(records))
	seen := map[int]struct{}{}
	for _, record := range records {
		if record.DerivedMessageID == 0 || record.DerivedChatID != dest.ChatID || record.DerivedTopicID != dest.TopicID {
			continue
		}
		if _, ok := seen[record.DerivedMessageID]; ok {
			continue
		}
		seen[record.DerivedMessageID] = struct{}{}
		out = append(out, pinnedMigrationDerivedLink{ChatID: record.DerivedChatID, TopicID: record.DerivedTopicID, MessageID: record.DerivedMessageID})
	}
	return out
}

func copyPinnedMigrationMessage(ctx context.Context, client *nest.Client, request nest.CopyMessageRequest) (nest.Message, error) {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		sent, err := client.CopyMessage(ctx, request)
		if err == nil {
			return sent, nil
		}
		lastErr = err
		delay, ok := telegramMigrationRetryDelay(err)
		if !ok {
			return nest.Message{}, err
		}
		if err := sleepContext(ctx, delay); err != nil {
			return nest.Message{}, err
		}
	}
	return nest.Message{}, lastErr
}

func sendPinnedMigrationMessage(ctx context.Context, client *nest.Client, request nest.SendMessageRequest) (nest.Message, error) {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		sent, err := client.SendMessageResult(ctx, request)
		if err == nil {
			return sent, nil
		}
		lastErr = err
		delay, ok := telegramMigrationRetryDelay(err)
		if !ok {
			return nest.Message{}, err
		}
		if err := sleepContext(ctx, delay); err != nil {
			return nest.Message{}, err
		}
	}
	return nest.Message{}, lastErr
}

func telegramMigrationRetryDelay(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	text := err.Error()
	if match := telegramRetryAfterPattern.FindStringSubmatch(text); len(match) == 2 {
		seconds, parseErr := strconv.Atoi(match[1])
		if parseErr == nil && seconds > 0 {
			return time.Duration(seconds+1) * time.Second, true
		}
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "tls handshake timeout") || strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "connection reset") || strings.Contains(lower, "temporary") {
		return 3 * time.Second, true
	}
	return 0, false
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func pinnedMigrationDestinationFor(cfg config.Config, decision pinnedMigrationDecision) (pinnedMigrationDestination, error) {
	target := strings.ToLower(strings.TrimSpace(decision.Target))
	switch target {
	case "sova.control", "control":
		if !cfg.ControlConfigured() {
			return pinnedMigrationDestination{}, fmt.Errorf("Sova.Control is not fully configured")
		}
		return pinnedMigrationDestination{
			Client:  nest.New(cfg.Control.BotToken),
			ChatID:  cfg.Control.ChatID,
			TopicID: cfg.Control.Topics.Workspace,
		}, nil
	case "study branch", "study":
		if !cfg.NestReady() {
			return pinnedMigrationDestination{}, fmt.Errorf("Sova.Nest is not fully configured for study branch")
		}
		return pinnedMigrationDestination{
			Client:  nest.New(cfg.NestBotToken),
			ChatID:  cfg.NestChatID,
			TopicID: cfg.NestTopics.Chat,
		}, nil
	default:
		topicID, err := pinnedMigrationTargetTopicID(cfg, decision.Target)
		if err != nil {
			return pinnedMigrationDestination{}, err
		}
		return pinnedMigrationDestination{
			Client:  nest.New(cfg.Workspace.BotToken),
			ChatID:  cfg.Workspace.ChatID,
			TopicID: topicID,
		}, nil
	}
}

func shouldCopyPinnedMigrationMessage(decision pinnedMigrationDecision) bool {
	return decision.Action == "migrate" && strings.TrimSpace(decision.TextPrefix) == "" && len(decision.AppendTags) == 0
}

func upsertPinnedMigrationDerivedMessage(ctx context.Context, store *sqlitestore.Store, source sqlitestore.WorkspaceSourceMessage, decision pinnedMigrationDecision, row pinnedMigrationReviewRow, dest pinnedMigrationDestination, targetMessageID int, now time.Time, sourcePart, chunkPart int) error {
	status := "active"
	if decision.Action == "publish" || decision.Target == "Полезное" {
		status = "published"
	}
	return store.UpsertWorkspaceDerivedMessage(ctx, sqlitestore.WorkspaceDerivedMessage{
		SourceChatID:     source.ChatID,
		SourceMessageID:  source.MessageID,
		DerivedType:      fmt.Sprintf("legacy_migration_%s_row_%s_part_%d_msg_%d", decision.Action, row.ClusterID, sourcePart, chunkPart),
		DerivedChatID:    dest.ChatID,
		DerivedTopicID:   dest.TopicID,
		DerivedMessageID: targetMessageID,
		Status:           status,
	}, now)
}

func botAPISupergroupChatID(chatID int64) int64 {
	if chatID < 0 || chatID == 0 {
		return chatID
	}
	converted, err := strconv.ParseInt("-100"+strconv.FormatInt(chatID, 10), 10, 64)
	if err != nil {
		return -1000000000000 - chatID
	}
	return converted
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
	if prefix := strings.TrimSpace(decision.TextPrefix); prefix != "" && !strings.HasPrefix(strings.TrimSpace(body), prefix) {
		body = strings.TrimSpace(prefix + "\n\n" + body)
	}
	if len(decision.AppendTags) > 0 {
		body = appendMigrationTags(body, decision.AppendTags)
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

func appendMigrationTags(body string, tags []string) string {
	body = strings.TrimSpace(body)
	var missing []string
	lower := strings.ToLower(body)
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || strings.Contains(lower, strings.ToLower(tag)) {
			continue
		}
		missing = append(missing, tag)
	}
	if len(missing) == 0 {
		return body
	}
	if body == "" {
		return strings.Join(missing, " ")
	}
	return body + "\n\n" + strings.Join(missing, " ")
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
	if ok, err := publishedLegacyDocumentExists(ctx, cfg, store, first); err != nil {
		return err
	} else if ok {
		return nil
	}
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

func publishedLegacyDocumentExists(ctx context.Context, cfg config.Config, store *sqlitestore.Store, first sqlitestore.WorkspaceSourceMessage) (bool, error) {
	parts, err := store.WorkspaceDocumentPartsBySource(ctx, first.ChatID, first.MessageID)
	if err != nil {
		return false, err
	}
	for _, part := range parts {
		doc, err := store.WorkspaceDocumentByID(ctx, part.DocumentID)
		if err != nil {
			return false, err
		}
		if doc.Type == "note" && doc.Status == "published" &&
			doc.TargetChatID == cfg.Workspace.ChatID && doc.TargetTopicID == cfg.Workspace.Topics.Useful {
			return true, nil
		}
	}
	return false, nil
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
