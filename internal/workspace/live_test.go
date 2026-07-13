package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestExtractTaskTexts(t *testing.T) {
	multi := ExtractTaskTexts(`#tasks
Сделать аудит Заметок
- Перенести рецепты
3. Создать Sova.Control`)
	want := []string{"Сделать аудит Заметок", "Перенести рецепты", "Создать Sova.Control"}
	if len(multi) != len(want) {
		t.Fatalf("multi tasks = %#v", multi)
	}
	for i := range want {
		if multi[i] != want[i] {
			t.Fatalf("multi[%d] = %q, want %q", i, multi[i], want[i])
		}
	}
	single := ExtractTaskTexts("Сделать аудит закрепа #task")
	if len(single) != 1 || single[0] != "Сделать аудит закрепа" {
		t.Fatalf("single = %#v", single)
	}
	if got := ExtractTaskTexts("просто заметка"); len(got) != 0 {
		t.Fatalf("unexpected tasks = %#v", got)
	}
}

func TestParseDeferredTaskDate(t *testing.T) {
	location := time.FixedZone("MSK", 3*60*60)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, location)
	tests := []struct {
		input string
		want  time.Time
	}{
		{"08.07", time.Date(2026, 7, 8, 9, 0, 0, 0, location)},
		{"07.07 13:30", time.Date(2026, 7, 7, 13, 30, 0, 0, location)},
		{"01.01", time.Date(2027, 1, 1, 9, 0, 0, 0, location)},
		{"09.07.2026 18:15", time.Date(2026, 7, 9, 18, 15, 0, 0, location)},
		{"2026-07-10", time.Date(2026, 7, 10, 9, 0, 0, 0, location)},
	}
	for _, tt := range tests {
		got, err := ParseDeferredTaskDate(tt.input, now, location)
		if err != nil {
			t.Fatalf("ParseDeferredTaskDate(%q): %v", tt.input, err)
		}
		if !got.Equal(tt.want) {
			t.Fatalf("ParseDeferredTaskDate(%q) = %s, want %s", tt.input, got, tt.want)
		}
	}
	if _, err := ParseDeferredTaskDate("послезавтра", now, location); err == nil {
		t.Fatal("expected unsupported date to fail")
	}
}

func TestDeferPresetUsesProjectMorning(t *testing.T) {
	location := time.FixedZone("MSK", 3*60*60)
	now := time.Date(2026, 7, 7, 21, 1, 0, 0, location)
	week := deferPreset("defer_week", now)
	if week == nil || !week.In(location).Equal(time.Date(2026, 7, 14, 8, 0, 0, 0, location)) {
		t.Fatalf("week preset = %v", week)
	}
	month := deferPreset("defer_month", now)
	if month == nil || !month.In(location).Equal(time.Date(2026, 8, 7, 8, 0, 0, 0, location)) {
		t.Fatalf("month preset = %v", month)
	}
	if none := deferPreset("defer_none", now); none != nil {
		t.Fatalf("none preset = %v", none)
	}
}

func TestTaskDateRenderingKeepsYearWhenDifferent(t *testing.T) {
	location := time.FixedZone("MSK", 3*60*60)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, location)
	if got := formatTaskDateRelative(time.Date(2026, 10, 11, 11, 5, 0, 0, location), now); got != "11.10 11:05" {
		t.Fatalf("same year date = %q", got)
	}
	if got := formatTaskDateRelative(time.Date(2028, 10, 11, 11, 5, 0, 0, location), now); got != "11.10.2028 11:05" {
		t.Fatalf("future year date = %q", got)
	}
	if got := formatTaskDateRelative(time.Date(2028, 10, 11, 9, 0, 0, 0, location), now); got != "11.10.2028" {
		t.Fatalf("future year date-only = %q", got)
	}
}

