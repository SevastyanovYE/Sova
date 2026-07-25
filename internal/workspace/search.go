package workspace

import (
	"context"
	"errors"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	"github.com/SevastyanovYE/Sova/internal/semanticsearch"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
	"github.com/SevastyanovYE/Sova/internal/telegrammt"
)

const (
	workspaceSearchSyncInterval = 5 * time.Minute
	workspaceSearchRecentLimit  = 500
)

var errSearchFullScanIncomplete = errors.New("search full scan incomplete")

type SearchIndexOptions struct {
	FullScan bool
	Limit    int
	Now      time.Time
}

type SearchIndexResult struct {
	Sources   map[string]int
	Changed   int
	Processed int
	Primary   int
	Fallback  int
}

type searchSyncTarget struct {
	scope string
	sync  func(context.Context, telegrammt.SyncOptions) (telegrammt.SyncResult, error)
}

func BuildSearchIndex(ctx context.Context, cfg config.Config, store *sqlitestore.Store, options SearchIndexOptions) (SearchIndexResult, error) {
	if store == nil {
		return SearchIndexResult{}, fmt.Errorf("store is required")
	}
	if strings.TrimSpace(cfg.Gemini.APIKey) == "" {
		return SearchIndexResult{}, fmt.Errorf("SOVA_GEMINI_API_KEY is required")
	}
	if cfg.Search.LegacyChatID == 0 {
		return SearchIndexResult{}, fmt.Errorf("SOVA_SEARCH_LEGACY_CHAT_ID is required")
	}
	if options.Limit <= 0 {
		options.Limit = workspaceSearchRecentLimit
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	mtproto := telegrammt.New(cfg)
	targets, err := searchTargets(cfg, mtproto, store)
	if err != nil {
		return SearchIndexResult{}, err
	}
	service := semanticsearch.NewService(store, cfg.Search.EmbeddingModel, cfg.Gemini.APIKey, cfg.Search.FallbackAPIKey)
	result := SearchIndexResult{Sources: map[string]int{}}
	previousStates, err := store.SearchSyncStates(ctx)
	if err != nil {
		return result, err
	}
	previousLast := make(map[string]int, len(previousStates))
	previousByScope := make(map[string]sqlitestore.SearchSyncState, len(previousStates))
	completedScopes := 0
	activeFullScan := false
	for _, state := range previousStates {
		previousLast[state.Scope] = state.LastMessageID
		previousByScope[state.Scope] = state
		if state.FullScanCompletedAt != nil {
			completedScopes++
		}
		activeFullScan = activeFullScan || state.ScanStartedAt != nil
	}
	resumeFullScan := options.FullScan && (activeFullScan || (completedScopes > 0 && completedScopes < len(targets)))
	var incompleteScans []error
	for _, target := range targets {
		if options.FullScan {
			state := previousByScope[target.scope]
			if resumeFullScan && state.FullScanCompletedAt != nil && state.ScanStartedAt == nil {
				continue
			}
			if err := runCheckpointedSearchFullScan(ctx, store, service, target, options, &result); err != nil {
				if errors.Is(err, errSearchFullScanIncomplete) {
					incompleteScans = append(incompleteScans, err)
					continue
				}
				return result, err
			}
			continue
		}
		syncResult, err := target.sync(ctx, telegrammt.SyncOptions{LimitPerSource: options.Limit, RefreshRecent: true})
		if err != nil {
			_ = store.SetSearchSyncState(context.WithoutCancel(ctx), target.scope, 0, false, err.Error(), options.Now)
			return result, fmt.Errorf("sync %s search source: %w", target.scope, err)
		}
		messages := searchMessages(syncResult)
		changed, seen, err := service.UpsertCorpus(ctx, target.scope, messages)
		if err != nil {
			return result, err
		}
		result.Changed += changed
		result.Sources[target.scope] = len(messages)
		allMessageIDs := searchMessageIDs(messages)
		lastMessageID := maxSearchMessageID(allMessageIDs)
		if len(allMessageIDs) > 0 {
			minMessageID, maxMessageID := minMaxSearchMessageID(allMessageIDs)
			if previousLast[target.scope] > maxMessageID {
				maxMessageID = previousLast[target.scope]
			}
			if err := store.ReconcileSearchScopeRange(ctx, target.scope, minMessageID, maxMessageID, seen, options.Now); err != nil {
				return result, err
			}
		}
		if err := store.SetSearchSyncState(ctx, target.scope, lastMessageID, false, "", options.Now); err != nil {
			return result, err
		}
		if previousByScope[target.scope].FullScanCompletedAt != nil {
			if err := runSearchAuditChunk(ctx, store, service, target, syncResult, previousByScope[target.scope], options, &result); err != nil {
				return result, err
			}
		}
	}
	if len(incompleteScans) > 0 {
		return result, errors.Join(incompleteScans...)
	}
	if err := store.PrepareSearchEmbeddingModel(ctx, cfg.Search.EmbeddingModel, semanticsearch.DefaultEmbeddingDimensions, options.Now); err != nil {
		return result, err
	}
	for {
		summary, err := service.IndexPending(ctx, 100)
		result.Processed += summary.Processed
		result.Primary += summary.Primary
		result.Fallback += summary.Fallback
		if err != nil {
			return result, err
		}
		if summary.Processed == 0 {
			break
		}
	}
	return result, nil
}

func runCheckpointedSearchFullScan(ctx context.Context, store *sqlitestore.Store, service *semanticsearch.Service, target searchSyncTarget, options SearchIndexOptions, result *SearchIndexResult) error {
	generation, cursor, err := store.BeginOrResumeSearchFullScan(ctx, target.scope, options.Now)
	if err != nil {
		return err
	}
	remaining := options.Limit
	lastMessageID := 0
	const pageSize = 500
	for remaining > 0 {
		limit := pageSize
		if remaining < limit {
			limit = remaining
		}
		syncResult, err := target.sync(ctx, telegrammt.SyncOptions{
			LimitPerSource: limit, FullScan: true, HistoryMaxID: cursor,
		})
		if err != nil {
			_ = store.CheckpointSearchFullScan(context.WithoutCancel(ctx), target.scope, generation, cursor, lastMessageID, err.Error(), options.Now)
			return fmt.Errorf("sync %s search source: %w", target.scope, err)
		}
		messages := searchMessages(syncResult)
		changed, seen, err := service.UpsertCorpus(ctx, target.scope, messages)
		if err != nil {
			return err
		}
		if err := store.MarkSearchScanSeen(ctx, target.scope, generation, seen, options.Now); err != nil {
			return err
		}
		result.Changed += changed
		result.Sources[target.scope] += len(messages)
		rawIDs := searchFetchedMessageIDs(syncResult)
		pageLast := maxSearchMessageID(rawIDs)
		if pageLast > lastMessageID {
			lastMessageID = pageLast
		}
		fetched := len(rawIDs)
		remaining -= fetched
		if fetched == 0 || fetched < limit {
			return store.CompleteSearchFullScan(ctx, target.scope, generation, lastMessageID, options.Now)
		}
		oldest, _ := minMaxSearchMessageID(rawIDs)
		if oldest <= 0 || oldest == cursor {
			return fmt.Errorf("%s full scan cursor did not advance from %d", target.scope, cursor)
		}
		cursor = oldest
		if err := store.CheckpointSearchFullScan(ctx, target.scope, generation, cursor, lastMessageID, "", options.Now); err != nil {
			return err
		}
	}
	limitErr := fmt.Errorf("%s full scan checkpointed at message %d after --limit=%d; rerun the same command to continue", target.scope, cursor, options.Limit)
	_ = store.CheckpointSearchFullScan(context.WithoutCancel(ctx), target.scope, generation, cursor, lastMessageID, limitErr.Error(), options.Now)
	return fmt.Errorf("%w: %v", errSearchFullScanIncomplete, limitErr)
}

func runSearchAuditChunk(ctx context.Context, store *sqlitestore.Store, service *semanticsearch.Service, target searchSyncTarget, recent telegrammt.SyncResult, state sqlitestore.SearchSyncState, options SearchIndexOptions, result *SearchIndexResult) error {
	cursor := state.AuditCursor
	if cursor == 0 {
		ids := searchFetchedMessageIDs(recent)
		cursor, _ = minMaxSearchMessageID(ids)
	}
	if cursor <= 0 {
		return nil
	}
	audit, err := target.sync(ctx, telegrammt.SyncOptions{
		LimitPerSource: options.Limit, RefreshRecent: true, HistoryMaxID: cursor,
	})
	if err != nil {
		return fmt.Errorf("audit %s search source: %w", target.scope, err)
	}
	messages := searchMessages(audit)
	changed, seen, err := service.UpsertCorpus(ctx, target.scope, messages)
	if err != nil {
		return err
	}
	result.Changed += changed
	result.Sources[target.scope] += len(messages)
	rawIDs := searchFetchedMessageIDs(audit)
	if len(rawIDs) == 0 {
		return store.SetSearchAuditCursor(ctx, target.scope, 0, options.Now)
	}
	minID, _ := minMaxSearchMessageID(rawIDs)
	maxID := cursor - 1
	if maxID < minID {
		maxID = minID
	}
	if err := store.ReconcileSearchScopeRange(ctx, target.scope, minID, maxID, seen, options.Now); err != nil {
		return err
	}
	if len(rawIDs) < options.Limit {
		minID = 0
	}
	return store.SetSearchAuditCursor(ctx, target.scope, minID, options.Now)
}

func runWorkspaceSearchSyncLoop(ctx context.Context, cfg config.Config, store *sqlitestore.Store) {
	process := func() {
		_, err := BuildSearchIndex(ctx, cfg, store, SearchIndexOptions{Limit: workspaceSearchRecentLimit, Now: time.Now().UTC()})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("workspace semantic search sync unavailable: %v\n", err)
		}
	}
	process()
	ticker := time.NewTicker(workspaceSearchSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			process()
		}
	}
}

