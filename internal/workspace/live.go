package workspace

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

const (
	workspacePollInitialBackoff = 5 * time.Second
	workspacePollMaxBackoff     = time.Minute
	taskCardSendDelay           = 250 * time.Millisecond
	taskCallbackPrefix          = "ws:task:"
	publishCallbackPrefix       = "ws:publish:"
	taskBacklogIndexKey         = "tasks_backlog"
	noteIndexKey                = "notes_active"
	templateIndexKey            = "templates_index"
	collectionIndexKey          = "collections_index"
	usefulIndexKey              = "useful_index"
)

type pendingTaskDateKey struct {
	chatID   int64
	threadID int
	userID   int64
}

type pendingWorkspaceInput struct {
	Kind       string
	DocumentID int64
	PartID     int64
	Title      string
}

type publishPreviewDraft struct {
	DocumentID        int64
	PreviewMessageIDs []int
	PreviewTexts      []string
	Revision          string
}

type SeedDocumentIndexesOptions struct {
	DryRun bool
	Now    time.Time
}

type SeedDocumentIndexesResult struct {
	DryRun bool
	Items  []SeedDocumentIndexItem
}

type SeedDocumentIndexItem struct {
	Type      string
	Topic     string
	TopicID   int
	MessageID int
	Status    string
	Text      string
}

func Serve(ctx context.Context, cfg config.Config, store *sqlitestore.Store) error {
	if !cfg.WorkspaceConfigured() {
		return fmt.Errorf("workspace group is not fully configured")
	}
	if store == nil {
		return fmt.Errorf("store is required")
	}
	client := nest.New(cfg.Workspace.BotToken)
	offset := 0
	pollFailures := 0
	pendingTaskDates := map[pendingTaskDateKey]int64{}
	pendingInputs := map[pendingTaskDateKey]pendingWorkspaceInput{}
	publishDrafts := map[int64]publishPreviewDraft{}
	fmt.Printf("sova workspace serve: polling Workspace chat %d; default topic Inbox=%d Tasks=%d\n",
		cfg.Workspace.ChatID, cfg.Workspace.Topics.Inbox, cfg.Workspace.Topics.Tasks)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		updates, err := client.GetUpdates(ctx, offset, 30)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			pollFailures++
			delay := workspacePollRetryDelay(pollFailures)
			if pollFailures <= 3 || pollFailures%5 == 0 {
				fmt.Printf("workspace getUpdates unavailable (attempt %d; retrying in %s): %v\n", pollFailures, delay, err)
			}
			workspaceSleepOrDone(ctx, delay)
			continue
		}
		if pollFailures > 0 {
			fmt.Printf("workspace getUpdates recovered after %d failed attempt(s)\n", pollFailures)
			pollFailures = 0
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			handleWorkspaceUpdate(ctx, cfg, store, client, pendingTaskDates, pendingInputs, publishDrafts, update)
		}
	}
}

func handleWorkspaceUpdate(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingTaskDates map[pendingTaskDateKey]int64, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, publishDrafts map[int64]publishPreviewDraft, update nest.Update) {
	if update.Message != nil {
		handleWorkspaceMessage(ctx, cfg, store, client, pendingTaskDates, pendingInputs, publishDrafts, *update.Message, false)
		return
	}
	if update.EditedMessage != nil {
		handleWorkspaceMessage(ctx, cfg, store, client, pendingTaskDates, pendingInputs, publishDrafts, *update.EditedMessage, true)
		return
	}
	if update.CallbackQuery != nil {
		handleWorkspaceCallback(ctx, cfg, store, client, pendingTaskDates, pendingInputs, publishDrafts, *update.CallbackQuery)
	}
}

func handleWorkspaceMessage(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingTaskDates map[pendingTaskDateKey]int64, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, publishDrafts map[int64]publishPreviewDraft, message nest.Message, edited bool) {
	if message.Chat.ID != cfg.Workspace.ChatID {
		return
	}
	if message.From != nil && message.From.IsBot {
		return
	}
	threadID := interactionThread(cfg, message.MessageThreadID)
	if !edited && handlePendingWorkspaceInputMessage(ctx, cfg, store, client, pendingInputs, publishDrafts, message, threadID) {
		return
	}
	if !edited && handlePendingTaskDateMessage(ctx, cfg, store, client, pendingTaskDates, message, threadID) {
		return
	}
	command, rest := workspaceCommandName(message.Text)
	if !edited && command == "cluster" {
		handleClusterCommand(ctx, cfg, store, client, message, threadID, rest)
		return
	}
	if !edited && (command == "note" || command == "doc" || command == "template" || command == "collection") {
		handleWorkspaceDocumentCommand(ctx, cfg, store, client, pendingInputs, publishDrafts, message, threadID, command, rest)
		return
	}
	if !edited && command == "publish" {
		if err := handleNotePublishCommand(ctx, cfg, store, client, publishDrafts, message, threadID, strings.TrimSpace(rest), time.Now().UTC()); err != nil {
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось подготовить публикацию", err)
		}
		return
	}
	if !edited && command == "new" {
		handleWorkspaceNewCommand(ctx, cfg, store, client, pendingInputs, message, threadID, rest)
		return
	}

	now := time.Now().UTC()
	sourceMessage := workspaceMessageFromBotAPI(cfg, message, now)
	if err := store.UpsertWorkspaceMessage(ctx, sourceMessage, now); err != nil {
		sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось сохранить сообщение", err)
		return
	}
	cluster, err := ensureMessageCluster(ctx, store, sourceMessage, edited, now)
	if err != nil {
		sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось обновить cluster", err)
		return
	}
	if err := syncTasksFromSource(ctx, cfg, store, client, sourceMessage, cluster.ID, edited, now); err != nil {
		sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось синхронизировать задачи", err)
		return
	}
	if edited {
		if err := syncWorkspaceDocumentIndexesForSourceEdit(ctx, cfg, store, client, sourceMessage, now); err != nil {
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось обновить индекс документа", err)
		}
		if _, err := store.MarkWorkspacePublishedSourceNeedsReview(ctx, sourceMessage.ChatID, sourceMessage.MessageID, now); err != nil {
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось отметить опубликованный материал на review", err)
		}
	}
}

func ensureMessageCluster(ctx context.Context, store *sqlitestore.Store, message sqlitestore.WorkspaceMessage, edited bool, now time.Time) (sqlitestore.WorkspaceCluster, error) {
	if edited {
		if cluster, ok, err := store.WorkspaceClusterByMessage(ctx, message.ChatID, message.MessageID); err != nil || ok {
			return cluster, err
		}
	}
	if message.ReplyToMessageID > 0 {
		cluster, ok, err := store.WorkspaceClusterByMessage(ctx, message.ChatID, message.ReplyToMessageID)
		if err != nil {
			return sqlitestore.WorkspaceCluster{}, err
		}
		if ok {
			if err := store.AddWorkspaceMessageToCluster(ctx, cluster.ID, message.ChatID, message.MessageID, "manual", now); err != nil {
				return sqlitestore.WorkspaceCluster{}, err
			}
			return cluster, nil
		}
	}
	if shouldAttachToImmediateCluster(message) {
		tail, ok, err := store.LatestWorkspaceClusterTail(ctx, message.ChatID, message.TopicID, message.FromUserID)
		if err != nil {
			return sqlitestore.WorkspaceCluster{}, err
		}
		if ok && tail.Message.MessageID == message.MessageID-1 {
			if err := store.AddWorkspaceMessageToCluster(ctx, tail.Cluster.ID, message.ChatID, message.MessageID, "part", now); err != nil {
				return sqlitestore.WorkspaceCluster{}, err
			}
			return tail.Cluster, nil
		}
	}
	return store.CreateWorkspaceClusterWithMessage(ctx, message, "primary", now)
}

func shouldAttachToImmediateCluster(message sqlitestore.WorkspaceMessage) bool {
	return message.Forwarded || strings.TrimSpace(message.MediaType) != ""
}

func syncTasksFromSource(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, source sqlitestore.WorkspaceMessage, clusterID int64, edited bool, now time.Time) error {
	taskTexts := ExtractTaskTexts(sourceText(source))
	if len(taskTexts) == 0 {
		return nil
	}
	existing, err := store.WorkspaceTasksBySource(ctx, source.ChatID, source.MessageID)
	if err != nil {
		return err
	}
	for i, text := range taskTexts {
		if i < len(existing) {
			task := existing[i]
			if task.Status != "open" && task.Status != "deferred" {
				continue
			}
			emoji := task.Emoji
			if emoji == "" {
				emoji = pleasantTaskEmoji(task.ID)
			}
			if err := store.UpdateWorkspaceTaskText(ctx, task.ID, text, emoji, now); err != nil {
				return err
			}
			task.Text = text
			task.Emoji = emoji
			if task.CardChatID != 0 && task.CardMessageID != 0 {
				_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
					ChatID:      task.CardChatID,
					MessageID:   task.CardMessageID,
					Text:        FormatTaskCardIn(task, mustLocation(cfg.Timezone)),
					ParseMode:   "HTML",
					ReplyMarkup: TaskActionMarkup(task.ID),
				})
			} else if err := sendAndStoreWorkspaceTaskCard(ctx, cfg, store, client, source, clusterID, task, now); err != nil {
				return err
			}
			continue
		}
		if err := createWorkspaceTaskCard(ctx, cfg, store, client, source, clusterID, text, now); err != nil {
			return err
		}
		if i < len(taskTexts)-1 {
			workspaceSleepOrDone(ctx, taskCardSendDelay)
		}
	}
	if edited && len(existing) > len(taskTexts) {
		return sendTaskConflictNotice(ctx, cfg, client, source, len(existing), len(taskTexts))
	}
	return nil
}

func createWorkspaceTaskCard(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, source sqlitestore.WorkspaceMessage, clusterID int64, text string, now time.Time) error {
	task, err := store.CreateWorkspaceTask(ctx, sqlitestore.WorkspaceTask{
		SourceChatID:    source.ChatID,
		SourceMessageID: source.MessageID,
		SourceLink:      source.SourceLink,
		SourceClusterID: clusterID,
		Text:            text,
		Status:          "open",
	}, now)
	if err != nil {
		return err
	}
	task.Emoji = pleasantTaskEmoji(task.ID)
	if err := store.UpdateWorkspaceTaskText(ctx, task.ID, task.Text, task.Emoji, now); err != nil {
		return err
	}
	return sendAndStoreWorkspaceTaskCard(ctx, cfg, store, client, source, clusterID, task, now)
}

func sendAndStoreWorkspaceTaskCard(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, source sqlitestore.WorkspaceMessage, clusterID int64, task sqlitestore.WorkspaceTask, now time.Time) error {
	card, err := sendWorkspaceMessageResultWithRetry(ctx, client, nest.SendMessageRequest{
		ChatID:          cfg.Workspace.ChatID,
		MessageThreadID: cfg.Workspace.Topics.Tasks,
		Text:            FormatTaskCardIn(task, mustLocation(cfg.Timezone)),
		ParseMode:       "HTML",
		ReplyMarkup:     TaskActionMarkup(task.ID),
	})
	if err != nil {
		return err
	}
	if err := store.SetWorkspaceTaskCard(ctx, task.ID, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks, card.MessageID, now); err != nil {
		return err
	}
	return store.UpsertWorkspaceDerivedMessage(ctx, sqlitestore.WorkspaceDerivedMessage{
		SourceChatID:     source.ChatID,
		SourceMessageID:  source.MessageID,
		SourceClusterID:  clusterID,
		DerivedType:      "task_card",
		DerivedChatID:    cfg.Workspace.ChatID,
		DerivedTopicID:   cfg.Workspace.Topics.Tasks,
		DerivedMessageID: card.MessageID,
		Status:           "active",
	}, now)
}