func TestTaskBacklogShowsOnlyDeferredTasks(t *testing.T) {
	location := time.FixedZone("MSK", 3*60*60)
	deferred := time.Date(2026, 7, 14, 21, 1, 0, 0, location)
	text := formatTaskBacklog([]sqlitestore.WorkspaceTask{
		{Text: "Открытая задача", Emoji: "✨", Status: "open"},
		{Text: "Отложенная задача", Emoji: "🌿", Status: "deferred", DeferredUntil: &deferred, CardChatID: -1004301779750, CardTopicID: 10, CardMessageID: 116},
	}, location, time.Date(2026, 7, 7, 12, 0, 0, 0, location))
	if strings.Contains(text, "Открытая задача") {
		t.Fatalf("backlog included open task:\n%s", text)
	}
	if !strings.Contains(text, `href="https://t.me/c/4301779750/10/116"`) {
		t.Fatalf("backlog missing task card link:\n%s", text)
	}
	if !strings.Contains(text, "Отложенная задача") || !strings.Contains(text, "14.07 21:01") {
		t.Fatalf("backlog missing deferred task:\n%s", text)
	}
}

func TestTaskCallbacksAndRendering(t *testing.T) {
	data := TaskCallbackData("defer_custom", 42)
	action, id, ok := ParseTaskCallback(data)
	if !ok || action != "defer_custom" || id != 42 {
		t.Fatalf("parsed callback = %q %d ok=%t", action, id, ok)
	}
	if _, _, ok := ParseTaskCallback("bad"); ok {
		t.Fatal("invalid callback parsed")
	}
	task := sqlitestore.WorkspaceTask{ID: 42, Text: "Сделать аудит закрепа", Emoji: "🟦", Status: "open"}
	card := FormatTaskCard(task)
	if strings.Contains(card, "Задача") || strings.Contains(card, "http") || !strings.Contains(card, "Сделать аудит закрепа") || !strings.Contains(card, "🟦") {
		t.Fatalf("open card = %q", card)
	}
	task.Status = "cancelled"
	card = FormatTaskCard(task)
	if !strings.Contains(card, "<s>") || !strings.Contains(card, "Отменено") {
		t.Fatalf("cancelled card = %q", card)
	}
	markup := TaskActionMarkup(42)
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 3 {
		t.Fatalf("action markup = %+v", markup)
	}
	deferMarkup := TaskDeferMarkup(42)
	if deferMarkup == nil || len(deferMarkup.InlineKeyboard) != 2 {
		t.Fatalf("defer markup = %+v", deferMarkup)
	}
}