func handleWorkspaceSearchCommand(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, message nest.Message, threadID int, query string) error {
	if threadID != cfg.Workspace.Topics.Inbox {
		return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID, Text: "Поиск запускается только из Inbox."})
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID, Text: "Формат: /search <запрос>"})
	}
	if !cfg.Search.Enabled {
		return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID, Text: "Семантический поиск ещё не включён. Сначала нужен успешный полный индекс."})
	}
	service := semanticsearch.NewService(store, cfg.Search.EmbeddingModel, cfg.Gemini.APIKey, cfg.Search.FallbackAPIKey)
	results, _, err := service.Search(ctx, query, 10)
	if err != nil {
		if errors.Is(err, semanticsearch.ErrIndexNotReady) {
			return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID, Text: "Индекс ещё строится. Попробуй позже."})
		}
		return fmt.Errorf("search: %w", err)
	}
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID,
		Text: formatWorkspaceSearchResults(cfg, query, results), ParseMode: "HTML",
	})
}

func indexWorkspaceMessageForSearch(ctx context.Context, cfg config.Config, store *sqlitestore.Store, message sqlitestore.WorkspaceMessage, now time.Time) error {
	if !cfg.Search.Enabled {
		return nil
	}
	text := strings.TrimSpace(message.Text)
	if text == "" {
		text = strings.TrimSpace(message.Caption)
	}
	if text == "" || isWorkspaceSearchCommand(text) {
		return store.MarkSearchDocumentDeleted(ctx, "workspace", message.MessageID, now)
	}
	document := sqlitestore.SearchDocument{
		Scope: "workspace", SourceRef: "workspace-bot-api", SourceTitle: "InSync",
		ChatID: message.ChatID, MessageID: message.MessageID, TopicID: message.TopicID,
		MessageDate: message.Date, Text: text, MediaType: message.MediaType, SourceLink: message.SourceLink,
		ContentHash: semanticsearch.ContentHash("workspace", message.ChatID, message.MessageID, text),
	}
	_, err := store.UpsertSearchDocuments(ctx, []sqlitestore.SearchDocument{document}, now)
	return err
}

