package workspace

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

const (
	TypeTask                    = "task"
	TypeDeferredTask            = "deferred_task"
	TypeDraftNote               = "draft_note"
	TypeNoteDocument            = "note_document"
	TypeUsefulMaterial          = "useful_material"
	TypeExperience              = "experience"
	TypeIdea                    = "idea"
	TypeTemplateDocument        = "template_document"
	TypeCollectionItem          = "collection_item"
	TypeExternalBranchReference = "external_branch_reference"

	DecisionTake         = "take"
	DecisionArchive      = "archive"
	DecisionReview       = "review"
	DecisionSkipDoneTask = "skip_done_task"
	DecisionRouteStudy   = "route_to_study"
	DecisionRouteControl = "route_to_control"
	DecisionTrash        = "trash"
)

var allowedUserDecisions = []string{"take", "archive", "trash", "study", "control", "collection", "later"}

type Check struct {
	Name    string
	Status  string
	Message string
}

type AuditOptions struct {
	DryRun bool
	Limit  int
	Now    time.Time
}

type AuditResult struct {
	RunID            int64
	ArtifactRunID    string
	SourceRef        string
	ArtifactDir      string
	SummaryPath      string
	ReviewCSVPath    string
	ReviewMDPath     string
	ControlCardsPath string
	TopicPinsPath    string
	Summary          string
	Messages         int
	ReviewCount      int
	DryRun           bool
}

type auditStats struct {
	GeneratedAt           time.Time
	SourceRef             string
	SourceTitle           string
	TotalMessages         int
	ReviewCandidates      int
	MediaPlaceholders     int
	PinnedMessages        int
	PinnedLinked          int
	PunctuationTrash      int
	PromptCards           int
	AutoApprovedTasks     int
	AutoApprovedTemplates int
	LongMessages          int
	EditedMessages        int
	Topics                map[string]int
	Types                 map[string]int
	Decisions             map[string]int
	AdditionalTypes       []string
	Risks                 []string
	NextAction            string
	HeuristicOnly         bool
}

type messageMeta struct {
	TopicID        int
	TopMessageID   int
	ReplyMessageID int
	EditDate       *time.Time
	Pinned         bool
	Edited         bool
}

type classifiedMessage struct {
	Record      sqlitestore.WorkspaceAuditRecord
	NeedsReview bool
}

type classificationContext struct {
	Topics             topicIndex
	LatestTasks        map[int]struct{}
	LatestTemplates    map[int]struct{}
	PinTargets         map[int]struct{}
	PinnedLinked       map[int]struct{}
	PinnedLinkedSource map[int]int
	PromptCards        map[int][]promptCard
}

type messageWithTopic struct {
	Message sqlitestore.WorkspaceSourceMessage
	Topic   string
	Meta    messageMeta
}

type promptCard struct {
	Number int
	Title  string
	Body   string
}

func DoctorChecks(ctx context.Context, cfg config.Config, store *sqlitestore.Store) []Check {
	checks := []Check{
		configuredCheck("workspace_legacy_source", strings.TrimSpace(cfg.Workspace.LegacySource) != "", "set SOVA_WORKSPACE_LEGACY_SOURCE to the old InSync source"),
		configuredCheck("telegram_credentials", cfg.TelegramAppID != 0 && strings.TrimSpace(cfg.TelegramAppHash) != "", "set SOVA_TELEGRAM_APP_ID and SOVA_TELEGRAM_APP_HASH"),
		configuredCheck("workspace_group", cfg.WorkspaceConfigured(), "set Workspace bot token, InSync v1.0 chat ID, and seven topic IDs"),
		configuredCheck("control_group", cfg.ControlConfigured(), "set Control bot token, chat ID, and eight topic IDs"),
	}
	if store == nil {
		return append(checks, Check{Name: "workspace_database", Status: "needs_input", Message: "open SQLite store before audit"})
	}
	source, err := ResolveLegacySource(ctx, cfg, store)
	if err != nil {
		checks = append(checks, Check{Name: "legacy_source_index", Status: "needs_input", Message: err.Error()})
		return checks
	}
	checks = append(checks, Check{Name: "legacy_source_index", Status: "ok", Message: source.Ref})
	topics, err := store.WorkspaceTopicsBySource(ctx, source.Ref)
	if err != nil {
		checks = append(checks, Check{Name: "workspace_topics", Status: "error", Message: err.Error()})
	} else if len(topics) == 0 {
		checks = append(checks, Check{Name: "workspace_topics", Status: "needs_input", Message: "run `sova workspace discover` to cache legacy forum topics"})
	} else {
		checks = append(checks, Check{Name: "workspace_topics", Status: "ok", Message: fmt.Sprintf("%d topics cached", len(topics))})
	}
	messages, err := store.WorkspaceMessagesBySourceRef(ctx, source.Ref, 1)
	if err != nil {
		checks = append(checks, Check{Name: "legacy_messages", Status: "error", Message: err.Error()})
	} else if len(messages) == 0 {
		checks = append(checks, Check{Name: "legacy_messages", Status: "needs_input", Message: "sync/index old InSync messages before audit"})
	} else {
		checks = append(checks, Check{Name: "legacy_messages", Status: "ok", Message: "messages are available in SQLite"})
	}
	return checks
}

func FormatChecks(checks []Check) string {
	var b strings.Builder
	for _, check := range checks {
		fmt.Fprintf(&b, "%-28s %-12s %s\n", check.Name, check.Status, check.Message)
	}
	return b.String()
}

func configuredCheck(name string, ready bool, missingMessage string) Check {
	if ready {
		return Check{Name: name, Status: "ok", Message: "configured"}
	}
	return Check{Name: name, Status: "needs_input", Message: missingMessage}
}

