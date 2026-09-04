package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
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

func startQuoteWizard(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, message nest.Message, threadID int) error {
	if threadID != cfg.Workspace.Topics.Inbox {
		return client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.Workspace.ChatID,
			MessageThreadID: threadID,
			Text:            "Команды <code>/quote</code> запускаются только из <b>Inbox</b>.",
			ParseMode:       "HTML",
		})
	}
	if message.From == nil {
		return fmt.Errorf("quote wizard requires user identity")
	}
	_, rest := workspaceCommandName(message.Text)
	fields := strings.Fields(rest)
	if len(fields) == 0 || strings.EqualFold(fields[0], "new") {
		return startNewQuoteWizard(ctx, client, pendingInputs, message, threadID)
	}
	switch strings.ToLower(fields[0]) {
	case "help":
		return client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID: message.Chat.ID, MessageThreadID: threadID,
			Text: QuoteHelpMessageText(), ParseMode: "HTML",
		})
	case "show":
		return showWorkspaceQuote(ctx, cfg, store, client, message.Chat.ID, threadID, strings.TrimSpace(strings.TrimPrefix(rest, fields[0])))
	case "edit":
		return startWorkspaceQuoteEdit(ctx, cfg, store, client, pendingInputs, message, threadID, strings.TrimSpace(strings.TrimPrefix(rest, fields[0])))
	default:
		return client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID: message.Chat.ID, MessageThreadID: threadID,
			Text: QuoteHelpMessageText(), ParseMode: "HTML",
		})
	}
}

func startNewQuoteWizard(ctx context.Context, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, message nest.Message, threadID int) error {
	key := pendingTaskDateKey{chatID: message.Chat.ID, threadID: threadID, userID: message.From.ID}
	pendingInputs[key] = pendingWorkspaceInput{Kind: "quote_text"}
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          message.Chat.ID,
		MessageThreadID: threadID,
		Text:            "Пришли <b>текст цитаты</b>. Я оформлю его нативным блоком цитаты Telegram. Отменить можно словом <code>Отмена</code>.",
		ParseMode:       "HTML",
	})
}

func startWorkspaceQuoteEdit(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, message nest.Message, threadID int, body string) error {
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return sendQuoteUsage(ctx, client, message.Chat.ID, threadID, "Укажи ID или ссылку итоговой цитаты.")
	}
	action := "start"
	ref := body
	if strings.EqualFold(fields[0], "retry") || strings.EqualFold(fields[0], "accept") {
		action = strings.ToLower(fields[0])
		ref = strings.TrimSpace(strings.TrimPrefix(body, fields[0]))
		if ref == "" {
			return sendQuoteUsage(ctx, client, message.Chat.ID, threadID, "Для recovery укажи ID или ссылку цитаты.")
		}
	}
	quote, err := resolveWorkspaceQuote(ctx, cfg, store, ref)
	if err != nil {
		return sendQuoteResolveError(ctx, client, message.Chat.ID, threadID, err)
	}
	if err := validateEditableWorkspaceQuote(quote); err != nil {
		return sendQuoteResolveError(ctx, client, message.Chat.ID, threadID, err)
	}
	now := time.Now().UTC()
	if action == "retry" {
		if quote.EditDeliveryStatus != "unknown" {
			return sendQuoteResolveError(ctx, client, message.Chat.ID, threadID, fmt.Errorf("у цитаты #%d нет неоднозначного изменения для повтора", quote.ID))
		}
		if _, err := applyWorkspaceQuoteEdit(ctx, cfg, store, client, quote.ID, true, now); err != nil {
			if isQuoteEditIndexPending(err) {
				return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: "Цитата обновлена, но индекс пока не подтвердился. Состояние сохранено и будет восстановлено без повторного edit итогового сообщения."})
			}
			return err
		}
		return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: "Изменение цитаты применено повторно и подтверждено."})
	}
	if action == "accept" {
		if quote.EditDeliveryStatus != "unknown" {
			return sendQuoteResolveError(ctx, client, message.Chat.ID, threadID, fmt.Errorf("у цитаты #%d нет неоднозначного изменения для подтверждения", quote.ID))
		}
		updated, err := acceptWorkspaceQuoteEdit(ctx, cfg, store, client, quote.ID, now)
		if err != nil {
			if isQuoteEditIndexPending(err) {
				return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: "Версия цитаты подтверждена, но индекс пока не обновился. Состояние сохранено для безопасного восстановления."})
			}
			return err
		}
		return client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID: message.Chat.ID, MessageThreadID: threadID,
			Text: "Подтвердила сохранённое состояние цитаты <b>" + html.EscapeString(workspaceQuoteIndexLabel(updated)) + "</b>.", ParseMode: "HTML",
		})
	}
	if quote.EditDeliveryStatus == "unknown" {
		return client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID: message.Chat.ID, MessageThreadID: threadID, ParseMode: "HTML",
			Text: fmt.Sprintf("⚠️ Исход предыдущего изменения цитаты <code>%d</code> неизвестен. Сначала сравни сообщение в <b>Опыт</b> с <code>/quote show %d</code>, затем явно выбери <code>/quote edit retry %d</code> или <code>/quote edit accept %d</code>.", quote.ID, quote.ID, quote.ID, quote.ID),
		})
	}
	key := pendingTaskDateKey{chatID: message.Chat.ID, threadID: threadID, userID: message.From.ID}
	pendingInputs[key] = pendingWorkspaceInput{
		Kind: "quote_edit_select", QuoteID: quote.ID, QuoteText: quote.Text,
		Title: quote.Title, Author: quote.Author,
	}
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: message.Chat.ID, MessageThreadID: threadID, ParseMode: "HTML",
		Text:        fmt.Sprintf("Что изменить в цитате <code>%d</code>? Сохранённое сообщение изменится только после preview и подтверждения.", quote.ID),
		ReplyMarkup: quoteEditFieldMarkup(quote.ID),
	})
}

