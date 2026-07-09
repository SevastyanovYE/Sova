package workspace

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type PinnedMigrationOptions struct {
	OutputDir string
	Limit     int
	Now       time.Time
}

type PinnedMigrationResult struct {
	RunID       string
	ArtifactDir string
	ReviewMD    string
	ReviewCSV   string
	Items       int
}

type pinnedMigrationItem struct {
	LegacyTopic          string
	SourceMessageLink    string
	SourceMessageIDs     []int
	ClusterID            string
	ShortTitle           string
	ShortSummary         string
	DetectedType         string
	SuggestedTargetTopic string
	Confidence           string
	Reason               string
	NeedsUserReview      bool
	ReviewStatus         string
	UserComment          string
}

type telegramLinkRef struct {
	Raw       string
	ChatIDRaw string
	MessageID int
}

var internalTelegramLinkRefPattern = regexp.MustCompile(`(?:https?://)?t\.me/c/(\d+)/(?:\d+/)?(\d+)`)

func PreparePinnedMigrationReview(ctx context.Context, cfg config.Config, store *sqlitestore.Store, opts PinnedMigrationOptions) (PinnedMigrationResult, error) {
	if store == nil {
		return PinnedMigrationResult{}, fmt.Errorf("store is required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	source, err := ResolveLegacySource(ctx, cfg, store)
	if err != nil {
		return PinnedMigrationResult{}, err
	}
	topics, err := store.WorkspaceTopicsBySource(ctx, source.Ref)
	if err != nil {
		return PinnedMigrationResult{}, err
	}
	messages, err := store.WorkspaceMessagesBySourceRef(ctx, source.Ref, opts.Limit)
	if err != nil {
		return PinnedMigrationResult{}, err
	}
	if len(messages) == 0 {
		return PinnedMigrationResult{}, fmt.Errorf("no indexed legacy messages for %s; run `sova workspace sync-legacy` first", source.Ref)
	}
	topicIndex := indexTopics(topics)
	classifyCtx := buildClassificationContext(messages, topicIndex)
	byID := map[int]sqlitestore.WorkspaceSourceMessage{}
	for _, message := range messages {
		byID[message.MessageID] = message
	}

	var pinned []sqlitestore.WorkspaceSourceMessage
	for _, message := range messages {
		meta := extractMessageMeta(message.RawJSON)
		topic := topicTitleForMessage(message, topicIndex, meta)
		if !isPinnedMigrationSourceTopic(topic) {
			continue
		}
		_, pinTarget := classifyCtx.PinTargets[message.MessageID]
		if meta.Pinned || pinTarget {
			pinned = append(pinned, message)
		}
	}
	sort.Slice(pinned, func(i, j int) bool {
		if pinned[i].MessageID != pinned[j].MessageID {
			return pinned[i].MessageID < pinned[j].MessageID
		}
		return pinned[i].Date.Before(pinned[j].Date)
	})

	items := make([]pinnedMigrationItem, 0, len(pinned))
	seen := map[string]struct{}{}
	for _, pin := range pinned {
		meta := extractMessageMeta(pin.RawJSON)
		legacyTopic := topicTitleForMessage(pin, topicIndex, meta)
		links := legacyInternalLinksForMessage(pin)
		if len(links) == 0 {
			item := buildPinnedMigrationItem(legacyTopic, pin, []int{pin.MessageID}, pin.SourceLink, "pinned message without linked children")
			items = appendPinnedMigrationItem(items, seen, item)
			continue
		}
		if isTemplatesTopic(legacyTopic) {
			items = appendTemplatePinnedMigrationItems(items, seen, legacyTopic, pin, links, byID)
			continue
		}
		for _, link := range links {
			linked, ok := byID[link.MessageID]
			if !ok {
				item := missingPinnedMigrationLinkItem(legacyTopic, pin, link)
				items = appendPinnedMigrationItem(items, seen, item)
				continue
			}
			item := buildPinnedMigrationItem(legacyTopic, linked, []int{linked.MessageID}, linked.SourceLink, "linked from old pinned message "+strconv.Itoa(pin.MessageID))
			items = appendPinnedMigrationItem(items, seen, item)
		}
	}

	runID := now.UTC().Format("20060102T150405Z")
	outputDir := strings.TrimSpace(opts.OutputDir)
	if outputDir == "" {
		outputDir = filepath.Join(cfg.StateDir, "artifacts", "workspace", "migration", runID)
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return PinnedMigrationResult{}, err
	}
	result := PinnedMigrationResult{
		RunID:       runID,
		ArtifactDir: outputDir,
		ReviewMD:    filepath.Join(outputDir, "pinned_migration_review.md"),
		ReviewCSV:   filepath.Join(outputDir, "pinned_migration_review.csv"),
		Items:       len(items),
	}
	if err := writePinnedMigrationReviewCSV(result.ReviewCSV, items); err != nil {
		return PinnedMigrationResult{}, err
	}
	if err := os.WriteFile(result.ReviewMD, []byte(renderPinnedMigrationReviewMarkdown(now, source, items)), 0o600); err != nil {
		return PinnedMigrationResult{}, err
	}
	return result, nil
}

func appendTemplatePinnedMigrationItems(items []pinnedMigrationItem, seen map[string]struct{}, legacyTopic string, pin sqlitestore.WorkspaceSourceMessage, links []telegramLinkRef, byID map[int]sqlitestore.WorkspaceSourceMessage) []pinnedMigrationItem {
	hasPersonalityA := false
	hasPersonalityB := false
	hasPersonalityLink := false
	for _, link := range links {
		if link.MessageID == 342 {
			hasPersonalityA = true
			hasPersonalityLink = true
		}
		if link.MessageID == 345 {
			hasPersonalityB = true
			hasPersonalityLink = true
		}
	}
	if hasPersonalityLink {
		_, hasPersonalityA = byID[342]
		_, hasPersonalityB = byID[345]
	}
	if hasPersonalityA && hasPersonalityB {
		link := workspaceMessageLink(pin.ChatID, 17, 342)
		if msg, ok := byID[342]; ok && msg.SourceLink != "" {
			link = msg.SourceLink
		}
		item := pinnedMigrationItem{
			LegacyTopic:          legacyTopic,
			SourceMessageLink:    link,
			SourceMessageIDs:     []int{342, 345},
			ClusterID:            "legacy-template-342-345",
			ShortTitle:           "Карточка личности",
			ShortSummary:         "Special case: объединить две старые linked-заготовки в один prompt.",
			DetectedType:         "template_document",
			SuggestedTargetTopic: "Заготовки",
			Confidence:           "high",
			Reason:               "explicit special case from migration request",
			NeedsUserReview:      true,
			ReviewStatus:         "pending_review",
		}
		items = appendPinnedMigrationItem(items, seen, item)
	}
	for _, link := range links {
		if hasPersonalityA && hasPersonalityB && (link.MessageID == 342 || link.MessageID == 345) {
			continue
		}
		linked, ok := byID[link.MessageID]
		if !ok {
			item := missingPinnedMigrationLinkItem(legacyTopic, pin, link)
			items = appendPinnedMigrationItem(items, seen, item)
			continue
		}
		cards := splitPromptCards(linked.Text)
		if len(cards) == 0 {
			item := buildPinnedMigrationItem(legacyTopic, linked, []int{linked.MessageID}, linked.SourceLink, "template linked from old pinned message "+strconv.Itoa(pin.MessageID))
			items = appendPinnedMigrationItem(items, seen, item)
			continue
		}
		for _, card := range cards {
			item := pinnedMigrationItem{
				LegacyTopic:          legacyTopic,
				SourceMessageLink:    linked.SourceLink,
				SourceMessageIDs:     []int{linked.MessageID},
				ClusterID:            fmt.Sprintf("legacy-template-%d-card-%d", linked.MessageID, card.Number),
				ShortTitle:           cleanMigrationTitle(card.Title),
				ShortSummary:         summarizeMessage(card.Body, linked.MediaType),
				DetectedType:         "template_document",
				SuggestedTargetTopic: "Заготовки",
				Confidence:           "high",
				Reason:               "split prompt by '<number>. <prompt title>' from linked old pinned material",
				NeedsUserReview:      true,
				ReviewStatus:         "pending_review",
			}
			items = appendPinnedMigrationItem(items, seen, item)
		}
	}
	return items
}

func buildPinnedMigrationItem(legacyTopic string, message sqlitestore.WorkspaceSourceMessage, ids []int, link, reason string) pinnedMigrationItem {
	detectedType, target, confidence, routeReason := classifyPinnedMigrationTarget(legacyTopic, message.Text)
	if strings.TrimSpace(reason) != "" {
		routeReason = reason + "; " + routeReason
	}
	title := cleanMigrationTitle(firstNonEmptyLine(message.Text))
	if title == "" {
		title = summarizeMessage(message.Text, message.MediaType)
	}
	if title == "" {
		title = "[" + emptyLabel(message.MediaType, "message") + "]"
	}
	return pinnedMigrationItem{
		LegacyTopic:          legacyTopic,
		SourceMessageLink:    firstNonEmpty(link, message.SourceLink, workspaceMessageLink(message.ChatID, 0, message.MessageID)),
		SourceMessageIDs:     ids,
		ClusterID:            "legacy-" + strings.Trim(strings.ToLower(legacyTopic), " ") + "-" + joinInts(ids, "-"),
		ShortTitle:           title,
		ShortSummary:         summarizeMessage(stripIgnoredMigrationTags(message.Text), message.MediaType),
		DetectedType:         detectedType,
		SuggestedTargetTopic: target,
		Confidence:           confidence,
		Reason:               routeReason,
		NeedsUserReview:      true,
		ReviewStatus:         "pending_review",
	}
}

func missingPinnedMigrationLinkItem(legacyTopic string, pin sqlitestore.WorkspaceSourceMessage, link telegramLinkRef) pinnedMigrationItem {
	return pinnedMigrationItem{
		LegacyTopic:          legacyTopic,
		SourceMessageLink:    link.Raw,
		SourceMessageIDs:     []int{link.MessageID},
		ClusterID:            "legacy-missing-" + strconv.Itoa(link.MessageID),
		ShortTitle:           "Linked message " + strconv.Itoa(link.MessageID),
		ShortSummary:         "Linked message is not indexed locally yet.",
		DetectedType:         "missing_linked_message",
		SuggestedTargetTopic: defaultPinnedMigrationTarget(legacyTopic),
		Confidence:           "low",
		Reason:               "old pinned message " + strconv.Itoa(pin.MessageID) + " links to a message missing from SQLite; sync/backfill may be needed",
		NeedsUserReview:      true,
		ReviewStatus:         "pending_review",
	}
}

func appendPinnedMigrationItem(items []pinnedMigrationItem, seen map[string]struct{}, item pinnedMigrationItem) []pinnedMigrationItem {
	key := item.LegacyTopic + "|" + item.ClusterID + "|" + joinInts(item.SourceMessageIDs, "+")
	if _, ok := seen[key]; ok {
		return items
	}
	seen[key] = struct{}{}
	return append(items, item)
}

func classifyPinnedMigrationTarget(legacyTopic, text string) (detectedType, target, confidence, reason string) {
	lowerTopic := strings.ToLower(legacyTopic)
	lowerText := strings.ToLower(text)
	switch {
	case isTemplatesTopic(legacyTopic):
		return "template_document", "Заготовки", "high", "old Заготовки pinned material"
	case strings.Contains(lowerTopic, "полез"):
		return "useful_material", "Полезное", "high", "old Полезное pinned material"
	case strings.Contains(lowerText, "#опыт"):
		return "experience", "Опыт", "medium", "#опыт tag"
	case strings.Contains(lowerText, "#аниме") || strings.Contains(lowerText, "#поэзия"):
		return "collection_item", "Коллекции", "medium", "collection-like legacy tag"
	case strings.Contains(lowerText, "#идеи") || strings.Contains(lowerText, "#мюсли") || strings.Contains(lowerText, "#связи"):
		return "draft_note", "Заметки", "medium", "note-like legacy tag"
	default:
		return "draft_note", "Заметки", "medium", "old Заметки pinned material"
	}
}

func defaultPinnedMigrationTarget(legacyTopic string) string {
	switch {
	case isTemplatesTopic(legacyTopic):
		return "Заготовки"
	case strings.Contains(strings.ToLower(legacyTopic), "полез"):
		return "Полезное"
	default:
		return "Заметки"
	}
}

func isPinnedMigrationSourceTopic(topic string) bool {
	lower := strings.ToLower(strings.TrimSpace(topic))
	return strings.Contains(lower, "заметки") ||
		strings.Contains(lower, "заготовки") ||
		strings.Contains(lower, "полез")
}

func isTemplatesTopic(topic string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(topic)), "заготовки")
}

