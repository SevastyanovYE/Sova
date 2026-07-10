package workspace

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestRunAuditDryRunClassifiesLegacyMessages(t *testing.T) {
	cfg, store, sourceRef := workspaceAuditFixture(t)
	result, err := RunAudit(context.Background(), cfg, store, AuditOptions{
		DryRun: true,
		Now:    time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Messages != 4 {
		t.Fatalf("messages = %d", result.Messages)
	}
	if result.ReviewCount == 0 {
		t.Fatal("expected review candidates")
	}
	if result.SourceRef != sourceRef {
		t.Fatalf("source ref = %q", result.SourceRef)
	}
	for _, want := range []string{"# Workspace Audit Summary", "`draft_note`", "`collection_item`", "`review`"} {
		if !strings.Contains(result.Summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, result.Summary)
		}
	}
	if result.SummaryPath != "" {
		t.Fatalf("dry-run should not write artifacts: %+v", result)
	}
}

func TestRunAuditWritesArtifacts(t *testing.T) {
	cfg, store, _ := workspaceAuditFixture(t)
	result, err := RunAudit(context.Background(), cfg, store, AuditOptions{
		Now: time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{result.SummaryPath, result.ReviewCSVPath, result.ReviewMDPath, result.ControlCardsPath, result.TopicPinsPath} {
		if path == "" {
			t.Fatalf("empty artifact path in %+v", result)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			t.Fatalf("empty artifact %s", path)
		}
	}
	csvData, err := os.ReadFile(result.ReviewCSVPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(csvData), "user_decision,user_comment") {
		t.Fatalf("review csv missing editable columns:\n%s", string(csvData))
	}
}

func TestResolveLegacySourceAcceptsBotAPIChannelID(t *testing.T) {
	cfg, store, sourceRef := workspaceAuditFixture(t)
	cfg.Workspace.LegacySource = "-100100"
	source, err := ResolveLegacySource(context.Background(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if source.Ref != sourceRef {
		t.Fatalf("source ref = %q, want %q", source.Ref, sourceRef)
	}
}

func TestResolveLegacySourceUsesSingleDiscoveredSourceForInviteLink(t *testing.T) {
	cfg, store, sourceRef := workspaceAuditFixture(t)
	cfg.Workspace.LegacySource = "https://t.me/+privateinvite"
	source, err := ResolveLegacySource(context.Background(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if source.Ref != sourceRef {
		t.Fatalf("source ref = %q, want %q", source.Ref, sourceRef)
	}
}

func TestClassifyMessageAppliesWorkspaceFilteringRules(t *testing.T) {
	topics := indexTopics([]sqlitestore.WorkspaceTopic{
		{SourceRef: "telegram:channel:100", ChatID: 100, TopicID: 2, TopMessageID: 2, Title: "Заметки"},
		{SourceRef: "telegram:channel:100", ChatID: 100, TopicID: 5, TopMessageID: 5, Title: "Задачи"},
	})
	messages := []sqlitestore.WorkspaceSourceMessage{
		{
			SourceRef: "telegram:channel:100", SourceTitle: "InSync", ChatID: 100, MessageID: 50,
			Date: time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC), Kind: "message", Text: "-",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":2}}}`,
		},
		{
			SourceRef: "telegram:channel:100", SourceTitle: "InSync", ChatID: 100, MessageID: 51,
			Date: time.Date(2026, 6, 30, 10, 1, 0, 0, time.UTC), Kind: "message", MediaType: "messageMediaPhoto",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":2}}}`,
		},
		{
			SourceRef: "telegram:channel:100", SourceTitle: "InSync", ChatID: 100, MessageID: 52,
			Date: time.Date(2026, 6, 30, 10, 2, 0, 0, time.UTC), Kind: "message", Text: "Купить лампу",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":5}}}`,
		},
		{
			SourceRef: "telegram:channel:100", SourceTitle: "InSync", ChatID: 100, MessageID: 53,
			Date: time.Date(2026, 6, 30, 10, 3, 0, 0, time.UTC), Kind: "message", Text: "Старая мысль #знания",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":5}}}`,
		},
	}
	ctx := buildClassificationContext(messages, topics)
	punctuation := classifyMessage(messages[0], ctx)
	if punctuation.Record.ModelDecision != DecisionTrash || punctuation.NeedsReview {
		t.Fatalf("punctuation classification = %+v", punctuation)
	}
	media := classifyMessage(messages[1], ctx)
	if media.Record.ModelDecision != DecisionReview || !media.NeedsReview {
		t.Fatalf("media classification = %+v", media)
	}
	task := classifyMessage(messages[2], ctx)
	if task.Record.ModelDecision != DecisionTake || task.NeedsReview {
		t.Fatalf("latest task classification = %+v", task)
	}
	tagged := classifyMessage(messages[3], ctx)
	if tagged.Record.ModelDecision != DecisionTake || tagged.Record.SuggestedTarget != "Полезное" || tagged.NeedsReview {
		t.Fatalf("tagged classification = %+v", tagged)
	}
}

func TestBuildReviewPreviewMergesUserDecisions(t *testing.T) {
	cfg, store, _ := workspaceAuditFixture(t)
	audit, err := RunAudit(context.Background(), cfg, store, AuditOptions{
		Now: time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	fillReviewDecisions(t, audit.ReviewCSVPath, "archive")
	preview, err := BuildReviewPreview(context.Background(), cfg, store, ReviewPreviewOptions{
		ReviewCSVPath: audit.ReviewCSVPath,
		Now:           time.Date(2026, 6, 30, 13, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.PendingDecisions != 0 || !preview.NeedsApproval {
		t.Fatalf("preview approval state = %+v", preview)
	}
	if preview.MigrationItems != 3 {
		t.Fatalf("migration items = %d, want deterministic recipe, tagged note, plus latest task", preview.MigrationItems)
	}
	data, err := os.ReadFile(preview.PreviewPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"# Workspace Migration Preview", "`ready_for_user_approval`", "Коллекции"} {
		if !strings.Contains(content, want) {
			t.Fatalf("preview missing %q:\n%s", want, content)
		}
	}
}

func TestManualTakeUsesWorkspaceTarget(t *testing.T) {
	record := sqlitestore.WorkspaceAuditRecord{
		DetectedType:    TypeExternalBranchReference,
		ModelDecision:   DecisionArchive,
		SuggestedTarget: "Legacy archive",
		SourceTopic:     "Учёба",
		MessageDate:     time.Date(2026, 7, 7, 8, 0, 0, 0, time.UTC),
		MessageLink:     "https://t.me/c/100/42",
		ShortSummary:    "fresh message",
		Confidence:      "high",
		Reason:          "legacy archive by heuristic",
		MediaType:       "",
	}
	item := mergePreviewItem(record, reviewRow{UserDecision: "take"}, true)
	if item.FinalAction != "migrate" {
		t.Fatalf("final action = %q, want migrate", item.FinalAction)
	}
	if item.Target != "Inbox" {
		t.Fatalf("manual take target = %q, want Inbox", item.Target)
	}
}

func TestReadReviewRowsAcceptsNumbersSemicolonCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.csv")
	data := strings.Join([]string{
		"source_topic;message_date;message_link;short_summary;detected_type;model_decision;confidence;suggested_target;reason;media_type;user_decision;user_comment",
		"Заметки;2026-07-07T08:00:00Z;https://t.me/c/100/42;summary;draft_note;review;low;Заметки;reason;;take;ok",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := readReviewRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].UserDecision != "take" || rows[0].MessageLink != "https://t.me/c/100/42" {
		t.Fatalf("unexpected row = %+v", rows[0])
	}
}

func TestBuildReviewPreviewRejectsDuplicateReviewRows(t *testing.T) {
	cfg, store, _ := workspaceAuditFixture(t)
	audit, err := RunAudit(context.Background(), cfg, store, AuditOptions{
		Now: time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicatePath := filepath.Join(t.TempDir(), "review_duplicate.csv")
	data, err := os.ReadFile(audit.ReviewCSVPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected review rows in %s", audit.ReviewCSVPath)
	}
	content := strings.Join(append(lines, lines[1]), "\n") + "\n"
	if err := os.WriteFile(duplicatePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = BuildReviewPreview(context.Background(), cfg, store, ReviewPreviewOptions{
		ReviewCSVPath: duplicatePath,
		Now:           time.Date(2026, 6, 30, 13, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate row") {
		t.Fatalf("expected duplicate row error, got %v", err)
	}
}

func TestTopicPinDraftsMapToConfiguredTopics(t *testing.T) {
	cfg := config.Config{
		Workspace: config.WorkspaceConfig{
			Topics: config.WorkspaceTopicIDs{
				Inbox: 1, Tasks: 2, Notes: 3, Experience: 4,
				Useful: 5, Templates: 6, Collections: 7,
			},
		},
	}
	for _, draft := range TopicPinDrafts() {
		if workspaceTopicID(cfg, draft.Topic) == 0 {
			t.Fatalf("topic %q has no configured id mapping", draft.Topic)
		}
		text := TopicPinMessageText(draft)
		if strings.Contains(text, "Закреп:") {
			t.Fatalf("pin text for %q still has old prefix: %q", draft.Topic, text)
		}
		if !strings.Contains(text, draft.Topic) {
			t.Fatalf("pin text for %q missing heading: %q", draft.Topic, text)
		}
	}
}

func TestWorkspaceCommandHelpSkipsTopicsWithoutCommands(t *testing.T) {
	cfg := config.Config{
		Workspace: config.WorkspaceConfig{
			Topics: config.WorkspaceTopicIDs{
				Inbox: 1, Tasks: 2, Notes: 3, Experience: 4,
				Useful: 5, Templates: 6, Collections: 7,
			},
		},
	}
	var topics []string
	for _, draft := range WorkspaceCommandHelpDrafts(cfg) {
		topics = append(topics, draft.Topic)
	}
	joined := strings.Join(topics, ",")
	for _, forbidden := range []string{"Опыт", "Полезное"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("command help includes %s: %v", forbidden, topics)
		}
	}
	for _, required := range []string{"Inbox", "Задачи", "Заметки", "Заготовки", "Коллекции"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("command help missing %s: %v", required, topics)
		}
	}
}

func TestPreparePinnedMigrationReviewBuildsFocusedArtifacts(t *testing.T) {
	stateDir := t.TempDir()
	cfg := config.Config{
		StateDir:     stateDir,
		DatabasePath: filepath.Join(stateDir, "sova.db"),
		Workspace: config.WorkspaceConfig{
			LegacySource: "telegram:channel:100",
		},
	}
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	source, err := store.UpsertTelegramSource(ctx, sqlitestore.TelegramSource{
		Ref: "telegram:channel:100", PeerKind: "channel", ChatID: 100, Title: "Old InSync",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkspaceTopics(ctx, []sqlitestore.WorkspaceTopic{
		{SourceRef: source.Ref, ChatID: source.ChatID, TopicID: 10, TopMessageID: 10, Title: "Заметки"},
		{SourceRef: source.Ref, ChatID: source.ChatID, TopicID: 17, TopMessageID: 17, Title: "Заготовки"},
		{SourceRef: source.Ref, ChatID: source.ChatID, TopicID: 18, TopMessageID: 18, Title: "Полезное"},
	}, now); err != nil {
		t.Fatal(err)
	}
	messages := []sqlitestore.TelegramMessage{
		{
			SourceID: source.ID, ChatID: 100, MessageID: 101, Date: now,
			Kind: "message", Text: "https://t.me/c/100/17/201", SourceLink: "https://t.me/c/100/17/101",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":17},"Pinned":true}}`,
		},
		{
			SourceID: source.ID, ChatID: 100, MessageID: 201, Date: now.Add(time.Minute),
			Kind: "message", Text: "1. Alpha prompt\nBody A\n\n2. Beta prompt\nBody B", SourceLink: "https://t.me/c/100/17/201",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":17}}}`,
		},
		{
			SourceID: source.ID, ChatID: 100, MessageID: 301, Date: now.Add(2 * time.Minute),
			Kind: "message", Text: "https://t.me/c/100/18/302", SourceLink: "https://t.me/c/100/18/301",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":18},"Pinned":true}}`,
		},
		{
			SourceID: source.ID, ChatID: 100, MessageID: 302, Date: now.Add(3 * time.Minute),
			Kind: "message", Text: "Готовая полезная заметка", SourceLink: "https://t.me/c/100/18/302",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":18}}}`,
		},
		{
			SourceID: source.ID, ChatID: 100, MessageID: 401, Date: now.Add(4 * time.Minute),
			Kind: "message", Text: "https://t.me/c/100/10/402", SourceLink: "https://t.me/c/100/10/401",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":10},"Pinned":true}}`,
		},
		{
			SourceID: source.ID, ChatID: 100, MessageID: 402, Date: now.Add(5 * time.Minute),
			Kind: "message", Text: "Личный вывод #опыт", SourceLink: "https://t.me/c/100/10/402",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":10}}}`,
		},
	}
	if _, _, err := store.InsertTelegramMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}

	result, err := PreparePinnedMigrationReview(ctx, cfg, store, PinnedMigrationOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items != 4 {
		t.Fatalf("items = %d, want 4", result.Items)
	}
	csvData, err := os.ReadFile(result.ReviewCSV)
	if err != nil {
		t.Fatal(err)
	}
	content := string(csvData)
	for _, want := range []string{"legacy_topic,source_message_link,source_message_ids,cluster_id", "Alpha prompt", "Beta prompt", "Полезное", "Опыт"} {
		if !strings.Contains(content, want) {
			t.Fatalf("csv missing %q:\n%s", want, content)
		}
	}
	mdData, err := os.ReadFile(result.ReviewMD)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mdData), "# Pinned Migration Review") {
		t.Fatalf("markdown missing title:\n%s", string(mdData))
	}
}

func TestApplyPinnedMigrationReviewDryRunUsesUserComments(t *testing.T) {
	cfg, store, _ := workspaceAuditFixture(t)
	path := filepath.Join(t.TempDir(), "review.csv")
	data := strings.Join([]string{
		"legacy_topic,source_message_link,source_message_ids,cluster_id,short_title,short_summary,detected_type,suggested_target_topic,confidence,reason,needs_user_review,review_status,user_comment",
		"Заметки,https://t.me/c/100/11,11,legacy-note,Raw note,summary,draft_note,Заметки,medium,reason,true,pending_review,archive",
		"Полезное,https://t.me/c/100/21,21,legacy-useful,Useful,summary,useful_material,Полезное,high,reason,true,pending_review,publish",
		"Заметки,https://t.me/c/100/31,31,legacy-target,Taskish,summary,draft_note,Заметки,medium,reason,true,pending_review,Заметки",
		"Заметки,https://t.me/c/100/40,40,legacy-pending,Pending,summary,draft_note,Заметки,medium,reason,true,pending_review,",
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyPinnedMigrationReview(context.Background(), cfg, store, PinnedMigrationApplyOptions{
		ReviewCSVPath: path,
		Now:           time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Rows != 4 || result.Archived != 1 || result.Planned != 3 || result.Pending != 0 || result.Errors != 0 {
		t.Fatalf("result = %+v", result)
	}
	plan, err := os.ReadFile(result.PlanCSV)
	if err != nil {
		t.Fatal(err)
	}
	content := string(plan)
	for _, want := range []string{"archive,Legacy archive,archived", "publish,Полезное,planned", "migrate,Заметки,planned", "Pending,,migrate,Заметки,planned"} {
		if !strings.Contains(content, want) {
			t.Fatalf("plan missing %q:\n%s", want, content)
		}
	}
}

func workspaceAuditFixture(t *testing.T) (config.Config, *sqlitestore.Store, string) {
	t.Helper()
	stateDir := t.TempDir()
	cfg := config.Config{
		StateDir:     stateDir,
		DatabasePath: filepath.Join(stateDir, "sova.db"),
		Timezone:     "Europe/Moscow",
		Workspace: config.WorkspaceConfig{
			LegacySource: "telegram:channel:100",
		},
	}
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	source, err := store.UpsertTelegramSource(ctx, sqlitestore.TelegramSource{
		Ref: "telegram:channel:100", PeerKind: "channel", ChatID: 100, Title: "InSync",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkspaceTopics(ctx, []sqlitestore.WorkspaceTopic{
		{SourceRef: source.Ref, ChatID: source.ChatID, TopicID: 10, TopMessageID: 10, Title: "Заметки"},
		{SourceRef: source.Ref, ChatID: source.ChatID, TopicID: 20, TopMessageID: 20, Title: "Рецепты"},
		{SourceRef: source.Ref, ChatID: source.ChatID, TopicID: 30, TopMessageID: 30, Title: "Задачи"},
	}, now); err != nil {
		t.Fatal(err)
	}
	messages := []sqlitestore.TelegramMessage{
		{
			SourceID: source.ID, ChatID: 100, MessageID: 11, Date: now,
			Kind: "message", Text: "#мюсли raw thought", SourceLink: "https://t.me/c/100/11",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":10},"Pinned":true}}`,
		},
		{
			SourceID: source.ID, ChatID: 100, MessageID: 21, Date: now.Add(time.Minute),
			Kind: "message", Text: "Блины: молоко, яйца, мука", SourceLink: "https://t.me/c/100/21",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":20}}}`,
		},
		{
			SourceID: source.ID, ChatID: 100, MessageID: 31, Date: now.Add(2 * time.Minute),
			Kind: "message", Text: "✅ закрытая задача", SourceLink: "https://t.me/c/100/31",
			RawJSON: `{"tl":{"ReplyTo":{"ReplyToTopID":30}}}`,
		},
		{
			SourceID: source.ID, ChatID: 100, MessageID: 40, Date: now.Add(3 * time.Minute),
			Kind: "message", Text: "", MediaType: "messageMediaPhoto", SourceLink: "https://t.me/c/100/40",
			RawJSON: `{}`,
		},
	}
	if _, _, err := store.InsertTelegramMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}
	return cfg, store, source.Ref
}

func fillReviewDecisions(t *testing.T, path, decision string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(file).ReadAll()
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("expected review rows in %s", path)
	}
	userDecisionColumn := -1
	for i, name := range rows[0] {
		if name == "user_decision" {
			userDecisionColumn = i
			break
		}
	}
	if userDecisionColumn < 0 {
		t.Fatal("missing user_decision column")
	}
	for i := 1; i < len(rows); i++ {
		rows[i][userDecisionColumn] = decision
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := csv.NewWriter(out)
	if err := writer.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