func showWorkspaceQuote(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, chatID int64, threadID int, ref string) error {
	if strings.TrimSpace(ref) == "" {
		quotes, err := store.WorkspaceQuoteDrafts(ctx)
		if err != nil {
			return err
		}
		if err := recoverWorkspaceQuoteIndexPending(ctx, cfg, store, client, quotes, time.Now().UTC()); err != nil {
			return err
		}
		text, err := renderExperienceQuoteIndex(ctx, store)
		if err != nil {
			return err
		}
		return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: chatID, MessageThreadID: threadID, Text: text, ParseMode: "HTML"})
	}
	quote, err := resolveWorkspaceQuote(ctx, cfg, store, ref)
	if err != nil {
		return sendQuoteResolveError(ctx, client, chatID, threadID, err)
	}
	if err := client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: chatID, MessageThreadID: threadID,
		Text: FormatWorkspaceQuote(quote.Title, quote.Text, quote.Author), ParseMode: "HTML",
	}); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ID: <code>%d</code> · статус: <code>%s</code>", quote.ID, html.EscapeString(quote.Status))
	if link := workspaceMessageLink(quote.TargetChatID, quote.TargetTopicID, quote.TargetMessageID); link != "" {
		b.WriteString(" · <a href=\"")
		b.WriteString(html.EscapeString(link))
		b.WriteString("\">открыть в Опыт</a>")
	}
	if err := client.SendMessage(ctx, nest.SendMessageRequest{ChatID: chatID, MessageThreadID: threadID, Text: b.String(), ParseMode: "HTML"}); err != nil {
		return err
	}
	if quote.EditDeliveryStatus != "unknown" {
		return nil
	}
	if err := client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: chatID, MessageThreadID: threadID,
		Text: "⚠️ Исход последнего изменения неизвестен. Ниже предложенная версия; сравни её с сообщением в <b>Опыт</b>.", ParseMode: "HTML",
	}); err != nil {
		return err
	}
	if err := client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: chatID, MessageThreadID: threadID,
		Text: FormatWorkspaceQuote(quote.EditTitle, quote.EditText, quote.EditAuthor), ParseMode: "HTML",
	}); err != nil {
		return err
	}
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: chatID, MessageThreadID: threadID, ParseMode: "HTML",
		Text: fmt.Sprintf("После ручной сверки: <code>/quote edit retry %d</code> или <code>/quote edit accept %d</code>.", quote.ID, quote.ID),
	})
}

func sendQuoteUsage(ctx context.Context, client *nest.Client, chatID int64, threadID int, prefix string) error {
	text := html.EscapeString(strings.TrimSpace(prefix))
	if text != "" {
		text += "\n\n"
	}
	text += QuoteHelpMessageText()
	return client.SendMessage(ctx, nest.SendMessageRequest{ChatID: chatID, MessageThreadID: threadID, Text: text, ParseMode: "HTML"})
}

func QuoteHelpMessageText() string {
	return strings.TrimSpace(`<b>Команды цитат</b>

• <code>/quote</code> — добавить новую цитату через мастер.
• <code>/quote new</code> — явный вариант той же команды.
• <code>/quote show</code> — показать индекс цитат.
• <code>/quote show ID|ссылка</code> — показать одну сохранённую цитату.
• <code>/quote edit ID|ссылка</code> — изменить title, text или author через preview и подтверждение.

Название и автора можно очистить кнопкой <b>Очистить</b>. Команды работают только из <b>Inbox</b>.`)
}

