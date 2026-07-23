package workspace

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

const (
	quoteCallbackPrefix     = "ws:quote:"
	experienceQuoteIndexKey = "experience_quotes"
	quoteTelegramTextLimit  = 4096
	quoteIndexTextLimit     = 3900
)

func startQuoteWizard(ctx context.Context, cfg config.Config, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, message nest.Message, threadID int) error {
	if threadID != cfg.Workspace.Topics.Inbox {
		return client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.Workspace.ChatID,
			MessageThreadID: threadID,
			Text:            "Команда <code>/quote</code> запускается только из <b>Inbox</b>.",
			ParseMode:       "HTML",
		})
	}
	if message.From == nil {
		return fmt.Errorf("quote wizard requires user identity")
	}
	key := pendingTaskDateKey{chatID: message.Chat.ID, threadID: threadID, userID: message.From.ID}
	pendingInputs[key] = pendingWorkspaceInput{Kind: "quote_text"}
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          message.Chat.ID,
		MessageThreadID: threadID,
		Text:            "Пришли <b>текст цитаты</b>. Я оформлю его нативным блоком цитаты Telegram. Отменить можно словом <code>Отмена</code>.",
		ParseMode:       "HTML",
	})
}

func handlePendingQuoteInput(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, key pendingTaskDateKey, pending pendingWorkspaceInput, message nest.Message, threadID int) bool {
	if command, _ := workspaceCommandName(message.Text); command != "" && !isCancelText(message.Text) {
		_ = store.ArchiveWorkspaceQuoteDraft(ctx, pending.QuoteID, time.Now().UTC())
		delete(pendingInputs, key)
		return false
	}
	if isCancelText(message.Text) {
		_ = store.ArchiveWorkspaceQuoteDraft(ctx, pending.QuoteID, time.Now().UTC())
		delete(pendingInputs, key)
		_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: "Отменила создание цитаты."})
		return true
	}
	value := strings.TrimSpace(message.Text)
	switch pending.Kind {
	case "quote_text":
		if value == "" {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: "Текст цитаты не может быть пустым."})
			return true
		}
		if err := validateWorkspaceQuoteMessage("", value, ""); err != nil {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: html.EscapeString(err.Error()), ParseMode: "HTML"})
			return true
		}
		now := time.Now().UTC()
		source := workspaceMessageFromBotAPI(cfg, message, now)
		if err := store.UpsertWorkspaceMessage(ctx, source, now); err != nil {
			sendWorkspaceError(ctx, client, message.Chat.ID, threadID, "Не удалось сохранить источник цитаты", err)
			return true
		}
		quote, err := store.CreateWorkspaceQuote(ctx, sqlitestore.WorkspaceQuote{
			Text: value, Status: "draft", WizardUserID: key.userID, WizardStage: "quote_author",
			SourceChatID: source.ChatID, SourceMessageID: source.MessageID, SourceLink: source.SourceLink,
		}, now)
		if err != nil {
			sendWorkspaceError(ctx, client, message.Chat.ID, threadID, "Не удалось сохранить черновик цитаты", err)
			return true
		}
		pending.Kind = "quote_author"
		pending.QuoteID = quote.ID
		pending.QuoteText = value
		pending.QuoteSource = source
		pendingInputs[key] = pending
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID: message.Chat.ID, MessageThreadID: threadID,
			Text: "Теперь пришли <b>автора</b> вместе с годом или пометками, если они нужны.", ParseMode: "HTML",
			ReplyMarkup: quoteOptionalFieldMarkup("skip_author"),
		})
		return true
	case "quote_author":
		if err := validateWorkspaceQuoteMessage("", pending.QuoteText, value); err != nil {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: html.EscapeString(err.Error()), ParseMode: "HTML"})
			return true
		}
		pending.Author = value
		pending.Kind = "quote_title"
		if err := store.UpdateWorkspaceQuoteDraft(ctx, pending.QuoteID, "", pending.Author, pending.Kind, time.Now().UTC()); err != nil {
			sendWorkspaceError(ctx, client, message.Chat.ID, threadID, "Не удалось обновить черновик цитаты", err)
			return true
		}
		pendingInputs[key] = pending
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID: message.Chat.ID, MessageThreadID: threadID,
			Text: "Пришли <b>название</b> цитаты, если оно нужно.", ParseMode: "HTML",
			ReplyMarkup: quoteOptionalFieldMarkup("skip_title"),
		})
		return true
	case "quote_title":
		pending.Title = value
		if err := validateWorkspaceQuoteMessage(pending.Title, pending.QuoteText, pending.Author); err != nil {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: html.EscapeString(err.Error()), ParseMode: "HTML"})
			return true
		}
		pending.Kind = "quote_preview"
		if err := store.UpdateWorkspaceQuoteDraft(ctx, pending.QuoteID, pending.Title, pending.Author, pending.Kind, time.Now().UTC()); err != nil {
			sendWorkspaceError(ctx, client, message.Chat.ID, threadID, "Не удалось обновить черновик цитаты", err)
			return true
		}
		pendingInputs[key] = pending
		_ = sendQuotePreview(ctx, client, message.Chat.ID, threadID, pending)
		return true
	case "quote_preview":
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID: message.Chat.ID, MessageThreadID: threadID,
			Text: "Предпросмотр уже готов — нажми <b>Сохранить</b> или <b>Отмена</b>.", ParseMode: "HTML",
			ReplyMarkup: QuotePreviewMarkup(),
		})
		return true
	default:
		return false
	}
}

