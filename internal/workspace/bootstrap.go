package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
	"github.com/SevastyanovYE/Sova/internal/telegrammt"
)

const (
	DefaultWorkspaceTitle = "InSync v1.0"
	DefaultControlTitle   = "Sova.Control"
)

type TopicSpec struct {
	EnvKey string
	Name   string
}

type BootstrapOptions struct {
	WorkspaceTitle string
	ControlTitle   string
	OutputPath     string
	DryRun         bool
	Now            time.Time
}

type BootstrapResult struct {
	OutputPath string
	DryRun     bool
	Groups     []BootstrapGroupResult
}

type BootstrapGroupResult struct {
	Kind         string
	Title        string
	SourceRef    string
	BotAPIChatID int64
	Topics       []BootstrapTopicResult
}

type BootstrapTopicResult struct {
	EnvKey  string
	Name    string
	TopicID int
	Status  string
}

func WorkspaceTopicSpecs() []TopicSpec {
	return []TopicSpec{
		{EnvKey: "SOVA_WORKSPACE_TOPIC_INBOX_ID", Name: "Inbox"},
		{EnvKey: "SOVA_WORKSPACE_TOPIC_TASKS_ID", Name: "Задачи"},
		{EnvKey: "SOVA_WORKSPACE_TOPIC_NOTES_ID", Name: "Заметки"},
		{EnvKey: "SOVA_WORKSPACE_TOPIC_EXPERIENCE_ID", Name: "Опыт"},
		{EnvKey: "SOVA_WORKSPACE_TOPIC_USEFUL_ID", Name: "Полезное"},
		{EnvKey: "SOVA_WORKSPACE_TOPIC_TEMPLATES_ID", Name: "Заготовки"},
		{EnvKey: "SOVA_WORKSPACE_TOPIC_COLLECTIONS_ID", Name: "Коллекции"},
	}
}

func ControlTopicSpecs() []TopicSpec {
	return []TopicSpec{
		{EnvKey: "SOVA_CONTROL_TOPIC_STATUS_ID", Name: "Status"},
		{EnvKey: "SOVA_CONTROL_TOPIC_ERRORS_ID", Name: "Errors"},
		{EnvKey: "SOVA_CONTROL_TOPIC_RUNS_ID", Name: "Runs"},
		{EnvKey: "SOVA_CONTROL_TOPIC_REVIEW_ID", Name: "Review"},
		{EnvKey: "SOVA_CONTROL_TOPIC_TEST_LAB_ID", Name: "Test Lab"},
		{EnvKey: "SOVA_CONTROL_TOPIC_WORKSPACE_ID", Name: "Workspace"},
		{EnvKey: "SOVA_CONTROL_TOPIC_NEST_ID", Name: "Nest"},
		{EnvKey: "SOVA_CONTROL_TOPIC_IDEAS_ID", Name: "Ideas"},
		{EnvKey: "SOVA_CONTROL_TOPIC_ARCHIVE_ID", Name: "Archive"},
	}
}