func handlePendingQuoteInput(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, key pendingTaskDateKey, pending pendingWorkspaceInput, message nest.Message, threadID int) bool {
	if command, _ := workspaceCommandName(message.Text); command != "" && !isCancelText(message.Text) {
		_ = cancelWorkspaceQuoteWizard(ctx, store, pending, time.Now().UTC())
		delete(pendingInputs, key)
		return false
	}
	if isCancelText(message.Text) {
		_ = cancelWorkspaceQuoteWizard(ctx, store, pending, time.Now().UTC())
		delete(pendingInputs, key)
		_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: "Отменила мастер цитаты. Сохранённое сообщение не изменилось."})
		return true
	}
	value := strings.TrimSpace(message.Text)
	switch pending.Kind {
	case "quote_edit_select":
		stage, ok := quoteEditStageFromInput(value)
		if !ok {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{
				ChatID: message.Chat.ID, MessageThreadID: threadID,
				Text: "Выбери <b>Название</b>, <b>Текст</b> или <b>Автор</b>.", ParseMode: "HTML",
				ReplyMarkup: quoteEditFieldMarkup(pending.QuoteID),
			})
			return true
		}
		if err := beginPendingWorkspaceQuoteEdit(ctx, store, pendingInputs, key, pending, stage, time.Now().UTC()); err != nil {
			sendWorkspaceError(ctx, client, message.Chat.ID, threadID, "Не удалось начать изменение цитаты", err)
			return true
		}
		_ = sendQuoteEditFieldPrompt(ctx, client, message.Chat.ID, threadID, pending.QuoteID, stage)
		return true
	case "quote_edit_title", "quote_edit_text", "quote_edit_author":
		if pending.Kind == "quote_edit_text" && value == "" {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: "Текст цитаты не может быть пустым."})
			return true
		}
		if (pending.Kind == "quote_edit_title" || pending.Kind == "quote_edit_author") && isQuoteClearText(value) {
			value = ""
		}
		switch pending.Kind {
		case "quote_edit_title":
			pending.Title = value
		case "quote_edit_text":
			pending.QuoteText = value
		case "quote_edit_author":
			pending.Author = value
		}
		if err := validateWorkspaceQuoteMessage(pending.Title, pending.QuoteText, pending.Author); err != nil {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{ChatID: message.Chat.ID, MessageThreadID: threadID, Text: html.EscapeString(err.Error()), ParseMode: "HTML"})
			return true
		}
		pending.Kind = "quote_edit_preview"
		if err := store.UpdateWorkspaceQuoteEdit(ctx, pending.QuoteID, pending.Title, pending.QuoteText, pending.Author, pending.Kind, time.Now().UTC()); err != nil {
			sendWorkspaceError(ctx, client, message.Chat.ID, threadID, "Не удалось сохранить preview изменения цитаты", err)
			return true
		}
		pendingInputs[key] = pending
		_ = sendQuoteEditPreview(ctx, client, message.Chat.ID, threadID, pending)
		return true
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
			ReplyMarkup: quoteOptionalFieldMarkup("skip_author", pending.QuoteID),
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
			ReplyMarkup: quoteOptionalFieldMarkup("skip_title", pending.QuoteID),
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
	case "quote_preview", "quote_edit_preview":
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID: message.Chat.ID, MessageThreadID: threadID,
			Text: "Предпросмотр уже готов — нажми <b>Сохранить</b> или <b>Отмена</b>.", ParseMode: "HTML",
			ReplyMarkup: QuotePreviewMarkup(pending.QuoteID),
		})
		return true
	default:
		return false
	}
}

func handleQuoteCallback(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, callback nest.CallbackQuery, action string, callbackQuoteID int64) {
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
	if !quoteCallbackMatchesPending(pending, callbackQuoteID) {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Эта кнопка относится к другому или уже завершённому мастеру.")
		return
	}
	if action == "cancel" {
		_ = cancelWorkspaceQuoteWizard(ctx, store, pending, time.Now().UTC())
		delete(pendingInputs, key)
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID: callback.Message.Chat.ID, MessageID: callback.Message.MessageID,
			Text: "Мастер цитаты отменён. Сохранённое сообщение не изменилось.", ReplyMarkup: emptyMarkup(),
		})
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Отменила.")
		return
	}
	switch action {
	case "edit_title", "edit_text", "edit_author":
		if pending.Kind != "quote_edit_select" {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Поле уже выбрано.")
			return
		}
		stage := "quote_" + action
		if err := beginPendingWorkspaceQuoteEdit(ctx, store, pendingInputs, key, pending, stage, time.Now().UTC()); err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не получилось начать изменение.")
			return
		}
		_ = editQuoteFieldPrompt(ctx, client, callback.Message.Chat.ID, callback.Message.MessageID, pending.QuoteID, stage)
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Поле выбрано.")
	case "clear_title", "clear_author":
		expected := "quote_edit_" + strings.TrimPrefix(action, "clear_")
		if pending.Kind != expected {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Это поле уже заполнено.")
			return
		}
		if action == "clear_title" {
			pending.Title = ""
		} else {
			pending.Author = ""
		}
		if err := validateWorkspaceQuoteMessage(pending.Title, pending.QuoteText, pending.Author); err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Цитата слишком длинная.")
			return
		}
		pending.Kind = "quote_edit_preview"
		if err := store.UpdateWorkspaceQuoteEdit(ctx, pending.QuoteID, pending.Title, pending.QuoteText, pending.Author, pending.Kind, time.Now().UTC()); err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не получилось сохранить preview.")
			return
		}
		pendingInputs[key] = pending
		_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID: callback.Message.Chat.ID, MessageID: callback.Message.MessageID,
			Text: quoteEditPreviewText(pending), ParseMode: "HTML", ReplyMarkup: QuotePreviewMarkup(pending.QuoteID),
		})
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Поле очищено.")
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
			ReplyMarkup: quoteOptionalFieldMarkup("skip_title", pending.QuoteID),
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
			ReplyMarkup: QuotePreviewMarkup(pending.QuoteID),
		})
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Без названия.")
	case "save":
		if pending.Kind != "quote_preview" && pending.Kind != "quote_edit_preview" {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Сначала заполни цитату.")
			return
		}
		if pending.Kind == "quote_edit_preview" {
			updated, err := applyWorkspaceQuoteEdit(ctx, cfg, store, client, pending.QuoteID, false, time.Now().UTC())
			if err != nil {
				if isQuoteEditOutcomeUnknown(err) {
					delete(pendingInputs, key)
					_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
						ChatID: callback.Message.Chat.ID, MessageID: callback.Message.MessageID, ParseMode: "HTML", ReplyMarkup: emptyMarkup(),
						Text: fmt.Sprintf("⚠️ Telegram не подтвердил изменение цитаты <code>%d</code>. Автоматически повторять его не буду. Сверь итоговое сообщение через <code>/quote show %d</code>, затем используй <code>/quote edit retry %d</code> или <code>/quote edit accept %d</code>.", pending.QuoteID, pending.QuoteID, pending.QuoteID, pending.QuoteID),
					})
					_ = client.AnswerCallbackQuery(ctx, callback.ID, "Исход изменения неизвестен.")
					return
				}
				if isQuoteEditIndexPending(err) {
					delete(pendingInputs, key)
					_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
						ChatID: callback.Message.Chat.ID, MessageID: callback.Message.MessageID, ParseMode: "HTML", ReplyMarkup: emptyMarkup(),
						Text: "Цитата обновлена в <b>Опыт</b>, но обновление индекса пока не подтвердилось. Состояние сохранено для безопасного восстановления без повторного edit итогового сообщения.",
					})
					_ = client.AnswerCallbackQuery(ctx, callback.ID, "Цитата обновлена; индекс ожидает retry.")
					return
				}
				_ = client.AnswerCallbackQuery(ctx, callback.ID, "Не получилось изменить цитату.")
				sendWorkspaceError(ctx, client, cfg.Workspace.ChatID, threadID, "Не удалось изменить цитату", err)
				return
			}
			delete(pendingInputs, key)
			_ = client.EditMessageText(ctx, nest.EditMessageTextRequest{
				ChatID: callback.Message.Chat.ID, MessageID: callback.Message.MessageID,
				Text: "Обновила цитату в <b>Опыт</b>: " + html.EscapeString(workspaceQuoteIndexLabel(updated)) + ".", ParseMode: "HTML", ReplyMarkup: emptyMarkup(),
			})
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Обновила.")
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