func handleQuoteCallback(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, callback nest.CallbackQuery, action string) {
	if callback.Message == nil || callback.Message.Chat.ID != cfg.Workspace.ChatID {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Эта кнопка не из Workspace.")
		return
	}
	threadID := interactionThread(cfg, callback.Message.MessageThreadID)
	if threadID != cfg.Workspace.Topics.Inbox {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Цитаты создаются только из Inbox.")
		return
	}
	key := pendingTaskDateKey{chatID: callback.Message.Chat.ID, threadID: threadID, userID: callback.From.ID}
	pending, ok := pendingInputs[key]
	if !ok || !strings.HasPrefix(pending.Kind, "quote_") {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Этот мастер уже завершён.")
		return
	}
	if action == "cancel" {
		_ = store.ArchiveWorkspaceQuoteDraft(ctx, pending.QuoteID, time.Now().UTC())
		delete(pendingInputs, key)
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID: callback.Message.Chat.ID, MessageID: callback.Message.MessageID,
			Text: "Создание цитаты отменено.", ReplyMarkup: emptyMarkup(),
		})
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Отменила.")
		return
	}
	switch action {
	case "skip_author":
		if pending.Kind != "quote_author" {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Автор уже выбран.")
			return
		}
		pending.Author = ""
		pending.Kind = "quote_title"
		if err := store.UpdateWorkspaceQuoteDraft(ctx, pending.QuoteID, "", "", pending.Kind, time.Now().UTC()); err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не получилось сохранить выбор.")
			return
		}
		pendingInputs[key] = pending
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID: callback.Message.Chat.ID, MessageID: callback.Message.MessageID,
			Text: "Без автора. Теперь пришли <b>название</b>, если оно нужно.", ParseMode: "HTML",
			ReplyMarkup: quoteOptionalFieldMarkup("skip_title"),
		})
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Без автора.")
	case "skip_title":
		if pending.Kind != "quote_title" {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Название уже выбрано.")
			return
		}
		pending.Title = ""
		if err := validateWorkspaceQuoteMessage(pending.Title, pending.QuoteText, pending.Author); err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Цитата слишком длинная.")
			return
		}
		pending.Kind = "quote_preview"
		if err := store.UpdateWorkspaceQuoteDraft(ctx, pending.QuoteID, "", pending.Author, pending.Kind, time.Now().UTC()); err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не получилось сохранить выбор.")
			return
		}
		pendingInputs[key] = pending
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID: callback.Message.Chat.ID, MessageID: callback.Message.MessageID,
			Text: FormatWorkspaceQuote(pending.Title, pending.QuoteText, pending.Author), ParseMode: "HTML",
			ReplyMarkup: QuotePreviewMarkup(),
		})
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Без названия.")
	case "save":
		if pending.Kind != "quote_preview" {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Сначала заполни цитату.")
			return
		}
		updated, err := publishQuoteFromWizard(ctx, cfg, store, client, pendingInputs, key, pending, time.Now().UTC())
		if err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не получилось сохранить цитату.")
			sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось сохранить цитату", err)
			return
		}
		delete(pendingInputs, key)
		label := workspaceQuoteIndexLabel(updated)
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID: callback.Message.Chat.ID, MessageID: callback.Message.MessageID,
			Text: "Сохранила в <b>Опыт</b>: " + html.EscapeString(label) + ".", ParseMode: "HTML", ReplyMarkup: emptyMarkup(),
		})
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Сохранила.")
	default:
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Неизвестное действие.")
	}
}

