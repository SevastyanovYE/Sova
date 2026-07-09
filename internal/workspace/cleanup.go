package workspace

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type CleanupTestTasksOptions struct {
	Execute       bool
	Terms         []string
	DeleteBacklog bool
	Limit         int
	Now           time.Time
}

type CleanupTestTasksResult struct {
	Execute          bool
	MatchedTasks     int
	DeletedCards     int
	CancelledTasks   int
	DeletedBacklog   bool
	BacklogMessageID int
	Items            []CleanupTestTaskItem
}

type CleanupTestTaskItem struct {
	TaskID        int64
	Text          string
	Status        string
	CardMessageID int
	Action        string
	Error         string
}

func CleanupTestTasks(ctx context.Context, cfg config.Config, store *sqlitestore.Store, opts CleanupTestTasksOptions) (CleanupTestTasksResult, error) {
	if !cfg.WorkspaceConfigured() {
		return CleanupTestTasksResult{}, fmt.Errorf("workspace group is not fully configured")
	}
	if store == nil {
		return CleanupTestTasksResult{}, fmt.Errorf("store is required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	terms := cleanupTerms(opts.Terms)
	tasks, err := store.WorkspaceTasksContaining(ctx, terms, opts.Limit)
	if err != nil {
		return CleanupTestTasksResult{}, err
	}
	client := nest.New(cfg.Workspace.BotToken)
	result := CleanupTestTasksResult{Execute: opts.Execute, MatchedTasks: len(tasks)}
	for _, task := range tasks {
		item := CleanupTestTaskItem{
			TaskID:        task.ID,
			Text:          task.Text,
			Status:        task.Status,
			CardMessageID: task.CardMessageID,
			Action:        "dry_run",
		}
		if opts.Execute {
			cardDeleted := false
			if task.CardChatID != 0 && task.CardMessageID != 0 {
				if err := client.DeleteMessage(ctx, task.CardChatID, task.CardMessageID); err != nil {
					item.Action = "delete_failed"
					item.Error = err.Error()
					result.Items = append(result.Items, item)
					continue
				}
				result.DeletedCards++
				cardDeleted = true
			}
			if task.Status != "cancelled" && task.Status != "done" {
				if err := store.UpdateWorkspaceTaskStatus(ctx, task.ID, "cancelled", nil, now); err != nil {
					item.Action = "cancel_failed"
					item.Error = err.Error()
					result.Items = append(result.Items, item)
					continue
				}
				result.CancelledTasks++
				if cardDeleted {
					item.Action = "deleted_card_cancelled_task"
				} else {
					item.Action = "cancelled_task"
				}
			} else if cardDeleted {
				item.Action = "deleted_card_status_kept"
			} else {
				item.Action = "already_terminal"
			}
		}
		result.Items = append(result.Items, item)
	}
	if opts.DeleteBacklog {
		messageID, ok, err := store.WorkspaceTopicIndexMessage(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks, taskBacklogIndexKey)
		if err != nil {
			return CleanupTestTasksResult{}, err
		}
		if ok {
			result.BacklogMessageID = messageID
			if opts.Execute {
				if err := client.DeleteMessage(ctx, cfg.Workspace.ChatID, messageID); err != nil {
					return CleanupTestTasksResult{}, fmt.Errorf("delete task backlog message %d: %w", messageID, err)
				}
				if err := store.DeleteWorkspaceTopicIndex(ctx, cfg.Workspace.ChatID, cfg.Workspace.Topics.Tasks, taskBacklogIndexKey); err != nil {
					return CleanupTestTasksResult{}, err
				}
				result.DeletedBacklog = true
			}
		}
	}
	return result, nil
}

func cleanupTerms(terms []string) []string {
	var out []string
	for _, term := range terms {
		for _, part := range strings.Split(term, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	if len(out) == 0 {
		return []string{"Провер", "Проверочная", "тест"}
	}
	return out
}