func ResolveLegacySource(ctx context.Context, cfg config.Config, store *sqlitestore.Store) (sqlitestore.TelegramSource, error) {
	configured := strings.TrimSpace(cfg.Workspace.LegacySource)
	if configured == "" {
		return sqlitestore.TelegramSource{}, fmt.Errorf("SOVA_WORKSPACE_LEGACY_SOURCE is required")
	}
	if ref, ok := stableSourceRefCandidate(configured); ok {
		source, err := store.TelegramSourceByRef(ctx, ref)
		if err == nil {
			return source, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return sqlitestore.TelegramSource{}, err
		}
		return sqlitestore.TelegramSource{}, fmt.Errorf("legacy source %s is not indexed yet; run `sova workspace discover` or sync it first", ref)
	}
	if isTelegramJoinLink(configured) {
		source, ok, err := singleDiscoveredWorkspaceSource(ctx, store)
		if err != nil {
			return sqlitestore.TelegramSource{}, err
		}
		if ok {
			return source, nil
		}
	}

	sources, err := store.TelegramSources(ctx)
	if err != nil {
		return sqlitestore.TelegramSource{}, err
	}
	needle := normalizeSourceName(configured)
	for _, source := range sources {
		if source.Ref == configured || normalizeSourceName(source.Username) == needle ||
			normalizeSourceName(source.Title) == needle || normalizeSourceName(source.Ref) == needle {
			return source, nil
		}
	}
	return sqlitestore.TelegramSource{}, fmt.Errorf("legacy source %q is not indexed yet; run `sova workspace discover` or configure an explicit telegram:channel:<id> ref", configured)
}

func singleDiscoveredWorkspaceSource(ctx context.Context, store *sqlitestore.Store) (sqlitestore.TelegramSource, bool, error) {
	sources, err := store.TelegramSources(ctx)
	if err != nil {
		return sqlitestore.TelegramSource{}, false, err
	}
	var match sqlitestore.TelegramSource
	matches := 0
	for _, source := range sources {
		topics, err := store.WorkspaceTopicsBySource(ctx, source.Ref)
		if err != nil {
			return sqlitestore.TelegramSource{}, false, err
		}
		if len(topics) == 0 {
			continue
		}
		match = source
		matches++
	}
	return match, matches == 1, nil
}

