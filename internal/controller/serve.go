package controller

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SevastyanovYE/Sova/internal/calendarflow"
	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	"github.com/SevastyanovYE/Sova/internal/overview"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

const createOverviewCallback = "sova:create_overview"

const (
	pollInitialBackoff = 5 * time.Second
	pollMaxBackoff     = time.Minute
	dateEditTTL        = 10 * time.Minute
)

type overviewJob struct {
	trigger     string
	chatReply   bool
	callbackID  string
	scheduledAt time.Time
}

type dateEditKey struct {
	chatID   int64
	threadID int
	userID   int64
}

type pendingDateEdit struct {
	CandidateID int64
	ExpiresAt   time.Time
}

func Serve(ctx context.Context, cfg config.Config) error {
	if !cfg.NestReady() {
		return fmt.Errorf("Nest is not fully configured")
	}
	if err := nest.CheckTopics(cfg); err != nil {
		return err
	}
	client := nest.New(cfg.NestBotToken)
	jobs := make(chan overviewJob, 1)
	var busy atomic.Bool
	submit := func(job overviewJob) bool {
		if !busy.CompareAndSwap(false, true) {
			return false
		}
		select {
		case jobs <- job:
			return true
		default:
			busy.Store(false)
			return false
		}
	}

	go overviewWorker(ctx, cfg, client, jobs, &busy)
	go dailyScheduler(ctx, cfg, client, submit)

	fmt.Printf("sova serve: polling Nest chat %d topic %d; daily run at %s %s\n",
		cfg.NestChatID, cfg.NestTopics.Chat, cfg.DailyRunTime, cfg.Timezone)

	offset := 0
	pollFailures := 0
	pendingEdits := map[dateEditKey]pendingDateEdit{}
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
			delay := pollRetryDelay(pollFailures)
			if shouldLogPollFailure(pollFailures) {
				fmt.Printf("getUpdates unavailable (attempt %d; retrying in %s): %v\n", pollFailures, delay, err)
			}
			sleepOrDone(ctx, delay)
			continue
		}
		if pollFailures > 0 {
			fmt.Printf("getUpdates recovered after %d failed attempt(s)\n", pollFailures)
			pollFailures = 0
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			handleUpdate(ctx, cfg, client, submit, pendingEdits, update)
		}
	}
}

func pollRetryDelay(failures int) time.Duration {
	if failures <= 1 {
		return pollInitialBackoff
	}
	delay := pollInitialBackoff
	for attempt := 1; attempt < failures && delay < pollMaxBackoff; attempt++ {
		delay *= 2
	}
	if delay > pollMaxBackoff {
		return pollMaxBackoff
	}
	return delay
}

func shouldLogPollFailure(failures int) bool {
	return failures <= 3 || failures%5 == 0
}

func overviewWorker(ctx context.Context, cfg config.Config, client *nest.Client, jobs <-chan overviewJob, busy *atomic.Bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			opts := overview.ProductionOptions()
			opts.Progress = newStatusProgressReporter(ctx, cfg, client)
			result, err := overview.Run(ctx, cfg, job.trigger, opts)
			busy.Store(false)
			if job.chatReply {
				reply := chatRunReply(cfg, result, err)
				_ = client.SendMessage(ctx, nest.SendMessageRequest{
					ChatID:          cfg.NestChatID,
					MessageThreadID: cfg.NestTopics.Chat,
					Text:            reply,
				})
			}
			if err != nil && !job.chatReply {
				var cooldownErr *sqlitestore.CooldownError
				if errors.As(err, &cooldownErr) {
					continue
				}
				_ = client.SendLongMessage(ctx, nest.SendMessageRequest{
					ChatID:          cfg.NestChatID,
					MessageThreadID: cfg.NestTopics.Status,
					Text:            "Scheduled Sova overview failed: " + compactLine(err.Error(), 500),
				})
			}
		}
	}
}

func handleUpdate(ctx context.Context, cfg config.Config, client *nest.Client, submit func(overviewJob) bool, pendingEdits map[dateEditKey]pendingDateEdit, update nest.Update) {
	if update.Message != nil {
		handleMessage(ctx, cfg, client, submit, pendingEdits, *update.Message)
		return
	}
	if update.CallbackQuery != nil {
		handleCallback(ctx, cfg, client, submit, pendingEdits, *update.CallbackQuery)
	}
}

