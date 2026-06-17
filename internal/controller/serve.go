package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	"github.com/SevastyanovYE/Sova/internal/overview"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

const createOverviewCallback = "sova:create_overview"

type overviewJob struct {
	trigger     string
	chatReply   bool
	callbackID  string
	scheduledAt time.Time
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
			fmt.Println("getUpdates error:", err)
			sleepOrDone(ctx, 5*time.Second)
			continue
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			handleUpdate(ctx, cfg, client, submit, update)
		}
	}
}

func overviewWorker(ctx context.Context, cfg config.Config, client *nest.Client, jobs <-chan overviewJob, busy *atomic.Bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			result, err := overview.Run(ctx, cfg, job.trigger, overview.ProductionOptions())
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

func handleUpdate(ctx context.Context, cfg config.Config, client *nest.Client, submit func(overviewJob) bool, update nest.Update) {
	if update.Message != nil {
		handleMessage(ctx, cfg, client, submit, *update.Message)
		return
	}
	if update.CallbackQuery != nil {
		handleCallback(ctx, cfg, client, submit, *update.CallbackQuery)
	}
}

func handleMessage(ctx context.Context, cfg config.Config, client *nest.Client, submit func(overviewJob) bool, message nest.Message) {
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
				Text:            "Запускаю обзор. Результат придет в Digest topic.",
			})
			return
		}
		_ = client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Chat,
			Text:            "Обзор уже выполняется или стоит в очереди.",
		})
	case "start", "help", "button":
		_ = sendControlMessage(ctx, cfg, client)
	}
}

func handleCallback(ctx context.Context, cfg config.Config, client *nest.Client, submit func(overviewJob) bool, callback nest.CallbackQuery) {
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
			Text:            "Запускаю обзор. Результат придет в Digest topic.",
		})
		return
	}
	_ = client.AnswerCallbackQuery(ctx, callback.ID, "Обзор уже выполняется.")
}

func sendControlMessage(ctx context.Context, cfg config.Config, client *nest.Client) error {
	return client.SendMessage(ctx, nest.SendMessageRequest{
		ChatID:          cfg.NestChatID,
		MessageThreadID: cfg.NestTopics.Chat,
		Text:            "Sova",
		ReplyMarkup: &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{
			{{Text: "Создать обзор", CallbackData: createOverviewCallback}},
		}},
	})
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
