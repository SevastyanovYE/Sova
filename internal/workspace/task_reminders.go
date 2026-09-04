package workspace

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

const (
	workspaceTaskReminderInterval = time.Minute
	workspaceTaskReminderLimit    = 100
)

type taskReminderTelegram interface {
	SendMessageResult(context.Context, nest.SendMessageRequest) (nest.Message, error)
	EditMessageText(context.Context, nest.EditMessageTextRequest) error
}

func runWorkspaceTaskReminderLoop(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client taskReminderTelegram) {
	runWorkspaceTaskReminderLoopWithClock(ctx, cfg, store, client, workspaceTaskReminderInterval, time.Now)
}

func runWorkspaceTaskReminderLoopWithClock(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client taskReminderTelegram, interval time.Duration, now func() time.Time) {
	if interval <= 0 {
		interval = workspaceTaskReminderInterval
	}
	if now == nil {
		now = time.Now
	}
	if _, err := store.RecoverInterruptedWorkspaceTaskReminders(ctx, now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Printf("workspace task reminder recovery unavailable: %v\n", err)
	}
	process := func() {
		if err := processWorkspaceTaskReminders(ctx, cfg, store, client, now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("workspace task reminders unavailable: %v\n", err)
		}
	}
	process() // startup catch-up, including reminders overdue during downtime
	ticker := time.NewTicker(interval)
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

func processWorkspaceTaskReminders(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client taskReminderTelegram, now time.Time) error {
	if store == nil || client == nil {
		return fmt.Errorf("task reminder store and Telegram client are required")
	}
	if _, err := store.EnsureDueWorkspaceTaskReminders(ctx, now); err != nil {
		return fmt.Errorf("create due task reminders: %w", err)
	}
	reminders, err := store.ReadyWorkspaceTaskReminders(ctx, now, workspaceTaskReminderLimit)
	if err != nil {
		return fmt.Errorf("load due task reminders: %w", err)
	}
	for _, reminder := range reminders {
		if err := processWorkspaceTaskReminder(ctx, cfg, store, client, reminder, now); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			fmt.Printf("workspace task reminder %d unavailable: %v\n", reminder.ID, err)
		}
	}
	return nil
}

func processWorkspaceTaskReminder(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client taskReminderTelegram, reminder sqlitestore.WorkspaceTaskReminder, now time.Time) error {
	if reminder.Status != "sent" {
		if !reminderMatchesDeferredTask(reminder) {
			return store.CancelWorkspaceTaskReminder(ctx, reminder.ID, "task changed before reminder delivery", now)
		}
		claimed, err := store.ClaimWorkspaceTaskReminderForSend(ctx, reminder.ID, now)
		if err != nil {
			return fmt.Errorf("claim task reminder for send: %w", err)
		}
		if !claimed {
			return nil
		}
		link := taskCardLink(reminder.Task)
		if link == "" {
			return scheduleWorkspaceTaskReminderRetry(ctx, store, reminder, now, "task card link is unavailable")
		}
		message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
			ChatID:          cfg.Workspace.ChatID,
			MessageThreadID: cfg.Workspace.Topics.Tasks,
			Text:            formatWorkspaceTaskReminder(reminder.Task, link),
			ParseMode:       "HTML",
		})
		if err != nil {
			if workspaceTaskReminderSendIsAmbiguous(err) {
				persistCtx, cancel := workspaceTaskReminderPersistenceContext(ctx)
				defer cancel()
				return store.MarkWorkspaceTaskReminderUnknown(persistCtx, reminder.ID, err.Error(), now)
			}
			return scheduleWorkspaceTaskReminderRetry(ctx, store, reminder, now, err.Error())
		}
		persistCtx, cancel := workspaceTaskReminderPersistenceContext(ctx)
		persistErr := store.MarkWorkspaceTaskReminderSent(persistCtx, reminder.ID, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks, message.MessageID, now)
		cancel()
		if persistErr != nil {
			// Telegram accepted the message, but persistence did not confirm it. Never
			// automatically send this generation again if the database still works.
			unknownCtx, unknownCancel := workspaceTaskReminderPersistenceContext(ctx)
			defer unknownCancel()
			if unknownErr := store.MarkWorkspaceTaskReminderUnknown(unknownCtx, reminder.ID, "sent message could not be recorded: "+persistErr.Error(), now); unknownErr != nil {
				return fmt.Errorf("record sent reminder: %v; mark unknown: %w", persistErr, unknownErr)
			}
			return nil
		}
		reminder.Status = "sent"
		reminder.ReminderMessageID = message.MessageID
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	reopened, err := store.ReopenWorkspaceTaskForReminder(ctx, reminder.ID, now)
	if err != nil {
		return rescheduleWorkspaceTaskReminderFinalization(ctx, store, reminder.ID, now, "reopen task: "+err.Error())
	}
	if !reopened {
		return nil
	}
	reminder.Task.Status = "open"
	reminder.Task.DeferredUntil = nil
	if reminder.Task.CardChatID == 0 || reminder.Task.CardMessageID == 0 {
		return rescheduleWorkspaceTaskReminderFinalization(ctx, store, reminder.ID, now, "task card identity is unavailable")
	}
	if err := client.EditMessageText(ctx, nest.EditMessageTextRequest{
		ChatID:      reminder.Task.CardChatID,
		MessageID:   reminder.Task.CardMessageID,
		Text:        FormatTaskCardIn(reminder.Task, mustLocation(cfg.Timezone)),
		ParseMode:   "HTML",
		ReplyMarkup: TaskActionMarkup(reminder.Task.ID),
	}); err != nil && !isTelegramMessageNotModified(err) {
		return rescheduleWorkspaceTaskReminderFinalization(ctx, store, reminder.ID, now, "edit task card: "+err.Error())
	}
	if err := updateTaskBacklog(ctx, cfg, store, client, now); err != nil {
		return rescheduleWorkspaceTaskReminderFinalization(ctx, store, reminder.ID, now, "update task backlog: "+err.Error())
	}
	return store.CompleteWorkspaceTaskReminder(ctx, reminder.ID, now)
}