func handleMessage(ctx context.Context, cfg config.Config, client *nest.Client, submit func(overviewJob) bool, pendingEdits map[dateEditKey]pendingDateEdit, message nest.Message) {
	if isCalendarTopicMessage(cfg, message) {
		handleCalendarDateEditMessage(ctx, cfg, client, pendingEdits, message)
		return
	}
	if !isChatTopicMessage(cfg, message) {
		return
	}
	command := commandName(message.Text)
	switch command {
	case "run":
		if submit(overviewJob{trigger: "nest_button", chatReply: true}) {
			_ = client.SendMessage(ctx, nest.SendMessageRequest{
				ChatID:          cfg.NestChatID,
				MessageThreadID: cfg.NestTopics.Chat,
				Text:            overviewStartedText(),
				ParseMode:       "HTML",
			})
			return
		}
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Chat,
			Text:            "<b>Обзор уже в работе</b>\n\nЯ не запускаю второй параллельно. Ход текущего обзора видно в <b>Status</b>.",
			ParseMode:       "HTML",
		})
	case "start", "help", "button":
		_ = SendControlMessage(ctx, cfg, client)
	}
}

func handleCallback(ctx context.Context, cfg config.Config, client *nest.Client, submit func(overviewJob) bool, pendingEdits map[dateEditKey]pendingDateEdit, callback nest.CallbackQuery) {
	if calendarflow.IsCallback(callback.Data) {
		handleCalendarCallback(ctx, cfg, client, pendingEdits, callback)
		return
	}
	if callback.Data != createOverviewCallback {
		return
	}
	if callback.Message == nil || !isChatTopicMessage(cfg, *callback.Message) {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Команды доступны только в Chat topic.")
		return
	}
	if submit(overviewJob{trigger: "nest_button", chatReply: true, callbackID: callback.ID}) {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Запускаю обзор.")
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Chat,
			Text:            overviewStartedText(),
			ParseMode:       "HTML",
		})
		return
	}
	_ = client.AnswerCallbackQuery(ctx, callback.ID, "Обзор уже выполняется.")
}

func handleCalendarCallback(ctx context.Context, cfg config.Config, client *nest.Client, pendingEdits map[dateEditKey]pendingDateEdit, callback nest.CallbackQuery) {
	if callback.Message == nil || callback.Message.Chat.ID != cfg.NestChatID || callback.Message.MessageThreadID != cfg.NestTopics.Calendar {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Calendar approvals are available only in Calendar topic.")
		return
	}
	action, id, ok := calendarflow.ParseCallback(callback.Data)
	if !ok {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Unsupported calendar action.")
		return
	}
	if calendarflow.IsDateEditAction(action) {
		candidate, err := calendarflow.CandidateForDateEdit(ctx, cfg, id)
		if err != nil {
			_ = client.AnswerCallbackQuery(ctx, callback.ID, "Нельзя изменить дату.")
			_ = client.SendLongMessage(ctx, nest.SendMessageRequest{
				ChatID:          cfg.NestChatID,
				MessageThreadID: cfg.NestTopics.Calendar,
				Text:            "<b>Не удалось начать изменение даты</b>\n\n<blockquote>" + html.EscapeString(compactLine(err.Error(), 400)) + "</blockquote>",
				ParseMode:       "HTML",
			})
			return
		}
		pendingEdits[dateEditKey{chatID: callback.Message.Chat.ID, threadID: callback.Message.MessageThreadID, userID: callback.From.ID}] = pendingDateEdit{
			CandidateID: candidate.ID,
			ExpiresAt:   time.Now().Add(dateEditTTL),
		}
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Жду новую дату.")
		_ = client.SendLongMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Calendar,
			Text:            calendarflow.DateEditPrompt(candidate),
			ParseMode:       "HTML",
		})
		return
	}
	text, err := calendarflow.HandleCallback(ctx, cfg, callback.Data)
	parseMode := "HTML"
	if err != nil {
		text = "<b>Действие не выполнено</b>\n\n<blockquote>" + html.EscapeString(compactLine(err.Error(), 500)) + "</blockquote>"
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Calendar action failed.")
	} else {
		_ = client.AnswerCallbackQuery(ctx, callback.ID, "Done.")
	}
	_ = client.SendLongMessage(ctx, nest.SendMessageRequest{
		ChatID:          cfg.NestChatID,
		MessageThreadID: cfg.NestTopics.Calendar,
		Text:            text,
		ParseMode:       parseMode,
	})
}