func RunAudit(ctx context.Context, cfg config.Config, store *sqlitestore.Store, opts AuditOptions) (AuditResult, error) {
	if store == nil {
		return AuditResult{}, fmt.Errorf("store is required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	source, err := ResolveLegacySource(ctx, cfg, store)
	if err != nil {
		return AuditResult{}, err
	}
	topics, err := store.WorkspaceTopicsBySource(ctx, source.Ref)
	if err != nil {
		return AuditResult{}, err
	}
	messages, err := store.WorkspaceMessagesBySourceRef(ctx, source.Ref, opts.Limit)
	if err != nil {
		return AuditResult{}, err
	}
	if len(messages) == 0 {
		return AuditResult{}, fmt.Errorf("no Telegram messages indexed for %s; sync old InSync before running Workspace audit", source.Ref)
	}

	topicIndex := indexTopics(topics)
	classifyCtx := buildClassificationContext(messages, topicIndex)
	classified := make([]classifiedMessage, 0, len(messages))
	stats := auditStats{
		GeneratedAt:   now,
		SourceRef:     source.Ref,
		SourceTitle:   source.Title,
		Topics:        map[string]int{},
		Types:         map[string]int{},
		Decisions:     map[string]int{},
		NextAction:    "Review the much smaller workspace_review_candidates.csv, fill user_decision/user_comment only where needed, then run Stage 2 merge/preview. Formatted card drafts must be checked in Sova.Control before any Workspace publication.",
		HeuristicOnly: true,
		Risks: []string{
			"Stage 1 uses deterministic heuristics when no Workspace LLM classifier is configured.",
			"Topic metadata is best-effort and depends on discovered forum topics plus Telegram reply headers in stored raw records.",
			"No migration, posting, deletion, or editing is performed by this audit.",
		},
	}
	if len(topics) == 0 {
		stats.Risks = append(stats.Risks, "No cached forum topics were found; run `sova workspace discover` for better topic-aware grouping.")
	}
	for _, message := range messages {
		item := classifyMessage(message, classifyCtx)
		classified = append(classified, item)
		record := item.Record
		stats.TotalMessages++
		stats.Topics[emptyLabel(record.SourceTopic, "unknown")]++
		stats.Types[record.DetectedType]++
		stats.Decisions[record.ModelDecision]++
		if record.MediaType != "" && strings.TrimSpace(message.Text) == "" {
			stats.MediaPlaceholders++
		}
		if record.Pinned {
			stats.PinnedMessages++
		}
		if _, ok := classifyCtx.PinnedLinked[record.MessageID]; ok {
			stats.PinnedLinked++
		}
		if record.ModelDecision == DecisionTrash && strings.Contains(record.Reason, "punctuation-only") {
			stats.PunctuationTrash++
		}
		if len(classifyCtx.PromptCards[record.MessageID]) > 0 {
			stats.PromptCards += len(classifyCtx.PromptCards[record.MessageID])
		}
		if record.ModelDecision == DecisionTake && record.SuggestedTarget == "Задачи" {
			stats.AutoApprovedTasks++
		}
		if record.ModelDecision == DecisionTake && record.SuggestedTarget == "Заготовки" {
			stats.AutoApprovedTemplates++
		}
		if record.LongMessage {
			stats.LongMessages++
		}
		if record.Edited {
			stats.EditedMessages++
		}
		if item.NeedsReview {
			stats.ReviewCandidates++
		}
	}

	summary := renderSummary(stats)
	result := AuditResult{
		SourceRef:   source.Ref,
		Summary:     summary,
		Messages:    stats.TotalMessages,
		ReviewCount: stats.ReviewCandidates,
		DryRun:      opts.DryRun,
	}
	if opts.DryRun {
		return result, nil
	}

	artifactRunID := now.UTC().Format("20060102T150405Z")
	artifactDir := filepath.Join(cfg.StateDir, "artifacts", "workspace", "audit", artifactRunID)
	run, err := store.StartWorkspaceAudit(ctx, source.Ref, false, artifactDir, now)
	if err != nil {
		return AuditResult{}, err
	}
	for i := range classified {
		classified[i].Record.RunID = run.ID
	}
	if err := store.InsertWorkspaceAuditRecords(ctx, auditRecords(classified), now); err != nil {
		_ = store.FinishWorkspaceAudit(ctx, run.ID, "failed", "workspace audit record insert failed", err.Error(), time.Now().UTC())
		return AuditResult{}, err
	}
	paths, err := writeArtifacts(artifactDir, summary, classified, classifyCtx)
	if err != nil {
		_ = store.FinishWorkspaceAudit(ctx, run.ID, "failed", "workspace audit artifact write failed", err.Error(), time.Now().UTC())
		return AuditResult{}, err
	}
	if err := store.FinishWorkspaceAudit(ctx, run.ID, "success", fmt.Sprintf("%d messages audited, %d review candidates", stats.TotalMessages, stats.ReviewCandidates), "", time.Now().UTC()); err != nil {
		return AuditResult{}, err
	}
	result.RunID = run.ID
	result.ArtifactRunID = artifactRunID
	result.ArtifactDir = artifactDir
	result.SummaryPath = paths.summary
	result.ReviewCSVPath = paths.csv
	result.ReviewMDPath = paths.markdown
	result.ControlCardsPath = paths.controlCard
	result.TopicPinsPath = paths.topicPins
	return result, nil
}

func buildClassificationContext(messages []sqlitestore.WorkspaceSourceMessage, topics topicIndex) classificationContext {
	ctx := classificationContext{
		Topics:             topics,
		LatestTasks:        map[int]struct{}{},
		LatestTemplates:    map[int]struct{}{},
		PinTargets:         map[int]struct{}{},
		PinnedLinked:       map[int]struct{}{},
		PinnedLinkedSource: map[int]int{},
		PromptCards:        map[int][]promptCard{},
	}
	byID := map[int]messageWithTopic{}
	var taskMessages []messageWithTopic
	var templateMessages []messageWithTopic
	for _, message := range messages {
		meta := extractMessageMeta(message.RawJSON)
		topic := topicTitleForMessage(message, topics, meta)
		item := messageWithTopic{Message: message, Topic: topic, Meta: meta}
		byID[message.MessageID] = item
		lowerTopic := strings.ToLower(topic)
		switch {
		case containsTopic(lowerTopic, "задачи"):
			taskMessages = append(taskMessages, item)
		case containsTopic(lowerTopic, "заготовки"):
			templateMessages = append(templateMessages, item)
		}
		if message.Kind == "service" && message.MediaType == "messageActionPinMessage" && meta.ReplyMessageID != 0 {
			ctx.PinTargets[meta.ReplyMessageID] = struct{}{}
		}
	}
	markLatest(ctx.LatestTasks, taskMessages, 10)
	markLatest(ctx.LatestTemplates, templateMessages, 10)
	for messageID := range ctx.PinTargets {
		item, ok := byID[messageID]
		if !ok {
			continue
		}
		for _, linkedID := range internalTelegramMessageLinks(item.Message.Text, item.Message.ChatID) {
			if _, ok := byID[linkedID]; !ok {
				continue
			}
			ctx.PinnedLinked[linkedID] = struct{}{}
			ctx.PinnedLinkedSource[linkedID] = messageID
		}
	}
	for _, item := range templateMessages {
		if _, ok := ctx.LatestTemplates[item.Message.MessageID]; !ok {
			if _, ok := ctx.PinnedLinked[item.Message.MessageID]; !ok {
				continue
			}
		}
		if !looksTemplate(strings.ToLower(item.Message.Text)) {
			continue
		}
		cards := splitPromptCards(item.Message.Text)
		if len(cards) > 0 {
			ctx.PromptCards[item.Message.MessageID] = cards
		}
	}
	return ctx
}

func topicTitleForMessage(message sqlitestore.WorkspaceSourceMessage, topics topicIndex, meta messageMeta) string {
	topic := topics.lookup(meta.TopicID, meta.TopMessageID, message.MessageID)
	if topic.Title != "" {
		return topic.Title
	}
	return message.SourceTitle
}

func markLatest(out map[int]struct{}, messages []messageWithTopic, limit int) {
	sort.Slice(messages, func(i, j int) bool {
		if !messages[i].Message.Date.Equal(messages[j].Message.Date) {
			return messages[i].Message.Date.After(messages[j].Message.Date)
		}
		return messages[i].Message.MessageID > messages[j].Message.MessageID
	})
	for i, item := range messages {
		if i >= limit {
			break
		}
		out[item.Message.MessageID] = struct{}{}
	}
}

func classifyMessage(message sqlitestore.WorkspaceSourceMessage, ctx classificationContext) classifiedMessage {
	meta := extractMessageMeta(message.RawJSON)
	topic := ctx.Topics.lookup(meta.TopicID, meta.TopMessageID, message.MessageID)
	sourceTopic := topicTitleForMessage(message, ctx.Topics, meta)
	text := strings.TrimSpace(message.Text)
	record := sqlitestore.WorkspaceAuditRecord{
		SourceRef:       message.SourceRef,
		ChatID:          message.ChatID,
		MessageID:       message.MessageID,
		SourceTopic:     sourceTopic,
		TopicID:         firstNonZero(meta.TopicID, topic.TopicID),
		TopMessageID:    firstNonZero(meta.TopMessageID, topic.TopMessageID),
		MessageDate:     message.Date,
		EditDate:        meta.EditDate,
		MessageLink:     message.SourceLink,
		ShortSummary:    summarizeMessage(text, message.MediaType),
		MediaType:       message.MediaType,
		Pinned:          meta.Pinned,
		LongMessage:     runeLen(text) >= 700,
		Edited:          meta.Edited,
		DetectedType:    TypeDraftNote,
		ModelDecision:   DecisionReview,
		Confidence:      "low",
		SuggestedTarget: "Review",
		Reason:          "heuristic fallback: uncertain personal workspace material",
	}
	if message.Kind == "service" && message.MediaType == "messageActionPinMessage" {
		setDecision(&record, TypeExternalBranchReference, DecisionArchive, "high", "Legacy archive", "service pin event is metadata; linked pinned content is handled separately when available")
		return classifiedMessage{Record: record, NeedsReview: false}
	}
	if applyTaggedMigrationDecision(&record, lowerTagText(text)) {
		return classifiedMessage{Record: record, NeedsReview: false}
	}
	if message.MediaType != "" {
		record.ShortSummary = summarizeMessage(text, message.MediaType)
		setDecision(&record, TypeDraftNote, DecisionReview, "low", "Review", "media or unsupported attachment must stay for user review")
		return classifiedMessage{Record: record, NeedsReview: true}
	}
	if text != "" && isPunctuationOnly(text) && message.MediaType == "" {
		setDecision(&record, TypeDraftNote, DecisionTrash, "high", "Trash", "punctuation-only placeholder")
		return classifiedMessage{Record: record, NeedsReview: false}
	}

	lowerTopic := strings.ToLower(sourceTopic)
	lowerText := strings.ToLower(text)
	hashtag := func(tag string) bool { return strings.Contains(lowerText, strings.ToLower(tag)) }

	switch {
	case containsTopic(lowerTopic, "задачи"):
		classifyTaskTopic(&record, lowerText, ctx)
	case containsTopic(lowerTopic, "рецепты"):
		setDecision(&record, TypeCollectionItem, DecisionTake, "high", "Коллекции", "legacy recipe topic maps to collection items")
	case containsTopic(lowerTopic, "полезное"):
		setDecision(&record, TypeUsefulMaterial, DecisionTake, "medium", "Полезное", "legacy useful topic is likely finished material")
	case containsTopic(lowerTopic, "учёба") || containsTopic(lowerTopic, "учеба"):
		setDecision(&record, TypeExternalBranchReference, DecisionRouteStudy, "high", "Study branch", "study topic is routed outside InSync v1.0")
	case containsTopic(lowerTopic, "головная боль") || containsTopic(lowerTopic, "goловная боль"):
		if looksProjectRelated(lowerText) {
			setDecision(&record, TypeExternalBranchReference, DecisionRouteControl, "medium", "Sova.Control", "project/Sova material belongs in Control")
		} else {
			setDecision(&record, TypeDraftNote, DecisionArchive, "high", "Legacy archive", "legacy headache topic remains archive")
		}
	case containsTopic(lowerTopic, "журнал причёсок") || containsTopic(lowerTopic, "журнал причесок") || containsTopic(lowerTopic, "btbw"):
		setDecision(&record, TypeDraftNote, DecisionArchive, "high", "Legacy archive", "legacy topic is explicitly archive-only for MVP")
	case containsTopic(lowerTopic, "заготовки"):
		classifyTemplateTopic(&record, lowerText, ctx)
	case containsTopic(lowerTopic, "заметки"):
		classifyNotesTopic(&record, lowerText, hashtag, ctx)
	default:
		classifyGeneral(&record, lowerText, hashtag)
	}
	return classifiedMessage{Record: record, NeedsReview: needsReview(record)}
}

func classifyTaskTopic(record *sqlitestore.WorkspaceAuditRecord, lowerText string, ctx classificationContext) {
	if _, ok := ctx.LatestTasks[record.MessageID]; ok {
		setDecision(record, TypeTask, DecisionTake, "high", "Задачи", "approved rule: migrate the latest 10 legacy task messages")
		return
	}
	if _, ok := ctx.PinnedLinked[record.MessageID]; ok {
		setDecision(record, TypeTask, DecisionTake, "high", "Задачи", "approved rule: task linked from pinned material")
		return
	}
	if looksDoneTask(lowerText) {
		setDecision(record, TypeTask, DecisionSkipDoneTask, "high", "Legacy archive", "completed/crossed-out task should not be deeply processed")
		return
	}
	if looksDeferred(lowerText) {
		setDecision(record, TypeDeferredTask, DecisionArchive, "high", "Legacy archive", "task topic reduced to pinned material and latest 10 messages")
		return
	}
	setDecision(record, TypeTask, DecisionArchive, "high", "Legacy archive", "task topic reduced to pinned material and latest 10 messages")
}

func classifyTemplateTopic(record *sqlitestore.WorkspaceAuditRecord, lowerText string, ctx classificationContext) {
	if _, ok := ctx.PinnedLinked[record.MessageID]; ok {
		setDecision(record, TypeTemplateDocument, DecisionTake, "high", "Заготовки", "approved rule: migrate template linked from pinned material; card formatting must be checked in Sova.Control")
		return
	}
	if _, ok := ctx.LatestTemplates[record.MessageID]; ok {
		setDecision(record, TypeTemplateDocument, DecisionTake, "high", "Заготовки", "approved rule: migrate the latest 10 legacy template messages; card formatting must be checked in Sova.Control")
		return
	}
	if looksTemplate(lowerText) {
		setDecision(record, TypeTemplateDocument, DecisionArchive, "high", "Legacy archive", "template topic reduced to pinned material and latest 10 messages")
		return
	}
	setDecision(record, TypeTemplateDocument, DecisionArchive, "high", "Legacy archive", "template topic reduced to pinned material and latest 10 messages")
}

func classifyNotesTopic(record *sqlitestore.WorkspaceAuditRecord, lowerText string, hashtag func(string) bool, ctx classificationContext) {
	if _, ok := ctx.PinnedLinked[record.MessageID]; ok {
		setDecision(record, TypeNoteDocument, DecisionReview, "high", "Заметки", "note linked from pinned material; route/card/useful split needs user review")
		return
	}
	switch {
	case hashtag("#опыт"):
		setDecision(record, TypeExperience, DecisionTake, "medium", "Опыт", "user #опыт tag marks personal conclusion/experience")
	case hashtag("#идеи"):
		setDecision(record, TypeIdea, DecisionTake, "medium", "Заметки", "user #идеи tag marks idea material")
	case hashtag("#поэзия") || hashtag("#аниме"):
		setDecision(record, TypeCollectionItem, DecisionReview, "medium", "Коллекции", "collection-like user tag needs manual target check")
	case looksStudyRelated(lowerText):
		setDecision(record, TypeExternalBranchReference, DecisionRouteStudy, "medium", "Study branch", "study material should route outside InSync v1.0")
	case looksProjectRelated(lowerText):
		setDecision(record, TypeExternalBranchReference, DecisionRouteControl, "medium", "Sova.Control", "project/Sova material should route to Control")
	case record.Pinned || record.LongMessage || record.Edited || hashtag("#мюсли"):
		setDecision(record, TypeDraftNote, DecisionReview, "medium", "Заметки", "important/raw note candidate needs manual check")
	default:
		setDecision(record, TypeDraftNote, DecisionReview, "low", "Заметки", "notes topic requires full audit; uncertain short note")
	}
}

func classifyGeneral(record *sqlitestore.WorkspaceAuditRecord, lowerText string, hashtag func(string) bool) {
	switch {
	case strings.Contains(lowerText, "#task") || strings.Contains(lowerText, "#tasks"):
		setDecision(record, TypeTask, DecisionReview, "medium", "Задачи", "task marker detected")
	case looksTemplate(lowerText):
		setDecision(record, TypeTemplateDocument, DecisionReview, "medium", "Заготовки", "prompt/template pattern detected")
	case hashtag("#опыт"):
		setDecision(record, TypeExperience, DecisionTake, "medium", "Опыт", "user #опыт tag detected")
	case hashtag("#идеи"):
		setDecision(record, TypeIdea, DecisionTake, "medium", "Заметки", "user #идеи tag detected")
	case looksStudyRelated(lowerText):
		setDecision(record, TypeExternalBranchReference, DecisionRouteStudy, "medium", "Study branch", "study-related material detected")
	case looksProjectRelated(lowerText):
		setDecision(record, TypeExternalBranchReference, DecisionRouteControl, "medium", "Sova.Control", "project/Sova material detected")
	default:
		setDecision(record, TypeDraftNote, DecisionReview, "low", "Review", "unknown topic requires manual audit")
	}
}

func setDecision(record *sqlitestore.WorkspaceAuditRecord, detectedType, decision, confidence, target, reason string) {
	record.DetectedType = detectedType
	record.ModelDecision = decision
	record.Confidence = confidence
	record.SuggestedTarget = target
	record.Reason = "heuristic fallback: " + reason
}

func applyTaggedMigrationDecision(record *sqlitestore.WorkspaceAuditRecord, lowerText string) bool {
	switch {
	case containsHashtag(lowerText, "#опыт"):
		setDecision(record, TypeExperience, DecisionTake, "high", "Опыт", "approved tag rule: #опыт must migrate to InSync v1.0")
	case containsHashtag(lowerText, "#идеи"):
		setDecision(record, TypeIdea, DecisionTake, "high", "Заметки", "approved tag rule: #идеи must migrate to InSync v1.0")
	case containsHashtag(lowerText, "#мюсли"):
		setDecision(record, TypeDraftNote, DecisionTake, "high", "Заметки", "approved tag rule: #мюсли must migrate to InSync v1.0")
	case containsHashtag(lowerText, "#знания"):
		setDecision(record, TypeUsefulMaterial, DecisionTake, "high", "Полезное", "approved tag rule: #знания must migrate to InSync v1.0")
	case containsHashtag(lowerText, "#связи"):
		setDecision(record, TypeNoteDocument, DecisionTake, "high", "Заметки", "approved tag rule: #связи must migrate to InSync v1.0")
	case containsHashtag(lowerText, "#поэзия") || containsHashtag(lowerText, "#аниме"):
		setDecision(record, TypeCollectionItem, DecisionTake, "high", "Коллекции", "approved tag rule: #поэзия/#аниме must migrate to InSync v1.0")
	default:
		return false
	}
	return true
}

func containsHashtag(lowerText, lowerTag string) bool {
	return strings.Contains(lowerText, strings.ToLower(lowerTag))
}

func lowerTagText(text string) string {
	return strings.ToLower(strings.TrimSpace(text))
}

func needsReview(record sqlitestore.WorkspaceAuditRecord) bool {
	return record.ModelDecision == DecisionReview || record.Confidence == "low"
}

func auditRecords(classified []classifiedMessage) []sqlitestore.WorkspaceAuditRecord {
	records := make([]sqlitestore.WorkspaceAuditRecord, 0, len(classified))
	for _, item := range classified {
		records = append(records, item.Record)
	}
	return records
}

type artifactPaths struct {
	summary     string
	csv         string
	markdown    string
	controlCard string
	topicPins   string
}

func writeArtifacts(dir, summary string, classified []classifiedMessage, classifyCtx classificationContext) (artifactPaths, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return artifactPaths{}, err
	}
	paths := artifactPaths{
		summary:     filepath.Join(dir, "workspace_audit_summary.md"),
		csv:         filepath.Join(dir, "workspace_review_candidates.csv"),
		markdown:    filepath.Join(dir, "workspace_review_candidates.md"),
		controlCard: filepath.Join(dir, "workspace_control_card_drafts.md"),
		topicPins:   filepath.Join(dir, "workspace_topic_pin_drafts.md"),
	}
	if err := os.WriteFile(paths.summary, []byte(summary), 0o600); err != nil {
		return artifactPaths{}, err
	}
	if err := writeReviewCSV(paths.csv, classified); err != nil {
		return artifactPaths{}, err
	}
	if err := os.WriteFile(paths.markdown, []byte(renderReviewMarkdown(classified)), 0o600); err != nil {
		return artifactPaths{}, err
	}
	if err := os.WriteFile(paths.controlCard, []byte(renderControlCardDrafts(classified, classifyCtx)), 0o600); err != nil {
		return artifactPaths{}, err
	}
	if err := os.WriteFile(paths.topicPins, []byte(renderTopicPinDrafts()), 0o600); err != nil {
		return artifactPaths{}, err
	}
	return paths, nil
}

func writeReviewCSV(path string, classified []classifiedMessage) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	header := []string{"source_topic", "message_date", "message_link", "short_summary", "detected_type", "model_decision", "confidence", "suggested_target", "reason", "media_type", "user_decision", "user_comment"}
	if err := writer.Write(header); err != nil {
		return err
	}
	for _, item := range classified {
		if !item.NeedsReview {
			continue
		}
		record := item.Record
		row := []string{
			record.SourceTopic,
			record.MessageDate.Format(time.RFC3339),
			record.MessageLink,
			record.ShortSummary,
			record.DetectedType,
			record.ModelDecision,
			record.Confidence,
			record.SuggestedTarget,
			record.Reason,
			record.MediaType,
			"",
			"",
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	return writer.Error()
}

func renderReviewMarkdown(classified []classifiedMessage) string {
	var b strings.Builder
	b.WriteString("# Workspace Review Candidates\n\n")
	b.WriteString("Allowed `user_decision` values: ")
	b.WriteString(strings.Join(allowedUserDecisions, ", "))
	b.WriteString("\n\n")
	b.WriteString("Filtering choices:\n\n")
	b.WriteString("- `take`: migrate to the suggested Workspace topic after Control review.\n")
	b.WriteString("- `archive`: keep only in the legacy archive; no migration.\n")
	b.WriteString("- `trash`: discard punctuation/noise/useless material from migration.\n")
	b.WriteString("- `study`: route outside Workspace to the study branch.\n")
	b.WriteString("- `control`: route project/service material to Sova.Control.\n")
	b.WriteString("- `collection`: migrate as a collection item.\n")
	b.WriteString("- `later`: keep for a later manual pass.\n\n")
	b.WriteString("| source_topic | date | link | summary | type | decision | confidence | target | reason |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, item := range classified {
		if !item.NeedsReview {
			continue
		}
		record := item.Record
		link := record.MessageLink
		if link == "" {
			link = fmt.Sprintf("%d/%d", record.ChatID, record.MessageID)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | `%s` | `%s` | `%s` | %s | %s |\n",
			mdCell(record.SourceTopic),
			record.MessageDate.Format("2006-01-02 15:04"),
			mdCell(link),
			mdCell(record.ShortSummary),
			record.DetectedType,
			record.ModelDecision,
			record.Confidence,
			mdCell(record.SuggestedTarget),
			mdCell(record.Reason),
		)
	}
	return b.String()
}

func renderControlCardDrafts(classified []classifiedMessage, classifyCtx classificationContext) string {
	var b strings.Builder
	b.WriteString("# Workspace Control Card Drafts\n\n")
	b.WriteString("These drafts are for Sova.Control format review only. Do not publish them into InSync v1.0 before explicit user approval.\n\n")
	wrote := false
	for _, item := range classified {
		cards := classifyCtx.PromptCards[item.Record.MessageID]
		if len(cards) == 0 {
			continue
		}
		wrote = true
		link := item.Record.MessageLink
		if link == "" {
			link = fmt.Sprintf("%d/%d", item.Record.ChatID, item.Record.MessageID)
		}
		fmt.Fprintf(&b, "## Source %d\n\n", item.Record.MessageID)
		fmt.Fprintf(&b, "Topic: `%s`\n\nSource: %s\n\n", item.Record.SourceTopic, link)
		b.WriteString("Pinned index draft:\n\n")
		for _, card := range cards {
			fmt.Fprintf(&b, "- %d. %s -> source `%d`\n", card.Number, card.Title, item.Record.MessageID)
		}
		b.WriteString("\n")
		for _, card := range cards {
			fmt.Fprintf(&b, "### %d. %s\n\n", card.Number, card.Title)
			b.WriteString(card.Body)
			b.WriteString("\n\n")
		}
	}
	if !wrote {
		b.WriteString("No multi-card prompt drafts were detected in this audit batch.\n")
	}
	return b.String()
}

type TopicPinDraft struct {
	Topic string
	Text  string
}

func TopicPinDrafts() []TopicPinDraft {
	return []TopicPinDraft{
		{"Inbox", "Сюда можно бросать всё без сортировки: мысль на бегу, ссылку, пересланное, фото с подписью или команду, если непонятно, где ей жить.\n\n<blockquote>Это живой буфер: бот сохранит источник и поможет разобрать материал позже.</blockquote>"},
		{"Задачи", "Карточки появляются здесь после <code>#task</code> или списка под <code>#tasks</code>. У каждой есть кнопки <b>Готово</b>, <b>Отменить</b> и <b>Отложить</b>.\n\nОтложенные задачи остаются карточками, а индекс выше даёт быстрые ссылки обратно к ним."},
		{"Заметки", "Сюда складываются сырые мысли, связки, черновики и заметки, которые ещё можно менять. Здесь нормально быть неточным, противоречивым и незавершённым.\n\n<blockquote>Когда мысль созреет, её можно собрать в аккуратный материал и отправить в Полезное после preview.</blockquote>"},
		{"Опыт", "Личные выводы и наблюдения: что сработало, что не сработало, какие правила хочется сохранить для себя.\n\n<blockquote>Здесь важны контекст, голос и практический след, а не энциклопедическая гладкость.</blockquote>"},
		{"Полезное", "Готовые материалы после preview/approval: инструкции, карточки, списки, маршруты и справки, к которым хочется быстро возвращаться.\n\n<blockquote>Сырые мысли сначала живут в Заметках, чтобы этот слой оставался чистым.</blockquote>"},
		{"Заготовки", "Промпты, шаблоны, reusable instructions, письма и рабочие заготовки. Индекс собирает документы и их части ссылками на исходные сообщения.\n\nСложные миграции старых промптов сначала проходят review."},
		{"Коллекции", "Рецепты, цитаты, стихи, аниме, списки и прочие подборки. Индекс держится по категориям: <b>Рецепты</b>, <b>Цитаты</b>, <b>Стихи</b>, <b>Аниме</b>, <b>Списки</b>, <b>Остальное</b>."},
	}
}

func ControlTopicPinDrafts() []TopicPinDraft {
	return []TopicPinDraft{
		{"Status", "Главная панель состояния. Сюда попадают короткие operational notes, health-check результаты и всё, что отвечает на вопрос: <i>жив ли Sova прямо сейчас?</i>"},
		{"Errors", "Ошибки, падения, странные ответы API и всё, что требует разборки.\n\n<blockquote>Одно понятное сообщение на проблему, короткий контекст и ссылка на источник, если она есть.</blockquote>"},
		{"Runs", "История запусков и служебных проходов: sync, audit, review-preview, миграции, публикации. Этот топик помогает восстановить, что именно было сделано и когда."},
		{"Review", "Ручная проверка: спорные миграции, preview карточек, сомнительные коллекции, шаблоны и материалы, которые нельзя публиковать автоматически."},
		{"Test Lab", "Песочница для проверок. Здесь можно гонять тестовые команды, формат сообщений, кнопки и новые сценарии до попадания в живой Workspace."},
		{"Workspace", "Служебные заметки про <b>InSync v1.0</b>: правила тем, качество закрепов, миграционные решения, поведение задач, заметок, шаблонов и коллекций."},
		{"Nest", "Служебные заметки про <b>Sova.Nest</b>: digest, calendar approval, источники, cooldown, публикации и всё, что не должно смешиваться с личным Workspace."},
		{"Ideas", "Идеи для будущих улучшений. Сюда можно складывать гипотезы, маленькие UX-наблюдения и желания, которые пока рано превращать в задачи."},
	}
}

func renderTopicPinDrafts() string {
	drafts := TopicPinDrafts()
	var b strings.Builder
	b.WriteString("# Workspace Topic Pin Drafts\n\n")
	b.WriteString("Raw first-pass pin texts. Send to Sova.Control for style/format check before pinning in InSync v1.0.\n\n")
	for _, draft := range drafts {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", draft.Topic, draft.Text)
	}
	return b.String()
}

func renderSummary(stats auditStats) string {
	var b strings.Builder
	b.WriteString("# Workspace Audit Summary\n\n")
	fmt.Fprintf(&b, "Generated: `%s`\n\n", stats.GeneratedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Source: `%s`", stats.SourceRef)
	if stats.SourceTitle != "" {
		fmt.Fprintf(&b, " (%s)", stats.SourceTitle)
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "- Messages audited: `%d`\n", stats.TotalMessages)
	fmt.Fprintf(&b, "- Review candidates: `%d`\n", stats.ReviewCandidates)
	fmt.Fprintf(&b, "- Media placeholders: `%d`\n", stats.MediaPlaceholders)
	fmt.Fprintf(&b, "- Pinned messages detected: `%d`\n", stats.PinnedMessages)
	fmt.Fprintf(&b, "- Linked from pinned material: `%d`\n", stats.PinnedLinked)
	fmt.Fprintf(&b, "- Punctuation-only trashed: `%d`\n", stats.PunctuationTrash)
	fmt.Fprintf(&b, "- Prompt card drafts detected: `%d`\n", stats.PromptCards)
	fmt.Fprintf(&b, "- Auto-approved latest task messages: `%d`\n", stats.AutoApprovedTasks)
	fmt.Fprintf(&b, "- Auto-approved latest template messages: `%d`\n", stats.AutoApprovedTemplates)
	fmt.Fprintf(&b, "- Long messages detected: `%d`\n", stats.LongMessages)
	fmt.Fprintf(&b, "- Edited messages detected: `%d`\n", stats.EditedMessages)
	if stats.HeuristicOnly {
		b.WriteString("- Classifier: `heuristic fallback` (no Workspace LLM classifier used)\n")
	}
	writeCounts(&b, "Topics Scanned", stats.Topics)
	writeCounts(&b, "Counts By Type", stats.Types)
	writeCounts(&b, "Counts By Disposition", stats.Decisions)
	b.WriteString("\n## Additional Types\n\n")
	if len(stats.AdditionalTypes) == 0 {
		b.WriteString("None.\n")
	} else {
		for _, item := range stats.AdditionalTypes {
			fmt.Fprintf(&b, "- `%s`\n", item)
		}
	}
	b.WriteString("\n## Risks And Assumptions\n\n")
	for _, risk := range stats.Risks {
		fmt.Fprintf(&b, "- %s\n", risk)
	}
	b.WriteString("\n## Next User Action\n\n")
	b.WriteString(stats.NextAction)
	b.WriteString("\n")
	return b.String()
}

func writeCounts(b *strings.Builder, title string, counts map[string]int) {
	b.WriteString("\n## ")
	b.WriteString(title)
	b.WriteString("\n\n")
	if len(counts) == 0 {
		b.WriteString("None.\n")
		return
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(b, "- `%s`: `%d`\n", key, counts[key])
	}
}

type topicIndex struct {
	byTopicID map[int]sqlitestore.WorkspaceTopic
	byTopID   map[int]sqlitestore.WorkspaceTopic
}

func indexTopics(topics []sqlitestore.WorkspaceTopic) topicIndex {
	idx := topicIndex{byTopicID: map[int]sqlitestore.WorkspaceTopic{}, byTopID: map[int]sqlitestore.WorkspaceTopic{}}
	for _, topic := range topics {
		if topic.TopicID != 0 {
			idx.byTopicID[topic.TopicID] = topic
		}
		if topic.TopMessageID != 0 {
			idx.byTopID[topic.TopMessageID] = topic
		}
	}
	return idx
}

func (idx topicIndex) lookup(topicID, topMessageID, messageID int) sqlitestore.WorkspaceTopic {
	for _, id := range []int{topicID, topMessageID, messageID} {
		if id == 0 {
			continue
		}
		if topic, ok := idx.byTopicID[id]; ok {
			return topic
		}
		if topic, ok := idx.byTopID[id]; ok {
			return topic
		}
	}
	return sqlitestore.WorkspaceTopic{}
}

func extractMessageMeta(raw string) messageMeta {
	var meta messageMeta
	if strings.TrimSpace(raw) == "" {
		return meta
	}
	var root any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return meta
	}
	if value, ok := findNumber(root, "replytotopid", 0); ok {
		meta.TopMessageID = int(value)
		meta.TopicID = int(value)
	}
	if value, ok := findNumber(root, "replytomsgid", 0); ok && meta.TopicID == 0 {
		meta.TopicID = int(value)
	}
	if value, ok := findNumber(root, "replytomsgid", 0); ok {
		meta.ReplyMessageID = int(value)
	}
	if value, ok := findNumber(root, "editdate", 0); ok && value > 0 {
		edited := time.Unix(value, 0).UTC()
		meta.EditDate = &edited
		meta.Edited = true
	}
	if value, ok := findBool(root, "pinned", 0); ok {
		meta.Pinned = value
	}
	return meta
}

func findNumber(value any, normalizedKey string, depth int) (int64, bool) {
	if depth > 8 {
		return 0, false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if normalizeKey(key) == normalizedKey {
				switch v := child.(type) {
				case float64:
					return int64(v), true
				case string:
					parsed, err := strconv.ParseInt(v, 10, 64)
					return parsed, err == nil
				}
			}
		}
		for _, child := range typed {
			if number, ok := findNumber(child, normalizedKey, depth+1); ok {
				return number, true
			}
		}
	case []any:
		for _, child := range typed {
			if number, ok := findNumber(child, normalizedKey, depth+1); ok {
				return number, true
			}
		}
	}
	return 0, false
}

func findBool(value any, normalizedKey string, depth int) (bool, bool) {
	if depth > 8 {
		return false, false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if normalizeKey(key) == normalizedKey {
				v, ok := child.(bool)
				return v, ok
			}
		}
		for _, child := range typed {
			if v, ok := findBool(child, normalizedKey, depth+1); ok {
				return v, true
			}
		}
	case []any:
		for _, child := range typed {
			if v, ok := findBool(child, normalizedKey, depth+1); ok {
				return v, true
			}
		}
	}
	return false, false
}