func TestPublishCallbacksAndMockPreview(t *testing.T) {
	data := PublishCallbackData("approve", 7)
	action, id, ok := ParsePublishCallback(data)
	if !ok || action != "approve" || id != 7 {
		t.Fatalf("publish callback = %q %d ok=%t", action, id, ok)
	}
	if _, _, ok := ParsePublishCallback("bad"); ok {
		t.Fatal("invalid publish callback parsed")
	}
	provider := NewNotePublishProvider(config.Config{})
	result, err := provider.FormatNote(context.Background(), NotePublishRequest{
		Title: "Тест",
		Parts: []sqlitestore.WorkspaceDocumentPart{
			{PartNo: 1, Text: "Первая часть"},
			{PartNo: 2, Title: "Продолжение", Text: "Вторая часть"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 || !strings.Contains(result.Messages[0], "<b>Тест</b>") || !strings.Contains(result.Messages[0], "Продолжение") {
		t.Fatalf("preview = %+v", result.Messages)
	}
	if result.Model != "mock" {
		t.Fatalf("model = %q", result.Model)
	}
}

func TestDocumentInputCallbacks(t *testing.T) {
	data := DocumentInputCallbackData("template_new_type", 2)
	action, index, ok := ParseDocumentInputCallback(data)
	if !ok || action != "template_new_type" || index != 2 {
		t.Fatalf("input callback = %q %d ok=%t", action, index, ok)
	}
	if _, _, ok := ParseDocumentInputCallback("bad"); ok {
		t.Fatal("invalid document input callback parsed")
	}
}

func TestPendingWorkspaceInputDoesNotConsumeSlashCommand(t *testing.T) {
	cfg := testWorkspaceLiveConfig()
	pending := map[pendingTaskDateKey]pendingWorkspaceInput{
		{chatID: cfg.Workspace.ChatID, threadID: cfg.Workspace.Topics.Templates, userID: 7}: {
			Kind:       "template_rename",
			DocumentID: 42,
		},
	}
	handled := handlePendingWorkspaceInputMessage(context.Background(), cfg, nil, nil, pending, nil, nest.Message{
		Chat:            nest.Chat{ID: cfg.Workspace.ChatID},
		MessageThreadID: cfg.Workspace.Topics.Templates,
		From:            &nest.User{ID: 7},
		Text:            "/template rename Старое | Новое",
	}, cfg.Workspace.Topics.Templates)
	if handled {
		t.Fatal("slash command was consumed as pending input")
	}
	if len(pending) != 0 {
		t.Fatalf("pending input was not cleared: %+v", pending)
	}
}

func TestWorkspaceDocumentIndexesRenderLinks(t *testing.T) {
	noteDoc := sqlitestore.WorkspaceDocument{ID: 1, Type: "note", Status: "active", Title: "Связки"}
	noteParts := map[int64][]sqlitestore.WorkspaceDocumentPart{1: {
		{PartNo: 1, SourceLink: "https://t.me/c/4301779750/12/10"},
		{PartNo: 2, Title: "Дополнение", SourceLink: "https://t.me/c/4301779750/12/11"},
	}}
	notes := renderNotesIndex([]sqlitestore.WorkspaceDocument{noteDoc}, noteParts)
	if !strings.Contains(notes, "📝 <b>Активные заметки</b>") ||
		!strings.Contains(notes, `<b><a href="https://t.me/c/4301779750/12/10">Связки</a></b>`) ||
		!strings.Contains(notes, `[<a href="https://t.me/c/4301779750/12/11">Дополнение</a>]`) {
		t.Fatalf("notes index = %s", notes)
	}

	templateDoc := sqlitestore.WorkspaceDocument{ID: 2, Type: "template", Status: "active", Title: "Implementation prompt", Category: "Codex"}
	templates := renderTemplatesIndex([]sqlitestore.WorkspaceDocumentType{{DocType: "template", Name: "Codex", Emoji: "🧩"}}, []sqlitestore.WorkspaceDocument{templateDoc}, map[int64][]sqlitestore.WorkspaceDocumentPart{2: {
		{PartNo: 1, Title: "Project context", SourceLink: "https://t.me/c/4301779750/14/20"},
	}})
	if !strings.Contains(templates, "• <b>Codex</b> 🧩") ||
		!strings.Contains(templates, `<b><a href="https://t.me/c/4301779750/14/20">Implementation prompt</a></b>`) {
		t.Fatalf("templates index = %s", templates)
	}

	collectionDoc := sqlitestore.WorkspaceDocument{ID: 3, Type: "collection", Status: "active", Title: "Мюсли", Category: "Домашние рецепты", TargetChatID: -1004301779750, TargetTopicID: 20, TargetMessageID: 40}
	collections := renderCollectionsIndex([]sqlitestore.WorkspaceDocument{collectionDoc}, map[int64][]sqlitestore.WorkspaceDocumentPart{3: {
		{PartNo: 1, SourceLink: "https://t.me/c/4301779750/20/30"},
	}})
	if strings.Contains(collections, "<b>Рецепты</b>") || !strings.Contains(collections, "Мюсли") ||
		!strings.Contains(collections, "Домашние рецепты") ||
		!strings.Contains(collections, `href="https://t.me/c/4301779750/20/40"`) {
		t.Fatalf("collections index = %s", collections)
	}
}

func TestDocumentCommandParsers(t *testing.T) {
	ref, title := parseDocumentRefAndOptionalTitle("12 Новая часть")
	if ref != "12" || title != "Новая часть" {
		t.Fatalf("numeric ref=%q title=%q", ref, title)
	}
	ref, title = parseDocumentRefAndOptionalTitle("Тестовая записка")
	if ref != "Тестовая записка" || title != "" {
		t.Fatalf("title ref=%q title=%q", ref, title)
	}
	ref, title = parseTemplateAppendBody("Карточка личности | Инструкция")
	if ref != "Карточка личности" || title != "Инструкция" {
		t.Fatalf("template ref=%q title=%q", ref, title)
	}
	if category, title, partTitle := parseTemplateNewBody("Codex | Системный промпт | Тело"); category != "Codex" || title != "Системный промпт" || partTitle != "Тело" {
		t.Fatalf("template new category=%q title=%q part=%q", category, title, partTitle)
	}
	if category, _, _ := parseTemplateNewBody("Остальные | Черновик"); category != "Остальное" {
		t.Fatalf("template default category = %q", category)
	}
	ref, title, category := parseCollectionAddBody("Рецепты | Мюсли")
	if ref != "Мюсли" || title != "Мюсли" || category != "Рецепты" {
		t.Fatalf("legacy collection ref=%q title=%q category=%q", ref, title, category)
	}
	ref, title, category = parseCollectionAddBody("Любимое | Ссылка")
	if ref != "Любимое" || title != "Ссылка" || category != "" {
		t.Fatalf("collection ref=%q title=%q category=%q", ref, title, category)
	}
}

func TestResolveUsefulDocumentRef(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	cfg := testWorkspaceLiveConfig()
	publishedAt := now
	doc, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{
		Type:            "note",
		Status:          "published",
		Title:           "Японский",
		TargetChatID:    cfg.Workspace.ChatID,
		TargetTopicID:   cfg.Workspace.Topics.Useful,
		TargetMessageID: 874,
		PublishedAt:     &publishedAt,
	}, sqlitestore.WorkspaceDocumentPart{Title: "Японский", SourceChatID: cfg.Workspace.ChatID, SourceMessageID: 12, SourceLink: "https://t.me/c/4301779750/12/12", Text: "text"}, now)
	if err != nil {
		t.Fatal(err)
	}
	byLink, err := resolveUsefulDocumentRef(ctx, cfg, store, "https://t.me/c/4301779750/18/874")
	if err != nil {
		t.Fatal(err)
	}
	if byLink.ID != doc.ID {
		t.Fatalf("by link = %+v, want doc #%d", byLink, doc.ID)
	}
	byTitle, err := resolveUsefulDocumentRef(ctx, cfg, store, "Японский")
	if err != nil {
		t.Fatal(err)
	}
	if byTitle.ID != doc.ID {
		t.Fatalf("by title = %+v, want doc #%d", byTitle, doc.ID)
	}
	active, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{
		Type:            "note",
		Status:          "active",
		Title:           "Черновик",
		TargetChatID:    cfg.Workspace.ChatID,
		TargetTopicID:   cfg.Workspace.Topics.Useful,
		TargetMessageID: 875,
	}, sqlitestore.WorkspaceDocumentPart{Title: "Черновик", SourceChatID: cfg.Workspace.ChatID, SourceMessageID: 13, Text: "draft"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := resolveUsefulDocumentRef(ctx, cfg, store, strconv.FormatInt(active.ID, 10)); err == nil || resolved.ID == active.ID {
		t.Fatalf("active target doc resolved as useful: doc=%+v err=%v", resolved, err)
	}
}

func TestResolveCollectionPartRefByReplyAndTitle(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	doc, err := store.CreateWorkspaceDocument(ctx, sqlitestore.WorkspaceDocument{
		Type:   "collection",
		Status: "active",
		Title:  "Проверка",
	}, sqlitestore.WorkspaceDocumentPart{
		Title:           "Первая часть",
		SourceChatID:    -1004301779750,
		SourceMessageID: 10,
		SourceLink:      "https://t.me/c/4301779750/20/10",
		Text:            "one",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddWorkspaceDocumentPart(ctx, sqlitestore.WorkspaceDocumentPart{
		DocumentID:      doc.ID,
		Title:           "Вторая часть",
		SourceChatID:    -1004301779750,
		SourceMessageID: 11,
		SourceLink:      "https://t.me/c/4301779750/20/11",
		Text:            "two",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	message := nest.Message{
		Chat: nest.Chat{ID: -1004301779750},
		ReplyToMessage: &nest.Message{
			MessageID: 11,
			Chat:      nest.Chat{ID: -1004301779750},
		},
	}
	byReply, err := resolveCollectionPartRef(ctx, store, message, doc, "")
	if err != nil {
		t.Fatal(err)
	}
	if byReply.ID != second.ID {
		t.Fatalf("reply part = %+v, want %+v", byReply, second)
	}
	byTitle, err := resolveCollectionPartRef(ctx, store, nest.Message{}, doc, "Вторая часть")
	if err != nil {
		t.Fatal(err)
	}
	if byTitle.ID != second.ID {
		t.Fatalf("title part = %+v, want %+v", byTitle, second)
	}
}

func TestWorkspaceDocumentCommandSourceUsesExpectedTopic(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	cfg := testWorkspaceLiveConfig()
	source := sqlitestore.WorkspaceMessage{
		ChatID: -1004301779750, MessageID: 10, TopicID: cfg.Workspace.Topics.Notes,
		FromUserID: 7, Date: now, Text: "текст заметки",
		SourceLink: "https://t.me/c/4301779750/12/10",
	}
	if err := store.UpsertWorkspaceMessage(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	command := nest.Message{
		MessageID:       11,
		MessageThreadID: cfg.Workspace.Topics.Inbox,
		Chat:            nest.Chat{ID: -1004301779750},
		From:            &nest.User{ID: 7},
		Text:            "/note new Тест",
	}
	part, err := workspaceDocumentPartFromCommandSource(ctx, cfg, store, command, "note", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if part.SourceMessageID != 10 || part.SourceLink != source.SourceLink || part.Text != "текст заметки" {
		t.Fatalf("part = %+v", part)
	}
}

func TestWorkspaceDocumentCommandSourceFallsBackToEmbeddedReply(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	cfg := testWorkspaceLiveConfig()
	command := nest.Message{
		MessageID:       21,
		MessageThreadID: cfg.Workspace.Topics.Collections,
		Chat:            nest.Chat{ID: -1004301779750},
		From:            &nest.User{ID: 7},
		Text:            "/collection add Списки | Проверка",
		ReplyToMessage: &nest.Message{
			MessageID: 20,
			Chat:      nest.Chat{ID: -1004301779750},
			From:      &nest.User{ID: 7},
			Date:      now.Unix(),
			Text:      "элемент коллекции",
		},
	}
	part, err := workspaceDocumentPartFromCommandSource(ctx, cfg, store, command, "collection", "Проверка", now)
	if err != nil {
		t.Fatal(err)
	}
	if part.SourceMessageID != 20 || part.SourceLink != "https://t.me/c/4301779750/20/20" {
		t.Fatalf("part = %+v", part)
	}
	stored, ok, err := store.WorkspaceMessageByID(ctx, -1004301779750, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || stored.TopicID != cfg.Workspace.Topics.Collections {
		t.Fatalf("stored = %+v ok=%t", stored, ok)
	}
}

func TestWorkspaceDocumentCommandSourceAllowsBotReply(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC)
	cfg := testWorkspaceLiveConfig()
	command := nest.Message{
		MessageID:       31,
		MessageThreadID: cfg.Workspace.Topics.Collections,
		Chat:            nest.Chat{ID: cfg.Workspace.ChatID},
		From:            &nest.User{ID: 7},
		Text:            "/collection add Рецепты | Шоколадный торт",
		ReplyToMessage: &nest.Message{
			MessageID: 30,
			From:      &nest.User{ID: 100, IsBot: true},
			Date:      now.Unix(),
			Text:      "🍫 Шоколадный торт\n1. Смешать.",
		},
	}
	part, err := workspaceDocumentPartFromCommandSource(ctx, cfg, store, command, "collection", "Шоколадный торт", now)
	if err != nil {
		t.Fatal(err)
	}
	if part.SourceMessageID != 30 || part.SourceClusterID != 0 || part.SourceLink != "https://t.me/c/4301779750/20/30" {
		t.Fatalf("part = %+v", part)
	}
	stored, ok, err := store.WorkspaceMessageByID(ctx, cfg.Workspace.ChatID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !stored.FromIsBot || stored.TopicID != cfg.Workspace.Topics.Collections {
		t.Fatalf("stored = %+v ok=%t", stored, ok)
	}
}

func TestWorkspaceDocumentCommandSourceRejectsEmptyBotReply(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC)
	cfg := testWorkspaceLiveConfig()
	command := nest.Message{
		MessageID:       41,
		MessageThreadID: cfg.Workspace.Topics.Collections,
		Chat:            nest.Chat{ID: cfg.Workspace.ChatID},
		From:            &nest.User{ID: 7},
		Text:            "/collection add Рецепты | Пусто",
		ReplyToMessage: &nest.Message{
			MessageID: 40,
			From:      &nest.User{ID: 100, IsBot: true},
			Date:      now.Unix(),
		},
	}
	if _, err := workspaceDocumentPartFromCommandSource(ctx, cfg, store, command, "collection", "Пусто", now); err == nil ||
		!strings.Contains(err.Error(), "bot source message is empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkspaceDocumentCommandSourceDoesNotUseBotLatestWithoutReply(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC)
	cfg := testWorkspaceLiveConfig()
	if err := store.UpsertWorkspaceMessage(ctx, sqlitestore.WorkspaceMessage{
		ChatID: cfg.Workspace.ChatID, MessageID: 50, TopicID: cfg.Workspace.Topics.Collections,
		FromUserID: 100, FromIsBot: true, Date: now, Text: "bot card",
		SourceLink: "https://t.me/c/4301779750/20/50",
	}, now); err != nil {
		t.Fatal(err)
	}
	command := nest.Message{
		MessageID:       51,
		MessageThreadID: cfg.Workspace.Topics.Inbox,
		Chat:            nest.Chat{ID: cfg.Workspace.ChatID},
		From:            &nest.User{ID: 7},
		Text:            "/collection add Рецепты | Карточка",
	}
	if _, err := workspaceDocumentPartFromCommandSource(ctx, cfg, store, command, "collection", "Карточка", now); err == nil ||
		!strings.Contains(err.Error(), "no source message found") {
		t.Fatalf("err = %v", err)
	}
}

func TestTelegramMessageNotModifiedIsNoop(t *testing.T) {
	if !isTelegramMessageNotModified(errors.New("Bot API editMessageText failed: Bad Request: message is not modified")) {
		t.Fatal("expected message-not-modified to be detected")
	}
}

func TestClusterCommandMessageIDsAcceptLinks(t *testing.T) {
	command := testClusterCommandMessage(78)
	ids := clusterCommandMessageIDs(command, []string{"https://t.me/c/4301779750/8/79", "80"})
	want := []int{78, 79, 80}
	if len(ids) != len(want) {
		t.Fatalf("ids = %#v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids[%d] = %d, want %d", i, ids[i], want[i])
		}
	}
	if id, ok := telegramLinkMessageIDFromArg(-1004301779750, "https://t.me/c/111/8/79"); ok || id != 0 {
		t.Fatalf("mismatched chat parsed as id=%d ok=%t", id, ok)
	}
}

func TestResolveAttachTargetClusterUsesReplyWhenSingleArg(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	message := sqlitestore.WorkspaceMessage{ChatID: -1004301779750, MessageID: 78, TopicID: 8, FromUserID: 7, Date: now, Text: "base"}
	if err := store.UpsertWorkspaceMessage(ctx, message, now); err != nil {
		t.Fatal(err)
	}
	cluster, err := store.CreateWorkspaceClusterWithMessage(ctx, message, "primary", now)
	if err != nil {
		t.Fatal(err)
	}
	command := testClusterCommandMessage(78)
	clusterID, args, err := resolveAttachTargetCluster(ctx, store, command, []string{"79"})
	if err != nil {
		t.Fatal(err)
	}
	if clusterID != cluster.ID || len(args) != 1 || args[0] != "79" {
		t.Fatalf("clusterID=%d args=%#v want cluster=%d arg 79", clusterID, args, cluster.ID)
	}
}

func testClusterCommandMessage(replyID int) nest.Message {
	return nest.Message{
		Chat: nest.Chat{ID: -1004301779750},
		ReplyToMessage: &nest.Message{
			MessageID: replyID,
		},
	}
}

func testWorkspaceLiveConfig() config.Config {
	return config.Config{
		Workspace: config.WorkspaceConfig{
			ChatID: -1004301779750,
			Topics: config.WorkspaceTopicIDs{
				Inbox: 8, Tasks: 10, Notes: 12, Templates: 14, Experience: 16, Useful: 18, Collections: 20,
			},
		},
	}
}