func publishQuoteFromWizard(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, key pendingTaskDateKey, pending pendingWorkspaceInput, now time.Time) (sqlitestore.WorkspaceQuote, error) {
	if err := validateWorkspaceQuoteMessage(pending.Title, pending.QuoteText, pending.Author); err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	var quote sqlitestore.WorkspaceQuote
	var err error
	if pending.QuoteID == 0 {
		quote, err = store.CreateWorkspaceQuote(ctx, sqlitestore.WorkspaceQuote{
			Title: pending.Title, Text: pending.QuoteText, Author: pending.Author, Status: "draft",
			SourceChatID: pending.QuoteSource.ChatID, SourceMessageID: pending.QuoteSource.MessageID,
			SourceLink: pending.QuoteSource.SourceLink,
		}, now)
		if err != nil {
			return sqlitestore.WorkspaceQuote{}, err
		}
		pending.QuoteID = quote.ID
		pendingInputs[key] = pending
	} else {
		quote, err = store.WorkspaceQuoteByID(ctx, pending.QuoteID)
		if err != nil {
			return sqlitestore.WorkspaceQuote{}, err
		}
	}
	if quote.TargetMessageID == 0 {
		if quote.DeliveryStatus != "draft" {
			return sqlitestore.WorkspaceQuote{}, fmt.Errorf("quote delivery is %s; manual reconciliation is required before retry", quote.DeliveryStatus)
		}
		if err := store.MarkWorkspaceQuoteSending(ctx, quote.ID, now); err != nil {
			return sqlitestore.WorkspaceQuote{}, err
		}
		message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
			ChatID: cfg.Workspace.ChatID, MessageThreadID: cfg.Workspace.Topics.Experience,
			Text: FormatWorkspaceQuote(quote.Title, quote.Text, quote.Author), ParseMode: "HTML",
		})
		if err != nil {
			_ = store.MarkWorkspaceQuoteDeliveryUnknown(context.WithoutCancel(ctx), quote.ID, err.Error(), time.Now().UTC())
			return sqlitestore.WorkspaceQuote{}, err
		}
		if err := store.PublishWorkspaceQuote(ctx, quote.ID, cfg.Workspace.ChatID, cfg.Workspace.Topics.Experience, message.MessageID, now); err != nil {
			_ = store.MarkWorkspaceQuoteDeliveryUnknown(context.WithoutCancel(ctx), quote.ID, err.Error(), time.Now().UTC())
			return sqlitestore.WorkspaceQuote{}, err
		}
		quote, err = store.WorkspaceQuoteByID(ctx, quote.ID)
		if err != nil {
			return sqlitestore.WorkspaceQuote{}, err
		}
	}
	if err := store.UpsertWorkspaceDerivedMessage(ctx, sqlitestore.WorkspaceDerivedMessage{
		SourceChatID: quote.SourceChatID, SourceMessageID: quote.SourceMessageID,
		DerivedType: "experience_quote", DerivedChatID: quote.TargetChatID,
		DerivedTopicID: quote.TargetTopicID, DerivedMessageID: quote.TargetMessageID,
		Status: "published",
	}, now); err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	if err := updateExperienceQuoteIndex(ctx, cfg, store, client, now); err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	return quote, nil
}