func handleWorkspaceCallback(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingTaskDates map[pendingTaskDateKey]int64, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, publishDrafts map[int64]publishPreviewDraft, callback nest.CallbackQuery) {
	if action, docID, ok := ParsePublishCallback(callback.Data); ok {
		handlePublishCallback(ctx, cfg, store, client, pendingInputs, publishDrafts, callback, action, docID)
		return
	}
	action, taskID, ok := ParseTaskCallback(callback.Data)
	if !ok {
		return
	}
	if callback.Message == nil || callback.Message.Chat.ID != cfg.Workspace.ChatID || callback.Message.MessageThreadID != cfg.Workspace.Topics.Tasks {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Кнопки задач работают только в топике Задачи.")
		return
	}
	task, err := store.WorkspaceTaskByID(ctx, taskID)
	if err != nil {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не нашла задачу.")
		return
	}
	now := time.Now().UTC()
	switch action {
	case "done":
		if err := store.UpdateWorkspaceTaskStatus(ctx, taskID, "done", nil, now); err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не получилось закрыть задачу.")
			return
		}
		task.Status = "done"
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID:      callback.Message.Chat.ID,
			MessageID:   callback.Message.MessageID,
			Text:        FormatTaskCardIn(task, mustLocation(cfg.Timezone)),
			ParseMode:   "HTML",
			ReplyMarkup: emptyMarkup(),
		})
		_ = updateTaskBacklog(ctx, cfg, store, client, now)
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Готово.")
	case "cancel":
		if err := store.UpdateWorkspaceTaskStatus(ctx, taskID, "cancelled", nil, now); err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не получилось отменить.")
			return
		}
		task.Status = "cancelled"
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID:      callback.Message.Chat.ID,
			MessageID:   callback.Message.MessageID,
			Text:        FormatTaskCardIn(task, mustLocation(cfg.Timezone)),
			ParseMode:   "HTML",
			ReplyMarkup: emptyMarkup(),
		})
		_ = updateTaskBacklog(ctx, cfg, store, client, now)
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Отменила.")
	case "defer":
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID:      callback.Message.Chat.ID,
			MessageID:   callback.Message.MessageID,
			Text:        FormatTaskCardIn(task, mustLocation(cfg.Timezone)),
			ParseMode:   "HTML",
			ReplyMarkup: TaskDeferMarkup(task.ID),
		})
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "На когда отложить?")
	case "defer_week", "defer_month", "defer_none":
		deferUntil := deferPreset(action, time.Now().In(mustLocation(cfg.Timezone)))
		if err := store.UpdateWorkspaceTaskStatus(ctx, taskID, "deferred", deferUntil, now); err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не получилось отложить.")
			return
		}
		task.Status = "deferred"
		task.DeferredUntil = deferUntilUTC(deferUntil)
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID:      callback.Message.Chat.ID,
			MessageID:   callback.Message.MessageID,
			Text:        FormatTaskCardIn(task, mustLocation(cfg.Timezone)),
			ParseMode:   "HTML",
			ReplyMarkup: TaskActionMarkup(task.ID),
		})
		_ = updateTaskBacklog(ctx, cfg, store, client, now)
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Отложила.")
	case "defer_custom":
		key := pendingTaskDateKey{chatID: callback.Message.Chat.ID, threadID: callback.Message.MessageThreadID, userID: callback.From.ID}
		pendingTaskDates[key] = taskID
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          callback.Message.Chat.ID,
			MessageThreadID: callback.Message.MessageThreadID,
			Text:            "Напиши дату: <code>DD.MM</code> или <code>DD.MM HH:MM</code>.",
			ParseMode:       "HTML",
		})
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Жду дату в Задачах.")
	}
}

func handlePendingTaskDateMessage(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingTaskDates map[pendingTaskDateKey]int64, message nest.Message, threadID int) bool {
	if message.From == nil || message.Chat.ID != cfg.Workspace.ChatID || threadID != cfg.Workspace.Topics.Tasks {
		return false
	}
	key := pendingTaskDateKey{chatID: message.Chat.ID, threadID: threadID, userID: message.From.ID}
	taskID, ok := pendingTaskDates[key]
	if !ok {
		return false
	}
	if isCancelText(message.Text) {
		delete(pendingTaskDates, key)
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          message.Chat.ID,
			MessageThreadID: threadID,
			Text:            "Отменила ожидание даты.",
		})
		return true
	}
	location := mustLocation(cfg.Timezone)
	parsed, err := ParseDeferredTaskDate(message.Text, time.Now().In(location), location)
	if err != nil {
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          message.Chat.ID,
			MessageThreadID: threadID,
			Text:            "Не разобрала дату. Подойдут <code>DD.MM</code>, <code>DD.MM HH:MM</code>, <code>DD.MM.YYYY</code> или <code>YYYY-MM-DD HH:MM</code>.",
			ParseMode:       "HTML",
		})
		return true
	}
	now := time.Now().UTC()
	utc := parsed.UTC()
	if err := store.UpdateWorkspaceTaskStatus(ctx, taskID, "deferred", &utc, now); err != nil {
		sendWorkspaceError(ctx, client, message.Chat.ID, threadID, "Не удалось отложить задачу", err)
		return true
	}
	delete(pendingTaskDates, key)
	task, err := store.WorkspaceTaskByID(ctx, taskID)
	if err == nil && task.CardChatID != 0 && task.CardMessageID != 0 {
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID:      task.CardChatID,
			MessageID:   task.CardMessageID,
			Text:        FormatTaskCardIn(task, mustLocation(cfg.Timezone)),
			ParseMode:   "HTML",
			ReplyMarkup: TaskActionMarkup(task.ID),
		})
	}
	_ = updateTaskBacklog(ctx, cfg, store, client, now)
	_ = client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          message.Chat.ID,
		MessageThreadID: threadID,
		Text:            "Отложила до <b>" + html.EscapeString(formatTaskDateRelative(parsed, time.Now().In(location))) + "</b>.",
		ParseMode:       "HTML",
	})
	return true
}

func updateTaskBacklog(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, now time.Time) error {
	tasks, err := store.DeferredWorkspaceTasks(ctx, 100)
	if err != nil {
		return err
	}
	location := mustLocation(cfg.Timezone)
	text := formatTaskBacklog(tasks, location, time.Now().In(location))
	messageID, ok, err := store.WorkspaceTopicIndexMessage(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks, taskBacklogIndexKey)
	if err != nil {
		return err
	}
	if ok {
		if err := client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID:    cfg.Workspace.ChatID,
			MessageID: messageID,
			Text:      text,
			ParseMode: "HTML",
		}); err == nil || isTelegramMessageNotModified(err) {
			return nil
		} else {
			return err
		}
	}
	message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
		ChatID:          cfg.Workspace.ChatID,
		MessageThreadID: cfg.Workspace.Topics.Tasks,
		Text:            text,
		ParseMode:       "HTML",
	})
	if err != nil {
		return err
	}
	return store.UpsertWorkspaceTopicIndex(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks, taskBacklogIndexKey, message.MessageID, now)
}

func handleClusterCommand(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, message nest.Message, threadID int, rest string) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		sendClusterHelp(ctx, client, cfg.Workspace.ChatID, threadID)
		return
	}
	action := strings.ToLower(fields[0])
	args := fields[1:]
	now := time.Now().UTC()
	switch action {
	case "help":
		sendClusterHelp(ctx, client, cfg.Workspace.ChatID, threadID)
	case "show":
		cluster, ok, err := resolveClusterForShow(ctx, store, message, args)
		if err != nil {
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось показать cluster", err)
			return
		}
		if !ok {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID, Text: "Не нашла cluster. Ответь командой на сообщение или передай ID."})
			return
		}
		sendClusterSummary(ctx, store, client, cfg.Workspace.ChatID, threadID, cluster)
	case "merge":
		messageIDs := clusterCommandMessageIDs(message, args)
		if len(messageIDs) < 2 {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID, Text: "Для merge нужны минимум два message_id или reply + message_id."})
			return
		}
		var target sqlitestore.WorkspaceCluster
		var sourceIDs []int64
		for i, messageID := range messageIDs {
			cluster, ok, err := store.WorkspaceClusterByMessage(ctx, message.Chat.ID, messageID)
			if err != nil {
				sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось найти cluster", err)
				return
			}
			if !ok {
				sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось найти cluster", fmt.Errorf("message %d is not clustered", messageID))
				return
			}
			if i == 0 {
				target = cluster
			} else {
				sourceIDs = append(sourceIDs, cluster.ID)
			}
		}
		if err := store.MergeWorkspaceClusters(ctx, target.ID, sourceIDs, now); err != nil {
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось объединить clusters", err)
			return
		}
		sendClusterSummary(ctx, store, client, cfg.Workspace.ChatID, threadID, target)
	case "attach":
		if len(args) == 0 {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID, Text: "Формат: /cluster attach <cluster_id> <message_id|link...> или reply + /cluster attach <message_id|link...>."})
			return
		}
		clusterID, messageArgs, err := resolveAttachTargetCluster(ctx, store, message, args)
		if err != nil {
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось выбрать target cluster", err)
			return
		}
		messageIDs := clusterCommandMessageIDs(message, messageArgs)
		if len(messageIDs) == 0 {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID, Text: "Нужен хотя бы один message_id, ссылка или reply."})
			return
		}
		if err := store.AttachWorkspaceMessagesToCluster(ctx, clusterID, message.Chat.ID, messageIDs, now); err != nil {
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось attach", err)
			return
		}
		cluster, ok, err := store.WorkspaceClusterByID(ctx, clusterID)
		if err != nil || !ok {
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось показать cluster", err)
			return
		}
		sendClusterSummary(ctx, store, client, cfg.Workspace.ChatID, threadID, cluster)
	case "detach", "split":
		messageIDs := clusterCommandMessageIDs(message, args)
		if len(messageIDs) == 0 {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID, Text: "Нужен message_id или reply."})
			return
		}
		clusters, err := store.DetachWorkspaceMessages(ctx, message.Chat.ID, messageIDs, now)
		if err != nil {
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось отделить сообщение", err)
			return
		}
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.Workspace.ChatID,
			MessageThreadID: threadID,
			Text:            fmt.Sprintf("Готово: создано отдельных clusters: %d.", len(clusters)),
		})
	default:
		sendClusterHelp(ctx, client, cfg.Workspace.ChatID, threadID)
	}
}

func handleWorkspaceDocumentCommand(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, publishDrafts map[int64]publishPreviewDraft, message nest.Message, threadID int, command, rest string) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		sendWorkspaceDocumentHelp(ctx, client, cfg.Workspace.ChatID, threadID, command)
		return
	}
	action := strings.ToLower(fields[0])
	body := strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
	now := time.Now().UTC()
	var err error
	switch command {
	case "note", "doc":
		err = handleNoteCommand(ctx, cfg, store, client, pendingInputs, publishDrafts, message, threadID, action, body, now)
	case "template":
		err = handleTemplateCommand(ctx, cfg, store, client, pendingInputs, message, threadID, action, body, now)
	case "collection":
		err = handleCollectionCommand(ctx, cfg, store, client, pendingInputs, message, threadID, action, body, now)
	}
	if err != nil {
		sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось выполнить /"+command, err)
	}
}