type quoteEditTelegram interface {
	EditMessageText(context.Context, nest.EditMessageTextRequest) error
	SendMessageResult(context.Context, nest.SendMessageRequest) (nest.Message, error)
	PinChatMessage(context.Context, nest.PinChatMessageRequest) error
}

const currentWorkspaceQuoteFormatVersion = 1

type quoteFormatTelegram interface {
	EditMessageText(context.Context, nest.EditMessageTextRequest) error
}

func refreshPublishedWorkspaceQuoteFormat(ctx context.Context, store *sqlitestore.Store, client quoteFormatTelegram, now time.Time) (int, error) {
	if store == nil || client == nil {
		return 0, fmt.Errorf("workspace quote format refresh requires store and Telegram client")
	}
	quotes, err := store.WorkspaceQuotesNeedingFormatVersion(ctx, currentWorkspaceQuoteFormatVersion, 10_000)
	if err != nil {
		return 0, err
	}
	updated := 0
	var refreshErrors []error
	for _, quote := range quotes {
		if err := validateWorkspaceQuoteMessage(quote.Title, quote.Text, quote.Author); err != nil {
			refreshErrors = append(refreshErrors, fmt.Errorf("quote %d: %w", quote.ID, err))
			continue
		}
		err := client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID: quote.TargetChatID, MessageID: quote.TargetMessageID,
			Text: FormatWorkspaceQuote(quote.Title, quote.Text, quote.Author), ParseMode: "HTML",
		})
		if err != nil && !isTelegramMessageNotModified(err) {
			refreshErrors = append(refreshErrors, fmt.Errorf("quote %d: %w", quote.ID, err))
			continue
		}
		if err := store.MarkWorkspaceQuoteFormatVersion(ctx, quote.ID, currentWorkspaceQuoteFormatVersion, now); err != nil {
			refreshErrors = append(refreshErrors, fmt.Errorf("quote %d format commit: %w", quote.ID, err))
			continue
		}
		updated++
	}
	return updated, errors.Join(refreshErrors...)
}

type quoteEditOutcomeUnknownError struct {
	err error
}

func (e quoteEditOutcomeUnknownError) Error() string {
	return "workspace quote edit outcome is unknown: " + e.err.Error()
}

func (e quoteEditOutcomeUnknownError) Unwrap() error { return e.err }

func isQuoteEditOutcomeUnknown(err error) bool {
	var target quoteEditOutcomeUnknownError
	return errors.As(err, &target)
}

type quoteEditIndexPendingError struct {
	err error
}

func (e quoteEditIndexPendingError) Error() string {
	return "workspace quote was edited but its index update is pending: " + e.err.Error()
}

func (e quoteEditIndexPendingError) Unwrap() error { return e.err }

func isQuoteEditIndexPending(err error) bool {
	var target quoteEditIndexPendingError
	return errors.As(err, &target)
}