func legacyInternalLinks(text string, sourceChatID int64) []telegramLinkRef {
	var out []telegramLinkRef
	seen := map[int]struct{}{}
	sourceRaw := normalizedInternalChatID(sourceChatID)
	for _, match := range internalTelegramLinkRefPattern.FindAllStringSubmatchIndex(text, -1) {
		if len(match) < 6 {
			continue
		}
		raw := text[match[0]:match[1]]
		chatRaw := text[match[2]:match[3]]
		if sourceRaw != "" && normalizedInternalChatIDRaw(chatRaw) != sourceRaw {
			continue
		}
		messageID, err := strconv.Atoi(text[match[4]:match[5]])
		if err != nil || messageID == 0 {
			continue
		}
		if _, ok := seen[messageID]; ok {
			continue
		}
		seen[messageID] = struct{}{}
		out = append(out, telegramLinkRef{Raw: raw, ChatIDRaw: chatRaw, MessageID: messageID})
	}
	return out
}

func legacyInternalLinksForMessage(message sqlitestore.WorkspaceSourceMessage) []telegramLinkRef {
	text := message.Text
	for _, link := range telegramLinksFromRawJSON(message.RawJSON) {
		text += "\n" + link
	}
	return legacyInternalLinks(text, message.ChatID)
}

