package calendarflow

import (
	"context"
	"fmt"
	"html"
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
	actionEditDate = "editdate"
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
			ParseMode:       "HTML",
			ReplyMarkup: &nest.InlineKeyboardMarkup{InlineKeyboard: [][]nest.InlineKeyboardButton{
				{
					{Text: "Approve", CallbackData: CallbackData(actionApprove, candidate.ID)},
					{Text: "Reject", CallbackData: CallbackData(actionReject, candidate.ID)},
				},
				{
					{Text: "Изменить дату", CallbackData: CallbackData(actionEditDate, candidate.ID)},
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
		return fmt.Sprintf("<b>Отклонено</b>\n\nКандидат <code>#%d</code>: %s", candidate.ID, html.EscapeString(candidate.Title)), nil
	case actionApprove:
		return approveCandidate(ctx, cfg, store, candidate)
	case actionEditDate:
		return DateEditPrompt(candidate), nil
	default:
		return "", fmt.Errorf("unsupported calendar action %q", action)
	}
}

func CandidateForDateEdit(ctx context.Context, cfg config.Config, id int64) (sqlitestore.CalendarCandidate, error) {
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		return sqlitestore.CalendarCandidate{}, err
	}
	defer store.Close()
	candidate, err := store.CalendarCandidateByID(ctx, id)
	if err != nil {
		return sqlitestore.CalendarCandidate{}, err
	}
	if candidate.Status == "created" || candidate.Status == "rejected" {
		return sqlitestore.CalendarCandidate{}, fmt.Errorf("candidate #%d is %s and cannot be edited", candidate.ID, candidate.Status)
	}
	return candidate, nil
}

func DateEditPrompt(candidate sqlitestore.CalendarCandidate) string {
	return fmt.Sprintf(
		"<b>Изменение даты</b>\n\nЖду новую дату для кандидата <code>#%d</code>: <b>%s</b>\n\nФормат: <code>2026-06-28</code> или <code>2026-06-28 11:00</code>\n\nЕсли время не указать, я сохраню текущее время события.",
		candidate.ID, html.EscapeString(candidate.Title),
	)
}

func UpdateCandidateDate(ctx context.Context, cfg config.Config, id int64, input string, now time.Time) (sqlitestore.CalendarCandidate, string, error) {
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		return sqlitestore.CalendarCandidate{}, "", err
	}
	defer store.Close()
	candidate, err := store.CalendarCandidateByID(ctx, id)
	if err != nil {
		return sqlitestore.CalendarCandidate{}, "", err
	}
	if candidate.Status == "created" || candidate.Status == "rejected" {
		return sqlitestore.CalendarCandidate{}, "", fmt.Errorf("candidate #%d is %s and cannot be edited", candidate.ID, candidate.Status)
	}
	start, end, err := shiftedCandidateTime(candidate, input, cfg.Timezone)
	if err != nil {
		return sqlitestore.CalendarCandidate{}, "", err
	}
	if err := store.UpdateCalendarCandidateTime(ctx, candidate.ID, start, end, now); err != nil {
		return sqlitestore.CalendarCandidate{}, "", err
	}
	updated, err := store.CalendarCandidateByID(ctx, candidate.ID)
	if err != nil {
		return sqlitestore.CalendarCandidate{}, "", err
	}
	return updated, "<b>Дата обновлена</b> ✨\n\n" + CandidateMessage(updated, cfg.Timezone), nil
}

func shiftedCandidateTime(candidate sqlitestore.CalendarCandidate, input string, timezone string) (time.Time, time.Time, error) {
	location := mustLocation(timezone)
	input = strings.TrimSpace(input)
	var parsed time.Time
	var err error
	hasTime := true
	switch {
	case len(input) == len("2006-01-02"):
		hasTime = false
		parsed, err = time.ParseInLocation("2006-01-02", input, location)
	case len(input) == len("2006-01-02 15:04"):
		parsed, err = time.ParseInLocation("2006-01-02 15:04", input, location)
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("invalid date format; use 2026-06-28 or 2026-06-28 11:00")
	}
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid date format; use 2026-06-28 or 2026-06-28 11:00")
	}
	oldStart := candidate.StartAt.In(location)
	if !hasTime {
		parsed = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), oldStart.Hour(), oldStart.Minute(), 0, 0, location)
	}
	duration := candidate.EndAt.Sub(candidate.StartAt)
	if duration <= 0 {
		duration = time.Hour
	}
	return parsed, parsed.Add(duration), nil
}

func approveCandidate(ctx context.Context, cfg config.Config, store *sqlitestore.Store, candidate sqlitestore.CalendarCandidate) (string, error) {
	if candidate.Status == "created" {
		return fmt.Sprintf("<b>Событие уже создано</b>\n\nКандидат <code>#%d</code>\nGoogle event: <code>%s</code>", candidate.ID, html.EscapeString(candidate.CalendarEventID)), nil
	}
	if candidate.Status == "rejected" {
		return fmt.Sprintf("<b>Кандидат уже отклонён</b>\n\nКандидат <code>#%d</code> больше нельзя approve.", candidate.ID), nil
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
		return fmt.Sprintf("<b>Событие создано</b> ✅\n\nКандидат <code>#%d</code>: <b>%s</b>\n%s", candidate.ID, html.EscapeString(candidate.Title), html.EscapeString(event.HTMLLink)), nil
	}
	return fmt.Sprintf("<b>Событие создано</b> ✅\n\nКандидат <code>#%d</code>: <b>%s</b>", candidate.ID, html.EscapeString(candidate.Title)), nil
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
	if action != actionApprove && action != actionReject && action != actionEditDate {
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

func IsDateEditAction(action string) bool {
	return action == actionEditDate
}

func IsCallback(data string) bool {
	_, _, ok := ParseCallback(data)
	return ok
}

func CandidateMessage(candidate sqlitestore.CalendarCandidate, timezone string) string {
	location := mustLocation(timezone)
	var b strings.Builder
	b.WriteString("<b>📅 Кандидат в календарь #")
	b.WriteString(strconv.FormatInt(candidate.ID, 10))
	b.WriteString("</b>\n\n")
	b.WriteString("<b>")
	b.WriteString(html.EscapeString(candidate.Title))
	b.WriteString("</b>\n")
	b.WriteString("<code>")
	b.WriteString(candidate.StartAt.In(location).Format("2006-01-02 15:04 MST"))
	b.WriteString(" - ")
	b.WriteString(candidate.EndAt.In(location).Format("15:04 MST"))
	b.WriteString("</code>")
	if candidate.Location != "" {
		b.WriteString("\n<b>Место:</b> ")
		b.WriteString(html.EscapeString(candidate.Location))
	}
	if candidate.Confidence != "" {
		b.WriteString("\n<b>Уверенность:</b> ")
		b.WriteString(html.EscapeString(candidate.Confidence))
	}
	if candidate.SourceLink != "" {
		b.WriteString("\n<b>Источник:</b> ")
		b.WriteString(html.EscapeString(candidate.SourceLink))
	}
	if candidate.Description != "" {
		b.WriteString("\n\n")
		b.WriteString("<blockquote>")
		b.WriteString(html.EscapeString(compactLine(candidate.Description, 500)))
		b.WriteString("</blockquote>")
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