func applyWorkspaceQuoteEdit(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client quoteEditTelegram, quoteID int64, retryUnknown bool, now time.Time) (sqlitestore.WorkspaceQuote, error) {
	quote, err := store.WorkspaceQuoteByID(ctx, quoteID)
	if err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	if err := validateEditableWorkspaceQuote(quote); err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	if err := validateWorkspaceQuoteMessage(quote.EditTitle, quote.EditText, quote.EditAuthor); err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	if retryUnknown {
		err = store.RetryWorkspaceQuoteEdit(ctx, quote.ID, now)
	} else {
		err = store.MarkWorkspaceQuoteEditing(ctx, quote.ID, now)
	}
	if err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	err = client.EditMessageText(ctx, nest.EditMessageTextRequest{
		ChatID: quote.TargetChatID, MessageID: quote.TargetMessageID,
		Text: FormatWorkspaceQuote(quote.EditTitle, quote.EditText, quote.EditAuthor), ParseMode: "HTML",
	})
	if err != nil && !isTelegramMessageNotModified(err) {
		if isDefinitiveQuoteEditFailure(err) {
			_ = store.MarkWorkspaceQuoteEditRetryable(context.WithoutCancel(ctx), quote.ID, err.Error(), time.Now().UTC())
			return sqlitestore.WorkspaceQuote{}, err
		}
		_ = store.MarkWorkspaceQuoteEditUnknown(context.WithoutCancel(ctx), quote.ID, err.Error(), time.Now().UTC())
		return sqlitestore.WorkspaceQuote{}, quoteEditOutcomeUnknownError{err: err}
	}
	if err := store.CommitWorkspaceQuoteEdit(ctx, quote.ID, now); err != nil {
		_ = store.MarkWorkspaceQuoteEditUnknown(context.WithoutCancel(ctx), quote.ID, "Telegram edit succeeded but the canonical quote commit failed: "+err.Error(), time.Now().UTC())
		return sqlitestore.WorkspaceQuote{}, quoteEditOutcomeUnknownError{err: err}
	}
	updated, err := store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	if err := finalizeWorkspaceQuoteEditIndex(ctx, cfg, store, client, updated, now); err != nil {
		return updated, quoteEditIndexPendingError{err: err}
	}
	return store.WorkspaceQuoteByID(ctx, quote.ID)
}

func acceptWorkspaceQuoteEdit(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client quoteEditTelegram, quoteID int64, now time.Time) (sqlitestore.WorkspaceQuote, error) {
	quote, err := store.WorkspaceQuoteByID(ctx, quoteID)
	if err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	if quote.EditDeliveryStatus != "unknown" {
		return sqlitestore.WorkspaceQuote{}, fmt.Errorf("workspace quote %d has no ambiguous edit to accept", quote.ID)
	}
	if err := validateWorkspaceQuoteMessage(quote.EditTitle, quote.EditText, quote.EditAuthor); err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	if err := store.CommitWorkspaceQuoteEdit(ctx, quote.ID, now); err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	updated, err := store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		return sqlitestore.WorkspaceQuote{}, err
	}
	if err := finalizeWorkspaceQuoteEditIndex(ctx, cfg, store, client, updated, now); err != nil {
		return updated, quoteEditIndexPendingError{err: err}
	}
	return store.WorkspaceQuoteByID(ctx, quote.ID)
}

func finalizeWorkspaceQuoteEditIndex(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client quoteEditTelegram, quote sqlitestore.WorkspaceQuote, now time.Time) error {
	if quote.EditDeliveryStatus != "index_pending" {
		return fmt.Errorf("workspace quote %d is not waiting for an index update", quote.ID)
	}
	if err := persistWorkspaceQuoteDerivedMessage(ctx, store, quote, now); err != nil {
		return err
	}
	if err := updateExistingExperienceQuoteIndexWithClient(ctx, cfg, store, client); err != nil {
		return err
	}
	return store.FinalizeWorkspaceQuoteEdit(ctx, quote.ID, now)
}

func persistWorkspaceQuoteDerivedMessage(ctx context.Context, store *sqlitestore.Store, quote sqlitestore.WorkspaceQuote, now time.Time) error {
	return store.UpsertWorkspaceDerivedMessage(ctx, sqlitestore.WorkspaceDerivedMessage{
		SourceChatID: quote.SourceChatID, SourceMessageID: quote.SourceMessageID,
		DerivedType: "experience_quote", DerivedChatID: quote.TargetChatID,
		DerivedTopicID: quote.TargetTopicID, DerivedMessageID: quote.TargetMessageID,
		Status: "published",
	}, now)
}

func isDefinitiveQuoteEditFailure(err error) bool {
	return err != nil && (nest.IsBotAPIClientError(err) || strings.Contains(err.Error(), "Bot API editMessageText failed:"))
}