func handleNoteCommand(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, publishDrafts map[int64]publishPreviewDraft, message nest.Message, threadID int, action, body string, now time.Time) error {
	switch action {
	case "help":
		sendWorkspaceDocumentHelp(ctx, client, cfg.Workspace.ChatID, threadID, "note")
	case "new":
		title := strings.TrimSpace(body)
		if title == "" {
			return fmt.Errorf("format: reply + /note new <название>")
		}
		part, err := workspaceDocumentPartFromCommandSource(ctx, cfg, store, message, "note", "", now)
		if err != nil {
			return err
		}
		doc, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{
			Type:            "note",
			Status:          "active",
			Title:           title,
			SourceChatID:    part.SourceChatID,
			SourceMessageID: part.SourceMessageID,
			SourceClusterID: part.SourceClusterID,
			SourceLink:      part.SourceLink,
		}, part, now)
		if err != nil {
			return err
		}
		if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, "note", now); err != nil {
			return err
		}
		return sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, fmt.Sprintf("Создала заметку <b>#%d</b>: %s", doc.ID, html.EscapeString(doc.Title)))
	case "append":
		ref, title := parseDocumentRefAndOptionalTitle(body)
		if ref == "" {
			return fmt.Errorf("format: reply + /doc append <id|название заметки> [название части]")
		}
		doc, err := resolveWorkspaceDocumentRef(ctx, store, "note", ref)
		if err != nil {
			return sendDocumentResolveError(ctx, client, cfg.Workspace.ChatID, threadID, "note", ref, err)
		}
		if err := requireWorkspaceDocumentActive(doc); err != nil {
			return err
		}
		part, err := workspaceDocumentPartFromCommandSource(ctx, cfg, store, message, "note", title, now)
		if err != nil {
			return err
		}
		part.DocumentID = doc.ID
		if _, err := store.AddWorkspaceDocumentPart(ctx, part, now); err != nil {
			return err
		}
		if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, "note", now); err != nil {
			return err
		}
		return sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, fmt.Sprintf("Добавила часть в заметку <b>#%d</b>.", doc.ID))
	case "rename":
		doc, _, err := workspaceDocumentFromReply(ctx, store, message, "note")
		if err != nil {
			return err
		}
		title := strings.TrimSpace(body)
		if title == "" {
			return setPendingWorkspaceInput(ctx, client, pendingInputs, message, threadID, pendingWorkspaceInput{Kind: "note_rename", DocumentID: doc.ID}, "Напиши новый заголовок заметки или <code>Отмена</code>.")
		}
		return renameWorkspaceDocument(ctx, cfg, store, client, threadID, doc.ID, title, now)
	case "rename-part":
		_, part, err := workspaceDocumentFromReply(ctx, store, message, "note")
		if err != nil {
			return err
		}
		title := strings.TrimSpace(body)
		if title == "" {
			return setPendingWorkspaceInput(ctx, client, pendingInputs, message, threadID, pendingWorkspaceInput{Kind: "note_rename_part", DocumentID: part.DocumentID, PartID: part.ID}, "Напиши новый заголовок части или <code>Отмена</code>.")
		}
		return renameWorkspaceDocumentPart(ctx, cfg, store, client, threadID, part.ID, title, now)
	case "delete-part":
		doc, part, err := workspaceDocumentFromReply(ctx, store, message, "note")
		if err != nil {
			return err
		}
		parts, err := store.WorkspaceDocumentParts(ctx, doc.ID)
		if err != nil {
			return err
		}
		if part.PartNo == 1 && len(parts) > 1 {
			return setPendingWorkspaceInput(ctx, client, pendingInputs, message, threadID, pendingWorkspaceInput{Kind: "note_delete_first_part", DocumentID: doc.ID, PartID: part.ID}, "Эта часть сейчас главная. Напиши новый заголовок заметки, и я уберу часть из индекса. Или <code>Отмена</code>.")
		}
		if part.PartNo == 1 {
			return fmt.Errorf("это единственная часть заметки; используй /doc delete, чтобы убрать всю заметку из индекса")
		}
		if err := store.DeleteWorkspaceDocumentPart(ctx, part.ID, now); err != nil {
			return err
		}
		if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, "note", now); err != nil {
			return err
		}
		return sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, "Убрала часть из индекса заметки.")
	case "delete":
		doc, _, err := workspaceDocumentFromReply(ctx, store, message, "note")
		if err != nil {
			return err
		}
		return setPendingWorkspaceInput(ctx, client, pendingInputs, message, threadID, pendingWorkspaceInput{Kind: "note_delete", DocumentID: doc.ID, Title: doc.Title}, "Убрать заметку <b>"+html.EscapeString(doc.Title)+"</b> из индекса? Напиши <code>Удалить</code> или <code>Отмена</code>.")
	case "publish":
		return handleNotePublishCommand(ctx, cfg, store, client, publishDrafts, message, threadID, body, now)
	case "show":
		return sendWorkspaceDocumentShow(ctx, cfg, store, client, threadID, "note", strings.TrimSpace(body), now)
	default:
		sendWorkspaceDocumentHelp(ctx, client, cfg.Workspace.ChatID, threadID, "note")
	}
	return nil
}

func handleTemplateCommand(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, message nest.Message, threadID int, action, body string, now time.Time) error {
	switch action {
	case "help":
		sendWorkspaceDocumentHelp(ctx, client, cfg.Workspace.ChatID, threadID, "template")
	case "new":
		category, title, partTitle := parseTemplateNewBody(body)
		if title == "" {
			return fmt.Errorf("format: reply + /template new <категория> | <название> [| название части]")
		}
		part, err := workspaceDocumentPartFromCommandSource(ctx, cfg, store, message, "template", partTitle, now)
		if err != nil {
			return err
		}
		doc, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{
			Type:            "template",
			Status:          "active",
			Title:           title,
			Category:        category,
			SourceChatID:    part.SourceChatID,
			SourceMessageID: part.SourceMessageID,
			SourceClusterID: part.SourceClusterID,
			SourceLink:      part.SourceLink,
		}, part, now)
		if err != nil {
			return err
		}
		if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, "template", now); err != nil {
			return err
		}
		return sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, fmt.Sprintf("Создала заготовку <b>#%d</b>: %s", doc.ID, html.EscapeString(doc.Title)))
	case "append":
		ref, title := parseTemplateAppendBody(body)
		if ref == "" {
			return fmt.Errorf("format: reply + /template append <id|название шаблона> | <название части>")
		}
		doc, err := resolveWorkspaceDocumentRef(ctx, store, "template", ref)
		if err != nil {
			return sendDocumentResolveError(ctx, client, cfg.Workspace.ChatID, threadID, "template", ref, err)
		}
		if err := requireWorkspaceDocumentActive(doc); err != nil {
			return err
		}
		part, err := workspaceDocumentPartFromCommandSource(ctx, cfg, store, message, "template", title, now)
		if err != nil {
			return err
		}
		part.DocumentID = doc.ID
		if _, err := store.AddWorkspaceDocumentPart(ctx, part, now); err != nil {
			return err
		}
		if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, "template", now); err != nil {
			return err
		}
		return sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, fmt.Sprintf("Добавила часть в заготовку <b>#%d</b>.", doc.ID))
	case "rename":
		doc, _, err := workspaceDocumentFromReply(ctx, store, message, "template")
		if err != nil {
			return err
		}
		title := strings.TrimSpace(body)
		if title == "" {
			return setPendingWorkspaceInput(ctx, client, pendingInputs, message, threadID, pendingWorkspaceInput{Kind: "template_rename", DocumentID: doc.ID}, "Напиши новое название заготовки или <code>Отмена</code>.")
		}
		return renameWorkspaceDocument(ctx, cfg, store, client, threadID, doc.ID, title, now)
	case "type":
		doc, _, err := workspaceDocumentFromReply(ctx, store, message, "template")
		if err != nil {
			return err
		}
		category := strings.TrimSpace(body)
		if category == "" {
			return setPendingWorkspaceInput(ctx, client, pendingInputs, message, threadID, pendingWorkspaceInput{Kind: "template_type", DocumentID: doc.ID}, "Напиши тип заготовки или <code>Отмена</code>. Для общего слоя подойдёт <code>Остальные</code>.")
		}
		return updateWorkspaceDocumentCategory(ctx, cfg, store, client, threadID, doc.ID, category, now)
	case "show":
		return sendWorkspaceDocumentShow(ctx, cfg, store, client, threadID, "template", strings.TrimSpace(body), now)
	default:
		sendWorkspaceDocumentHelp(ctx, client, cfg.Workspace.ChatID, threadID, "template")
	}
	return nil
}

func handleCollectionCommand(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, message nest.Message, threadID int, action, body string, now time.Time) error {
	switch action {
	case "help":
		sendWorkspaceDocumentHelp(ctx, client, cfg.Workspace.ChatID, threadID, "collection")
	case "new":
		return startCollectionCreate(ctx, cfg, store, client, pendingInputs, message, threadID, body, now)
	case "add":
		ref, itemTitle, legacyCategory := parseCollectionAddBody(body)
		if ref == "" {
			return fmt.Errorf("format: reply + /collection add <название коллекции> [| название элемента]")
		}
		part, err := workspaceDocumentPartFromCommandSource(ctx, cfg, store, message, "collection", itemTitle, now)
		if err != nil {
			return err
		}
		part.Title = itemTitle
		doc, err := resolveWorkspaceDocumentRef(ctx, store, "collection", ref)
		if err != nil {
			if _, ok := err.(documentNotFoundError); !ok || legacyCategory == "" {
				return sendDocumentResolveError(ctx, client, cfg.Workspace.ChatID, threadID, "collection", ref, err)
			}
			doc, err = store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{
				Type:            "collection",
				Status:          "active",
				Title:           ref,
				Category:        legacyCategory,
				SourceChatID:    part.SourceChatID,
				SourceMessageID: part.SourceMessageID,
				SourceClusterID: part.SourceClusterID,
				SourceLink:      part.SourceLink,
			}, part, now)
			if err != nil {
				return err
			}
		} else {
			if err := requireWorkspaceDocumentActive(doc); err != nil {
				return err
			}
			part.DocumentID = doc.ID
			if _, err := store.AddWorkspaceDocumentPart(ctx, part, now); err != nil {
				return err
			}
		}
		if err := syncCollectionMessage(ctx, cfg, store, client, doc.ID, now); err != nil {
			return err
		}
		if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, "collection", now); err != nil {
			return err
		}
		return sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, fmt.Sprintf("Добавила в коллекцию <b>#%d</b>: %s", doc.ID, html.EscapeString(doc.Title)))
	case "rename":
		doc, _, err := workspaceDocumentFromReply(ctx, store, message, "collection")
		if err != nil {
			return err
		}
		title := strings.TrimSpace(body)
		if title == "" {
			return setPendingWorkspaceInput(ctx, client, pendingInputs, message, threadID, pendingWorkspaceInput{Kind: "collection_rename", DocumentID: doc.ID}, "Напиши новое название коллекции или <code>Отмена</code>.")
		}
		if err := renameWorkspaceDocument(ctx, cfg, store, client, threadID, doc.ID, title, now); err != nil {
			return err
		}
		return syncCollectionMessage(ctx, cfg, store, client, doc.ID, now)
	case "show":
		return sendWorkspaceDocumentShow(ctx, cfg, store, client, threadID, "collection", strings.TrimSpace(body), now)
	default:
		sendWorkspaceDocumentHelp(ctx, client, cfg.Workspace.ChatID, threadID, "collection")
	}
	return nil
}

func handleWorkspaceNewCommand(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, message nest.Message, threadID int, rest string) {
	fields := strings.Fields(rest)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "collection") {
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.Workspace.ChatID,
			MessageThreadID: threadID,
			Text:            "Пока здесь живёт <code>/new collection Название</code>.",
			ParseMode:       "HTML",
		})
		return
	}
	body := strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
	if err := startCollectionCreate(ctx, cfg, store, client, pendingInputs, message, threadID, body, time.Now().UTC()); err != nil {
		sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось создать коллекцию", err)
	}
}

func startCollectionCreate(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, message nest.Message, threadID int, body string, now time.Time) error {
	parts := splitPipeFields(body)
	title := ""
	description := ""
	if len(parts) > 0 {
		title = parts[0]
	}
	if len(parts) > 1 {
		description = parts[1]
	}
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("format: /new collection <название>")
	}
	if description == "" {
		return setPendingWorkspaceInput(ctx, client, pendingInputs, message, threadID, pendingWorkspaceInput{Kind: "collection_description", Title: title}, "Напиши короткое описание коллекции или <code>Без описания</code>. Отменить можно словом <code>Отмена</code>.")
	}
	_, err := createWorkspaceCollection(ctx, cfg, store, client, title, description, now)
	return err
}

func handlePendingWorkspaceInputMessage(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, publishDrafts map[int64]publishPreviewDraft, message nest.Message, threadID int) bool {
	if message.From == nil || message.Chat.ID != cfg.Workspace.ChatID {
		return false
	}
	key := pendingTaskDateKey{chatID: message.Chat.ID, threadID: threadID, userID: message.From.ID}
	pending, ok := pendingInputs[key]
	if !ok {
		return false
	}
	if isCancelText(message.Text) {
		delete(pendingInputs, key)
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          message.Chat.ID,
			MessageThreadID: threadID,
			Text:            "Отменила.",
		})
		return true
	}
	now := time.Now().UTC()
	value := strings.TrimSpace(message.Text)
	var err error
	switch pending.Kind {
	case "note_rename", "template_rename", "collection_rename":
		err = renameWorkspaceDocument(ctx, cfg, store, client, threadID, pending.DocumentID, value, now)
		if err == nil && pending.Kind == "collection_rename" {
			err = syncCollectionMessage(ctx, cfg, store, client, pending.DocumentID, now)
		}
	case "note_rename_part":
		err = renameWorkspaceDocumentPart(ctx, cfg, store, client, threadID, pending.PartID, value, now)
	case "note_delete_first_part":
		if strings.TrimSpace(value) == "" {
			err = fmt.Errorf("new note title is required")
			break
		}
		err = store.UpdateWorkspaceDocumentTitle(ctx, pending.DocumentID, value, now)
		if err == nil {
			err = store.DeleteWorkspaceDocumentPart(ctx, pending.PartID, now)
		}
		if err == nil {
			err = updateWorkspaceDocumentIndex(ctx, cfg, store, client, "note", now)
		}
		if err == nil {
			err = sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, "Убрала главную часть и обновила заголовок заметки.")
		}
	case "note_delete":
		if !isDeleteConfirmText(value) {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{
				ChatID:          message.Chat.ID,
				MessageThreadID: threadID,
				Text:            "Не удаляю. Напиши <code>Удалить</code> или <code>Отмена</code>.",
				ParseMode:       "HTML",
			})
			return true
		}
		err = store.UpdateWorkspaceDocumentStatus(ctx, pending.DocumentID, "archived", now)
		if err == nil {
			err = updateWorkspaceDocumentIndex(ctx, cfg, store, client, "note", now)
		}
		if err == nil {
			err = sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, "Убрала заметку из индекса. Исходные сообщения не трогала.")
		}
	case "template_type":
		err = updateWorkspaceDocumentCategory(ctx, cfg, store, client, threadID, pending.DocumentID, value, now)
	case "collection_description":
		description := value
		if strings.EqualFold(description, "без описания") {
			description = ""
		}
		_, err = createWorkspaceCollection(ctx, cfg, store, client, pending.Title, description, now)
		if err == nil {
			err = sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, "Создала коллекцию <b>"+html.EscapeString(pending.Title)+"</b>.")
		}
	case "publish_revision":
		err = rerunPublishPreview(ctx, cfg, store, client, publishDrafts, pending.DocumentID, value, now)
	default:
		err = fmt.Errorf("unknown pending input %q", pending.Kind)
	}
	if err != nil {
		sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось обработать ответ", err)
		return true
	}
	delete(pendingInputs, key)
	return true
}

