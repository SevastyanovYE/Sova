package calendarflow

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/gcalendar"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type fakeCalendarPublicationTelegram struct {
	sends int
	err   error
}

func (f *fakeCalendarPublicationTelegram) SendMessageResult(_ context.Context, _ nest.SendMessageRequest) (nest.Message, error) {
	f.sends++
	if f.err != nil {
		return nest.Message{}, f.err
	}
	return nest.Message{MessageID: 900 + f.sends}, nil
}

func TestCalendarPublicationDoesNotDuplicateAmbiguousSend(t *testing.T) {
	store, candidate, cfg := newCalendarApprovalCandidate(t)
	defer store.Close()
	cfg.NestChatID = -1001
	cfg.NestTopics = config.TopicIDs{Digest: 2, Calendar: 3, Status: 4, Chat: 5}
	ctx := context.Background()
	failing := &fakeCalendarPublicationTelegram{err: errors.New("Bot API sendMessage request failed: unexpected EOF")}
	if err := publishCandidatesWithClient(ctx, cfg, store, candidate.RunID, []sqlitestore.CalendarCandidate{candidate}, failing); err == nil {
		t.Fatal("ambiguous calendar send unexpectedly succeeded")
	}
	good := &fakeCalendarPublicationTelegram{}
	if err := publishCandidatesWithClient(ctx, cfg, store, candidate.RunID, []sqlitestore.CalendarCandidate{candidate}, good); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("second publication err=%v", err)
	}
	if failing.sends != 1 || good.sends != 0 {
		t.Fatalf("failing sends=%d retry sends=%d", failing.sends, good.sends)
	}
}

func TestCalendarPublicationRetriesConfirmedRejectionThenSkipsSent(t *testing.T) {
	store, candidate, cfg := newCalendarApprovalCandidate(t)
	defer store.Close()
	cfg.NestChatID = -1001
	cfg.NestTopics = config.TopicIDs{Digest: 2, Calendar: 3, Status: 4, Chat: 5}
	ctx := context.Background()
	rejected := &fakeCalendarPublicationTelegram{err: errors.New("Bot API sendMessage failed: Bad Request")}
	if err := publishCandidatesWithClient(ctx, cfg, store, candidate.RunID, []sqlitestore.CalendarCandidate{candidate}, rejected); err == nil {
		t.Fatal("confirmed rejection unexpectedly succeeded")
	}
	good := &fakeCalendarPublicationTelegram{}
	if err := publishCandidatesWithClient(ctx, cfg, store, candidate.RunID, []sqlitestore.CalendarCandidate{candidate}, good); err != nil {
		t.Fatal(err)
	}
	if err := publishCandidatesWithClient(ctx, cfg, store, candidate.RunID, []sqlitestore.CalendarCandidate{candidate}, good); err != nil {
		t.Fatal(err)
	}
	if rejected.sends != 1 || good.sends != 1 {
		t.Fatalf("rejected sends=%d good sends=%d", rejected.sends, good.sends)
	}
}

func TestCallbackDataRoundTrip(t *testing.T) {
	data := CallbackData(actionApprove, 42)
	action, id, ok := ParseCallback(data)
	if !ok {
		t.Fatal("callback did not parse")
	}
	if action != actionApprove || id != 42 {
		t.Fatalf("parsed callback = %q %d", action, id)
	}
	editAction, editID, editOK := ParseCallback(CallbackData(actionEditDate, 43))
	if !editOK || !IsDateEditAction(editAction) || editID != 43 {
		t.Fatalf("edit callback parsed as %q %d ok=%t", editAction, editID, editOK)
	}
	for _, invalid := range []string{"", "run", "cal:approve", "cal:approve:x", "cal:unknown:42"} {
		if IsCallback(invalid) {
			t.Fatalf("invalid callback parsed: %q", invalid)
		}
	}
}