func telegramLinksFromRawJSON(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var root any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case string:
			for _, match := range internalTelegramLinkRefPattern.FindAllString(typed, -1) {
				if _, ok := seen[match]; ok {
					continue
				}
				seen[match] = struct{}{}
				out = append(out, match)
			}
		}
	}
	walk(root)
	return out
}

func normalizedInternalChatID(chatID int64) string {
	raw := strconv.FormatInt(chatID, 10)
	return normalizedInternalChatIDRaw(raw)
}

func normalizedInternalChatIDRaw(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "-100")
	raw = strings.TrimPrefix(raw, "-")
	return raw
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(stripIgnoredMigrationTags(line))
		if line != "" {
			return line
		}
	}
	return ""
}

func cleanMigrationTitle(value string) string {
	value = strings.TrimSpace(stripIgnoredMigrationTags(value))
	value = strings.Trim(value, " \t\n\r-*•")
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:77]) + "..."
	}
	return value
}

func stripIgnoredMigrationTags(value string) string {
	for _, tag := range []string{"#знания"} {
		value = strings.ReplaceAll(value, tag, "")
		value = strings.ReplaceAll(value, strings.ToUpper(tag), "")
	}
	return strings.Join(strings.Fields(value), " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func joinInts(values []int, sep string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, sep)
}

func writePinnedMigrationReviewCSV(path string, items []pinnedMigrationItem) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	header := []string{
		"legacy_topic",
		"source_message_link",
		"source_message_ids",
		"cluster_id",
		"short_title",
		"short_summary",
		"detected_type",
		"suggested_target_topic",
		"confidence",
		"reason",
		"needs_user_review",
		"review_status",
		"user_comment",
	}
	if err := writer.Write(header); err != nil {
		return err
	}
	for _, item := range items {
		if err := writer.Write([]string{
			item.LegacyTopic,
			item.SourceMessageLink,
			joinInts(item.SourceMessageIDs, "+"),
			item.ClusterID,
			item.ShortTitle,
			item.ShortSummary,
			item.DetectedType,
			item.SuggestedTargetTopic,
			item.Confidence,
			item.Reason,
			strconv.FormatBool(item.NeedsUserReview),
			item.ReviewStatus,
			item.UserComment,
		}); err != nil {
			return err
		}
	}
	return writer.Error()
}