func setPendingWorkspaceInput(ctx context.Context, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, message nest.Message, threadID int, pending pendingWorkspaceInput, prompt string) error {
	if message.From == nil {
		return fmt.Errorf("user identity is required")
	}
	key := pendingTaskDateKey{chatID: message.Chat.ID, threadID: threadID, userID: message.From.ID}
	pendingInputs[key] = pending
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          message.Chat.ID,
		MessageThreadID: threadID,
		Text:            prompt,
		ParseMode:       "HTML",
	})
}

func renameWorkspaceDocument(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, threadID int, documentID int64, title string, now time.Time) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("new document title is required")
	}
	if err := store.UpdateWorkspaceDocumentTitle(ctx, documentID, title, now); err != nil {
		return err
	}
	doc, err := store.WorkspaceDocumentByID(ctx, documentID)
	if err != nil {
		return err
	}
	if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, doc.Type, now); err != nil {
		return err
	}
	return sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, "Переименовала: <b>"+html.EscapeString(title)+"</b>.")
}

func renameWorkspaceDocumentPart(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, threadID int, partID int64, title string, now time.Time) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("new part title is required")
	}
	if err := store.UpdateWorkspaceDocumentPartTitle(ctx, partID, title, now); err != nil {
		return err
	}
	part, err := store.WorkspaceDocumentPartByID(ctx, partID)
	if err != nil {
		return err
	}
	doc, err := store.WorkspaceDocumentByID(ctx, part.DocumentID)
	if err != nil {
		return err
	}
	if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, doc.Type, now); err != nil {
		return err
	}
	return sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, "Переименовала часть: <b>"+html.EscapeString(title)+"</b>.")
}

func updateWorkspaceDocumentCategory(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, threadID int, documentID int64, category string, now time.Time) error {
	category = strings.TrimSpace(category)
	if category == "" {
		category = "Остальные"
	}
	if err := store.UpdateWorkspaceDocumentCategory(ctx, documentID, category, now); err != nil {
		return err
	}
	doc, err := store.WorkspaceDocumentByID(ctx, documentID)
	if err != nil {
		return err
	}
	if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, doc.Type, now); err != nil {
		return err
	}
	return sendWorkspaceDocumentDone(ctx, client, cfg.Workspace.ChatID, threadID, "Обновила тип: <b>"+html.EscapeString(category)+"</b>.")
}

func workspaceDocumentFromReply(ctx context.Context, store *sqlitestore.Store, message nest.Message, docType string) (sqlitestore.WorkspaceDocument, sqlitestore.WorkspaceDocumentPart, error) {
	if message.ReplyToMessage == nil || message.ReplyToMessage.MessageID == 0 {
		return sqlitestore.WorkspaceDocument{}, sqlitestore.WorkspaceDocumentPart{}, fmt.Errorf("reply to a %s message first", workspaceDocumentIndexTopicName(docType))
	}
	if doc, ok, err := store.WorkspaceDocumentByTargetMessage(ctx, message.Chat.ID, message.ReplyToMessage.MessageID); err != nil {
		return sqlitestore.WorkspaceDocument{}, sqlitestore.WorkspaceDocumentPart{}, err
	} else if ok && doc.Type == docType && doc.Status == "active" {
		parts, err := store.WorkspaceDocumentParts(ctx, doc.ID)
		if err != nil {
			return sqlitestore.WorkspaceDocument{}, sqlitestore.WorkspaceDocumentPart{}, err
		}
		var first sqlitestore.WorkspaceDocumentPart
		if len(parts) > 0 {
			first = parts[0]
		}
		return doc, first, nil
	}
	parts, err := store.WorkspaceDocumentPartsBySource(ctx, message.Chat.ID, message.ReplyToMessage.MessageID)
	if err != nil {
		return sqlitestore.WorkspaceDocument{}, sqlitestore.WorkspaceDocumentPart{}, err
	}
	var matches []struct {
		doc  sqlitestore.WorkspaceDocument
		part sqlitestore.WorkspaceDocumentPart
	}
	for _, part := range parts {
		doc, err := store.WorkspaceDocumentByID(ctx, part.DocumentID)
		if err != nil {
			return sqlitestore.WorkspaceDocument{}, sqlitestore.WorkspaceDocumentPart{}, err
		}
		if doc.Type == docType && doc.Status == "active" {
			matches = append(matches, struct {
				doc  sqlitestore.WorkspaceDocument
				part sqlitestore.WorkspaceDocumentPart
			}{doc: doc, part: part})
		}
	}
	if len(matches) == 0 {
		return sqlitestore.WorkspaceDocument{}, sqlitestore.WorkspaceDocumentPart{}, fmt.Errorf("reply message is not attached to an active %s", workspaceDocumentIndexTopicName(docType))
	}
	if len(matches) > 1 {
		return sqlitestore.WorkspaceDocument{}, sqlitestore.WorkspaceDocumentPart{}, fmt.Errorf("reply message is attached to several %s items; use an explicit id", workspaceDocumentIndexTopicName(docType))
	}
	return matches[0].doc, matches[0].part, nil
}

type documentNotFoundError struct {
	DocType string
	Ref     string
}

func (e documentNotFoundError) Error() string {
	return fmt.Sprintf("%s %q not found", e.DocType, e.Ref)
}

type documentAmbiguousError struct {
	DocType string
	Ref     string
	Matches []sqlitestore.WorkspaceDocument
}

func (e documentAmbiguousError) Error() string {
	return fmt.Sprintf("%s %q is ambiguous", e.DocType, e.Ref)
}

func resolveWorkspaceDocumentRef(ctx context.Context, store *sqlitestore.Store, docType, ref string) (sqlitestore.WorkspaceDocument, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return sqlitestore.WorkspaceDocument{}, documentNotFoundError{DocType: docType, Ref: ref}
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil && id > 0 {
		doc, err := store.WorkspaceDocumentByID(ctx, id)
		if err != nil {
			return sqlitestore.WorkspaceDocument{}, err
		}
		if doc.Type != docType {
			return sqlitestore.WorkspaceDocument{}, fmt.Errorf("document #%d is %s, not %s", id, doc.Type, docType)
		}
		return doc, nil
	}
	docs, err := store.WorkspaceDocumentsByType(ctx, docType, []string{"active"}, 1000)
	if err != nil {
		return sqlitestore.WorkspaceDocument{}, err
	}
	var matches []sqlitestore.WorkspaceDocument
	for _, doc := range docs {
		if strings.EqualFold(strings.TrimSpace(doc.Title), ref) {
			matches = append(matches, doc)
		}
	}
	if len(matches) == 0 {
		return sqlitestore.WorkspaceDocument{}, documentNotFoundError{DocType: docType, Ref: ref}
	}
	if len(matches) > 1 {
		return sqlitestore.WorkspaceDocument{}, documentAmbiguousError{DocType: docType, Ref: ref, Matches: matches}
	}
	return matches[0], nil
}

func sendDocumentResolveError(ctx context.Context, client *nest.Client, chatID int64, threadID int, docType, ref string, err error) error {
	if ambiguous, ok := err.(documentAmbiguousError); ok {
		var b strings.Builder
		b.WriteString("Нашла несколько вариантов для <b>")
		b.WriteString(html.EscapeString(ref))
		b.WriteString("</b>. Укажи ID:\n\n")
		for _, doc := range ambiguous.Matches {
			fmt.Fprintf(&b, "• <code>%d</code> — %s\n", doc.ID, html.EscapeString(doc.Title))
		}
		return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: chatID, MessageThreadID: threadID, Text: strings.TrimSpace(b.String()), ParseMode: "HTML"})
	}
	return err
}

func requireWorkspaceDocumentActive(doc sqlitestore.WorkspaceDocument) error {
	if doc.Status != "active" {
		return fmt.Errorf("document #%d is %s, not active", doc.ID, doc.Status)
	}
	return nil
}

func createWorkspaceCollection(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, title, description string, now time.Time) (sqlitestore.WorkspaceDocument, error) {
	doc, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{
		Type:     "collection",
		Status:   "active",
		Title:    title,
		Category: strings.TrimSpace(description),
	}, sqlitestore.WorkspaceDocumentPart{}, now)
	if err != nil {
		return sqlitestore.WorkspaceDocument{}, err
	}
	if err := syncCollectionMessage(ctx, cfg, store, client, doc.ID, now); err != nil {
		return sqlitestore.WorkspaceDocument{}, err
	}
	if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, "collection", now); err != nil {
		return sqlitestore.WorkspaceDocument{}, err
	}
	return store.WorkspaceDocumentByID(ctx, doc.ID)
}

func syncCollectionMessage(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, documentID int64, now time.Time) error {
	doc, err := store.WorkspaceDocumentByID(ctx, documentID)
	if err != nil {
		return err
	}
	parts, err := store.WorkspaceDocumentParts(ctx, documentID)
	if err != nil {
		return err
	}
	text := renderCollectionDocumentMessage(doc, parts)
	if doc.TargetChatID != 0 && doc.TargetMessageID != 0 {
		if err := client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID:    doc.TargetChatID,
			MessageID: doc.TargetMessageID,
			Text:      text,
			ParseMode: "HTML",
		}); err == nil || isTelegramMessageNotModified(err) {
			return nil
		}
	}
	message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
		ChatID:          cfg.Workspace.ChatID,
		MessageThreadID: cfg.Workspace.Topics.Collections,
		Text:            text,
		ParseMode:       "HTML",
	})
	if err != nil {
		return err
	}
	return store.UpdateWorkspaceDocumentTarget(ctx, documentID, cfg.Workspace.ChatID, cfg.Workspace.Topics.Collections, message.MessageID, nil, now)
}

func renderCollectionDocumentMessage(doc sqlitestore.WorkspaceDocument, parts []sqlitestore.WorkspaceDocumentPart) string {
	var b strings.Builder
	b.WriteString("🗂 <b>")
	b.WriteString(html.EscapeString(doc.Title))
	b.WriteString("</b> ")
	b.WriteString(pleasantDocumentEmoji(doc.ID))
	if strings.TrimSpace(doc.Category) != "" {
		b.WriteString("\n\n")
		b.WriteString(html.EscapeString(doc.Category))
	}
	b.WriteString("\n\n")
	if len(parts) == 0 {
		b.WriteString("<i>Пока пусто.</i>")
	} else {
		for _, part := range parts {
			b.WriteString("• ")
			writeHTMLLinkOrText(&b, part.SourceLink, documentPartTitle(part, compactWorkspaceLine(part.Text, 80)))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n<blockquote>Можно пополнять по reply через <code>/collection add ")
	b.WriteString(html.EscapeString(doc.Title))
	b.WriteString("</code>.</blockquote>")
	return strings.TrimSpace(b.String())
}

func isCancelText(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "отмена" || value == "cancel" || value == "/cancel"
}

func isDeleteConfirmText(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "удалить" || value == "да, удалить" || value == "delete"
}

func handleNotePublishCommand(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, publishDrafts map[int64]publishPreviewDraft, message nest.Message, threadID int, body string, now time.Time) error {
	body = strings.TrimSpace(body)
	force := false
	if strings.EqualFold(body, "force") {
		force = true
		body = ""
	}
	var doc sqlitestore.WorkspaceDocument
	var err error
	if body != "" {
		doc, err = resolveWorkspaceDocumentRef(ctx, store, "note", body)
	} else {
		doc, _, err = workspaceDocumentFromReply(ctx, store, message, "note")
	}
	if err != nil {
		return err
	}
	if err := requireWorkspaceDocumentActive(doc); err != nil {
		return err
	}
	if doc.TargetChatID != 0 && doc.TargetMessageID != 0 && !force {
		text := "Эта заметка уже опубликована: "
		writeBuilder := strings.Builder{}
		writeHTMLLinkOrText(&writeBuilder, workspaceMessageLink(doc.TargetChatID, doc.TargetTopicID, doc.TargetMessageID), doc.Title)
		text += writeBuilder.String() + "\n\nЕсли точно нужен новый дубль, отправь <code>/doc publish force</code> в reply к части заметки."
		return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: cfg.Workspace.ChatID, MessageThreadID: threadID, Text: text, ParseMode: "HTML"})
	}
	return createPublishPreview(ctx, cfg, store, client, publishDrafts, doc.ID, "", now)
}