func stableSourceRefCandidate(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "-100") {
		channelID := strings.TrimPrefix(trimmed, "-100")
		if channelID != "" {
			if _, err := strconv.ParseInt(channelID, 10, 64); err == nil {
				return "telegram:channel:" + channelID, true
			}
		}
	}
	trimmed = strings.TrimPrefix(trimmed, "telegram:")
	parts := strings.Split(trimmed, ":")
	if len(parts) < 2 {
		return "", false
	}
	if parts[0] != "chat" && parts[0] != "channel" && parts[0] != "user" {
		return "", false
	}
	if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
		return "", false
	}
	return "telegram:" + parts[0] + ":" + parts[1], true
}

func isTelegramJoinLink(value string) bool {
	normalized := strings.TrimSpace(strings.ToLower(value))
	normalized = strings.TrimPrefix(normalized, "https://")
	normalized = strings.TrimPrefix(normalized, "http://")
	normalized = strings.TrimPrefix(normalized, "t.me/")
	normalized = strings.TrimPrefix(normalized, "telegram.me/")
	return strings.HasPrefix(normalized, "+") || strings.HasPrefix(normalized, "joinchat/")
}

func normalizeSourceName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "t.me/")
	value = strings.TrimPrefix(value, "@")
	return strings.Trim(value, "/")
}