func restoreWorkspaceQuoteDrafts(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, now time.Time) error {
	quotes, err := store.WorkspaceQuoteDrafts(ctx)
	if err != nil {
		return err
	}
	for _, quote := range quotes {
		if quote.DeliveryStatus == "sending" {
			_ = store.MarkWorkspaceQuoteDeliveryUnknown(ctx, quote.ID, "process restarted while Telegram delivery was in progress", now)
			_ = client.SendMessage(ctx, nest.SendMessageRequest{
				ChatID: cfg.Workspace.ChatID, MessageThreadID: cfg.Workspace.Topics.Inbox,
				Text: "⚠️ Отправка цитаты прервалась в неопределённом состоянии. Автоматически повторять её не буду, чтобы не создать дубль; нужна ручная сверка.",
			})
			continue
		}
		if quote.DeliveryStatus != "draft" {
			continue
		}
		key := pendingTaskDateKey{chatID: quote.SourceChatID, threadID: cfg.Workspace.Topics.Inbox, userID: quote.WizardUserID}
		pendingInputs[key] = pendingWorkspaceInput{
			Kind: quote.WizardStage, QuoteID: quote.ID, QuoteText: quote.Text,
			Title: quote.Title, Author: quote.Author,
			QuoteSource: sqlitestore.WorkspaceMessage{
				ChatID: quote.SourceChatID, MessageID: quote.SourceMessageID, SourceLink: quote.SourceLink,
			},
		}
	}
	return nil
}

func syncWorkspaceQuoteForSourceEdit(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, source sqlitestore.WorkspaceMessage, now time.Time) error {
	quotes, err := store.MarkWorkspaceQuoteSourceNeedsReview(ctx, source.ChatID, source.MessageID, now)
	if err != nil || len(quotes) == 0 {
		return err
	}
	var labels []string
	for _, quote := range quotes {
		labels = append(labels, workspaceQuoteIndexLabel(quote))
	}
	text := "⚠️ Исходный текст цитаты <b>" + html.EscapeString(strings.Join(labels, ", ")) + "</b> изменился. Итог в <b>Опыт</b> не переписывала и пометила для проверки. Чтобы опубликовать обновлённую версию, запусти <code>/quote</code> заново."
	if strings.TrimSpace(source.SourceLink) != "" {
		text += "\n\n<a href=\"" + html.EscapeString(source.SourceLink) + "\">Открыть изменённый источник</a>"
	}
	if err := client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: cfg.Workspace.ChatID, MessageThreadID: cfg.Workspace.Topics.Inbox,
		Text: text, ParseMode: "HTML",
	}); err != nil {
		return err
	}
	return updateExperienceQuoteIndex(ctx, cfg, store, client, now)
}

func updateExperienceQuoteIndex(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, now time.Time) error {
	text, err := renderExperienceQuoteIndex(ctx, store)
	if err != nil {
		return err
	}
	_, _, err = upsertWorkspacePinnedIndexMessage(ctx, cfg, store, client, cfg.Workspace.Topics.Experience, experienceQuoteIndexKey, text, now)
	return err
}

func renderExperienceQuoteIndex(ctx context.Context, store *sqlitestore.Store) (string, error) {
	quotes, err := store.WorkspaceQuotes(ctx, []string{"active", "needs_review"}, 100)
	if err != nil {
		return "", err
	}
	return renderExperienceQuoteIndexFromQuotes(quotes), nil
}