func rerunPublishPreview(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, publishDrafts map[int64]publishPreviewDraft, documentID int64, revision string, now time.Time) error {
	if draft, ok := publishDrafts[documentID]; ok {
		deletePublishPreviewMessages(ctx, client, cfg.Workspace.ChatID, draft.PreviewMessageIDs)
	}
	return createPublishPreview(ctx, cfg, store, client, publishDrafts, documentID, revision, now)
}

func createPublishPreview(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, publishDrafts map[int64]publishPreviewDraft, documentID int64, revision string, now time.Time) error {
	doc, err := store.WorkspaceDocumentByID(ctx, documentID)
	if err != nil {
		return err
	}
	parts, err := store.WorkspaceDocumentParts(ctx, documentID)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		return fmt.Errorf("note has no parts")
	}
	provider := NewNotePublishProvider(cfg)
	result, err := provider.FormatNote(ctx, NotePublishRequest{
		Title:    doc.Title,
		Parts:    parts,
		Revision: revision,
	})
	if err != nil {
		return err
	}
	if len(result.Messages) == 0 {
		return fmt.Errorf("publish provider returned empty preview")
	}
	var messageIDs []int
	for i, text := range result.Messages {
		request := nest.SendMessageRequest{
			ChatID:          cfg.Workspace.ChatID,
			MessageThreadID: cfg.Workspace.Topics.Inbox,
			Text:            text,
			ParseMode:       "HTML",
		}
		if i == len(result.Messages)-1 {
			request.ReplyMarkup = PublishPreviewMarkup(documentID)
		}
		message, err := client.SendMessageResult(ctx, request)
		if err != nil {
			deletePublishPreviewMessages(ctx, client, cfg.Workspace.ChatID, messageIDs)
			return err
		}
		messageIDs = append(messageIDs, message.MessageID)
	}
	publishDrafts[documentID] = publishPreviewDraft{
		DocumentID:        documentID,
		PreviewMessageIDs: messageIDs,
		PreviewTexts:      result.Messages,
		Revision:          revision,
	}
	return nil
}

func handlePublishCallback(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, publishDrafts map[int64]publishPreviewDraft, callback nest.CallbackQuery, action string, documentID int64) {
	if callback.Message == nil || callback.Message.Chat.ID != cfg.Workspace.ChatID {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Эта кнопка не из Workspace.")
		return
	}
	draft, ok := publishDrafts[documentID]
	if !ok {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Preview устарел. Запусти /doc publish ещё раз.")
		return
	}
	now := time.Now().UTC()
	switch action {
	case "approve":
		if err := approvePublishPreview(ctx, cfg, store, client, documentID, draft, callback.Message, now); err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не получилось опубликовать.")
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, callback.Message.MessageThreadID, "Не удалось опубликовать", err)
			return
		}
		delete(publishDrafts, documentID)
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Опубликовала.")
	case "cancel":
		deletePublishPreviewMessages(ctx, client, cfg.Workspace.ChatID, draft.PreviewMessageIDs)
		delete(publishDrafts, documentID)
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Отменила preview.")
	case "edit":
		key := pendingTaskDateKey{chatID: callback.Message.Chat.ID, threadID: callback.Message.MessageThreadID, userID: callback.From.ID}
		pendingInputs[key] = pendingWorkspaceInput{Kind: "publish_revision", DocumentID: documentID}
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          callback.Message.Chat.ID,
			MessageThreadID: callback.Message.MessageThreadID,
			Text:            "Напиши, что поменять в preview. В mock-режиме я пересоберу текст без добавления новых фактов. Отменить можно словом <code>Отмена</code>.",
			ParseMode:       "HTML",
		})
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Жду правку.")
	default:
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Неизвестное действие.")
	}
}

func approvePublishPreview(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, documentID int64, draft publishPreviewDraft, callbackMessage *nest.Message, now time.Time) error {
	doc, err := store.WorkspaceDocumentByID(ctx, documentID)
	if err != nil {
		return err
	}
	parts, err := store.WorkspaceDocumentParts(ctx, documentID)
	if err != nil {
		return err
	}
	var published []nest.Message
	for _, text := range draft.PreviewTexts {
		message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
			ChatID:          cfg.Workspace.ChatID,
			MessageThreadID: cfg.Workspace.Topics.Useful,
			Text:            text,
			ParseMode:       "HTML",
		})
		if err != nil {
			return err
		}
		published = append(published, message)
	}
	if len(published) == 0 {
		return fmt.Errorf("nothing was published")
	}
	publishedAt := now
	if err := store.UpdateWorkspaceDocumentTarget(ctx, documentID, cfg.Workspace.ChatID, cfg.Workspace.Topics.Useful, published[0].MessageID, &publishedAt, now); err != nil {
		return err
	}
	for _, part := range parts {
		if part.SourceChatID == 0 || part.SourceMessageID == 0 {
			continue
		}
		for i, message := range published {
			derivedType := fmt.Sprintf("useful_material_part_%d_msg_%d", part.PartNo, i+1)
			if err := store.UpsertWorkspaceDerivedMessage(ctx, sqlitestore.WorkspaceDerivedMessage{
				SourceChatID:     part.SourceChatID,
				SourceMessageID:  part.SourceMessageID,
				SourceClusterID:  part.SourceClusterID,
				DerivedType:      derivedType,
				DerivedChatID:    cfg.Workspace.ChatID,
				DerivedTopicID:   cfg.Workspace.Topics.Useful,
				DerivedMessageID: message.MessageID,
				Status:           "published",
			}, now); err != nil {
				return err
			}
		}
	}
	if err := updateUsefulIndex(ctx, cfg, store, client, now); err != nil {
		return err
	}
	if callbackMessage != nil {
		text := ""
		if len(draft.PreviewTexts) > 0 {
			text = draft.PreviewTexts[len(draft.PreviewTexts)-1]
		}
		link := workspaceMessageLink(cfg.Workspace.ChatID, cfg.Workspace.Topics.Useful, published[0].MessageID)
		text = strings.TrimSpace(text) + "\n\n<i>Опубликовано в Полезное:</i> <a href=\"" + html.EscapeString(link) + "\">" + html.EscapeString(doc.Title) + "</a>"
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID:      callbackMessage.Chat.ID,
			MessageID:   callbackMessage.MessageID,
			Text:        text,
			ParseMode:   "HTML",
			ReplyMarkup: emptyMarkup(),
		})
	}
	return nil
}

func deletePublishPreviewMessages(ctx context.Context, client *nest.Client, chatID int64, messageIDs []int) {
	for _, messageID := range messageIDs {
		if messageID > 0 {
			_ = client.DeleteMessage(ctx, chatID, messageID)
		}
	}
}

func updateUsefulIndex(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, now time.Time) error {
	text, err := renderUsefulIndex(ctx, cfg, store)
	if err != nil {
		return err
	}
	_, _, err = upsertWorkspaceDocumentIndexMessage(ctx, cfg, store, client, cfg.Workspace.Topics.Useful, usefulIndexKey, text, now)
	return err
}

func renderUsefulIndex(ctx context.Context, cfg config.Config, store *sqlitestore.Store) (string, error) {
	docs, err := store.PublishedWorkspaceDocuments(ctx, "note", 100)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("💎 <b>Полезное</b>\n\n")
	b.WriteString("Готовые материалы после preview и подтверждения. Здесь живут чистые версии, а исходники остаются в Заметках.\n\n")
	if len(docs) == 0 {
		b.WriteString("<i>Пока пусто.</i>")
		return b.String(), nil
	}
	for _, doc := range docs {
		b.WriteString("• ")
		writeHTMLLinkOrText(&b, workspaceMessageLink(cfg.Workspace.ChatID, cfg.Workspace.Topics.Useful, doc.TargetMessageID), doc.Title)
		b.WriteString(" ")
		b.WriteString(pleasantDocumentEmoji(doc.ID))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func PublishPreviewMarkup(documentID int64) *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{{
		{Text: "✅ Опубликовать", CallbackData: PublishCallbackData("approve", documentID)},
		{Text: "🚫 Отменить", CallbackData: PublishCallbackData("cancel", documentID)},
		{Text: "✏️ Изменить", CallbackData: PublishCallbackData("edit", documentID)},
	}}}
}

func PublishCallbackData(action string, documentID int64) string {
	return publishCallbackPrefix + action + ":" + strconv.FormatInt(documentID, 10)
}

func ParsePublishCallback(data string) (string, int64, bool) {
	if !strings.HasPrefix(data, publishCallbackPrefix) {
		return "", 0, false
	}
	rest := strings.TrimPrefix(data, publishCallbackPrefix)
	action, idRaw, ok := strings.Cut(rest, ":")
	if !ok {
		return "", 0, false
	}
	id, err := strconv.ParseInt(idRaw, 10, 64)
	if err != nil || id <= 0 {
		return "", 0, false
	}
	return action, id, true
}

func workspaceDocumentPartFromCommandSource(ctx context.Context, cfg config.Config, store *sqlitestore.Store, message nest.Message, docType, title string, now time.Time) (sqlitestore.WorkspaceDocumentPart, error) {
	expectedTopic, _, err := workspaceDocumentIndexTarget(cfg, docType)
	if err != nil {
		return sqlitestore.WorkspaceDocumentPart{}, err
	}
	var source sqlitestore.WorkspaceMessage
	if message.ReplyToMessage != nil && message.ReplyToMessage.MessageID != 0 {
		source, err = workspaceDocumentSourceFromReply(ctx, cfg, store, message, expectedTopic, now)
	} else {
		source, err = workspaceDocumentSourceFromLatestTopic(ctx, store, message, expectedTopic)
	}
	if err != nil {
		return sqlitestore.WorkspaceDocumentPart{}, err
	}
	if source.TopicID != expectedTopic {
		return sqlitestore.WorkspaceDocumentPart{}, fmt.Errorf("source message must be in %s", workspaceDocumentIndexTopicName(docType))
	}
	clusterID := int64(0)
	if cluster, ok, err := store.WorkspaceClusterByMessage(ctx, source.ChatID, source.MessageID); err != nil {
		return sqlitestore.WorkspaceDocumentPart{}, err
	} else if ok {
		clusterID = cluster.ID
	} else if cluster, err := ensureMessageCluster(ctx, store, source, false, now); err == nil {
		clusterID = cluster.ID
	}
	return sqlitestore.WorkspaceDocumentPart{
		Title:           strings.TrimSpace(title),
		SourceChatID:    source.ChatID,
		SourceMessageID: source.MessageID,
		SourceClusterID: clusterID,
		SourceLink:      source.SourceLink,
		Text:            sourceText(source),
	}, nil
}

func workspaceDocumentSourceFromReply(ctx context.Context, cfg config.Config, store *sqlitestore.Store, message nest.Message, expectedTopic int, now time.Time) (sqlitestore.WorkspaceMessage, error) {
	if message.ReplyToMessage == nil || message.ReplyToMessage.MessageID == 0 {
		return sqlitestore.WorkspaceMessage{}, fmt.Errorf("reply to the source message first")
	}
	source, ok, err := store.WorkspaceMessageByID(ctx, message.Chat.ID, message.ReplyToMessage.MessageID)
	if err != nil {
		return sqlitestore.WorkspaceMessage{}, err
	}
	if ok {
		return source, nil
	}
	source = workspaceMessageFromBotAPI(cfg, *message.ReplyToMessage, now)
	if source.TopicID == cfg.Workspace.Topics.Inbox && expectedTopic != cfg.Workspace.Topics.Inbox {
		source.TopicID = expectedTopic
		source.SourceLink = workspaceMessageLink(source.ChatID, expectedTopic, source.MessageID)
	}
	if err := store.UpsertWorkspaceMessage(ctx, source, now); err != nil {
		return sqlitestore.WorkspaceMessage{}, err
	}
	return source, nil
}

