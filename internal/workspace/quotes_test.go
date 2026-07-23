package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestFormatWorkspaceQuoteUsesNativeBlockquote(t *testing.T) {
	formatted := FormatWorkspaceQuote("Название & смысл", "<reason>\nи passions", "David Hume, 1739 (c)")
	want := "<b>Название &amp; смысл</b>\n\n<blockquote>&lt;reason&gt;\nи passions</blockquote>\n\nDavid Hume, 1739 (c)"
	if formatted != want {
		t.Fatalf("formatted quote = %q, want %q", formatted, want)
	}
	if strings.Contains(formatted, `"&lt;reason&gt;`) || strings.Contains(formatted, `&gt;"`) {
		t.Fatalf("formatted quote contains printable quotation marks: %q", formatted)
	}
	withoutMetadata := FormatWorkspaceQuote("", "Только текст", "")
	if withoutMetadata != "<blockquote>Только текст</blockquote>" {
		t.Fatalf("minimal quote = %q", withoutMetadata)
	}
}

func TestRestoreWorkspaceQuoteDraftRehydratesWizard(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	quote, err := store.CreateWorkspaceQuote(ctx, sqlitestore.WorkspaceQuote{
		Text: "Persisted quote", Author: "Author", Status: "draft",
		WizardUserID: 77, WizardStage: "quote_title",
		SourceChatID: -1001, SourceMessageID: 501, SourceLink: "https://t.me/c/1/8/501",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	pending := map[pendingTaskDateKey]pendingWorkspaceInput{}
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.Topics.Inbox = 8
	if err := restoreWorkspaceQuoteDrafts(ctx, cfg, store, nil, pending, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	key := pendingTaskDateKey{chatID: -1001, threadID: 8, userID: 77}
	restored, ok := pending[key]
	if !ok || restored.QuoteID != quote.ID || restored.Kind != "quote_title" || restored.Author != "Author" {
		t.Fatalf("restored=%+v ok=%t", restored, ok)
	}
}

func TestWorkspaceQuoteOneMessageValidation(t *testing.T) {
	if err := validateWorkspaceQuoteMessage("", strings.Repeat("a", quoteTelegramTextLimit), ""); err != nil {
		t.Fatalf("exact Telegram limit rejected: %v", err)
	}
	if err := validateWorkspaceQuoteMessage("", strings.Repeat("a", quoteTelegramTextLimit+1), ""); err == nil {
		t.Fatal("quote over Telegram limit accepted")
	}
	// Telegram counts non-BMP emoji as two UTF-16 code units.
	if err := validateWorkspaceQuoteMessage("", strings.Repeat("🦉", quoteTelegramTextLimit/2+1), ""); err == nil {
		t.Fatal("UTF-16 quote over Telegram limit accepted")
	}
}

func TestQuoteCallbacksAndMarkup(t *testing.T) {
	for _, action := range []string{"skip_author", "skip_title", "save", "cancel"} {
		parsed, ok := ParseQuoteCallback(QuoteCallbackData(action))
		if !ok || parsed != action {
			t.Fatalf("callback %q parsed as %q ok=%t", action, parsed, ok)
		}
	}
	if _, ok := ParseQuoteCallback("ws:quote:unknown"); ok {
		t.Fatal("invalid quote callback parsed")
	}
	markup := QuotePreviewMarkup()
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("preview markup = %+v", markup)
	}
}

func TestExperienceQuoteIndexLabelsAndReviewMarker(t *testing.T) {
	text := renderExperienceQuoteIndexFromQuotes([]sqlitestore.WorkspaceQuote{
		{
			ID: 2, Text: "Первая строка цитаты\nвторая строка", Author: "Автор",
			Status: "active", TargetChatID: -1004301779750, TargetTopicID: 16, TargetMessageID: 702,
		},
		{
			ID: 1, Title: "Именованная", Text: "Текст", Status: "needs_review",
			TargetChatID: -1004301779750, TargetTopicID: 16, TargetMessageID: 701,
		},
	})
	for _, want := range []string{
		"🌱 <b>Опыт</b>", "<b>Цитаты</b>",
		`href="https://t.me/c/4301779750/16/702"`, "Первая строка цитаты вторая строка — Автор",
		`href="https://t.me/c/4301779750/16/701"`, "Именованная", "⚠️",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("experience index missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(strings.Join(collectionCategories(), ","), "Цитаты") {
		t.Fatalf("quotes remain in collection taxonomy: %#v", collectionCategories())
	}
}

func TestQuoteHelpDocumentsInboxOnlyFlow(t *testing.T) {
	if !strings.Contains(InboxHelpMessageText(), "/quote") || !strings.Contains(InboxHelpMessageText(), "/search") || !strings.Contains(ExperienceHelpMessageText(), "только из <b>Inbox</b>") {
		t.Fatalf("quote help is incomplete: inbox=%q experience=%q", InboxHelpMessageText(), ExperienceHelpMessageText())
	}
	empty := renderExperienceQuoteIndexFromQuotes(nil)
	if !strings.Contains(empty, "/quote из Inbox") {
		t.Fatalf("empty experience index = %q", empty)
	}
}

func TestQuoteIndexParticipatesInSeedAndSurvivesGeneralPinReset(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.BotToken = "test-token"
	seed, err := SeedWorkspaceDocumentIndexes(context.Background(), cfg, store, SeedDocumentIndexesOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	foundQuote := false
	for _, item := range seed.Items {
		if item.Type == "quote" && item.TopicID == cfg.Workspace.Topics.Experience && strings.Contains(item.Text, "<b>Цитаты</b>") {
			foundQuote = true
		}
	}
	if !foundQuote {
		t.Fatalf("quote index missing from seed: %+v", seed.Items)
	}
	reset, err := ResetWorkspaceTopicPins(context.Background(), cfg, SeedTopicPinsOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	foundPreserved := false
	for _, item := range reset.Items {
		if item.Topic == "Опыт" && item.Status == "preserved_dynamic_index" {
			foundPreserved = true
		}
	}
	if !foundPreserved {
		t.Fatalf("general reset did not preserve experience index: %+v", reset.Items)
	}
}
