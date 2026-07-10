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
	DryRun             bool
	Now                time.Time
	IncludeClusterHelp bool
}

type SeedTopicPinsResult struct {
	DryRun bool
	Items  []SeedTopicPinItem
}

type SeedTopicPinItem struct {
	Group     string
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
			Group:   "workspace",
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
				ParseMode:       "HTML",
			})
			if err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("send topic pin draft to %s: %w", draft.Topic, err)
			}
			item.MessageID = message.MessageID
			item.Status = "sent"
		}
		result.Items = append(result.Items, item)
	}
	if opts.IncludeClusterHelp {
		item := SeedTopicPinItem{
			Group:   "workspace",
			Topic:   "Inbox",
			TopicID: cfg.Workspace.Topics.Inbox,
			Text:    ClusterHelpMessageText(),
			Status:  "dry_run",
		}
		if !opts.DryRun {
			message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
				ChatID:          cfg.Workspace.ChatID,
				MessageThreadID: cfg.Workspace.Topics.Inbox,
				Text:            item.Text,
				ParseMode:       "HTML",
			})
			if err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("send cluster help to Inbox: %w", err)
			}
			item.MessageID = message.MessageID
			item.Status = "sent"
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func SeedControlTopicPins(ctx context.Context, cfg config.Config, opts SeedTopicPinsOptions) (SeedTopicPinsResult, error) {
	if !cfg.ControlConfigured() {
		return SeedTopicPinsResult{}, fmt.Errorf("control group is not fully configured")
	}
	result := SeedTopicPinsResult{DryRun: opts.DryRun}
	client := nest.New(cfg.Control.BotToken)
	for _, draft := range ControlTopicPinDrafts() {
		topicID := controlTopicID(cfg, draft.Topic)
		if topicID == 0 {
			return SeedTopicPinsResult{}, fmt.Errorf("control topic %q is not configured", draft.Topic)
		}
		item := SeedTopicPinItem{
			Group:   "control",
			Topic:   draft.Topic,
			TopicID: topicID,
			Text:    TopicPinMessageText(draft),
			Status:  "dry_run",
		}
		if !opts.DryRun {
			message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
				ChatID:          cfg.Control.ChatID,
				MessageThreadID: topicID,
				Text:            item.Text,
				ParseMode:       "HTML",
			})
			if err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("send control topic pin draft to %s: %w", draft.Topic, err)
			}
			item.MessageID = message.MessageID
			item.Status = "sent"
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func SeedWorkspaceCommandHelp(ctx context.Context, cfg config.Config, opts SeedTopicPinsOptions) (SeedTopicPinsResult, error) {
	if !cfg.WorkspaceConfigured() {
		return SeedTopicPinsResult{}, fmt.Errorf("workspace group is not fully configured")
	}
	result := SeedTopicPinsResult{DryRun: opts.DryRun}
	client := nest.New(cfg.Workspace.BotToken)
	for _, draft := range WorkspaceCommandHelpDrafts(cfg) {
		item := SeedTopicPinItem{
			Group:   "workspace_help",
			Topic:   draft.Topic,
			TopicID: draft.TopicID,
			Text:    draft.Text,
			Status:  "dry_run",
		}
		if !opts.DryRun {
			message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
				ChatID:          cfg.Workspace.ChatID,
				MessageThreadID: draft.TopicID,
				Text:            draft.Text,
				ParseMode:       "HTML",
			})
			if err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("send command help to %s: %w", draft.Topic, err)
			}
			item.MessageID = message.MessageID
			item.Status = "sent"
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func WorkspaceCommandHelpDrafts(cfg config.Config) []SeedTopicPinItem {
	return []SeedTopicPinItem{
		{Topic: "Inbox", TopicID: cfg.Workspace.Topics.Inbox, Text: InboxHelpMessageText() + "\n\n" + ClusterHelpMessageText()},
		{Topic: "Задачи", TopicID: cfg.Workspace.Topics.Tasks, Text: TaskHelpMessageText()},
		{Topic: "Заметки", TopicID: cfg.Workspace.Topics.Notes, Text: WorkspaceDocumentHelpText("doc")},
		{Topic: "Заготовки", TopicID: cfg.Workspace.Topics.Templates, Text: WorkspaceDocumentHelpText("template")},
		{Topic: "Коллекции", TopicID: cfg.Workspace.Topics.Collections, Text: WorkspaceDocumentHelpText("collection")},
	}
}