func normalizeKey(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func summarizeMessage(text, mediaType string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	text = removeIgnoredSystemTag(text)
	if text == "" && mediaType != "" {
		return "[" + mediaType + "]"
	}
	runes := []rune(text)
	if len(runes) <= 180 {
		return text
	}
	return string(runes[:177]) + "..."
}

func removeIgnoredSystemTag(text string) string {
	return strings.TrimSpace(text)
}

func looksDoneTask(text string) bool {
	patterns := []string{"✅", "готово", "сделано", "done", "completed", "- [x]", "[x]", "<s>", "</s>", "~~"}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func looksDeferred(text string) bool {
	patterns := []string{"потом", "когда-нибудь", "отлож", "later", "backlog", "remind", "напомни", "через неделю", "через месяц"}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func looksTemplate(text string) bool {
	patterns := []string{"prompt", "промпт", "шаблон", "инструкция", "system prompt", "codex", "gpt", "llm"}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func looksStudyRelated(text string) bool {
	patterns := []string{"учёб", "учеб", "экзамен", "зачет", "зачёт", "универ", "дедлайн", "лекци", "семинар", "лаба", "дз"}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func looksProjectRelated(text string) bool {
	patterns := []string{"sova", "сова", "control", "nest", "workspace", "codex", "github", "архитектур", "сервер", "деплой"}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func isPunctuationOnly(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		if unicode.IsSymbol(r) {
			return false
		}
	}
	return true
}

var internalTelegramLinkPattern = regexp.MustCompile(`(?:https?://)?t\.me/c/(\d+)/(?:\d+/)?(\d+)`)

func internalTelegramMessageLinks(text string, chatID int64) []int {
	var out []int
	seen := map[int]struct{}{}
	for _, match := range internalTelegramLinkPattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 3 {
			continue
		}
		linkedChatID, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || linkedChatID != chatID {
			continue
		}
		messageID, err := strconv.Atoi(match[2])
		if err != nil || messageID == 0 {
			continue
		}
		if _, ok := seen[messageID]; ok {
			continue
		}
		seen[messageID] = struct{}{}
		out = append(out, messageID)
	}
	return out
}

var promptCardHeadingPattern = regexp.MustCompile(`(?m)^\s*(\d{1,3})\.\s+(.+?)\s*$`)

func splitPromptCards(text string) []promptCard {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	matches := promptCardHeadingPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) < 2 {
		return nil
	}
	cards := make([]promptCard, 0, len(matches))
	for i, match := range matches {
		if len(match) < 6 {
			continue
		}
		numberRaw := text[match[2]:match[3]]
		title := strings.TrimSpace(text[match[4]:match[5]])
		bodyStart := match[1]
		bodyEnd := len(text)
		if i+1 < len(matches) {
			bodyEnd = matches[i+1][0]
		}
		body := strings.TrimSpace(text[bodyStart:bodyEnd])
		if body == "" || title == "" {
			continue
		}
		number, err := strconv.Atoi(numberRaw)
		if err != nil {
			continue
		}
		cards = append(cards, promptCard{Number: number, Title: title, Body: body})
	}
	if len(cards) < 2 {
		return nil
	}
	return cards
}

func containsTopic(topic, fragment string) bool {
	return strings.Contains(topic, strings.ToLower(fragment))
}

func mdCell(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	return strings.TrimSpace(value)
}

func emptyLabel(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func runeLen(value string) int {
	return len([]rune(value))
}

var markdownLinkUnsafe = regexp.MustCompile(`[\[\]\(\)]`)

func EscapeMarkdownLabel(value string) string {
	return markdownLinkUnsafe.ReplaceAllStringFunc(value, func(match string) string {
		return `\` + match
	})
}
