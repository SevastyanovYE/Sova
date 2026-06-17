package calendarflow

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/gcalendar"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

const (
	callbackPrefix = "cal:"
	actionApprove  = "approve"
	actionReject   = "reject"
)

func PublishCandidates(ctx context.Context, cfg config.Config, candidates []sqlitestore.CalendarCandidate) error {
	if len(candidates) == 0 {
		return nil
	}
	if !cfg.NestReady() {
		return fmt.Errorf("Nest is not fully configured")
	}
	if err := nest.CheckTopics(cfg); err != nil {
		return err
	}
	client := nest.New(cfg.NestBotToken)
	for _, candidate := range candidates {
		if err := client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Calendar,
			Text:            CandidateMessage(candidate, cfg.Timezone),
			ReplyMarkup: &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{
				{
					{Text: "Approve", CallbackData: CallbackData(actionApprove, candidate.ID)},
					{Text: "Reject", CallbackData: CallbackData(actionReject, candidate.ID)},
				},
			}},
		}); err != nil {
			return err
		}
	}
	return nil
}

func HandleCallback(ctx context.Context, cfg config.Config, data string) (string, error) {
	action, id, ok := ParseCallback(data)
	if !ok {
		return "", fmt.Errorf("unsupported calendar callback")
	}
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		return "", err
	}
	defer store.Close()
	candidate, err := store.CalendarCandidateByID(ctx, id)
	if err != nil {
		return "", err
	}
	switch action {
	case actionReject:
		if err := store.UpdateCalendarCandidateStatus(ctx, candidate.ID, "rejected", candidate.CalendarEventID, "", time.Now().UTC()); err != nil {
			return "", err
		}
		return fmt.Sprintf("Calendar candidate #%d rejected: %s", candidate.ID, candidate.Title), nil
	case actionApprove:
		return approveCandidate(ctx, cfg, store, candidate)
	default:
		return "", fmt.Errorf("unsupported calendar action %q", action)
	}
}

func approveCandidate(ctx context.Context, cfg config.Config, store *sqlitestore.Store, candidate sqlitestore.CalendarCandidate) (string, error) {
	if candidate.Status == "created" {
		return fmt.Sprintf("Calendar event already exists for candidate #%d: %s", candidate.ID, candidate.CalendarEventID), nil
	}
	if candidate.Status == "rejected" {
		return fmt.Sprintf("Calendar candidate #%d was already rejected.", candidate.ID), nil
	}
	event, err := gcalendar.CreateEvent(ctx, cfg, gcalendar.Event{
		Title:       candidate.Title,
		StartAt:     candidate.StartAt,
		EndAt:       candidate.EndAt,
		Timezone:    candidate.Timezone,
		Location:    candidate.Location,
		Description: calendarDescription(candidate),
	})
	if err != nil {
		_ = store.UpdateCalendarCandidateStatus(ctx, candidate.ID, "failed", "", err.Error(), time.Now().UTC())
		return "", err
	}
	if err := store.UpdateCalendarCandidateStatus(ctx, candidate.ID, "created", event.ID, "", time.Now().UTC()); err != nil {
		return "", err
	}
	if event.HTMLLink != "" {
		return fmt.Sprintf("Calendar event created for candidate #%d: %s\n%s", candidate.ID, candidate.Title, event.HTMLLink), nil
	}
	return fmt.Sprintf("Calendar event created for candidate #%d: %s", candidate.ID, candidate.Title), nil
}

func ParseCallback(data string) (string, int64, bool) {
	if !strings.HasPrefix(data, callbackPrefix) {
		return "", 0, false
	}
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return "", 0, false
	}
	action := parts[1]
	if action != actionApprove && action != actionReject {
		return "", 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return "", 0, false
	}
	return action, id, true
}

func CallbackData(action string, id int64) string {
	return callbackPrefix + action + ":" + strconv.FormatInt(id, 10)
}

func IsCallback(data string) bool {
	_, _, ok := ParseCallback(data)
	return ok
}

func CandidateMessage(candidate sqlitestore.CalendarCandidate, timezone string) string {
	location := mustLocation(timezone)
	var b strings.Builder
	b.WriteString("Calendar candidate #")
	b.WriteString(strconv.FormatInt(candidate.ID, 10))
	b.WriteString("\n")
	b.WriteString(candidate.Title)
	b.WriteString("\n")
	b.WriteString(candidate.StartAt.In(location).Format("2006-01-02 15:04 MST"))
	b.WriteString(" - ")
	b.WriteString(candidate.EndAt.In(location).Format("15:04 MST"))
	if candidate.Location != "" {
		b.WriteString("\nLocation: ")
		b.WriteString(candidate.Location)
	}
	if candidate.Confidence != "" {
		b.WriteString("\nConfidence: ")
		b.WriteString(candidate.Confidence)
	}
	if candidate.SourceLink != "" {
		b.WriteString("\nSource: ")
		b.WriteString(candidate.SourceLink)
	}
	if candidate.Description != "" {
		b.WriteString("\n\n")
		b.WriteString(compactLine(candidate.Description, 500))
	}
	return b.String()
}

func calendarDescription(candidate sqlitestore.CalendarCandidate) string {
	parts := []string{}
	if strings.TrimSpace(candidate.Description) != "" {
		parts = append(parts, candidate.Description)
	}
	if candidate.SourceLink != "" {
		parts = append(parts, "Source: "+candidate.SourceLink)
	}
	parts = append(parts, "Created by Sova after approval in Nest Calendar topic.")
	return strings.Join(parts, "\n\n")
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
