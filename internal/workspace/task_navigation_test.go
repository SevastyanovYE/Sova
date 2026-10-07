package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestTaskNavigationUpdatesExistingIndexAndClearsLastLink(t *testing.T) {
	store, first, _, now := openReminderWorkspaceTest(t)
	ctx := context.Background()
	cfg := testWorkspaceLiveConfig()
	if err := store.UpdateWorkspaceTaskStatus(ctx, first.ID, "open", nil, now); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateWorkspaceTask(ctx, sqlitestore.WorkspaceTask{
		SourceChatID: cfg.Workspace.ChatID, SourceMessageID: 112, Text: "Следующая",
		Status: "open", CardChatID: cfg.Workspace.ChatID, CardTopicID: cfg.Workspace.Topics.Tasks, CardMessageID: 212,
	}, now.Add(-time.Hour)) // Creation time must not override original card order.
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeTaskReminderTelegram{}
	if err := refreshExistingTaskBacklog(ctx, cfg, store, client, now); err != nil {
		t.Fatal(err)
	}
	if len(client.sends) != 0 || len(client.edits) != 0 {
		t.Fatal("created an index without an existing tracked pin")
	}
	if err := store.UpsertWorkspaceTopicIndex(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks, taskBacklogIndexKey, 500, now); err != nil {
		t.Fatal(err)
	}
	if err := refreshExistingTaskBacklog(ctx, cfg, store, client, now); err != nil {
		t.Fatal(err)
	}
	if got := client.edits[0]; got.MessageID != 500 || !strings.Contains(got.Text, `/10/211">Самая давняя задача</a>`) {
		t.Fatalf("index = %+v", got)
	}
	if err := store.UpdateWorkspaceTaskStatus(ctx, first.ID, "done", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := maintainWorkspaceTasks(ctx, cfg, store, client, now); err != nil {
		t.Fatal(err)
	}
	if got := client.edits[len(client.edits)-1]; got.MessageID != 500 || !strings.Contains(got.Text, `/10/212">Самая давняя задача</a>`) {
		t.Fatalf("next index = %+v", got)
	}
	if err := store.UpdateWorkspaceTaskStatus(ctx, second.ID, "cancelled", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := maintainWorkspaceTasks(ctx, cfg, store, client, now); err != nil {
		t.Fatal(err)
	}
	if got := client.edits[len(client.edits)-1]; strings.Contains(got.Text, "Самая давняя задача</a>") || !strings.Contains(got.Text, "Открытых задач пока нет") {
		t.Fatalf("empty index = %+v", got)
	}
	if len(client.sends) != 0 {
		t.Fatal("replaced existing index")
	}
}

func TestTaskNavigationSkipsDeferredUntilReminderReopensTask(t *testing.T) {
	store, first, _, now := openReminderWorkspaceTest(t)
	ctx := context.Background()
	cfg := testWorkspaceLiveConfig()
	text, err := renderTaskBacklog(ctx, cfg, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Самая давняя задача</a>") || !strings.Contains(text, "Открытых задач пока нет") || !strings.Contains(text, first.Text) {
		t.Fatalf("deferred-only index = %s", text)
	}
	_, err = store.CreateWorkspaceTask(ctx, sqlitestore.WorkspaceTask{
		SourceChatID: cfg.Workspace.ChatID, SourceMessageID: 112, Text: "Открытая задача",
		Status: "open", CardChatID: cfg.Workspace.ChatID, CardTopicID: cfg.Workspace.Topics.Tasks, CardMessageID: 212,
	}, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	text, err = renderTaskBacklog(ctx, cfg, store, now)
	if err != nil || !strings.Contains(text, `/10/212">Самая давняя задача</a>`) {
		t.Fatalf("open-only navigation = %s, err=%v", text, err)
	}
	if err := processWorkspaceTaskReminders(ctx, cfg, store, &fakeTaskReminderTelegram{}, now); err != nil {
		t.Fatal(err)
	}
	text, err = renderTaskBacklog(ctx, cfg, store, now)
	if err != nil || !strings.Contains(text, `/10/211">Самая давняя задача</a>`) {
		t.Fatalf("reopened original card = %s, err=%v", text, err)
	}
}

func TestTaskNavigationReservesTelegramLengthBudget(t *testing.T) {
	var tasks []sqlitestore.WorkspaceTask
	for i := 0; i < 100; i++ {
		tasks = append(tasks, sqlitestore.WorkspaceTask{Text: strings.Repeat("🙂", 90), Status: "deferred", CardChatID: -1004301779750, CardTopicID: 10, CardMessageID: 211 + i})
	}
	prefix := `<a href="https://t.me/c/4301779750/10/211">Самая давняя задача</a>` + "\n\n"
	text := formatTaskBacklogWithPrefix(tasks, time.UTC, time.Now(), prefix)
	if !strings.HasPrefix(text, prefix) || !telegramHTMLFits(text, workspaceTelegramSafeTextLimit) || !strings.Contains(text, "Показаны") {
		t.Fatalf("invalid bounded navigation: %s", text)
	}
}