func workspaceDocumentSourceFromLatestTopic(ctx context.Context, store *sqlitestore.Store, message nest.Message, expectedTopic int) (sqlitestore.WorkspaceMessage, error) {
	fromUserID := int64(0)
	if message.From != nil {
		fromUserID = message.From.ID
	}
	source, ok, err := store.LatestWorkspaceMessageInTopic(ctx, message.Chat.ID, expectedTopic, fromUserID, message.MessageID)
	if err != nil {
		return sqlitestore.WorkspaceMessage{}, err
	}
	if !ok {
		return sqlitestore.WorkspaceMessage{}, fmt.Errorf("no source message found in %s; send the content there first or reply to it", workspaceTopicNameByID(expectedTopic))
	}
	return source, nil
}

func workspaceTopicNameByID(topicID int) string {
	if topicID <= 0 {
		return "configured topic"
	}
	return "topic " + strconv.Itoa(topicID)
}

func updateWorkspaceDocumentIndex(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, docType string, now time.Time) error {
	topicID, key, err := workspaceDocumentIndexTarget(cfg, docType)
	if err != nil {
		return err
	}
	text, err := renderWorkspaceDocumentIndex(ctx, store, docType)
	if err != nil {
		return err
	}
	_, _, err = upsertWorkspaceDocumentIndexMessage(ctx, cfg, store, client, topicID, key, text, now)
	return err
}

func SeedWorkspaceDocumentIndexes(ctx context.Context, cfg config.Config, store *sqlitestore.Store, opts SeedDocumentIndexesOptions) (SeedDocumentIndexesResult, error) {
	if !cfg.WorkspaceConfigured() {
		return SeedDocumentIndexesResult{}, fmt.Errorf("workspace group is not fully configured")
	}
	if store == nil {
		return SeedDocumentIndexesResult{}, fmt.Errorf("store is required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	client := nest.New(cfg.Workspace.BotToken)
	result := SeedDocumentIndexesResult{DryRun: opts.DryRun}
	for _, docType := range []string{"note", "template", "collection"} {
		topicID, key, err := workspaceDocumentIndexTarget(cfg, docType)
		if err != nil {
			return SeedDocumentIndexesResult{}, err
		}
		text, err := renderWorkspaceDocumentIndex(ctx, store, docType)
		if err != nil {
			return SeedDocumentIndexesResult{}, err
		}
		item := SeedDocumentIndexItem{
			Type:    docType,
			Topic:   workspaceDocumentIndexTopicName(docType),
			TopicID: topicID,
			Status:  "dry_run",
			Text:    text,
		}
		if !opts.DryRun {
			messageID, status, err := upsertWorkspaceDocumentIndexMessage(ctx, cfg, store, client, topicID, key, text, now)
			if err != nil {
				return SeedDocumentIndexesResult{}, err
			}
			item.MessageID = messageID
			item.Status = status
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func upsertWorkspaceDocumentIndexMessage(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, topicID int, key, text string, now time.Time) (int, string, error) {
	messageID, ok, err := store.WorkspaceTopicIndexMessage(ctx, cfg.Workspace.ChatID, topicID, key)
	if err != nil {
		return 0, "", err
	}
	if ok {
		if err := client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID:    cfg.Workspace.ChatID,
			MessageID: messageID,
			Text:      text,
			ParseMode: "HTML",
		}); err == nil {
			return messageID, "updated", nil
		} else if isTelegramMessageNotModified(err) {
			return messageID, "unchanged", nil
		}
	}
	message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
		ChatID:          cfg.Workspace.ChatID,
		MessageThreadID: topicID,
		Text:            text,
		ParseMode:       "HTML",
	})
	if err != nil {
		return 0, "", err
	}
	if err := store.UpsertWorkspaceTopicIndex(ctx, cfg.Workspace.ChatID, topicID, key, message.MessageID, now); err != nil {
		return 0, "", err
	}
	return message.MessageID, "sent", nil
}

func renderWorkspaceDocumentIndex(ctx context.Context, store *sqlitestore.Store, docType string) (string, error) {
	docs, err := store.WorkspaceDocumentsByType(ctx, docType, []string{"active"}, 100)
	if err != nil {
		return "", err
	}
	partsByDoc := map[int64][]sqlitestore.WorkspaceDocumentPart{}
	for _, doc := range docs {
		parts, err := store.WorkspaceDocumentParts(ctx, doc.ID)
		if err != nil {
			return "", err
		}
		partsByDoc[doc.ID] = parts
	}
	switch docType {
	case "note":
		return renderNotesIndex(docs, partsByDoc), nil
	case "template":
		return renderTemplatesIndex(docs, partsByDoc), nil
	case "collection":
		return renderCollectionsIndex(docs, partsByDoc), nil
	default:
		return "", fmt.Errorf("unknown workspace document type %q", docType)
	}
}

func syncWorkspaceDocumentIndexesForSourceEdit(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, source sqlitestore.WorkspaceMessage, now time.Time) error {
	parts, err := store.WorkspaceDocumentPartsBySource(ctx, source.ChatID, source.MessageID)
	if err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, part := range parts {
		doc, err := store.WorkspaceDocumentByID(ctx, part.DocumentID)
		if err != nil {
			return err
		}
		if doc.Status != "active" {
			continue
		}
		if _, ok := seen[doc.Type]; ok {
			continue
		}
		seen[doc.Type] = struct{}{}
		if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, doc.Type, now); err != nil {
			return err
		}
	}
	return nil
}

func renderNotesIndex(docs []sqlitestore.WorkspaceDocument, partsByDoc map[int64][]sqlitestore.WorkspaceDocumentPart) string {
	var b strings.Builder
	b.WriteString("📝 <b>Активные заметки</b>\n\n")
	if len(docs) == 0 {
		b.WriteString("<i>Пока пусто.</i>")
		return b.String()
	}
	for _, doc := range docs {
		parts := partsByDoc[doc.ID]
		firstLink := firstPartLink(parts)
		b.WriteString("• ")
		writeBoldHTMLLinkOrText(&b, firstLink, doc.Title)
		b.WriteString(" ")
		b.WriteString(pleasantDocumentEmoji(doc.ID))
		b.WriteString("\n")
		for _, part := range parts {
			if part.PartNo <= 1 {
				continue
			}
			b.WriteString("  [")
			writeHTMLLinkOrText(&b, part.SourceLink, documentPartTitle(part, "Часть "+strconv.Itoa(part.PartNo)))
			b.WriteString("]\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderTemplatesIndex(docs []sqlitestore.WorkspaceDocument, partsByDoc map[int64][]sqlitestore.WorkspaceDocumentPart) string {
	var b strings.Builder
	b.WriteString("🧰 <b>Промпты и шаблоны</b>\n\n")
	if len(docs) == 0 {
		b.WriteString("<i>Пока пусто.</i>")
		return b.String()
	}
	for _, doc := range docs {
		label := doc.Title
		if strings.TrimSpace(doc.Category) != "" {
			label = doc.Category + " / " + doc.Title
		}
		b.WriteString("• <b>")
		b.WriteString(html.EscapeString(label))
		b.WriteString("</b>\n")
		for _, part := range partsByDoc[doc.ID] {
			b.WriteString("  ")
			writeHTMLLinkOrText(&b, part.SourceLink, documentPartTitle(part, doc.Title))
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderCollectionsIndex(docs []sqlitestore.WorkspaceDocument, partsByDoc map[int64][]sqlitestore.WorkspaceDocumentPart) string {
	var b strings.Builder
	b.WriteString("🗂 <b>Коллекции</b>\n\n")
	if len(docs) == 0 {
		b.WriteString("<i>Пока пусто.</i>")
		return b.String()
	}
	byCategory := map[string][]sqlitestore.WorkspaceDocument{}
	for _, doc := range docs {
		category := normalizeCollectionCategory(doc.Category)
		byCategory[category] = append(byCategory[category], doc)
	}
	for _, category := range collectionCategories() {
		items := byCategory[category]
		if len(items) == 0 {
			continue
		}
		b.WriteString("<b>")
		b.WriteString(html.EscapeString(category))
		b.WriteString("</b>\n")
		for _, doc := range items {
			b.WriteString("• ")
			writeHTMLLinkOrText(&b, documentTargetOrFirstPartLink(doc, partsByDoc[doc.ID]), doc.Title)
			b.WriteString(" ")
			b.WriteString(pleasantDocumentEmoji(doc.ID))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func workspaceDocumentIndexTarget(cfg config.Config, docType string) (int, string, error) {
	switch docType {
	case "note":
		return cfg.Workspace.Topics.Notes, noteIndexKey, nil
	case "template":
		return cfg.Workspace.Topics.Templates, templateIndexKey, nil
	case "collection":
		return cfg.Workspace.Topics.Collections, collectionIndexKey, nil
	default:
		return 0, "", fmt.Errorf("unknown workspace document type %q", docType)
	}
}

func workspaceDocumentIndexTopicName(docType string) string {
	switch docType {
	case "note":
		return "Заметки"
	case "template":
		return "Заготовки"
	case "collection":
		return "Коллекции"
	default:
		return ""
	}
}

func sendWorkspaceDocumentShow(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, threadID int, docType, body string, now time.Time) error {
	body = strings.TrimSpace(body)
	if body == "" {
		text, err := renderWorkspaceDocumentIndex(ctx, store, docType)
		if err != nil {
			return err
		}
		if err := updateWorkspaceDocumentIndex(ctx, cfg, store, client, docType, now); err != nil {
			return err
		}
		return client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.Workspace.ChatID,
			MessageThreadID: threadID,
			Text:            text,
			ParseMode:       "HTML",
		})
	}
	doc, err := resolveWorkspaceDocumentRef(ctx, store, docType, body)
	if err != nil {
		return err
	}
	parts, err := store.WorkspaceDocumentParts(ctx, doc.ID)
	if err != nil {
		return err
	}
	text := formatWorkspaceDocumentSummary(doc, parts)
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          cfg.Workspace.ChatID,
		MessageThreadID: threadID,
		Text:            text,
		ParseMode:       "HTML",
	})
}

func requireWorkspaceDocumentType(ctx context.Context, store *sqlitestore.Store, id int64, docType string) error {
	doc, err := store.WorkspaceDocumentByID(ctx, id)
	if err != nil {
		return err
	}
	if doc.Type != docType {
		return fmt.Errorf("document #%d is %s, not %s", id, doc.Type, docType)
	}
	if doc.Status != "active" {
		return fmt.Errorf("document #%d is %s, not active", id, doc.Status)
	}
	return nil
}

func formatWorkspaceDocumentSummary(doc sqlitestore.WorkspaceDocument, parts []sqlitestore.WorkspaceDocumentPart) string {
	var b strings.Builder
	b.WriteString("<b>#")
	b.WriteString(strconv.FormatInt(doc.ID, 10))
	b.WriteString(" ")
	b.WriteString(html.EscapeString(doc.Title))
	b.WriteString("</b>\n")
	if doc.Category != "" {
		b.WriteString("<i>")
		b.WriteString(html.EscapeString(doc.Category))
		b.WriteString("</i>\n")
	}
	for _, part := range parts {
		b.WriteString("\n")
		b.WriteString(strconv.Itoa(part.PartNo))
		b.WriteString(". ")
		writeHTMLLinkOrText(&b, part.SourceLink, documentPartTitle(part, "Часть "+strconv.Itoa(part.PartNo)))
	}
	return b.String()
}

func sendWorkspaceDocumentHelp(ctx context.Context, client *nest.Client, chatID int64, threadID int, command string) {
	var text string
	switch command {
	case "note", "doc":
		text = "📝 <b>Заметки</b>\n\nИсточник берётся из <b>Заметки</b>: ответь на сообщение или отправь команду после него из Заметки/Inbox.\n\n<code>/doc new Название</code>\n<code>/doc append 3</code>\n<code>/doc append Название заметки | Часть 2</code>\n<code>/doc rename</code> в reply\n<code>/doc rename-part</code> в reply\n<code>/doc delete-part</code> в reply\n<code>/doc delete</code> в reply\n<code>/doc publish</code> или <code>/publish</code> в reply\n<code>/doc show</code> или <code>/doc show Название</code>"
	case "template":
		text = "🧰 <b>Заготовки</b>\n\nИсточник берётся из <b>Заготовки</b>: ответь на сообщение или отправь команду после него из Заготовки/Inbox.\n\n<code>/template new Тип | Заготовка | Часть</code>\n<code>/template append Заготовка | Новая часть</code>\n<code>/template rename</code> в reply\n<code>/template type</code> в reply\n<code>/template show</code> или <code>/template show Заготовка</code>"
	case "collection":
		text = "🗂 <b>Коллекции</b>\n\nКоллекция — отдельное сообщение со списком ссылок. Источник элемента берётся из <b>Коллекции</b>: reply или последнее твоё сообщение в этом topic.\n\n<code>/new collection Название</code>\n<code>/collection add Название коллекции | Название элемента</code>\n<code>/collection rename</code> в reply к карточке коллекции\n<code>/collection show</code> или <code>/collection show Название</code>"
	default:
		text = "Команды: <code>/doc</code>, <code>/template</code>, <code>/collection</code>."
	}
	_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: chatID, MessageThreadID: threadID, Text: text, ParseMode: "HTML"})
}

func sendWorkspaceDocumentDone(ctx context.Context, client *nest.Client, chatID int64, threadID int, text string) error {
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          chatID,
		MessageThreadID: threadID,
		Text:            text,
		ParseMode:       "HTML",
	})
}

func parseDocumentIDAndRest(body string) (int64, string, error) {
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return 0, "", fmt.Errorf("document id is required")
	}
	id, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", fmt.Errorf("document id must be positive")
	}
	return id, strings.TrimSpace(strings.TrimPrefix(body, fields[0])), nil
}

