package workspace

import (
	"context"
	"html"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func renderTaskBacklog(ctx context.Context, cfg config.Config, store *sqlitestore.Store, now time.Time) (string, error) {
	tasks, err := store.DeferredWorkspaceTasks(ctx, 100)
	if err != nil {
		return "", err
	}
	oldest, found, err := store.OldestActiveWorkspaceTask(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks)
	if err != nil {
		return "", err
	}
	text := "<i>Открытых задач пока нет.</i>\n\n"
	if found {
		text = "<a href=\"" + html.EscapeString(taskCardLink(oldest)) + "\">Самая давняя задача</a>\n\n"
	}
	location := mustLocation(cfg.Timezone)
	// Reserve the navigation's UTF-16 budget in the existing bounded renderer.
	return formatTaskBacklogWithPrefix(tasks, location, now.In(location), text), nil
}

func refreshExistingTaskBacklog(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client taskReminderTelegram, now time.Time) error {
	_, exists, err := store.WorkspaceTopicIndexMessage(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks, taskBacklogIndexKey)
	if err != nil || !exists {
		return err
	}
	return updateTaskBacklog(ctx, cfg, store, client, now)
}