func renderPinnedMigrationReviewMarkdown(now time.Time, source sqlitestore.TelegramSource, items []pinnedMigrationItem) string {
	var b strings.Builder
	b.WriteString("# Pinned Migration Review\n\n")
	fmt.Fprintf(&b, "Generated: `%s`\n\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Legacy source: `%s`", source.Ref)
	if source.Title != "" {
		fmt.Fprintf(&b, " (%s)", source.Title)
	}
	b.WriteString("\n\n")
	b.WriteString("Scope: old pinned material from `Заметки`, `Заготовки`, and `Полезное`. No Workspace publication is performed by this artifact pass.\n\n")
	fmt.Fprintf(&b, "Items: `%d`\n\n", len(items))
	b.WriteString("| legacy_topic | source | ids | title | summary | type | target | confidence | review |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, item := range items {
		sourceLink := item.SourceMessageLink
		if sourceLink == "" {
			sourceLink = joinInts(item.SourceMessageIDs, "+")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | `%s` | %s | `%s` | `%s` |\n",
			mdCell(item.LegacyTopic),
			mdCell(sourceLink),
			mdCell(joinInts(item.SourceMessageIDs, "+")),
			mdCell(item.ShortTitle),
			mdCell(item.ShortSummary),
			item.DetectedType,
			mdCell(item.SuggestedTargetTopic),
			item.Confidence,
			item.ReviewStatus,
		)
	}
	b.WriteString("\nReview statuses are intentionally left at `pending_review` until the user approves concrete transfers.\n")
	return b.String()
}