func parseDocumentRefAndOptionalTitle(body string) (string, string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ""
	}
	if parts := splitPipeFields(body); len(parts) >= 2 {
		return parts[0], parts[1]
	}
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return "", ""
	}
	if isPositiveInteger(fields[0]) {
		return fields[0], strings.TrimSpace(strings.TrimPrefix(body, fields[0]))
	}
	return body, ""
}

func parseTemplateAppendBody(body string) (string, string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ""
	}
	if parts := splitPipeFields(body); len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return parseDocumentRefAndOptionalTitle(body)
}

func parseTemplateNewBody(body string) (string, string, string) {
	parts := splitPipeFields(body)
	switch len(parts) {
	case 0:
		return "Остальное", "", ""
	case 1:
		return "Остальное", parts[0], parts[0]
	case 2:
		return parts[0], parts[1], parts[1]
	default:
		return parts[0], parts[1], parts[2]
	}
}

func parseCollectionAddBody(body string) (string, string, string) {
	parts := splitPipeFields(body)
	switch len(parts) {
	case 0:
		return "", "", ""
	case 1:
		return parts[0], "", ""
	default:
		category := normalizeCollectionCategory(parts[0])
		if category != "Остальное" || strings.EqualFold(parts[0], "Остальное") {
			return parts[1], parts[1], category
		}
		return parts[0], parts[1], ""
	}
}

func splitPipeFields(value string) []string {
	raw := strings.Split(value, "|")
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstPartLink(parts []sqlitestore.WorkspaceDocumentPart) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[0].SourceLink
}

func documentPartTitle(part sqlitestore.WorkspaceDocumentPart, fallback string) string {
	if strings.TrimSpace(part.Title) != "" {
		return strings.TrimSpace(part.Title)
	}
	return fallback
}

func writeHTMLLinkOrText(b *strings.Builder, link, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "Без названия"
	}
	if strings.TrimSpace(link) == "" {
		b.WriteString(html.EscapeString(text))
		return
	}
	b.WriteString("<a href=\"")
	b.WriteString(html.EscapeString(strings.TrimSpace(link)))
	b.WriteString("\">")
	b.WriteString(html.EscapeString(text))
	b.WriteString("</a>")
}

func writeBoldHTMLLinkOrText(b *strings.Builder, link, text string) {
	b.WriteString("<b>")
	writeHTMLLinkOrText(b, link, text)
	b.WriteString("</b>")
}

func documentTargetOrFirstPartLink(doc sqlitestore.WorkspaceDocument, parts []sqlitestore.WorkspaceDocumentPart) string {
	if doc.TargetChatID != 0 && doc.TargetMessageID != 0 {
		return workspaceMessageLink(doc.TargetChatID, doc.TargetTopicID, doc.TargetMessageID)
	}
	return firstPartLink(parts)
}

func normalizeCollectionCategory(category string) string {
	category = strings.TrimSpace(category)
	for _, known := range collectionCategories() {
		if strings.EqualFold(category, known) {
			return known
		}
	}
	return "Остальное"
}

func collectionCategories() []string {
	return []string{"Рецепты", "Цитаты", "Стихи", "Аниме", "Списки", "Остальное"}
}

func resolveClusterForShow(ctx context.Context, store *sqlitestore.Store, message nest.Message, args []string) (sqlitestore.WorkspaceCluster, bool, error) {
	if len(args) > 0 {
		if id, ok := telegramLinkMessageIDFromArg(message.Chat.ID, args[0]); ok {
			return store.WorkspaceClusterByMessage(ctx, message.Chat.ID, id)
		}
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return sqlitestore.WorkspaceCluster{}, false, err
		}
		return store.WorkspaceClusterByID(ctx, id)
	}
	if message.ReplyToMessage != nil {
		return store.WorkspaceClusterByMessage(ctx, message.Chat.ID, message.ReplyToMessage.MessageID)
	}
	if message.From != nil {
		tail, ok, err := store.LatestWorkspaceClusterTail(ctx, message.Chat.ID, message.MessageThreadID, message.From.ID)
		if err != nil || !ok {
			return sqlitestore.WorkspaceCluster{}, ok, err
		}
		return tail.Cluster, true, nil
	}
	return sqlitestore.WorkspaceCluster{}, false, nil
}

func sendClusterSummary(ctx context.Context, store *sqlitestore.Store, client *nest.Client, chatID int64, threadID int, cluster sqlitestore.WorkspaceCluster) {
	messages, err := store.WorkspaceClusterMessages(ctx, cluster.ID)
	if err != nil {
		sendWorkspaceError(ctx, client, chatID, threadID, "Не удалось прочитать cluster", err)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Cluster #%d</b>\n", cluster.ID)
	fmt.Fprintf(&b, "<i>topic=%d status=%s parts=%d</i>\n\n", cluster.TopicID, html.EscapeString(cluster.Status), len(messages))
	for _, item := range messages {
		body := compactWorkspaceLine(sourceText(item.Message), 140)
		if body == "" && item.Message.MediaType != "" {
			body = "[" + item.Message.MediaType + "]"
		}
		fmt.Fprintf(&b, "%d. <code>%d</code> <i>%s</i>", item.Position, item.Message.MessageID, html.EscapeString(item.Role))
		if item.Message.SourceLink != "" {
			fmt.Fprintf(&b, " <a href=\"%s\">link</a>", html.EscapeString(item.Message.SourceLink))
		}
		if body != "" {
			fmt.Fprintf(&b, "\n%s", html.EscapeString(body))
		}
		b.WriteString("\n")
	}
	_ = client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          chatID,
		MessageThreadID: threadID,
		Text:            b.String(),
		ParseMode:       "HTML",
	})
}

func ExtractTaskTexts(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "#tasks") {
		return extractMultiTaskTexts(text)
	}
	if !strings.Contains(lower, "#task") {
		return nil
	}
	cleaned := cleanupTaskText(strings.ReplaceAll(strings.ReplaceAll(text, "#task", ""), "#Task", ""))
	if cleaned == "" {
		return nil
	}
	return []string{cleaned}
}

func extractMultiTaskTexts(text string) []string {
	lines := strings.Split(text, "\n")
	started := false
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !started {
			index := strings.Index(strings.ToLower(trimmed), "#tasks")
			if index < 0 {
				continue
			}
			started = true
			trimmed = strings.TrimSpace(trimmed[index+len("#tasks"):])
			if trimmed == "" {
				continue
			}
		}
		cleaned := cleanupTaskText(trimmed)
		if cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return out
}

func cleanupTaskText(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "-")
	value = strings.TrimPrefix(value, "•")
	value = strings.TrimSpace(value)
	if dot := strings.Index(value, ". "); dot > 0 && dot <= 3 {
		if _, err := strconv.Atoi(value[:dot]); err == nil {
			value = strings.TrimSpace(value[dot+2:])
		}
	}
	value = strings.ReplaceAll(value, "#task", "")
	value = strings.ReplaceAll(value, "#tasks", "")
	return strings.TrimSpace(value)
}

func FormatTaskCard(task sqlitestore.WorkspaceTask) string {
	return FormatTaskCardIn(task, time.Local)
}

func FormatTaskCardIn(task sqlitestore.WorkspaceTask, location *time.Location) string {
	if location == nil {
		location = time.Local
	}
	now := time.Now().In(location)
	text := html.EscapeString(strings.TrimSpace(task.Text))
	if task.Emoji != "" {
		text += " " + html.EscapeString(task.Emoji)
	}
	switch task.Status {
	case "done":
		return "<s>" + text + "</s>"
	case "cancelled":
		return "<s>" + text + "</s>\n<i>Отменено.</i>"
	case "deferred":
		if task.DeferredUntil != nil {
			return text + "\n<i>Отложено до " + html.EscapeString(formatTaskDateRelative(task.DeferredUntil.In(location), now)) + ".</i>"
		}
		return text + "\n<i>Отложено без даты.</i>"
	default:
		return text
	}
}

func TaskActionMarkup(taskID int64) *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{{
		{Text: "✅ Готово", CallbackData: TaskCallbackData("done", taskID)},
		{Text: "🚫 Отменить", CallbackData: TaskCallbackData("cancel", taskID)},
		{Text: "⏳ Отложить", CallbackData: TaskCallbackData("defer", taskID)},
	}}}
}

func TaskDeferMarkup(taskID int64) *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{
		{
			{Text: "На неделю", CallbackData: TaskCallbackData("defer_week", taskID)},
			{Text: "На месяц", CallbackData: TaskCallbackData("defer_month", taskID)},
		},
		{
			{Text: "Ввести дату", CallbackData: TaskCallbackData("defer_custom", taskID)},
			{Text: "Без даты", CallbackData: TaskCallbackData("defer_none", taskID)},
		},
	}}
}

func TaskCallbackData(action string, taskID int64) string {
	return taskCallbackPrefix + action + ":" + strconv.FormatInt(taskID, 10)
}

func ParseTaskCallback(data string) (string, int64, bool) {
	if !strings.HasPrefix(data, taskCallbackPrefix) {
		return "", 0, false
	}
	rest := strings.TrimPrefix(data, taskCallbackPrefix)
	action, idRaw, ok := strings.Cut(rest, ":")
	if !ok {
		return "", 0, false
	}
	id, err := strconv.ParseInt(idRaw, 10, 64)
	if err != nil || id <= 0 {
		return "", 0, false
	}
	return action, id, true
}

func ParseDeferredTaskDate(value string, now time.Time, location *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("date is empty")
	}
	if location == nil {
		location = time.Local
	}
	layouts := []struct {
		layout   string
		noYear   bool
		dateOnly bool
	}{
		{"02.01 15:04", true, false},
		{"02.01", true, true},
		{"02.01.2006 15:04", false, false},
		{"02.01.2006", false, true},
		{"2006-01-02 15:04", false, false},
		{"2006-01-02", false, true},
	}
	for _, candidate := range layouts {
		parsed, err := time.ParseInLocation(candidate.layout, value, location)
		if err != nil {
			continue
		}
		if candidate.noYear {
			parsed = time.Date(now.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), 0, 0, location)
		}
		if candidate.dateOnly {
			parsed = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 9, 0, 0, 0, location)
		}
		if candidate.noYear && !parsed.After(now) {
			parsed = parsed.AddDate(1, 0, 0)
		}
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("unsupported date format")
}