func handleCalendarDateEditMessage(ctx context.Context, cfg config.Config, client *nest.Client, pendingEdits map[dateEditKey]pendingDateEdit, message nest.Message) {
	if message.From == nil {
		return
	}
	key := dateEditKey{chatID: message.Chat.ID, threadID: message.MessageThreadID, userID: message.From.ID}
	pending, ok := pendingEdits[key]
	if !ok {
		return
	}
	if time.Now().After(pending.ExpiresAt) {
		delete(pendingEdits, key)
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Calendar,
			Text:            "<b>Ожидание даты истекло</b>\n\nНажми <b>Изменить дату</b> ещё раз, и я снова подожду новую дату.",
			ParseMode:       "HTML",
		})
		return
	}
	updated, text, err := calendarflow.UpdateCandidateDate(ctx, cfg, pending.CandidateID, message.Text, time.Now().UTC())
	if err != nil {
		_ = client.SendLongMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Calendar,
			Text:            "<b>Не получилось изменить дату</b>\n\n<blockquote>" + html.EscapeString(compactLine(err.Error(), 300)) + "</blockquote>\n\nФормат: <code>2026-06-28</code> или <code>2026-06-28 11:00</code>",
			ParseMode:       "HTML",
		})
		return
	}
	delete(pendingEdits, key)
	_ = client.SendLongMessage(ctx, nest.SendMessageRequest{
		ChatID:          cfg.NestChatID,
		MessageThreadID: cfg.NestTopics.Calendar,
		Text:            text,
		ParseMode:       "HTML",
		ReplyMarkup:     calendarCandidateMarkup(updated.ID),
	})
}

func SendControlMessage(ctx context.Context, cfg config.Config, client *nest.Client) error {
	return client.SendMessage(ctx, ControlMessageRequest(cfg))
}

func ControlMessageRequest(cfg config.Config) nest.SendMessageRequest {
	return nest.SendMessageRequest{
		ChatID:          cfg.NestChatID,
		MessageThreadID: cfg.NestTopics.Chat,
		Text:            chatControlText(),
		ParseMode:       "HTML",
		ReplyMarkup:     controlMessageMarkup(),
	}
}

func chatControlText() string {
	return "<b>🦉 Sova Control</b>\n\nНажми <b>Создать обзор</b> или отправь <code>/run</code>, чтобы запустить свежий обзор.\n\n<blockquote>Общий cooldown для всех запусков — 15 минут.</blockquote>\n\nАвтоматические дайджесты, статусы и календарные карточки сюда не пишу: этот топик остаётся для твоих команд."
}

func overviewStartedText() string {
	return "<b>Запускаю обзор</b> ✨\n\nРезультат придёт в <b>Digest</b>, а ход выполнения буду обновлять в <b>Status</b>."
}

func controlMessageMarkup() *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{
		{{Text: "Создать обзор", CallbackData: createOverviewCallback}},
	}}
}

func calendarCandidateMarkup(id int64) *nest.InlineKeyboardMarkup {
	return &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{
		{
			{Text: "Approve", CallbackData: calendarflow.CallbackData("approve", id)},
			{Text: "Reject", CallbackData: calendarflow.CallbackData("reject", id)},
		},
		{
			{Text: "Изменить дату", CallbackData: calendarflow.CallbackData("editdate", id)},
		},
	}}
}

func dailyScheduler(ctx context.Context, cfg config.Config, client *nest.Client, submit func(overviewJob) bool) {
	location := mustLocation(cfg.Timezone)
	for {
		next := nextDailyRun(time.Now().In(location), cfg.DailyRunTime, location)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if !submit(overviewJob{trigger: "scheduled", scheduledAt: next}) {
				_ = client.SendMessage(ctx, nest.SendMessageRequest{
					ChatID:          cfg.NestChatID,
					MessageThreadID: cfg.NestTopics.Status,
					Text:            "Scheduled Sova overview skipped: another overview is already running.",
				})
			}
		}
	}
}