func searchTargets(cfg config.Config, mtproto *telegrammt.Client, store *sqlitestore.Store) ([]searchSyncTarget, error) {
	if cfg.Workspace.ChatID == 0 || cfg.NestChatID == 0 {
		return nil, fmt.Errorf("Workspace and Nest chat IDs are required for semantic search")
	}
	legacySync := func(ctx context.Context, options telegrammt.SyncOptions) (telegrammt.SyncResult, error) {
		return mtproto.SyncBotAPIChat(ctx, store, cfg.Search.LegacyChatID, options)
	}
	return []searchSyncTarget{
		{scope: "workspace", sync: func(ctx context.Context, options telegrammt.SyncOptions) (telegrammt.SyncResult, error) {
			return mtproto.SyncWorkspaceCurrent(ctx, store, options)
		}},
		{scope: "legacy", sync: legacySync},
		{scope: "nest", sync: func(ctx context.Context, options telegrammt.SyncOptions) (telegrammt.SyncResult, error) {
			return mtproto.SyncBotAPIChat(ctx, store, cfg.NestChatID, options)
		}},
	}, nil
}

func searchMessages(result telegrammt.SyncResult) []sqlitestore.TelegramRecentMessage {
	var messages []sqlitestore.TelegramRecentMessage
	for _, source := range result.Sources {
		for _, message := range source.FetchedMessages {
			messages = append(messages, sqlitestore.TelegramRecentMessage{
				SourceRef: message.SourceRef, SourceTitle: message.SourceTitle, Username: message.Username,
				ChatID: message.ChatID, MessageID: message.MessageID, TopicID: message.TopicID,
				Date: message.Date, Kind: message.Kind, Text: message.Text,
				MediaType: message.MediaType, SourceLink: message.SourceLink,
			})
		}
	}
	return messages
}