func TestApproveCandidateRetryAfterLostResponseUsesOneDeterministicEvent(t *testing.T) {
	store, candidate, cfg := newCalendarApprovalCandidate(t)
	defer store.Close()
	ctx := context.Background()
	created := map[string]gcalendar.CreatedEvent{}
	calls := 0
	creator := func(_ context.Context, _ config.Config, event gcalendar.Event) (gcalendar.CreatedEvent, error) {
		calls++
		result, exists := created[event.ID]
		if !exists {
			result = gcalendar.CreatedEvent{ID: event.ID, HTMLLink: "https://calendar.example/" + event.ID}
			created[event.ID] = result
			return gcalendar.CreatedEvent{}, errors.New("provider accepted event but response was lost")
		}
		return result, nil
	}
	if _, err := approveCandidateWithCreate(ctx, cfg, store, candidate, creator); err == nil {
		t.Fatal("lost response unexpectedly reported success")
	}
	failed, err := store.CalendarCandidateByID(ctx, candidate.ID)
	if err != nil || failed.Status != "failed" || failed.CalendarEventID != gcalendar.EventIDForCandidate(candidate.ID) {
		t.Fatalf("failed reservation=%+v err=%v", failed, err)
	}
	text, err := approveCandidateWithCreate(ctx, cfg, store, failed, creator)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || calls != 2 || !strings.Contains(text, "Событие создано") {
		t.Fatalf("created=%d calls=%d text=%q", len(created), calls, text)
	}
	stored, err := store.CalendarCandidateByID(ctx, candidate.ID)
	if err != nil || stored.Status != "created" || stored.CalendarEventID != gcalendar.EventIDForCandidate(candidate.ID) {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestParallelCalendarApprovalsShareOneExternalIdentity(t *testing.T) {
	store, candidate, cfg := newCalendarApprovalCandidate(t)
	defer store.Close()
	ctx := context.Background()
	var mutex sync.Mutex
	created := map[string]struct{}{}
	creator := func(_ context.Context, _ config.Config, event gcalendar.Event) (gcalendar.CreatedEvent, error) {
		mutex.Lock()
		created[event.ID] = struct{}{}
		mutex.Unlock()
		return gcalendar.CreatedEvent{ID: event.ID}, nil
	}
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	for attempt := 0; attempt < 2; attempt++ {
		go func() {
			<-start
			_, err := approveCandidateWithCreate(ctx, cfg, store, candidate, creator)
			errorsOut <- err
		}()
	}
	close(start)
	for attempt := 0; attempt < 2; attempt++ {
		if err := <-errorsOut; err != nil {
			t.Fatalf("parallel approval: %v", err)
		}
	}
	if len(created) != 1 {
		t.Fatalf("unique external event ids = %d", len(created))
	}
	stored, err := store.CalendarCandidateByID(ctx, candidate.ID)
	if err != nil || stored.Status != "created" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestRejectAndDateEditCannotRaceReservedApproval(t *testing.T) {
	store, candidate, cfg := newCalendarApprovalCandidate(t)
	defer store.Close()
	ctx := context.Background()
	eventID := gcalendar.EventIDForCandidate(candidate.ID)
	reserved, claimed, err := store.ReserveCalendarCandidateApproval(ctx, candidate.ID, eventID, time.Now().UTC())
	if err != nil || !claimed || reserved.Status != "approved" {
		t.Fatalf("reserve: candidate=%+v claimed=%t err=%v", reserved, claimed, err)
	}
	current, rejected, err := store.RejectCalendarCandidate(ctx, candidate.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if rejected || current.Status != "approved" || current.CalendarEventID != eventID {
		t.Fatalf("reject raced reservation: candidate=%+v rejected=%t", current, rejected)
	}
	if _, err := CandidateForDateEdit(ctx, cfg, candidate.ID); err == nil || !strings.Contains(err.Error(), "approved") {
		t.Fatalf("date edit of reserved candidate err=%v", err)
	}
}

func TestRejectCalendarCandidateIsIdempotent(t *testing.T) {
	store, candidate, _ := newCalendarApprovalCandidate(t)
	defer store.Close()
	ctx := context.Background()
	current, rejected, err := store.RejectCalendarCandidate(ctx, candidate.ID, time.Now().UTC())
	if err != nil || !rejected || current.Status != "rejected" {
		t.Fatalf("first reject: candidate=%+v rejected=%t err=%v", current, rejected, err)
	}
	current, rejected, err = store.RejectCalendarCandidate(ctx, candidate.ID, time.Now().UTC())
	if err != nil || rejected || current.Status != "rejected" {
		t.Fatalf("second reject: candidate=%+v rejected=%t err=%v", current, rejected, err)
	}
}

func newCalendarApprovalCandidate(t *testing.T) (*sqlitestore.Store, sqlitestore.CalendarCandidate, config.Config) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sova.db")
	store, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	source, err := store.UpsertTelegramSource(ctx, sqlitestore.TelegramSource{Ref: "telegram:channel:200", PeerKind: "channel", ChatID: 200, Title: "Study"}, now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, _, err := store.InsertTelegramMessages(ctx, []sqlitestore.TelegramMessage{{SourceID: source.ID, ChatID: 200, MessageID: 1, Date: now, Kind: "message", Text: "Экзамен"}}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	inserted, err := store.InsertCalendarCandidates(ctx, []sqlitestore.CalendarCandidate{{
		RunID: run.ID, ChatID: 200, MessageID: 1, Title: "Экзамен",
		StartAt: now.Add(24 * time.Hour), EndAt: now.Add(25 * time.Hour), Timezone: "Europe/Moscow", Status: "pending",
	}}, now)
	if err != nil || len(inserted) != 1 {
		store.Close()
		t.Fatalf("inserted=%+v err=%v", inserted, err)
	}
	return store, inserted[0], config.Config{DatabasePath: dbPath, Timezone: "Europe/Moscow"}
}

func TestCandidateMessage(t *testing.T) {
	start := time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC)
	message := CandidateMessage(sqlitestore.CalendarCandidate{
		ID:          7,
		Title:       "[ОММ] Экзамен <важно>",
		StartAt:     start,
		EndAt:       start.Add(time.Hour),
		Location:    "504",
		Confidence:  "medium",
		SourceLink:  "https://t.me/c/100/42",
		Description: "Экзамен по ОММ <проверить>",
	}, "Europe/Moscow")
	for _, want := range []string{
		"Кандидат в календарь #7",
		"[ОММ] Экзамен &lt;важно&gt;",
		"<b>Место:</b> 504",
		"<b>Уверенность:</b> medium",
		"https://t.me/c/100/42",
		"<blockquote>Экзамен по ОММ &lt;проверить&gt;</blockquote>",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("candidate message missing %q:\n%s", want, message)
		}
	}
}

func TestShiftedCandidateTimeParsesDateAndDateTime(t *testing.T) {
	start := time.Date(2026, 6, 18, 8, 30, 0, 0, time.UTC)
	candidate := sqlitestore.CalendarCandidate{
		ID:      7,
		StartAt: start,
		EndAt:   start.Add(90 * time.Minute),
	}

	nextStart, nextEnd, err := shiftedCandidateTime(candidate, "2026-06-28", "Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	location := mustLocation("Europe/Moscow")
	if got := nextStart.In(location).Format("2006-01-02 15:04"); got != "2026-06-28 11:30" {
		t.Fatalf("date-only start = %s", got)
	}
	if nextEnd.Sub(nextStart) != 90*time.Minute {
		t.Fatalf("duration = %s", nextEnd.Sub(nextStart))
	}

	nextStart, nextEnd, err = shiftedCandidateTime(candidate, "2026-06-29 14:15", "Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	if got := nextStart.In(location).Format("2006-01-02 15:04"); got != "2026-06-29 14:15" {
		t.Fatalf("date-time start = %s", got)
	}
	if nextEnd.Sub(nextStart) != 90*time.Minute {
		t.Fatalf("duration after date-time = %s", nextEnd.Sub(nextStart))
	}

	if _, _, err := shiftedCandidateTime(candidate, "29.06.2026", "Europe/Moscow"); err == nil {
		t.Fatal("expected invalid format error")
	}
}

func TestDateEditPromptIncludesFormatHint(t *testing.T) {
	prompt := DateEditPrompt(sqlitestore.CalendarCandidate{ID: 5, Title: "Экзамен"})
	for _, want := range []string{"кандидата <code>#5</code>", "2026-06-28", "2026-06-28 11:00"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestUpdateCandidateDatePersistsDateChange(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sova.db")
	store, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.UpsertTelegramSource(ctx, sqlitestore.TelegramSource{
		Ref: "telegram:channel:100", PeerKind: "channel", ChatID: 100, Title: "Study",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertTelegramMessages(ctx, []sqlitestore.TelegramMessage{{
		SourceID: source.ID, ChatID: 100, MessageID: 42, Date: now,
		Kind: "message", Text: "Экзамен", SourceLink: "https://t.me/c/100/42",
	}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 18, 8, 30, 0, 0, time.UTC)
	inserted, err := store.InsertCalendarCandidates(ctx, []sqlitestore.CalendarCandidate{{
		RunID: run.ID, ChatID: 100, MessageID: 42, SourceLink: "https://t.me/c/100/42",
		Title: "Экзамен", StartAt: start, EndAt: start.Add(time.Hour),
		Timezone: "Europe/Moscow", Status: "pending",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	cfg := config.Config{DatabasePath: dbPath, Timezone: "Europe/Moscow"}
	updated, text, err := UpdateCandidateDate(ctx, cfg, inserted[0].ID, "2026-06-29 14:15", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.StartAt.In(mustLocation("Europe/Moscow")).Format("2006-01-02 15:04"); got != "2026-06-29 14:15" {
		t.Fatalf("updated start = %s", got)
	}
	if updated.EndAt.Sub(updated.StartAt) != time.Hour {
		t.Fatalf("duration = %s", updated.EndAt.Sub(updated.StartAt))
	}
	if !strings.Contains(text, "Дата обновлена") {
		t.Fatalf("response text = %s", text)
	}
}