func nextDailyRun(now time.Time, daily string, location *time.Location) time.Time {
	parsed, err := time.Parse("15:04", daily)
	if err != nil {
		parsed = time.Date(0, 1, 1, 8, 0, 0, 0, time.UTC)
	}
	next := time.Date(now.In(location).Year(), now.In(location).Month(), now.In(location).Day(),
		parsed.Hour(), parsed.Minute(), 0, 0, location)
	if !now.Before(next) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func chatRunReply(cfg config.Config, result overview.Result, err error) string {
	if err == nil {
		if result.Published {
			if result.Degraded {
				return "Обзор опубликован в Digest topic в резервном формате; подробности отправлены в Status topic."
			}
			return "Обзор готов и опубликован в Digest topic."
		}
		return "Обзор завершен: " + result.Summary
	}
	var cooldownErr *sqlitestore.CooldownError
	if errors.As(err, &cooldownErr) {
		return "Пока рано: следующий обзор можно запустить после " +
			cooldownErr.NextAllowedAt.In(mustLocation(cfg.Timezone)).Format("15:04:05 MST") + "."
	}
	if errors.Is(err, sqlitestore.ErrRunActive) {
		return "Обзор уже выполняется."
	}
	return "Не получилось запустить обзор: " + compactLine(err.Error(), 300)
}

func isChatTopicMessage(cfg config.Config, message nest.Message) bool {
	return message.Chat.ID == cfg.NestChatID && cfg.IsCommandTopic(message.MessageThreadID)
}

func isCalendarTopicMessage(cfg config.Config, message nest.Message) bool {
	return message.Chat.ID == cfg.NestChatID && message.MessageThreadID == cfg.NestTopics.Calendar
}

func newStatusProgressReporter(ctx context.Context, cfg config.Config, client *nest.Client) func(context.Context, overview.ProgressEvent) {
	var messageID int
	return func(_ context.Context, event overview.ProgressEvent) {
		text := formatProgressMessage(event)
		if messageID == 0 {
			message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
				ChatID:          cfg.NestChatID,
				MessageThreadID: cfg.NestTopics.Status,
				Text:            text,
				ParseMode:       "HTML",
			})
			if err == nil {
				messageID = message.MessageID
			}
			return
		}
		if err := client.EditMessageText(ctx, nest.EditMessageTextRequest{
			ChatID:    cfg.NestChatID,
			MessageID: messageID,
			Text:      text,
			ParseMode: "HTML",
		}); err != nil {
			message, sendErr := client.SendMessageResult(ctx, nest.SendMessageRequest{
				ChatID:          cfg.NestChatID,
				MessageThreadID: cfg.NestTopics.Status,
				Text:            text,
				ParseMode:       "HTML",
			})
			if sendErr == nil {
				messageID = message.MessageID
			}
		}
	}
}

func formatProgressMessage(event overview.ProgressEvent) string {
	status := "Выполняется"
	if event.Failed {
		status = "Ошибка"
	} else if event.Done {
		status = "Готово"
	}
	var b strings.Builder
	b.WriteString("<b>🦉 Sova run")
	if event.RunID > 0 {
		b.WriteString(" #")
		b.WriteString(strconv.FormatInt(event.RunID, 10))
	}
	b.WriteString("</b>\n")
	b.WriteString("<i>")
	b.WriteString(status)
	b.WriteString("</i>")
	b.WriteString("\n\n")
	b.WriteString(html.EscapeString(event.Message))
	if event.Total > 0 {
		b.WriteString("\n\n<b>Шаг:</b> ")
		b.WriteString(strconv.Itoa(event.Current))
		b.WriteString("/")
		b.WriteString(strconv.Itoa(event.Total))
	}
	if event.EstimatedRemaining > 0 && !event.Done && !event.Failed {
		b.WriteString("\n<b>Осталось примерно:</b> ")
		b.WriteString(roundDuration(event.EstimatedRemaining))
	}
	return b.String()
}

func roundDuration(duration time.Duration) string {
	if duration < time.Minute {
		return "<1 мин"
	}
	minutes := int(duration.Round(time.Minute) / time.Minute)
	return fmt.Sprintf("%d мин", minutes)
}

func commandName(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	first := strings.Fields(text)[0]
	if !strings.HasPrefix(first, "/") {
		return ""
	}
	first = strings.TrimPrefix(first, "/")
	if name, _, ok := strings.Cut(first, "@"); ok {
		first = name
	}
	return strings.ToLower(first)
}

func sleepOrDone(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func compactLine(value string, limit int) string {
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