func resolveWorkspaceQuote(ctx context.Context, cfg config.Config, store *sqlitestore.Store, ref string) (sqlitestore.WorkspaceQuote, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return sqlitestore.WorkspaceQuote{}, fmt.Errorf("ID или ссылка цитаты не указаны")
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil && id > 0 {
		quote, err := store.WorkspaceQuoteByID(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return sqlitestore.WorkspaceQuote{}, fmt.Errorf("цитата #%d не найдена", id)
		}
		return quote, err
	}
	if messageID, ok := telegramLinkMessageIDFromArg(cfg.Workspace.ChatID, ref); ok {
		quote, found, err := store.WorkspaceQuoteByTargetMessage(ctx, cfg.Workspace.ChatID, messageID)
		if err != nil {
			return sqlitestore.WorkspaceQuote{}, err
		}
		if !found {
			return sqlitestore.WorkspaceQuote{}, fmt.Errorf("для сообщения %d сохранённая цитата не найдена", messageID)
		}
		return quote, nil
	}
	return sqlitestore.WorkspaceQuote{}, fmt.Errorf("не удалось распознать %q как ID или ссылку цитаты", compactWorkspaceLine(ref, 100))
}

func validateEditableWorkspaceQuote(quote sqlitestore.WorkspaceQuote) error {
	if quote.Status != "active" && quote.Status != "needs_review" {
		return fmt.Errorf("цитата #%d недоступна для изменения: статус %s", quote.ID, quote.Status)
	}
	if quote.TargetChatID == 0 || quote.TargetMessageID == 0 || quote.DeliveryStatus != "sent" {
		return fmt.Errorf("итоговое сообщение цитаты #%d отсутствует или не подтверждено", quote.ID)
	}
	return nil
}

func sendQuoteResolveError(ctx context.Context, client *nest.Client, chatID int64, threadID int, err error) error {
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: chatID, MessageThreadID: threadID,
		Text: "Не получилось открыть цитату: " + html.EscapeString(err.Error()), ParseMode: "HTML",
	})
}

func beginPendingWorkspaceQuoteEdit(ctx context.Context, store *sqlitestore.Store, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, key pendingTaskDateKey, pending pendingWorkspaceInput, stage string, now time.Time) error {
	quote, err := store.BeginWorkspaceQuoteEdit(ctx, pending.QuoteID, key.userID, stage, now)
	if err != nil {
		return err
	}
	pending.Kind = stage
	pending.Title = quote.EditTitle
	pending.QuoteText = quote.EditText
	pending.Author = quote.EditAuthor
	pendingInputs[key] = pending
	return nil
}

func quoteEditStageFromInput(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "title", "название":
		return "quote_edit_title", true
	case "text", "текст":
		return "quote_edit_text", true
	case "author", "автор":
		return "quote_edit_author", true
	default:
		return "", false
	}
}

func isQuoteClearText(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "очистить", "clear", "пусто":
		return true
	default:
		return false
	}
}

func cancelWorkspaceQuoteWizard(ctx context.Context, store *sqlitestore.Store, pending pendingWorkspaceInput, now time.Time) error {
	if strings.HasPrefix(pending.Kind, "quote_edit_") {
		if pending.Kind == "quote_edit_select" {
			return nil
		}
		return store.CancelWorkspaceQuoteEdit(ctx, pending.QuoteID, now)
	}
	return store.ArchiveWorkspaceQuoteDraft(ctx, pending.QuoteID, now)
}