func renderExperienceQuoteIndexFromQuotes(quotes []sqlitestore.WorkspaceQuote) string {
	const intro = "🌱 <b>Опыт</b>\n\nЛичные выводы и наблюдения: что сработало, что не сработало и какие правила хочется сохранить для себя.\n\n<blockquote>Здесь важны контекст, голос и практический след, а не энциклопедическая гладкость.</blockquote>\n\n<b>Цитаты</b>\n"
	var b strings.Builder
	b.WriteString(intro)
	if len(quotes) == 0 {
		b.WriteString("<i>Пока пусто. Добавить цитату можно командой /quote из Inbox.</i>")
		return b.String()
	}
	shown := 0
	for _, quote := range quotes {
		var line strings.Builder
		line.WriteString("• ")
		link := workspaceMessageLink(quote.TargetChatID, quote.TargetTopicID, quote.TargetMessageID)
		writeHTMLLinkOrText(&line, link, workspaceQuoteIndexLabel(quote))
		if quote.Status == "needs_review" {
			line.WriteString(" ⚠️")
		}
		line.WriteString("\n")
		if len([]rune(b.String()+line.String())) > quoteIndexTextLimit {
			break
		}
		b.WriteString(line.String())
		shown++
	}
	if shown < len(quotes) {
		b.WriteString("\n<i>Показаны последние ")
		b.WriteString(fmt.Sprintf("%d из %d", shown, len(quotes)))
		b.WriteString(" цитат.</i>")
	}
	return strings.TrimRight(b.String(), "\n")
}

func workspaceQuoteIndexLabel(quote sqlitestore.WorkspaceQuote) string {
	label := strings.TrimSpace(quote.Title)
	if label == "" {
		label = compactWorkspaceLine(quote.Text, 80)
	}
	if strings.TrimSpace(quote.Author) != "" {
		label += " — " + strings.TrimSpace(quote.Author)
	}
	return compactWorkspaceLine(label, 120)
}

func FormatWorkspaceQuote(title, text, author string) string {
	title = strings.TrimSpace(title)
	text = strings.TrimSpace(text)
	author = strings.TrimSpace(author)
	var parts []string
	if title != "" {
		parts = append(parts, "<b>"+html.EscapeString(title)+"</b>")
	}
	parts = append(parts, "<blockquote>"+html.EscapeString(text)+"</blockquote>")
	if author != "" {
		parts = append(parts, html.EscapeString(author))
	}
	return strings.Join(parts, "\n\n")
}

func validateWorkspaceQuoteMessage(title, text, author string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("текст цитаты не может быть пустым")
	}
	plain := strings.TrimSpace(text)
	if strings.TrimSpace(title) != "" {
		plain = strings.TrimSpace(title) + "\n\n" + plain
	}
	if strings.TrimSpace(author) != "" {
		plain += "\n\n" + strings.TrimSpace(author)
	}
	if utf16Length(plain) > quoteTelegramTextLimit {
		return fmt.Errorf("цитата вместе с названием и автором длиннее лимита одного сообщения Telegram (%d символов); сократи текст", quoteTelegramTextLimit)
	}
	return nil
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func sendQuotePreview(ctx context.Context, client *nest.Client, chatID int64, threadID int, pending pendingWorkspaceInput) error {
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: chatID, MessageThreadID: threadID,
		Text: FormatWorkspaceQuote(pending.Title, pending.QuoteText, pending.Author), ParseMode: "HTML",
		ReplyMarkup: QuotePreviewMarkup(),
	})
}

func quoteOptionalFieldMarkup(skipAction string) *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{{
		{Text: "Пропустить", CallbackData: QuoteCallbackData(skipAction)},
		{Text: "Отмена", CallbackData: QuoteCallbackData("cancel")},
	}}}
}

func QuotePreviewMarkup() *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{{
		{Text: "Сохранить", CallbackData: QuoteCallbackData("save")},
		{Text: "Отмена", CallbackData: QuoteCallbackData("cancel")},
	}}}
}

func QuoteCallbackData(action string) string {
	return quoteCallbackPrefix + strings.TrimSpace(action)
}

func ParseQuoteCallback(data string) (string, bool) {
	if !strings.HasPrefix(data, quoteCallbackPrefix) {
		return "", false
	}
	action := strings.TrimSpace(strings.TrimPrefix(data, quoteCallbackPrefix))
	switch action {
	case "skip_author", "skip_title", "save", "cancel":
		return action, true
	default:
		return "", false
	}
}
