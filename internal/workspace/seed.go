package workspace

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
)

type SeedTopicPinsOptions struct {
	DryRun bool
	Now    time.Time
}

type SeedTopicPinsResult struct {
	DryRun bool
	Items  []SeedTopicPinItem
}

type SeedTopicPinItem struct {
	Topic     string
	TopicID   int
	MessageID int
	Status    string
	Text      string
}

func SeedWorkspaceTopicPins(ctx context.Context, cfg config.Config, opts SeedTopicPinsOptions) (SeedTopicPinsResult, error) {
	if !cfg.WorkspaceConfigured() {
		return SeedTopicPinsResult{}, fmt.Errorf("workspace group is not fully configured")
	}
	result := SeedTopicPinsResult{DryRun: opts.DryRun}
	client := nest.New(cfg.Workspace.BotToken)
	for _, draft := range TopicPinDrafts() {
		topicID := workspaceTopicID(cfg, draft.Topic)
		if topicID == 0 {
			return SeedTopicPinsResult{}, fmt.Errorf("workspace topic %q is not configured", draft.Topic)
		}
		item := SeedTopicPinItem{
			Topic:   draft.Topic,
			TopicID: topicID,
			Text:    TopicPinMessageText(draft),
			Status:  "dry_run",
		}
		if !opts.DryRun {
			message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
				ChatID:          cfg.Workspace.ChatID,
				MessageThreadID: topicID,
				Text:            item.Text,
			})
			if err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("send topic pin draft to %s: %w", draft.Topic, err)
			}
			item.MessageID = message.MessageID
			item.Status = "sent"
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func TopicPinMessageText(draft TopicPinDraft) string {
	topic := strings.TrimSpace(draft.Topic)
	text := strings.TrimSpace(draft.Text)
	if topic == "" {
		return text
	}
	if text == "" {
		return "Закреп: " + topic
	}
	return "Закреп: " + topic + "\n\n" + text
}

func workspaceTopicID(cfg config.Config, topic string) int {
	switch strings.TrimSpace(topic) {
	case "Inbox":
		return cfg.Workspace.Topics.Inbox
	case "Задачи":
		return cfg.Workspace.Topics.Tasks
	case "Заметки":
		return cfg.Workspace.Topics.Notes
	case "Опыт":
		return cfg.Workspace.Topics.Experience
	case "Полезное":
		return cfg.Workspace.Topics.Useful
	case "Заготовки":
		return cfg.Workspace.Topics.Templates
	case "Коллекции":
		return cfg.Workspace.Topics.Collections
	default:
		return 0
	}
}