func restoreWorkspaceQuoteDrafts(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client *nest.Client, pendingInputs map[pendingTaskDateKey]pendingWorkspaceInput, now time.Time) error {
	quotes, err := store.WorkspaceQuoteDrafts(ctx)
	if err != nil {
		return err
	}
	var indexPending []sqlitestore.WorkspaceQuote
	for _, quote := range quotes {
		if quote.Status == "draft" {
			if quote.DeliveryStatus == "sending" {
				_ = store.MarkWorkspaceQuoteDeliveryUnknown(ctx, quote.ID, "process restarted while Telegram delivery was in progress", now)
				if client != nil {
					_ = client.SendMessage(ctx, nest.SendMessageRequest{
						ChatID: cfg.Workspace.ChatID, MessageThreadID: cfg.Workspace.Topics.Inbox,
						Text: "⚠️ Отправка цитаты прервалась в неопределённом состоянии. Автоматически повторять её не буду, чтобы не создать дубль; нужна ручная сверка.",
					})
				}
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
			continue
		}
		if quote.EditDeliveryStatus == "editing" {
			_ = store.MarkWorkspaceQuoteEditUnknown(ctx, quote.ID, "process restarted while Telegram edit was in progress", now)
			if client != nil {
				_ = client.SendMessage(ctx, nest.SendMessageRequest{
					ChatID: cfg.Workspace.ChatID, MessageThreadID: cfg.Workspace.Topics.Inbox, ParseMode: "HTML",
					Text: fmt.Sprintf("⚠️ Изменение цитаты <code>%d</code> прервалось в неопределённом состоянии. Автоматически повторять его не буду. Используй <code>/quote show %d</code> для ручной сверки.", quote.ID, quote.ID),
				})
			}
			continue
		}
		if quote.EditDeliveryStatus == "index_pending" {
			indexPending = append(indexPending, quote)
			continue
		}
		if quote.EditDeliveryStatus != "pending" {
			continue
		}
		key := pendingTaskDateKey{chatID: cfg.Workspace.ChatID, threadID: cfg.Workspace.Topics.Inbox, userID: quote.WizardUserID}
		pendingInputs[key] = pendingWorkspaceInput{
			Kind: quote.WizardStage, QuoteID: quote.ID, QuoteText: quote.EditText,
			Title: quote.EditTitle, Author: quote.EditAuthor,
		}
	}
	return recoverWorkspaceQuoteIndexPending(ctx, cfg, store, client, indexPending, now)
}

func recoverWorkspaceQuoteIndexPending(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client quoteEditTelegram, quotes []sqlitestore.WorkspaceQuote, now time.Time) error {
	var pending []sqlitestore.WorkspaceQuote
	for _, quote := range quotes {
		if quote.EditDeliveryStatus == "index_pending" {
			pending = append(pending, quote)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	if client == nil {
		return fmt.Errorf("workspace quote index recovery requires Telegram client")
	}
	for _, quote := range pending {
		if err := persistWorkspaceQuoteDerivedMessage(ctx, store, quote, now); err != nil {
			return err
		}
	}
	if err := updateExistingExperienceQuoteIndexWithClient(ctx, cfg, store, client); err != nil {
		return err
	}
	for _, quote := range pending {
		if err := store.FinalizeWorkspaceQuoteEdit(ctx, quote.ID, now); err != nil {
			return err
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
	text := "⚠️ Исходный текст цитаты <b>" + html.EscapeString(strings.Join(labels, ", ")) + "</b> изменился. Итог в <b>Опыт</b> не переписывала и пометила для проверки. Открой цитату через <code>/quote show ID|ссылка</code> и измени явно через <code>/quote edit ID|ссылка</code>."
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
	return updateExperienceQuoteIndexWithClient(ctx, cfg, store, client, now)
}

func updateExperienceQuoteIndexWithClient(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client quoteEditTelegram, now time.Time) error {
	text, err := renderExperienceQuoteIndex(ctx, store)
	if err != nil {
		return err
	}
	messageID, exists, err := store.WorkspaceTopicIndexMessage(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Experience, experienceQuoteIndexKey)
	if err != nil {
		return err
	}
	if exists {
		err := client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID: cfg.Workspace.ChatID, MessageID: messageID, Text: text, ParseMode: "HTML",
		})
		if err != nil && !isTelegramMessageNotModified(err) {
			return err
		}
	} else {
		message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
			ChatID: cfg.Workspace.ChatID, MessageThreadID: cfg.Workspace.Topics.Experience,
			Text: text, ParseMode: "HTML",
		})
		if err != nil {
			return err
		}
		messageID = message.MessageID
		if err := store.UpsertWorkspaceTopicIndex(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Experience, experienceQuoteIndexKey, messageID, now); err != nil {
			return err
		}
	}
	return client.PinChatMessage(ctx, nest.PinChatMessageRequest{
		ChatID: cfg.Workspace.ChatID, MessageID: messageID, DisableNotification: true,
	})
}

func updateExistingExperienceQuoteIndexWithClient(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client quoteEditTelegram) error {
	text, err := renderExperienceQuoteIndex(ctx, store)
	if err != nil {
		return err
	}
	messageID, exists, err := store.WorkspaceTopicIndexMessage(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Experience, experienceQuoteIndexKey)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("experience quote index is not tracked; run workspace seed-document-indexes --type quote")
	}
	err = client.EditMessageText(ctx, nest.EditMessageTextRequest{
		ChatID: cfg.Workspace.ChatID, MessageID: messageID, Text: text, ParseMode: "HTML",
	})
	if err != nil && !isTelegramMessageNotModified(err) {
		return err
	}
	return client.PinChatMessage(ctx, nest.PinChatMessageRequest{
		ChatID: cfg.Workspace.ChatID, MessageID: messageID, DisableNotification: true,
	})
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
	quotes = append([]sqlitestore.WorkspaceQuote(nil), quotes...)
	sort.SliceStable(quotes, func(i, j int) bool {
		left := quotes[i].CreatedAt
		right := quotes[j].CreatedAt
		if quotes[i].PublishedAt != nil {
			left = *quotes[i].PublishedAt
		}
		if quotes[j].PublishedAt != nil {
			right = *quotes[j].PublishedAt
		}
		if !left.Equal(right) {
			return left.Before(right)
		}
		return quotes[i].ID < quotes[j].ID
	})
	shown := 0
	for _, quote := range quotes {
		var line strings.Builder
		fmt.Fprintf(&line, "%d. ", shown+1)
		link := workspaceMessageLink(quote.TargetChatID, quote.TargetTopicID, quote.TargetMessageID)
		writeHTMLLinkOrText(&line, link, workspaceQuoteIndexLabel(quote))
		if quote.Status == "needs_review" {
			line.WriteString(" ⚠️")
		}
		line.WriteString("\n")
		if !telegramHTMLFits(b.String()+line.String(), quoteIndexTextLimit) {
			break
		}
		b.WriteString(line.String())
		shown++
	}
	if shown < len(quotes) {
		note := "\n<i>Показаны " + fmt.Sprintf("%d из %d", shown, len(quotes)) + " цитат.</i>"
		if telegramHTMLFits(b.String()+note, quoteIndexTextLimit) {
			b.WriteString(note)
		}
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
		parts = append(parts, "<i>"+html.EscapeString(author)+"</i>")
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
		ReplyMarkup: QuotePreviewMarkup(pending.QuoteID),
	})
}

