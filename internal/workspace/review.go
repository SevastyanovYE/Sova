package workspace

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type ReviewPreviewOptions struct {
	AuditRunID    int64
	ReviewCSVPath string
	OutputDir     string
	Now           time.Time
}

type ReviewPreviewResult struct {
	AuditRunID       int64
	ReviewCSVPath    string
	OutputDir        string
	PreviewPath      string
	PreviewCSVPath   string
	Records          int
	ReviewRows       int
	UserDecisions    int
	PendingDecisions int
	MigrationItems   int
	ExternalRoutes   int
	ExcludedItems    int
	NeedsApproval    bool
}

type reviewRow struct {
	SourceTopic     string
	MessageDate     string
	MessageLink     string
	ShortSummary    string
	DetectedType    string
	ModelDecision   string
	Confidence      string
	SuggestedTarget string
	Reason          string
	MediaType       string
	UserDecision    string
	UserComment     string
}

type previewItem struct {
	Record        sqlitestore.WorkspaceAuditRecord
	UserDecision  string
	UserComment   string
	FinalAction   string
	Target        string
	DecisionNote  string
	RequiresInput bool
}

func BuildReviewPreview(ctx context.Context, cfg config.Config, store *sqlitestore.Store, opts ReviewPreviewOptions) (ReviewPreviewResult, error) {
	if store == nil {
		return ReviewPreviewResult{}, fmt.Errorf("store is required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	run, err := resolveAuditRun(ctx, store, opts.AuditRunID)
	if err != nil {
		return ReviewPreviewResult{}, err
	}
	reviewCSVPath := strings.TrimSpace(opts.ReviewCSVPath)
	if reviewCSVPath == "" {
		reviewCSVPath = filepath.Join(run.ArtifactDir, "workspace_review_candidates.csv")
	}
	reviewRows, err := readReviewRows(reviewCSVPath)
	if err != nil {
		return ReviewPreviewResult{}, err
	}
	reviewByKey := map[string]reviewRow{}
	userDecisions := 0
	for _, row := range reviewRows {
		if row.UserDecision != "" {
			userDecisions++
		}
		reviewByKey[reviewRowKey(row)] = row
	}

	records, err := store.WorkspaceAuditRecordsByRun(ctx, run.ID)
	if err != nil {
		return ReviewPreviewResult{}, err
	}
	if len(records) == 0 {
		return ReviewPreviewResult{}, fmt.Errorf("workspace audit run %d has no records", run.ID)
	}

	items := make([]previewItem, 0, len(records))
	for _, record := range records {
		row, hasReview := reviewByKey[auditRecordReviewKey(record)]
		items = append(items, mergePreviewItem(record, row, hasReview))
	}
	sortPreviewItems(items)

	outputDir := strings.TrimSpace(opts.OutputDir)
	if outputDir == "" {
		outputDir = filepath.Join(cfg.StateDir, "artifacts", "workspace", "migration_preview", now.UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return ReviewPreviewResult{}, err
	}
	result := ReviewPreviewResult{
		AuditRunID:     run.ID,
		ReviewCSVPath:  reviewCSVPath,
		OutputDir:      outputDir,
		PreviewPath:    filepath.Join(outputDir, "workspace_migration_preview.md"),
		PreviewCSVPath: filepath.Join(outputDir, "workspace_migration_preview.csv"),
		Records:        len(records),
		ReviewRows:     len(reviewRows),
		UserDecisions:  userDecisions,
	}
	for _, item := range items {
		switch item.FinalAction {
		case "migrate":
			result.MigrationItems++
		case "route_to_study", "route_to_control":
			result.ExternalRoutes++
		default:
			result.ExcludedItems++
		}
		if item.RequiresInput {
			result.PendingDecisions++
		}
	}
	result.NeedsApproval = result.PendingDecisions == 0

	if err := os.WriteFile(result.PreviewPath, []byte(renderMigrationPreview(now, run, result, items)), 0o600); err != nil {
		return ReviewPreviewResult{}, err
	}
	if err := writePreviewCSV(result.PreviewCSVPath, items); err != nil {
		return ReviewPreviewResult{}, err
	}
	return result, nil
}

func resolveAuditRun(ctx context.Context, store *sqlitestore.Store, id int64) (sqlitestore.WorkspaceAuditRun, error) {
	if id > 0 {
		run, ok, err := store.WorkspaceAuditRunByID(ctx, id)
		if err != nil {
			return sqlitestore.WorkspaceAuditRun{}, err
		}
		if !ok {
			return sqlitestore.WorkspaceAuditRun{}, fmt.Errorf("workspace audit run %d not found", id)
		}
		if run.Status != "success" || run.DryRun {
			return sqlitestore.WorkspaceAuditRun{}, fmt.Errorf("workspace audit run %d is not a successful durable audit", id)
		}
		return run, nil
	}
	run, ok, err := store.LatestSuccessfulWorkspaceAuditRun(ctx)
	if err != nil {
		return sqlitestore.WorkspaceAuditRun{}, err
	}
	if !ok {
		return sqlitestore.WorkspaceAuditRun{}, fmt.Errorf("no successful workspace audit run found; run `sova workspace audit` first")
	}
	return run, nil
}

func readReviewRows(path string) ([]reviewRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open review csv %s: %w", path, err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read review csv %s: %w", path, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("review csv %s is empty", path)
	}
	header := map[string]int{}
	for i, name := range rows[0] {
		header[strings.TrimSpace(name)] = i
	}
	required := []string{"source_topic", "message_date", "message_link", "short_summary", "detected_type", "model_decision", "confidence", "suggested_target", "reason", "media_type", "user_decision", "user_comment"}
	for _, name := range required {
		if _, ok := header[name]; !ok {
			return nil, fmt.Errorf("review csv %s is missing column %q", path, name)
		}
	}
	out := make([]reviewRow, 0, len(rows)-1)
	for i, raw := range rows[1:] {
		row := reviewRow{
			SourceTopic:     csvValue(raw, header, "source_topic"),
			MessageDate:     csvValue(raw, header, "message_date"),
			MessageLink:     csvValue(raw, header, "message_link"),
			ShortSummary:    csvValue(raw, header, "short_summary"),
			DetectedType:    csvValue(raw, header, "detected_type"),
			ModelDecision:   csvValue(raw, header, "model_decision"),
			Confidence:      csvValue(raw, header, "confidence"),
			SuggestedTarget: csvValue(raw, header, "suggested_target"),
			Reason:          csvValue(raw, header, "reason"),
			MediaType:       csvValue(raw, header, "media_type"),
			UserDecision:    strings.ToLower(csvValue(raw, header, "user_decision")),
			UserComment:     csvValue(raw, header, "user_comment"),
		}
		if row.UserDecision != "" && !validUserDecision(row.UserDecision) {
			return nil, fmt.Errorf("review csv row %d has invalid user_decision %q", i+2, row.UserDecision)
		}
		out = append(out, row)
	}
	return out, nil
}

func csvValue(row []string, header map[string]int, name string) string {
	idx := header[name]
	if idx < 0 || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

func validUserDecision(value string) bool {
	for _, allowed := range allowedUserDecisions {
		if value == allowed {
			return true
		}
	}
	return false
}

func mergePreviewItem(record sqlitestore.WorkspaceAuditRecord, row reviewRow, hasReview bool) previewItem {
	item := previewItem{
		Record:      record,
		FinalAction: "pending_review",
		Target:      targetFromAudit(record),
	}
	if hasReview {
		item.UserDecision = row.UserDecision
		item.UserComment = row.UserComment
	}
	if item.UserDecision != "" {
		applyUserDecision(&item)
		return item
	}
	applyAuditDecision(&item)
	return item
}

func applyUserDecision(item *previewItem) {
	switch item.UserDecision {
	case "take":
		item.FinalAction = "migrate"
		item.Target = targetFromAudit(item.Record)
		item.DecisionNote = "manual take"
	case "collection":
		item.FinalAction = "migrate"
		item.Target = "Коллекции"
		item.DecisionNote = "manual collection"
	case "study":
		item.FinalAction = "route_to_study"
		item.Target = "Study branch"
		item.DecisionNote = "manual study route"
	case "control":
		item.FinalAction = "route_to_control"
		item.Target = "Sova.Control"
		item.DecisionNote = "manual control route"
	case "archive":
		item.FinalAction = "archive"
		item.Target = "Legacy archive"
		item.DecisionNote = "manual archive"
	case "trash":
		item.FinalAction = "trash"
		item.Target = "Trash"
		item.DecisionNote = "manual trash"
	case "later":
		item.FinalAction = "later"
		item.Target = "Later review"
		item.DecisionNote = "manual later"
	default:
		item.FinalAction = "pending_review"
		item.Target = "Review"
		item.RequiresInput = true
		item.DecisionNote = "missing manual decision"
	}
}

func applyAuditDecision(item *previewItem) {
	switch item.Record.ModelDecision {
	case DecisionTake:
		item.FinalAction = "migrate"
		item.Target = targetFromAudit(item.Record)
		item.DecisionNote = "audit take"
	case DecisionRouteStudy:
		item.FinalAction = "route_to_study"
		item.Target = "Study branch"
		item.DecisionNote = "audit study route"
	case DecisionRouteControl:
		item.FinalAction = "route_to_control"
		item.Target = "Sova.Control"
		item.DecisionNote = "audit control route"
	case DecisionArchive:
		item.FinalAction = "archive"
		item.Target = "Legacy archive"
		item.DecisionNote = "audit archive"
	case DecisionTrash:
		item.FinalAction = "trash"
		item.Target = "Trash"
		item.DecisionNote = "audit trash"
	case DecisionSkipDoneTask:
		item.FinalAction = "skip_done_task"
		item.Target = "Legacy archive"
		item.DecisionNote = "audit skip done task"
	default:
		item.FinalAction = "pending_review"
		item.Target = "Review"
		item.RequiresInput = true
		item.DecisionNote = "review candidate has no user_decision"
	}
}

func targetFromAudit(record sqlitestore.WorkspaceAuditRecord) string {
	target := strings.TrimSpace(record.SuggestedTarget)
	if isWorkspaceTarget(target) {
		return target
	}
	switch record.DetectedType {
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
	case TypeIdea, TypeDraftNote, TypeNoteDocument:
		return "Заметки"
	default:
		return "Inbox"
	}
}

func isWorkspaceTarget(target string) bool {
	switch strings.TrimSpace(target) {
	case "Inbox", "Задачи", "Заметки", "Опыт", "Полезное", "Заготовки", "Коллекции":
		return true
	default:
		return false
	}
}

func renderMigrationPreview(now time.Time, run sqlitestore.WorkspaceAuditRun, result ReviewPreviewResult, items []previewItem) string {
	var b strings.Builder
	b.WriteString("# Workspace Migration Preview\n\n")
	fmt.Fprintf(&b, "Generated: `%s`\n\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Audit run: `%d`\n\n", run.ID)
	fmt.Fprintf(&b, "Review CSV: `%s`\n\n", result.ReviewCSVPath)
	if result.PendingDecisions > 0 {
		b.WriteString("Status: `blocked_pending_review_decisions`\n\n")
	} else {
		b.WriteString("Status: `ready_for_user_approval`\n\n")
	}
	fmt.Fprintf(&b, "- Audit records: `%d`\n", result.Records)
	fmt.Fprintf(&b, "- Review rows: `%d`\n", result.ReviewRows)
	fmt.Fprintf(&b, "- User decisions filled: `%d`\n", result.UserDecisions)
	fmt.Fprintf(&b, "- Pending manual decisions: `%d`\n", result.PendingDecisions)
	fmt.Fprintf(&b, "- Migration candidates: `%d`\n", result.MigrationItems)
	fmt.Fprintf(&b, "- External routes: `%d`\n", result.ExternalRoutes)
	fmt.Fprintf(&b, "- Excluded/later/skipped: `%d`\n", result.ExcludedItems)
	writePreviewCounts(&b, "Counts By Final Action", countPreview(items, func(item previewItem) string { return item.FinalAction }))
	writePreviewCounts(&b, "Counts By Target", countPreview(items, func(item previewItem) string { return item.Target }))
	writePreviewSection(&b, "Pending Review Decisions", items, func(item previewItem) bool { return item.RequiresInput })
	writePreviewSection(&b, "Migration Candidates", items, func(item previewItem) bool { return item.FinalAction == "migrate" })
	writePreviewSection(&b, "External Routes", items, func(item previewItem) bool {
		return item.FinalAction == "route_to_study" || item.FinalAction == "route_to_control"
	})
	b.WriteString("\n## Stop Point\n\n")
	if result.PendingDecisions > 0 {
		b.WriteString("Fill missing `user_decision` values in the review CSV, then regenerate this preview. No migration should be published yet.\n")
	} else {
		b.WriteString("Review this preview. Format/card drafts may be checked in Sova.Control, but nothing should be published into InSync v1.0 before explicit user approval.\n")
	}
	return b.String()
}

func writePreviewCounts(b *strings.Builder, title string, counts map[string]int) {
	b.WriteString("\n## ")
	b.WriteString(title)
	b.WriteString("\n\n")
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		b.WriteString("None.\n")
		return
	}
	for _, key := range keys {
		fmt.Fprintf(b, "- `%s`: `%d`\n", key, counts[key])
	}
}

func writePreviewSection(b *strings.Builder, title string, items []previewItem, include func(previewItem) bool) {
	b.WriteString("\n## ")
	b.WriteString(title)
	b.WriteString("\n\n")
	wrote := false
	currentTarget := ""
	for _, item := range items {
		if !include(item) {
			continue
		}
		if item.Target != currentTarget {
			if wrote {
				b.WriteString("\n")
			}
			currentTarget = item.Target
			fmt.Fprintf(b, "### %s\n\n", currentTarget)
		}
		wrote = true
		link := item.Record.MessageLink
		if link == "" {
			link = strconv.FormatInt(item.Record.ChatID, 10) + "/" + strconv.Itoa(item.Record.MessageID)
		}
		fmt.Fprintf(b, "- `%s` `%s` [%s](%s): %s\n",
			item.Record.MessageDate.Format("2006-01-02 15:04"),
			item.Record.DetectedType,
			strconv.Itoa(item.Record.MessageID),
			link,
			mdCell(item.Record.ShortSummary),
		)
	}
	if !wrote {
		b.WriteString("None.\n")
	}
}

func countPreview(items []previewItem, keyFn func(previewItem) string) map[string]int {
	counts := map[string]int{}
	for _, item := range items {
		key := keyFn(item)
		if key == "" {
			key = "unknown"
		}
		counts[key]++
	}
	return counts
}

func writePreviewCSV(path string, items []previewItem) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	header := []string{"source_topic", "message_date", "message_link", "short_summary", "detected_type", "audit_decision", "confidence", "user_decision", "user_comment", "final_action", "target", "reason"}
	if err := writer.Write(header); err != nil {
		return err
	}
	for _, item := range items {
		record := item.Record
		row := []string{
			record.SourceTopic,
			record.MessageDate.Format(time.RFC3339),
			record.MessageLink,
			record.ShortSummary,
			record.DetectedType,
			record.ModelDecision,
			record.Confidence,
			item.UserDecision,
			item.UserComment,
			item.FinalAction,
			item.Target,
			record.Reason,
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	return writer.Error()
}

func auditRecordReviewKey(record sqlitestore.WorkspaceAuditRecord) string {
	if strings.TrimSpace(record.MessageLink) != "" {
		return "link:" + strings.TrimSpace(record.MessageLink)
	}
	return "fallback:" + record.SourceTopic + "|" + record.MessageDate.Format(time.RFC3339) + "|" + record.ShortSummary
}

func reviewRowKey(row reviewRow) string {
	if strings.TrimSpace(row.MessageLink) != "" {
		return "link:" + strings.TrimSpace(row.MessageLink)
	}
	return "fallback:" + row.SourceTopic + "|" + row.MessageDate + "|" + row.ShortSummary
}

func sortPreviewItems(items []previewItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].FinalAction != items[j].FinalAction {
			return items[i].FinalAction < items[j].FinalAction
		}
		if items[i].Target != items[j].Target {
			return items[i].Target < items[j].Target
		}
		if !items[i].Record.MessageDate.Equal(items[j].Record.MessageDate) {
			return items[i].Record.MessageDate.Before(items[j].Record.MessageDate)
		}
		return items[i].Record.MessageID < items[j].Record.MessageID
	})
}