func workspaceMessageFromBotAPI(cfg config.Config, message nest.Message, fallback time.Time) sqlitestore.WorkspaceMessage {
	date := fallback
	if message.Date > 0 {
		date = time.Unix(message.Date, 0).UTC()
	}
	var editDate *time.Time
	if message.EditDate > 0 {
		parsed := time.Unix(message.EditDate, 0).UTC()
		editDate = &parsed
	}
	fromUserID := int64(0)
	fromIsBot := false
	if message.From != nil {
		fromUserID = message.From.ID
		fromIsBot = message.From.IsBot
	}
	replyID := 0
	if message.ReplyToMessage != nil {
		replyID = message.ReplyToMessage.MessageID
	}
	forwardChatID := int64(0)
	if message.ForwardFromChat != nil {
		forwardChatID = message.ForwardFromChat.ID
	}
	return sqlitestore.WorkspaceMessage{
		ChatID:           message.Chat.ID,
		MessageID:        message.MessageID,
		TopicID:          interactionThread(cfg, message.MessageThreadID),
		FromUserID:       fromUserID,
		FromIsBot:        fromIsBot,
		Date:             date,
		EditDate:         editDate,
		Text:             strings.TrimSpace(message.Text),
		Caption:          strings.TrimSpace(message.Caption),
		MediaType:        messageMediaType(message),
		Forwarded:        isForwardedMessage(message),
		ForwardChatID:    forwardChatID,
		ForwardMessageID: message.ForwardFromMessageID,
		ReplyToMessageID: replyID,
		SourceLink:       workspaceMessageLink(message.Chat.ID, interactionThread(cfg, message.MessageThreadID), message.MessageID),
	}
}

func sourceText(message sqlitestore.WorkspaceMessage) string {
	if strings.TrimSpace(message.Text) != "" {
		return strings.TrimSpace(message.Text)
	}
	return strings.TrimSpace(message.Caption)
}

func messageMediaType(message nest.Message) string {
	switch {
	case len(message.Photo) > 0:
		return "photo"
	case message.Video != nil:
		return "video"
	case message.Voice != nil:
		return "voice"
	case message.Audio != nil:
		return "audio"
	case message.Document != nil:
		return "document"
	default:
		return ""
	}
}

func isForwardedMessage(message nest.Message) bool {
	return len(message.ForwardOrigin) > 0 || message.ForwardFrom != nil ||
		message.ForwardFromChat != nil || message.ForwardFromMessageID != 0
}

func workspaceMessageLink(chatID int64, topicID int, messageID int) string {
	if chatID == 0 || messageID == 0 {
		return ""
	}
	raw := strconv.FormatInt(chatID, 10)
	raw = strings.TrimPrefix(raw, "-100")
	raw = strings.TrimPrefix(raw, "-")
	if topicID > 0 {
		return "https://t.me/c/" + raw + "/" + strconv.Itoa(topicID) + "/" + strconv.Itoa(messageID)
	}
	return "https://t.me/c/" + raw + "/" + strconv.Itoa(messageID)
}

func interactionThread(cfg config.Config, threadID int) int {
	if threadID > 0 {
		return threadID
	}
	return cfg.Workspace.Topics.Inbox
}

func workspaceCommandName(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", ""
	}
	first := strings.TrimPrefix(fields[0], "/")
	if name, _, ok := strings.Cut(first, "@"); ok {
		first = name
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	return strings.ToLower(first), rest
}

func clusterCommandMessageIDs(command nest.Message, args []string) []int {
	var ids []int
	if command.ReplyToMessage != nil {
		ids = append(ids, command.ReplyToMessage.MessageID)
	}
	for _, arg := range args {
		if id, ok := telegramMessageIDFromArg(command.Chat.ID, arg); ok {
			ids = append(ids, id)
		}
	}
	return uniqueInts(ids)
}

func resolveAttachTargetCluster(ctx context.Context, store *sqlitestore.Store, message nest.Message, args []string) (int64, []string, error) {
	if message.ReplyToMessage != nil && (len(args) == 1 || !isPositiveInteger(args[0])) {
		cluster, ok, err := store.WorkspaceClusterByMessage(ctx, message.Chat.ID, message.ReplyToMessage.MessageID)
		if err != nil {
			return 0, nil, err
		}
		if !ok {
			return 0, nil, fmt.Errorf("replied message %d is not clustered", message.ReplyToMessage.MessageID)
		}
		return cluster.ID, args, nil
	}
	clusterID, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
	if err != nil || clusterID <= 0 {
		if err == nil {
			err = fmt.Errorf("cluster id must be positive")
		}
		return 0, nil, err
	}
	return clusterID, args[1:], nil
}

func telegramMessageIDFromArg(chatID int64, arg string) (int, bool) {
	arg = cleanTelegramArg(arg)
	if id, err := strconv.Atoi(arg); err == nil && id > 0 {
		return id, true
	}
	return telegramLinkMessageIDFromArg(chatID, arg)
}

func telegramLinkMessageIDFromArg(chatID int64, arg string) (int, bool) {
	arg = cleanTelegramArg(arg)
	if !strings.Contains(arg, "t.me/c/") {
		return 0, false
	}
	parts := strings.Split(arg, "/")
	for i, part := range parts {
		if part != "c" || i+2 >= len(parts) {
			continue
		}
		if !sameTelegramInternalChat(chatID, parts[i+1]) {
			return 0, false
		}
		for j := len(parts) - 1; j > i+1; j-- {
			id, err := strconv.Atoi(parts[j])
			if err == nil && id > 0 {
				return id, true
			}
		}
	}
	return 0, false
}

func cleanTelegramArg(arg string) string {
	arg = strings.TrimSpace(arg)
	arg = strings.Trim(arg, "<>()[]{}.,;")
	if before, _, ok := strings.Cut(arg, "?"); ok {
		arg = before
	}
	return strings.TrimRight(arg, "/")
}

func sameTelegramInternalChat(chatID int64, raw string) bool {
	current := strconv.FormatInt(chatID, 10)
	current = strings.TrimPrefix(current, "-100")
	current = strings.TrimPrefix(current, "-")
	raw = strings.TrimSpace(raw)
	return current != "" && raw == current
}

func isPositiveInteger(value string) bool {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return err == nil && parsed > 0
}

func uniqueInts(values []int) []int {
	seen := map[int]struct{}{}
	var out []int
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

func sendClusterHelp(ctx context.Context, client *nest.Client, chatID int64, threadID int) {
	_ = client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          chatID,
		MessageThreadID: threadID,
		Text:            ClusterHelpMessageText(),
		ParseMode:       "HTML",
	})
}

func ClusterHelpMessageText() string {
	return "🧩 <b>Кластеры сообщений</b>\n\n" +
		"Cluster — это несколько сообщений, которые бот держит вместе: твой текст, пересланные сообщения, фото и явные ответы.\n\n" +
		"<b>Команды</b>\n" +
		"• <code>/cluster show</code> — показать cluster для сообщения в reply или последний твой cluster в топике.\n" +
		"• <code>/cluster show 12</code> — показать cluster по ID.\n" +
		"• <code>/cluster merge</code> в reply + ссылка или message_id — объединить clusters.\n" +
		"• <code>/cluster attach</code> в reply + ссылка или message_id — добавить сообщение в cluster из reply.\n" +
		"• <code>/cluster detach</code> в reply — вынести сообщение в отдельный cluster.\n" +
		"• <code>/cluster split</code> — синоним detach для одного или нескольких сообщений.\n\n" +
		"Ссылки формата <code>https://t.me/c/.../.../...</code> тоже подходят. Bot messages и сами command messages не добавляются в clusters."
}

func sendWorkspaceError(ctx context.Context, client *nest.Client, chatID int64, threadID int, title string, err error) {
	message := "<b>" + html.EscapeString(title) + "</b>"
	if err != nil {
		message += "\n\n<blockquote>" + html.EscapeString(compactWorkspaceLine(err.Error(), 500)) + "</blockquote>"
	}
	_ = client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          chatID,
		MessageThreadID: threadID,
		Text:            message,
		ParseMode:       "HTML",
	})
}

func sendTaskConflictNotice(ctx context.Context, cfg config.Config, client *nest.Client, source sqlitestore.WorkspaceMessage, oldCount, newCount int) error {
	text := fmt.Sprintf("<b>Нужен review по задачам</b>\n\nВ исходном сообщении теперь %d task-пунктов, а карточек было %d. Я обновила совпадающие открытые карточки и не удаляла лишние автоматически.", newCount, oldCount)
	if source.SourceLink != "" {
		text += "\n\n<a href=\"" + html.EscapeString(source.SourceLink) + "\">Открыть источник</a>"
	}
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          cfg.Workspace.ChatID,
		MessageThreadID: cfg.Workspace.Topics.Tasks,
		Text:            text,
		ParseMode:       "HTML",
	})
}

func formatTaskBacklog(tasks []sqlitestore.WorkspaceTask, location *time.Location, now time.Time) string {
	var b strings.Builder
	b.WriteString("<b>Отложенные задачи</b>\n\n")
	written := 0
	for _, task := range tasks {
		if task.Status != "deferred" {
			continue
		}
		b.WriteString("• ")
		taskText := html.EscapeString(task.Text)
		if task.Emoji != "" {
			taskText += " " + html.EscapeString(task.Emoji)
		}
		if link := taskCardLink(task); link != "" {
			b.WriteString("<a href=\"")
			b.WriteString(html.EscapeString(link))
			b.WriteString("\">")
			b.WriteString(taskText)
			b.WriteString("</a>")
		} else {
			b.WriteString(taskText)
		}
		if task.Status == "deferred" {
			if task.DeferredUntil != nil {
				b.WriteString(" - <i>")
				b.WriteString(html.EscapeString(formatTaskDateRelative(task.DeferredUntil.In(location), now)))
				b.WriteString("</i>")
			} else {
				b.WriteString(" - <i>без даты</i>")
			}
		}
		b.WriteString("\n")
		written++
	}
	if written == 0 {
		b.WriteString("<i>Пока пусто.</i>")
	}
	return b.String()
}

func taskCardLink(task sqlitestore.WorkspaceTask) string {
	if task.CardChatID == 0 || task.CardMessageID == 0 {
		return ""
	}
	return workspaceMessageLink(task.CardChatID, task.CardTopicID, task.CardMessageID)
}

func formatTaskDate(value time.Time) string {
	if value.Hour() == 9 && value.Minute() == 0 {
		return value.Format("02.01")
	}
	return value.Format("02.01 15:04")
}

func formatTaskDateRelative(value, now time.Time) string {
	if value.Year() != now.Year() {
		if value.Hour() == 9 && value.Minute() == 0 {
			return value.Format("02.01.2006")
		}
		return value.Format("02.01.2006 15:04")
	}
	return formatTaskDate(value)
}

func deferPreset(action string, now time.Time) *time.Time {
	var result time.Time
	switch action {
	case "defer_week":
		target := now.AddDate(0, 0, 7)
		result = time.Date(target.Year(), target.Month(), target.Day(), 8, 0, 0, 0, now.Location())
	case "defer_month":
		target := now.AddDate(0, 1, 0)
		result = time.Date(target.Year(), target.Month(), target.Day(), 8, 0, 0, 0, now.Location())
	case "defer_none":
		return nil
	default:
		return nil
	}
	utc := result.UTC()
	return &utc
}

func deferUntilUTC(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func pleasantTaskEmoji(id int64) string {
	emojis := []string{
		"🌿", "✨", "🟦", "🫶", "☀️", "🍀", "💫", "🧩",
		"🌙", "⭐", "🌸", "🔹", "🟣", "🌼", "💎", "🪄",
		"🟢", "🧡", "🪷", "⚡", "🎯", "🧵", "🔆", "🪩",
		"🌻", "🫧", "🪽", "🌊", "🟡", "🔮", "🪁", "🧿",
		"🌺", "🫐", "🍋", "🌾", "🧬", "🧭", "💡", "🛠",
		"🪴", "🧡", "💚", "🩵", "💜", "🪅", "🌟", "🪙",
	}
	if id <= 0 {
		return emojis[0]
	}
	return emojis[int(id-1)%len(emojis)]
}

func pleasantDocumentEmoji(id int64) string {
	emojis := []string{"✨", "🌿", "💫", "🧩", "🌸", "🪄", "🫶", "☀️", "🍀", "💎", "🧡", "🌙"}
	if id <= 0 {
		return emojis[0]
	}
	return emojis[int(id-1)%len(emojis)]
}

func sendWorkspaceMessageResultWithRetry(ctx context.Context, client *nest.Client, request nest.SendMessageRequest) (nest.Message, error) {
	message, err := client.SendMessageResult(ctx, request)
	if err == nil {
		return message, nil
	}
	workspaceSleepOrDone(ctx, time.Second)
	return client.SendMessageResult(ctx, request)
}

func isTelegramMessageNotModified(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}

func emptyMarkup() *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{}}
}

func compactWorkspaceLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

func workspacePollRetryDelay(failures int) time.Duration {
	if failures <= 1 {
		return workspacePollInitialBackoff
	}
	delay := workspacePollInitialBackoff
	for attempt := 1; attempt < failures && delay < workspacePollMaxBackoff; attempt++ {
		delay *= 2
	}
	if delay > workspacePollMaxBackoff {
		return workspacePollMaxBackoff
	}
	return delay
}

func workspaceSleepOrDone(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
