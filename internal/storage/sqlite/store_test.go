package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestOverviewCooldownSharedAcrossTriggers(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(context.Background(), "scheduled", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishOverview(context.Background(), run.ID, "success", "done", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	_, err = store.TryStartOverview(context.Background(), "nest_button", now.Add(14*time.Minute), 15*time.Minute)
	var cooldownErr *CooldownError
	if !errors.As(err, &cooldownErr) {
		t.Fatalf("expected cooldown error, got %v", err)
	}

	if _, err := store.TryStartOverview(context.Background(), "manual", now.Add(15*time.Minute), 15*time.Minute); err != nil {
		t.Fatalf("run at cooldown boundary: %v", err)
	}
}

func TestRejectsConcurrentRun(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC()
	if _, err := store.TryStartOverview(context.Background(), "manual", now, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TryStartOverview(context.Background(), "scheduled", now.Add(time.Hour), 15*time.Minute); !errors.Is(err, ErrRunActive) {
		t.Fatalf("expected active run error, got %v", err)
	}
}

func TestRecoverFailedOverview(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 6, 18, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishOverview(ctx, run.ID, "failed", "codex failed", "codex digest: missing", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverFailedOverview(ctx, run.ID, "recovered", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	recovered, ok, err := store.RunByID(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || recovered.Status != "success" || recovered.Summary != "recovered" || recovered.Error != "" {
		t.Fatalf("recovered run = %+v, ok=%t", recovered, ok)
	}
	if err := store.RecoverFailedOverview(ctx, run.ID, "again", now.Add(3*time.Minute)); err == nil {
		t.Fatal("expected a recovered run to reject a second recovery")
	}
}

func TestTelegramMessagesAreIdempotentCursorAndRecent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref:      "telegram:channel:100",
		PeerKind: "channel",
		ChatID:   100,
		Title:    "Study",
		Username: "studychat",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	messages := []TelegramMessage{
		{
			SourceID:   source.ID,
			ChatID:     100,
			MessageID:  10,
			Date:       time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC),
			Kind:       "message",
			Text:       "deadline tomorrow",
			SourceLink: "https://t.me/studychat/10",
			RawJSON:    `{"message_id":10}`,
		},
		{
			SourceID:   source.ID,
			ChatID:     100,
			MessageID:  12,
			Date:       time.Date(2026, 6, 17, 10, 5, 0, 0, time.UTC),
			Kind:       "message",
			Text:       "room changed",
			SourceLink: "https://t.me/studychat/12",
			RawJSON:    `{"message_id":12}`,
		},
	}

	newMessages, err := store.FilterNewTelegramMessages(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(newMessages) != 2 {
		t.Fatalf("new messages = %d", len(newMessages))
	}
	inserted, total, err := store.InsertTelegramMessages(ctx, newMessages)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 2 || total != 2 {
		t.Fatalf("inserted,total = %d,%d", inserted, total)
	}
	source, err = store.TelegramSourceByRef(ctx, source.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if source.LastMessageID != 12 {
		t.Fatalf("last message id = %d", source.LastMessageID)
	}
	newMessages, err = store.FilterNewTelegramMessages(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(newMessages) != 0 {
		t.Fatalf("duplicate messages reported new: %d", len(newMessages))
	}

	recent, err := store.RecentTelegramMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("recent messages = %d", len(recent))
	}
	if recent[0].SourceTitle != "Study" || recent[0].Text != "room changed" {
		t.Fatalf("recent[0] = %+v", recent[0])
	}
	personal, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref:      "telegram:channel:200",
		PeerKind: "channel",
		ChatID:   200,
		Title:    "Personal",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{{
		SourceID:   personal.ID,
		ChatID:     200,
		MessageID:  20,
		Date:       time.Date(2026, 6, 17, 10, 10, 0, 0, time.UTC),
		Kind:       "message",
		Text:       "personal note",
		SourceLink: "https://t.me/c/200/20",
		RawJSON:    `{"message_id":20}`,
	}}); err != nil {
		t.Fatal(err)
	}
	filteredRecent, err := store.RecentTelegramMessagesBySourceRefs(ctx, []string{source.Ref}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(filteredRecent) != 2 {
		t.Fatalf("filtered recent messages = %d", len(filteredRecent))
	}
	for _, message := range filteredRecent {
		if message.SourceRef != source.Ref {
			t.Fatalf("filtered recent included %q", message.SourceRef)
		}
	}

	olderNewMessage := TelegramMessage{
		SourceID:   source.ID,
		ChatID:     100,
		MessageID:  11,
		Date:       time.Date(2026, 6, 17, 10, 2, 0, 0, time.UTC),
		Kind:       "message",
		Text:       "extra note",
		SourceLink: "https://t.me/studychat/11",
		RawJSON:    `{"message_id":11}`,
	}
	inserted, total, err = store.InsertTelegramMessages(ctx, []TelegramMessage{olderNewMessage})
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 1 || total != 1 {
		t.Fatalf("inserted,total for older new message = %d,%d", inserted, total)
	}
	source, err = store.TelegramSourceByRef(ctx, source.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if source.LastMessageID != 12 {
		t.Fatalf("last message id after older insert = %d", source.LastMessageID)
	}
	minID, maxID, ok, err := store.TelegramMessageIDBounds(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || minID != 10 || maxID != 12 {
		t.Fatalf("message bounds = min:%d max:%d ok:%v", minID, maxID, ok)
	}
}

func TestTelegramMessagesCreatedBetween(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref: "telegram:channel:100", PeerKind: "channel", ChatID: 100, Title: "Study",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Second)
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{{
		SourceID: source.ID, ChatID: 100, MessageID: 90, Date: time.Now().UTC(),
		Kind: "message", Text: "exam", SourceLink: "https://t.me/c/100/90",
	}}); err != nil {
		t.Fatal(err)
	}
	end := time.Now().UTC().Add(time.Second)
	messages, err := store.TelegramMessagesCreatedBetween(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].MessageID != 90 || messages[0].SourceTitle != "Study" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestWorkspaceTopicsAndAuditRecords(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref: "telegram:channel:100", PeerKind: "channel", ChatID: 100, Title: "InSync",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkspaceTopics(ctx, []WorkspaceTopic{{
		SourceRef: source.Ref, ChatID: source.ChatID, TopicID: 10, TopMessageID: 10,
		Title: "Заметки", Pinned: true, CreatedAt: now.Add(-time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	topics, err := store.WorkspaceTopicsBySource(ctx, source.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 || topics[0].Title != "Заметки" || !topics[0].Pinned {
		t.Fatalf("topics = %+v", topics)
	}
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{{
		SourceID: source.ID, ChatID: 100, MessageID: 11, Date: now,
		Kind: "message", Text: "#мюсли draft", SourceLink: "https://t.me/c/100/11",
	}}); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartWorkspaceAudit(ctx, source.Ref, false, ".state/artifacts/workspace/audit/test", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertWorkspaceAuditRecords(ctx, []WorkspaceAuditRecord{{
		RunID: run.ID, SourceRef: source.Ref, ChatID: 100, MessageID: 11,
		SourceTopic: "Заметки", MessageDate: now, MessageLink: "https://t.me/c/100/11",
		ShortSummary: "#мюсли draft", DetectedType: "draft_note", ModelDecision: "review",
		Confidence: "medium", SuggestedTarget: "Заметки", Reason: "test",
	}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWorkspaceAudit(ctx, run.ID, "success", "done", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestSampleTelegramTextMessagesSkipsServiceAndEmpty(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref: "telegram:channel:100", PeerKind: "channel", ChatID: 100, Title: "Study",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 18, 8, 0, 0, 0, time.UTC)
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{
		{SourceID: source.ID, ChatID: 100, MessageID: 1, Date: now, Kind: "message", Text: "exam"},
		{SourceID: source.ID, ChatID: 100, MessageID: 2, Date: now, Kind: "message", Text: ""},
		{SourceID: source.ID, ChatID: 100, MessageID: 3, Date: now, Kind: "service", Text: "joined"},
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := store.SampleTelegramTextMessages(ctx, 10, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].MessageID != 1 {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestInsertMessageDecisions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref:      "telegram:channel:100",
		PeerKind: "channel",
		ChatID:   100,
		Title:    "Study",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{{
		SourceID:   source.ID,
		ChatID:     100,
		MessageID:  42,
		Date:       now,
		Kind:       "message",
		Text:       "exam tomorrow",
		SourceLink: "https://t.me/c/100/42",
		RawJSON:    `{"message_id":42}`,
	}}); err != nil {
		t.Fatal(err)
	}

	err = store.InsertMessageDecisions(ctx, []MessageDecision{{
		RunID:      run.ID,
		ChatID:     100,
		MessageID:  42,
		Keep:       true,
		Importance: 3,
		Reason:     "экзамен",
		Tags:       []string{"exam", "urgent"},
		HasEvent:   true,
		Model:      "qwen3:14b",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertMessageDecisions(ctx, []MessageDecision{{
		RunID: run.ID, ChatID: 100, MessageID: 42, Keep: true, Importance: 3,
		Reason: "экзамен", Tags: []string{"exam", "urgent"}, HasEvent: true, Model: "qwen3:14b",
	}}, now); err != nil {
		t.Fatalf("idempotent decision insert: %v", err)
	}

	var keep, importance, hasEvent int
	var reason, tags, model string
	err = store.db.QueryRowContext(ctx, `
SELECT keep, importance, reason, tags_json, has_event, model
FROM message_decisions
WHERE run_id = ? AND chat_id = ? AND message_id = ?`, run.ID, 100, 42).
		Scan(&keep, &importance, &reason, &tags, &hasEvent, &model)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatal("decision was not inserted")
		}
		t.Fatal(err)
	}
	if keep != 1 || importance != 3 || reason != "экзамен" || tags != `["exam","urgent"]` || hasEvent != 1 || model != "qwen3:14b" {
		t.Fatalf("stored decision = keep:%d importance:%d reason:%q tags:%q event:%d model:%q",
			keep, importance, reason, tags, hasEvent, model)
	}
}

func TestInsertModelCallAndRecent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 6, 18, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertModelCall(ctx, ModelCall{
		RunID:          run.ID,
		Stage:          "qwen_classify",
		BatchIndex:     1,
		InputMessages:  24,
		InputChars:     1800,
		DurationMillis: 1200,
		Success:        true,
		Model:          "qwen3:14b",
	}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertModelCall(ctx, ModelCall{
		RunID:          run.ID,
		Stage:          "qwen_classify",
		BatchIndex:     2,
		InputMessages:  24,
		InputChars:     1700,
		DurationMillis: 75000,
		Success:        false,
		Fallbacks:      24,
		Error:          "deadline exceeded",
		Model:          "qwen3:14b",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	calls, err := store.RecentModelCalls(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if calls[0].BatchIndex != 2 || calls[0].Success || calls[0].Fallbacks != 24 {
		t.Fatalf("latest call = %+v", calls[0])
	}
	if calls[1].BatchIndex != 1 || !calls[1].Success {
		t.Fatalf("older call = %+v", calls[1])
	}
}

func TestCalendarCandidatesLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.UpsertTelegramSource(ctx, TelegramSource{
		Ref:      "telegram:channel:100",
		PeerKind: "channel",
		ChatID:   100,
		Title:    "Study",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertTelegramMessages(ctx, []TelegramMessage{{
		SourceID:   source.ID,
		ChatID:     100,
		MessageID:  50,
		Date:       now,
		Kind:       "message",
		Text:       "exam tomorrow",
		SourceLink: "https://t.me/c/100/50",
		RawJSON:    `{"message_id":50}`,
	}}); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	inserted, err := store.InsertCalendarCandidates(ctx, []CalendarCandidate{{
		RunID:       run.ID,
		ChatID:      100,
		MessageID:   50,
		SourceLink:  "https://t.me/c/100/50",
		Title:       "[ОММ] Экзамен",
		StartAt:     start,
		EndAt:       start.Add(2 * time.Hour),
		Timezone:    "Europe/Moscow",
		Location:    "504",
		Description: "Экзамен",
		Confidence:  "medium",
		Status:      "pending",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted) != 1 || inserted[0].ID == 0 {
		t.Fatalf("inserted = %+v", inserted)
	}
	again, err := store.InsertCalendarCandidates(ctx, []CalendarCandidate{inserted[0]}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("duplicate candidate inserted again: %+v", again)
	}
	shiftedStart := start.Add(24 * time.Hour)
	if err := store.UpdateCalendarCandidateTime(ctx, inserted[0].ID, shiftedStart, shiftedStart.Add(2*time.Hour), now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	shifted, err := store.CalendarCandidateByID(ctx, inserted[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !shifted.StartAt.Equal(shiftedStart) || shifted.EndAt.Sub(shifted.StartAt) != 2*time.Hour {
		t.Fatalf("candidate after time update = %+v", shifted)
	}

	if err := store.UpdateCalendarCandidateStatus(ctx, inserted[0].ID, "created", "google-event-1", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	candidate, err := store.CalendarCandidateByID(ctx, inserted[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != "created" || candidate.CalendarEventID != "google-event-1" {
		t.Fatalf("candidate after update = %+v", candidate)
	}
	if err := store.UpdateCalendarCandidateTime(ctx, inserted[0].ID, start.Add(24*time.Hour), start.Add(25*time.Hour), now.Add(2*time.Minute)); err == nil {
		t.Fatal("expected created candidate time update to be rejected")
	}
	recent, err := store.RecentCalendarCandidates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].ID != inserted[0].ID {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestWorkspaceLiveClustersTasksAndDerivedMessages(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	first := WorkspaceMessage{
		ChatID:     -100200,
		MessageID:  10,
		TopicID:    2,
		FromUserID: 7,
		Date:       now,
		Text:       "#task Сделать аудит закрепа",
		SourceLink: "https://t.me/c/200/2/10",
	}
	if err := store.UpsertWorkspaceMessage(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	cluster, err := store.CreateWorkspaceClusterWithMessage(ctx, first, "primary", now)
	if err != nil {
		t.Fatal(err)
	}
	second := WorkspaceMessage{
		ChatID: -100200, MessageID: 11, TopicID: 2, FromUserID: 7,
		Date: now.Add(time.Minute), MediaType: "photo", Forwarded: true,
		SourceLink: "https://t.me/c/200/2/11",
	}
	if err := store.UpsertWorkspaceMessage(ctx, second, now); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkspaceMessageToCluster(ctx, cluster.ID, second.ChatID, second.MessageID, "part", now); err != nil {
		t.Fatal(err)
	}
	messages, err := store.WorkspaceClusterMessages(ctx, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Message.MessageID != 10 || messages[1].Message.MediaType != "photo" {
		t.Fatalf("cluster messages = %+v", messages)
	}
	tail, ok, err := store.LatestWorkspaceClusterTail(ctx, -100200, 2, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || tail.Cluster.ID != cluster.ID || tail.Message.MessageID != 11 {
		t.Fatalf("tail = %+v ok=%t", tail, ok)
	}

	task, err := store.CreateWorkspaceTask(ctx, WorkspaceTask{
		SourceChatID: first.ChatID, SourceMessageID: first.MessageID,
		SourceLink: first.SourceLink, SourceClusterID: cluster.ID,
		Text: "Сделать аудит закрепа", Emoji: "🟦", Status: "open",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkspaceTaskCard(ctx, task.ID, -100200, 3, 50, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkspaceDerivedMessage(ctx, WorkspaceDerivedMessage{
		SourceChatID: first.ChatID, SourceMessageID: first.MessageID, SourceClusterID: cluster.ID,
		DerivedType: "task_card", DerivedChatID: -100200, DerivedTopicID: 3,
		DerivedMessageID: 50, Status: "active",
	}, now); err != nil {
		t.Fatal(err)
	}
	deferred := now.AddDate(0, 0, 7)
	if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "deferred", &deferred, now); err != nil {
		t.Fatal(err)
	}
	openTask, err := store.CreateWorkspaceTask(ctx, WorkspaceTask{
		SourceChatID: first.ChatID, SourceMessageID: first.MessageID,
		Text: "Открытая проверочная задача", Emoji: "✨", Status: "open",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := store.WorkspaceTasksBySource(ctx, first.ChatID, first.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].CardMessageID != 50 || tasks[0].Status != "deferred" || tasks[0].DeferredUntil == nil || tasks[1].ID != openTask.ID {
		t.Fatalf("tasks = %+v", tasks)
	}
	deferredTasks, err := store.DeferredWorkspaceTasks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deferredTasks) != 1 || deferredTasks[0].ID != task.ID {
		t.Fatalf("deferred tasks = %+v", deferredTasks)
	}
	matchedTasks, err := store.WorkspaceTasksContaining(ctx, []string{"провер"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matchedTasks) != 1 || matchedTasks[0].ID != openTask.ID {
		t.Fatalf("matched tasks = %+v", matchedTasks)
	}
	if affected, err := store.MarkWorkspacePublishedSourceNeedsReview(ctx, first.ChatID, first.MessageID, now); err != nil || affected != 0 {
		t.Fatalf("active derived review mark affected=%d err=%v", affected, err)
	}
	if err := store.UpsertWorkspaceDerivedMessage(ctx, WorkspaceDerivedMessage{
		SourceChatID: first.ChatID, SourceMessageID: first.MessageID, SourceClusterID: cluster.ID,
		DerivedType: "useful_material", DerivedChatID: -100200, DerivedTopicID: 4,
		DerivedMessageID: 60, Status: "published",
	}, now); err != nil {
		t.Fatal(err)
	}
	if affected, err := store.MarkWorkspacePublishedSourceNeedsReview(ctx, first.ChatID, first.MessageID, now); err != nil || affected != 1 {
		t.Fatalf("published derived review mark affected=%d err=%v", affected, err)
	}
}

func TestWorkspaceAttachTrackedReplyMessage(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	target := WorkspaceMessage{ChatID: -100200, MessageID: 10, TopicID: 2, FromUserID: 7, Date: now, Text: "target"}
	if err := store.UpsertWorkspaceMessage(ctx, target, now); err != nil {
		t.Fatal(err)
	}
	cluster, err := store.CreateWorkspaceClusterWithMessage(ctx, target, "primary", now)
	if err != nil {
		t.Fatal(err)
	}
	replyMessage := WorkspaceMessage{
		ChatID:           -100200,
		MessageID:        11,
		TopicID:          2,
		FromUserID:       7,
		Date:             now.Add(time.Minute),
		Text:             "this message replies to another one",
		ReplyToMessageID: 9,
	}
	if err := store.UpsertWorkspaceMessage(ctx, replyMessage, now); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachWorkspaceMessagesToCluster(ctx, cluster.ID, replyMessage.ChatID, []int{replyMessage.MessageID}, now); err != nil {
		t.Fatal(err)
	}
	messages, err := store.WorkspaceClusterMessages(ctx, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Message.MessageID != replyMessage.MessageID || messages[1].Message.ReplyToMessageID != 9 {
		t.Fatalf("cluster messages = %+v", messages)
	}
}

func TestLatestWorkspaceMessageInTopic(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	for _, message := range []WorkspaceMessage{
		{ChatID: -100200, MessageID: 10, TopicID: 12, FromUserID: 7, Date: now, Text: "note source"},
		{ChatID: -100200, MessageID: 11, TopicID: 12, FromUserID: 7, Date: now, Text: "/note new Test"},
		{ChatID: -100200, MessageID: 12, TopicID: 20, FromUserID: 7, Date: now, Text: "collection source"},
	} {
		if err := store.UpsertWorkspaceMessage(ctx, message, now); err != nil {
			t.Fatal(err)
		}
	}
	source, ok, err := store.LatestWorkspaceMessageInTopic(ctx, -100200, 12, 7, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || source.MessageID != 10 {
		t.Fatalf("latest note source = %+v ok=%t", source, ok)
	}
	if _, ok, err := store.LatestWorkspaceMessageInTopic(ctx, -100200, 12, 8, 20); err != nil || ok {
		t.Fatalf("unexpected source for other user ok=%t err=%v", ok, err)
	}
}

func TestWorkspaceDocumentsLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	doc, err := store.CreateWorkspaceDocument(ctx, WorkspaceDocument{
		Type:     "note",
		Status:   "active",
		Title:    "Связки про Workspace",
		Category: "Личное",
	}, WorkspaceDocumentPart{
		Title:           "Начало",
		SourceChatID:    -100200,
		SourceMessageID: 10,
		SourceClusterID: 3,
		SourceLink:      "https://t.me/c/200/12/10",
		Text:            "первая часть",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	part, err := store.AddWorkspaceDocumentPart(ctx, WorkspaceDocumentPart{
		DocumentID:      doc.ID,
		Title:           "Продолжение",
		SourceChatID:    -100200,
		SourceMessageID: 11,
		SourceLink:      "https://t.me/c/200/12/11",
		Text:            "вторая часть",
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if part.PartNo != 2 {
		t.Fatalf("part no = %d", part.PartNo)
	}
	docs, err := store.WorkspaceDocumentsByType(ctx, "note", []string{"active"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].ID != doc.ID || docs[0].Title != "Связки про Workspace" {
		t.Fatalf("docs = %+v", docs)
	}
	parts, err := store.WorkspaceDocumentParts(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0].SourceMessageID != 10 || parts[1].SourceMessageID != 11 {
		t.Fatalf("parts = %+v", parts)
	}
	sourceParts, err := store.WorkspaceDocumentPartsBySource(ctx, -100200, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceParts) != 1 || sourceParts[0].DocumentID != doc.ID {
		t.Fatalf("source parts = %+v", sourceParts)
	}
	if err := store.UpdateWorkspaceDocumentTitle(ctx, doc.ID, "Связки", now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceDocumentPartTitle(ctx, part.ID, "Вторая часть", now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceDocumentTarget(ctx, doc.ID, -100200, 18, 50, &now, now); err != nil {
		t.Fatal(err)
	}
	byTarget, ok, err := store.WorkspaceDocumentByTargetMessage(ctx, -100200, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || byTarget.ID != doc.ID || byTarget.Title != "Связки" {
		t.Fatalf("target doc = %+v ok=%t", byTarget, ok)
	}
	published, err := store.PublishedWorkspaceDocuments(ctx, "note", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0].TargetMessageID != 50 {
		t.Fatalf("published docs = %+v", published)
	}
	if err := store.DeleteWorkspaceDocumentPart(ctx, part.ID, now); err != nil {
		t.Fatal(err)
	}
	parts, err = store.WorkspaceDocumentParts(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].PartNo != 1 || parts[0].SourceMessageID != 10 {
		t.Fatalf("parts after delete = %+v", parts)
	}
}

func TestWorkspaceDocumentTypesAndPartReordering(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	if _, err := store.UpsertWorkspaceDocumentType(ctx, "template", "Codex", "🧩", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertWorkspaceDocumentType(ctx, "template", "Остальное", "✨", now); err != nil {
		t.Fatal(err)
	}
	types, err := store.WorkspaceDocumentTypes(ctx, "template", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 || types[0].Name != "Codex" || types[1].Name != "Остальное" {
		t.Fatalf("types = %+v", types)
	}
	if err := store.RenameWorkspaceDocumentType(ctx, "template", "Codex", "Работа", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.WorkspaceDocumentTypeByName(ctx, "template", "Работа"); err != nil || !ok {
		t.Fatalf("renamed type ok=%t err=%v", ok, err)
	}

	doc, err := store.CreateWorkspaceDocument(ctx, WorkspaceDocument{
		Type:     "note",
		Status:   "active",
		Title:    "Заметка",
		Category: "",
	}, WorkspaceDocumentPart{
		SourceChatID:    -100200,
		SourceMessageID: 10,
		SourceLink:      "https://t.me/c/200/12/10",
		Text:            "первая",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddWorkspaceDocumentPart(ctx, WorkspaceDocumentPart{
		DocumentID:      doc.ID,
		SourceChatID:    -100200,
		SourceMessageID: 11,
		SourceLink:      "https://t.me/c/200/12/11",
		Text:            "вторая",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	third, err := store.AddWorkspaceDocumentPart(ctx, WorkspaceDocumentPart{
		DocumentID:      doc.ID,
		SourceChatID:    -100200,
		SourceMessageID: 12,
		SourceLink:      "https://t.me/c/200/12/12",
		Text:            "третья",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.WorkspaceDocumentPartByNo(ctx, doc.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWorkspaceDocumentPart(ctx, first.ID, now); err != nil {
		t.Fatal(err)
	}
	updated, err := store.WorkspaceDocumentByID(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SourceMessageID != second.SourceMessageID || updated.SourceLink != second.SourceLink {
		t.Fatalf("primary source after delete = %+v", updated)
	}
	if err := store.ReorderWorkspaceDocumentPart(ctx, doc.ID, 2, 1, now); err != nil {
		t.Fatal(err)
	}
	parts, err := store.WorkspaceDocumentParts(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0].SourceMessageID != third.SourceMessageID || parts[1].SourceMessageID != second.SourceMessageID {
		t.Fatalf("reordered parts = %+v", parts)
	}

	target, err := store.CreateWorkspaceDocument(ctx, WorkspaceDocument{
		Type:   "collection",
		Status: "active",
		Title:  "Цели",
	}, WorkspaceDocumentPart{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MoveWorkspaceDocumentPart(ctx, parts[1].ID, target.ID, now); err != nil {
		t.Fatal(err)
	}
	sourceParts, err := store.WorkspaceDocumentParts(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	targetParts, err := store.WorkspaceDocumentParts(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceParts) != 1 || len(targetParts) != 1 || targetParts[0].PartNo != 1 {
		t.Fatalf("move source=%+v target=%+v", sourceParts, targetParts)
	}
}