func formatWorkspaceSearchResults(cfg config.Config, query string, results []semanticsearch.SearchResult) string {
	var builder strings.Builder
	builder.WriteString("🔎 <b>")
	builder.WriteString(html.EscapeString(truncatePlainUTF16(query, 300)))
	builder.WriteString("</b>\n")
	if len(results) == 0 {
		builder.WriteString("\nНичего подходящего не нашлось.")
		return builder.String()
	}
	shown := 0
	for index, result := range results {
		document := result.Document
		var item strings.Builder
		item.WriteString("\n")
		item.WriteString(fmt.Sprintf("%d. <b>%s</b>", index+1, html.EscapeString(searchScopeLabel(document.Scope))))
		if topic := workspaceTopicLabel(cfg, document.Scope, document.TopicID); topic != "" {
			item.WriteString(" · ")
			item.WriteString(html.EscapeString(topic))
		}
		item.WriteString(" · ")
		item.WriteString(document.MessageDate.In(mustLocation(cfg.Timezone)).Format("02.01.2006"))
		item.WriteString("\n")
		snippet := html.EscapeString(semanticsearch.CompactSnippet(document.Text, 220))
		if strings.TrimSpace(document.SourceLink) != "" {
			item.WriteString("<a href=\"")
			item.WriteString(html.EscapeString(document.SourceLink))
			item.WriteString("\">")
			item.WriteString(snippet)
			item.WriteString("</a>")
		} else {
			item.WriteString(snippet)
		}
		if !telegramHTMLFits(builder.String()+item.String(), workspaceTelegramSafeTextLimit) {
			break
		}
		builder.WriteString(item.String())
		shown++
	}
	if shown < len(results) {
		note := fmt.Sprintf("\n\n<i>Показаны %d из %d результатов: остальные не поместились в одно сообщение.</i>", shown, len(results))
		if telegramHTMLFits(builder.String()+note, workspaceTelegramSafeTextLimit) {
			builder.WriteString(note)
		}
	}
	return builder.String()
}

func searchScopeLabel(scope string) string {
	switch scope {
	case "workspace":
		return "новый InSync"
	case "legacy":
		return "старый InSync"
	case "nest":
		return "Sova.Nest"
	default:
		return scope
	}
}

func workspaceTopicLabel(cfg config.Config, scope string, topicID int) string {
	if topicID <= 0 {
		return ""
	}
	var labels map[int]string
	switch scope {
	case "workspace":
		labels = map[int]string{
			cfg.Workspace.Topics.Inbox: "Inbox", cfg.Workspace.Topics.Tasks: "Задачи",
			cfg.Workspace.Topics.Notes: "Заметки", cfg.Workspace.Topics.Experience: "Опыт",
			cfg.Workspace.Topics.Useful: "Полезное", cfg.Workspace.Topics.Templates: "Заготовки",
			cfg.Workspace.Topics.Collections: "Коллекции",
		}
	case "nest":
		labels = map[int]string{
			cfg.NestTopics.Digest: "Digest", cfg.NestTopics.Calendar: "Calendar",
			cfg.NestTopics.Status: "Status", cfg.NestTopics.Chat: "Chat",
		}
	}
	if label := labels[topicID]; label != "" {
		return label
	}
	return fmt.Sprintf("топик %d", topicID)
}

func maxSearchMessageID(messageIDs []int) int {
	_, max := minMaxSearchMessageID(messageIDs)
	return max
}

func searchMessageIDs(messages []sqlitestore.TelegramRecentMessage) []int {
	ids := make([]int, 0, len(messages))
	for _, message := range messages {
		if message.MessageID > 0 {
			ids = append(ids, message.MessageID)
		}
	}
	return ids
}

func searchFetchedMessageIDs(result telegrammt.SyncResult) []int {
	var ids []int
	for _, source := range result.Sources {
		for _, message := range source.FetchedMessages {
			if message.MessageID > 0 {
				ids = append(ids, message.MessageID)
			}
		}
	}
	return ids
}

func minMaxSearchMessageID(messageIDs []int) (int, int) {
	if len(messageIDs) == 0 {
		return 0, 0
	}
	copyIDs := append([]int(nil), messageIDs...)
	sort.Ints(copyIDs)
	return copyIDs[0], copyIDs[len(copyIDs)-1]
}

func isWorkspaceSearchCommand(text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return false
	}
	first := strings.ToLower(fields[0])
	return first == "/search" || strings.HasPrefix(first, "/search@")
}