func sendQuoteEditPreview(ctx context.Context, client *nest.Client, chatID int64, threadID int, pending pendingWorkspaceInput) error {
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: chatID, MessageThreadID: threadID,
		Text: quoteEditPreviewText(pending), ParseMode: "HTML", ReplyMarkup: QuotePreviewMarkup(pending.QuoteID),
	})
}

func quoteEditPreviewText(pending pendingWorkspaceInput) string {
	return FormatWorkspaceQuote(pending.Title, pending.QuoteText, pending.Author)
}

func quoteEditFieldMarkup(quoteID int64) *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{
		{
			{Text: "Название", CallbackData: QuoteCallbackDataForQuote("edit_title", quoteID)},
			{Text: "Текст", CallbackData: QuoteCallbackDataForQuote("edit_text", quoteID)},
			{Text: "Автор", CallbackData: QuoteCallbackDataForQuote("edit_author", quoteID)},
		},
		{{Text: "Отмена", CallbackData: QuoteCallbackDataForQuote("cancel", quoteID)}},
	}}
}

func quoteEditOptionalFieldMarkup(field string, quoteID int64) *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{{
		{Text: "Очистить", CallbackData: QuoteCallbackDataForQuote("clear_"+field, quoteID)},
		{Text: "Отмена", CallbackData: QuoteCallbackDataForQuote("cancel", quoteID)},
	}}}
}

func sendQuoteEditFieldPrompt(ctx context.Context, client *nest.Client, chatID int64, threadID int, quoteID int64, stage string) error {
	text, markup := quoteEditFieldPrompt(quoteID, stage)
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID: chatID, MessageThreadID: threadID, Text: text, ParseMode: "HTML", ReplyMarkup: markup,
	})
}

func editQuoteFieldPrompt(ctx context.Context, client *nest.Client, chatID int64, messageID int, quoteID int64, stage string) error {
	text, markup := quoteEditFieldPrompt(quoteID, stage)
	return client.EditMessageText(ctx, nest.EditMessageTextRequest{
		ChatID: chatID, MessageID: messageID, Text: text, ParseMode: "HTML", ReplyMarkup: markup,
	})
}

func quoteEditFieldPrompt(quoteID int64, stage string) (string, *nest.InlineKeyboardMarkup) {
	prefix := fmt.Sprintf("Цитата <code>%d</code>. ", quoteID)
	switch stage {
	case "quote_edit_title":
		return prefix + "Пришли новое <b>название</b> или нажми <b>Очистить</b>.", quoteEditOptionalFieldMarkup("title", quoteID)
	case "quote_edit_text":
		return prefix + "Пришли новый обязательный <b>текст</b> цитаты.", quoteOptionalFieldMarkup("", quoteID)
	case "quote_edit_author":
		return prefix + "Пришли нового <b>автора</b> или нажми <b>Очистить</b>.", quoteEditOptionalFieldMarkup("author", quoteID)
	default:
		return prefix + "Выбери поле.", quoteEditFieldMarkup(quoteID)
	}
}

func quoteOptionalFieldMarkup(skipAction string, quoteID int64) *nest.InlineKeyboardMarkup {
	if strings.TrimSpace(skipAction) == "" {
		return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{{
			{Text: "Отмена", CallbackData: QuoteCallbackDataForQuote("cancel", quoteID)},
		}}}
	}
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{{
		{Text: "Пропустить", CallbackData: QuoteCallbackDataForQuote(skipAction, quoteID)},
		{Text: "Отмена", CallbackData: QuoteCallbackDataForQuote("cancel", quoteID)},
	}}}
}

func QuotePreviewMarkup(quoteID int64) *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{{
		{Text: "Сохранить", CallbackData: QuoteCallbackDataForQuote("save", quoteID)},
		{Text: "Отмена", CallbackData: QuoteCallbackDataForQuote("cancel", quoteID)},
	}}}
}

func QuoteCallbackDataForQuote(action string, quoteID int64) string {
	return quoteCallbackPrefix + strconv.FormatInt(quoteID, 10) + ":" + strings.TrimSpace(action)
}

func ParseQuoteCallback(data string) (string, int64, bool) {
	if !strings.HasPrefix(data, quoteCallbackPrefix) {
		return "", 0, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(data, quoteCallbackPrefix))
	quoteID := int64(0)
	action := rest
	if rawID, rawAction, ok := strings.Cut(rest, ":"); ok {
		parsed, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if err != nil || parsed <= 0 {
			return "", 0, false
		}
		quoteID = parsed
		action = strings.TrimSpace(rawAction)
	}
	switch action {
	case "skip_author", "skip_title", "save", "cancel",
		"edit_title", "edit_text", "edit_author", "clear_title", "clear_author":
		return action, quoteID, true
	default:
		return "", 0, false
	}
}

func quoteCallbackMatchesPending(pending pendingWorkspaceInput, callbackQuoteID int64) bool {
	return pending.QuoteID > 0 && callbackQuoteID == pending.QuoteID
}