func ResetWorkspaceTopicPins(ctx context.Context, cfg config.Config, opts SeedTopicPinsOptions) (SeedTopicPinsResult, error) {
	if !cfg.WorkspaceConfigured() {
		return SeedTopicPinsResult{}, fmt.Errorf("workspace group is not fully configured")
	}
	result := SeedTopicPinsResult{DryRun: opts.DryRun}
	client := nest.New(cfg.Workspace.BotToken)
	helpByTopicID := map[int]SeedTopicPinItem{}
	for _, help := range WorkspaceCommandHelpDrafts(cfg) {
		if help.TopicID > 0 {
			helpByTopicID[help.TopicID] = help
		}
	}
	for _, draft := range TopicPinDrafts() {
		topicID := workspaceTopicID(cfg, draft.Topic)
		if topicID == 0 {
			return SeedTopicPinsResult{}, fmt.Errorf("workspace topic %q is not configured", draft.Topic)
		}
		unpin := SeedTopicPinItem{
			Group:   "workspace",
			Topic:   draft.Topic,
			TopicID: topicID,
			Status:  "dry_run_unpin",
		}
		if !opts.DryRun {
			if err := client.UnpinAllForumTopicMessages(ctx, cfg.Workspace.ChatID, topicID); err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("unpin workspace topic %s: %w", draft.Topic, err)
			}
			unpin.Status = "unpinned"
		}
		result.Items = append(result.Items, unpin)

		item := SeedTopicPinItem{
			Group:   "workspace",
			Topic:   draft.Topic,
			TopicID: topicID,
			Text:    TopicPinMessageText(draft),
			Status:  "dry_run_pin",
		}
		if !opts.DryRun {
			message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
				ChatID:          cfg.Workspace.ChatID,
				MessageThreadID: topicID,
				Text:            item.Text,
				ParseMode:       "HTML",
			})
			if err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("send workspace pin to %s: %w", draft.Topic, err)
			}
			if err := client.PinChatMessage(ctx, nest.PinChatMessageRequest{
				ChatID:              cfg.Workspace.ChatID,
				MessageID:           message.MessageID,
				DisableNotification: true,
			}); err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("pin workspace topic %s message %d: %w", draft.Topic, message.MessageID, err)
			}
			item.MessageID = message.MessageID
			item.Status = "sent_pinned"
		}
		result.Items = append(result.Items, item)

		help, ok := helpByTopicID[topicID]
		if !ok {
			continue
		}
		help.Group = "workspace_help"
		help.Status = "dry_run_help"
		if !opts.DryRun {
			message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
				ChatID:          cfg.Workspace.ChatID,
				MessageThreadID: topicID,
				Text:            help.Text,
				ParseMode:       "HTML",
			})
			if err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("send command help to %s: %w", help.Topic, err)
			}
			help.MessageID = message.MessageID
			help.Status = "sent"
		}
		result.Items = append(result.Items, help)
	}
	return result, nil
}

func ResetControlTopicPins(ctx context.Context, cfg config.Config, opts SeedTopicPinsOptions) (SeedTopicPinsResult, error) {
	if !cfg.ControlConfigured() {
		return SeedTopicPinsResult{}, fmt.Errorf("control group is not fully configured")
	}
	result := SeedTopicPinsResult{DryRun: opts.DryRun}
	client := nest.New(cfg.Control.BotToken)
	for _, draft := range ControlTopicPinDrafts() {
		topicID := controlTopicID(cfg, draft.Topic)
		if topicID == 0 {
			return SeedTopicPinsResult{}, fmt.Errorf("control topic %q is not configured", draft.Topic)
		}
		unpin := SeedTopicPinItem{
			Group:   "control",
			Topic:   draft.Topic,
			TopicID: topicID,
			Status:  "dry_run_unpin",
		}
		if !opts.DryRun {
			if err := client.UnpinAllForumTopicMessages(ctx, cfg.Control.ChatID, topicID); err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("unpin control topic %s: %w", draft.Topic, err)
			}
			unpin.Status = "unpinned"
		}
		result.Items = append(result.Items, unpin)

		item := SeedTopicPinItem{
			Group:   "control",
			Topic:   draft.Topic,
			TopicID: topicID,
			Text:    TopicPinMessageText(draft),
			Status:  "dry_run_pin",
		}
		if !opts.DryRun {
			message, err := client.SendMessageResult(ctx, nest.SendMessageRequest{
				ChatID:          cfg.Control.ChatID,
				MessageThreadID: topicID,
				Text:            item.Text,
				ParseMode:       "HTML",
			})
			if err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("send control pin to %s: %w", draft.Topic, err)
			}
			if err := client.PinChatMessage(ctx, nest.PinChatMessageRequest{
				ChatID:              cfg.Control.ChatID,
				MessageID:           message.MessageID,
				DisableNotification: true,
			}); err != nil {
				return SeedTopicPinsResult{}, fmt.Errorf("pin control topic %s message %d: %w", draft.Topic, message.MessageID, err)
			}
			item.MessageID = message.MessageID
			item.Status = "sent_pinned"
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
	heading := topicPinHeading(topic)
	if text == "" {
		return heading
	}
	return heading + "\n\n" + text
}

func topicPinHeading(topic string) string {
	emoji := map[string]string{
		"Inbox":     "📥",
		"Задачи":    "✅",
		"Заметки":   "📝",
		"Опыт":      "🌱",
		"Полезное":  "💎",
		"Заготовки": "🧰",
		"Коллекции": "🗂",
		"Status":    "🟢",
		"Errors":    "🚨",
		"Runs":      "🧭",
		"Review":    "🔎",
		"Test Lab":  "🧪",
		"Workspace": "🧱",
		"Nest":      "🌿",
		"Ideas":     "💡",
	}[topic]
	if emoji == "" {
		emoji = "✨"
	}
	return emoji + " <b>" + htmlEscapeTopic(topic) + "</b>"
}

func htmlEscapeTopic(topic string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(topic)
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

func controlTopicID(cfg config.Config, topic string) int {
	switch strings.TrimSpace(topic) {
	case "Status":
		return cfg.Control.Topics.Status
	case "Errors":
		return cfg.Control.Topics.Errors
	case "Runs":
		return cfg.Control.Topics.Runs
	case "Review":
		return cfg.Control.Topics.Review
	case "Test Lab":
		return cfg.Control.Topics.TestLab
	case "Workspace":
		return cfg.Control.Topics.Workspace
	case "Nest":
		return cfg.Control.Topics.Nest
	case "Ideas":
		return cfg.Control.Topics.Ideas
	case "Archive":
		return cfg.Control.Topics.Archive
	default:
		return 0
	}
}