func reminderMatchesDeferredTask(reminder sqlitestore.WorkspaceTaskReminder) bool {
	return reminder.Task.Status == "deferred" && reminder.Task.DeferredUntil != nil &&
		reminder.Task.DeferredUntil.Equal(reminder.ScheduledFor) &&
		reminder.Generation == reminder.Task.DeferredGeneration
}

func formatWorkspaceTaskReminder(task sqlitestore.WorkspaceTask, link string) string {
	label := strings.TrimSpace(task.Text)
	if task.Emoji != "" {
		label += " " + task.Emoji
	}
	return "⏰ <b>Пора вернуться к задаче</b>\n\n<a href=\"" + html.EscapeString(link) + "\">" + html.EscapeString(label) + "</a>"
}

func workspaceTaskReminderRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 15 * time.Minute
	default:
		return time.Hour
	}
}

func scheduleWorkspaceTaskReminderRetry(ctx context.Context, store *sqlitestore.Store, reminder sqlitestore.WorkspaceTaskReminder, now time.Time, reason string) error {
	next := now.Add(workspaceTaskReminderRetryDelay(reminder.Attempts + 1))
	return store.MarkWorkspaceTaskReminderRetry(ctx, reminder.ID, next, reason, now)
}

func rescheduleWorkspaceTaskReminderFinalization(ctx context.Context, store *sqlitestore.Store, id int64, now time.Time, reason string) error {
	if err := store.RescheduleWorkspaceTaskReminderFinalization(ctx, id, now.Add(time.Minute), reason, now); err != nil {
		return err
	}
	return nil
}

func workspaceTaskReminderSendIsAmbiguous(err error) bool {
	if err == nil {
		return false
	}
	if nest.IsDefinitelyUnsent(err) {
		return false
	}
	if nest.IsBotAPIClientError(err) {
		return false
	}
	message := strings.ToLower(err.Error())
	// A Bot API JSON rejection or an explicit 4xx response confirms that no
	// message result was returned. Transport, read, parse and 5xx failures can
	// occur after Telegram accepted the request, so they must not be resent.
	if strings.Contains(message, "bot api sendmessage failed:") {
		return false
	}
	if strings.Contains(message, "bot api sendmessage returned 4") {
		return false
	}
	return true
}

func workspaceTaskReminderPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// Once a Telegram send has an ambiguous or successful result, preserve that
	// fact even if service shutdown cancelled the request context. The bound keeps
	// shutdown context-aware while preventing a duplicate on the next startup.
	return context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
}