func BootstrapTopics(ctx context.Context, cfg config.Config, opts BootstrapOptions) (BootstrapResult, error) {
	if strings.TrimSpace(cfg.Workspace.BotToken) == "" {
		return BootstrapResult{}, fmt.Errorf("SOVA_WORKSPACE_BOT_TOKEN is required")
	}
	if strings.TrimSpace(cfg.Control.BotToken) == "" {
		return BootstrapResult{}, fmt.Errorf("SOVA_CONTROL_BOT_TOKEN is required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	workspaceTitle := strings.TrimSpace(opts.WorkspaceTitle)
	if workspaceTitle == "" {
		workspaceTitle = DefaultWorkspaceTitle
	}
	controlTitle := strings.TrimSpace(opts.ControlTitle)
	if controlTitle == "" {
		controlTitle = DefaultControlTitle
	}

	discoveries, err := telegrammt.New(cfg).DiscoverForumTopicsByTitles(ctx, []string{workspaceTitle, controlTitle}, 100)
	if err != nil {
		return BootstrapResult{}, err
	}
	byTitle := map[string]telegrammt.ForumTopicsByTitle{}
	for _, discovery := range discoveries {
		byTitle[normalizeTopicTitle(discovery.RequestedTitle)] = discovery
	}
	outputPath := strings.TrimSpace(opts.OutputPath)
	if outputPath == "" {
		outputPath = filepath.Join(cfg.StateDir, "artifacts", "workspace", "bootstrap", "workspace_control_topic_ids.env")
	}

	result := BootstrapResult{OutputPath: outputPath, DryRun: opts.DryRun}
	workspaceGroup, err := bootstrapTopicGroup(ctx, "workspace", byTitle[normalizeTopicTitle(workspaceTitle)], cfg.Workspace.BotToken, WorkspaceTopicSpecs(), opts.DryRun)
	if err != nil {
		return BootstrapResult{}, err
	}
	controlGroup, err := bootstrapTopicGroup(ctx, "control", byTitle[normalizeTopicTitle(controlTitle)], cfg.Control.BotToken, ControlTopicSpecs(), opts.DryRun)
	if err != nil {
		return BootstrapResult{}, err
	}
	result.Groups = []BootstrapGroupResult{workspaceGroup, controlGroup}
	if err := writeBootstrapEnv(outputPath, now, result); err != nil {
		return BootstrapResult{}, err
	}
	return result, nil
}

func bootstrapTopicGroup(ctx context.Context, kind string, discovery telegrammt.ForumTopicsByTitle, botToken string, specs []TopicSpec, dryRun bool) (BootstrapGroupResult, error) {
	if discovery.BotAPIChatID == 0 {
		return BootstrapGroupResult{}, fmt.Errorf("%s target chat was not resolved", kind)
	}
	client := nest.New(botToken)
	chat, err := client.GetChat(ctx, discovery.BotAPIChatID)
	if err != nil {
		return BootstrapGroupResult{}, fmt.Errorf("check %s bot access to %s: %w", kind, discovery.Source.Title, err)
	}
	if !chat.IsForum {
		return BootstrapGroupResult{}, fmt.Errorf("%s target %s is not reported as a forum supergroup by Bot API", kind, discovery.Source.Title)
	}
	existing := topicMap(discovery.Topics)
	group := BootstrapGroupResult{
		Kind:         kind,
		Title:        discovery.Source.Title,
		SourceRef:    discovery.Source.Ref,
		BotAPIChatID: discovery.BotAPIChatID,
		Topics:       make([]BootstrapTopicResult, 0, len(specs)),
	}
	for _, spec := range specs {
		if topic, ok := existing[normalizeTopicTitle(spec.Name)]; ok {
			group.Topics = append(group.Topics, BootstrapTopicResult{
				EnvKey: spec.EnvKey, Name: spec.Name, TopicID: topic.TopicID, Status: "existing",
			})
			continue
		}
		if dryRun {
			group.Topics = append(group.Topics, BootstrapTopicResult{
				EnvKey: spec.EnvKey, Name: spec.Name, Status: "missing",
			})
			continue
		}
		created, err := client.CreateForumTopic(ctx, nest.CreateForumTopicRequest{
			ChatID: discovery.BotAPIChatID,
			Name:   spec.Name,
		})
		if err != nil {
			return BootstrapGroupResult{}, fmt.Errorf("create %s topic %q in %s: %w", kind, spec.Name, discovery.Source.Title, err)
		}
		group.Topics = append(group.Topics, BootstrapTopicResult{
			EnvKey: spec.EnvKey, Name: spec.Name, TopicID: created.MessageThreadID, Status: "created",
		})
	}
	return group, nil
}

func writeBootstrapEnv(path string, now time.Time, result BootstrapResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Generated by `sova workspace bootstrap-topics`.\n")
	b.WriteString("# Contains numeric chat/topic IDs only; copy values into .env.\n")
	fmt.Fprintf(&b, "# Generated at %s\n\n", now.UTC().Format(time.RFC3339))
	for _, group := range result.Groups {
		switch group.Kind {
		case "workspace":
			fmt.Fprintf(&b, "SOVA_WORKSPACE_CHAT_ID=%d\n", group.BotAPIChatID)
		case "control":
			fmt.Fprintf(&b, "SOVA_CONTROL_CHAT_ID=%d\n", group.BotAPIChatID)
		}
		for _, topic := range group.Topics {
			fmt.Fprintf(&b, "# %s: %s\n", topic.Name, topic.Status)
			if topic.TopicID > 0 {
				fmt.Fprintf(&b, "%s=%d\n", topic.EnvKey, topic.TopicID)
			} else {
				fmt.Fprintf(&b, "%s=\n", topic.EnvKey)
			}
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func topicMap(topics []sqlitestore.WorkspaceTopic) map[string]sqlitestore.WorkspaceTopic {
	out := map[string]sqlitestore.WorkspaceTopic{}
	sorted := append([]sqlitestore.WorkspaceTopic(nil), topics...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TopicID < sorted[j].TopicID
	})
	for _, topic := range sorted {
		key := normalizeTopicTitle(topic.Title)
		if key == "" {
			continue
		}
		if _, exists := out[key]; !exists {
			out[key] = topic
		}
	}
	return out
}

func normalizeTopicTitle(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}
