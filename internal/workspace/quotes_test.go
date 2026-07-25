package workspace

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestFormatWorkspaceQuoteUsesNativeBlockquote(t *testing.T) {
	formatted := FormatWorkspaceQuote("Название & смысл", "<reason>\nи passions", "David Hume, 1739 (c)")
	want := "<b>Название &amp; смысл</b>\n\n<blockquote>&lt;reason&gt;\nи passions</blockquote>\n\n<i>David Hume, 1739 (c)</i>"
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
	withoutTitle := FormatWorkspaceQuote("", "Текст & смысл", "Автор <A>")
	if withoutTitle != "<blockquote>Текст &amp; смысл</blockquote>\n\n<i>Автор &lt;A&gt;</i>" {
		t.Fatalf("quote without title = %q", withoutTitle)
	}
	withoutAuthor := FormatWorkspaceQuote("Название", "Текст", "")
	if withoutAuthor != "<b>Название</b>\n\n<blockquote>Текст</blockquote>" {
		t.Fatalf("quote without author = %q", withoutAuthor)
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
	for _, action := range []string{"skip_author", "skip_title", "save", "cancel", "edit_title", "edit_text", "edit_author", "clear_title", "clear_author"} {
		parsed, quoteID, ok := ParseQuoteCallback(QuoteCallbackDataForQuote(action, 42))
		if !ok || parsed != action || quoteID != 42 {
			t.Fatalf("callback %q parsed as %q quote=%d ok=%t", action, parsed, quoteID, ok)
		}
	}
	if _, _, ok := ParseQuoteCallback("ws:quote:unknown"); ok {
		t.Fatal("invalid quote callback parsed")
	}
	markup := QuotePreviewMarkup(42)
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("preview markup = %+v", markup)
	}
}

func TestQuoteCallbackIdentityRejectsStaleWizardButton(t *testing.T) {
	oldData := QuoteCallbackDataForQuote("save", 101)
	current := pendingWorkspaceInput{Kind: "quote_edit_preview", QuoteID: 202, QuoteText: "B"}
	action, oldQuoteID, ok := ParseQuoteCallback(oldData)
	if !ok || action != "save" {
		t.Fatalf("old callback parse = %q quote=%d ok=%t", action, oldQuoteID, ok)
	}
	if quoteCallbackMatchesPending(current, oldQuoteID) {
		t.Fatal("old wizard A callback matched current wizard B")
	}
	if current.QuoteID != 202 || current.QuoteText != "B" {
		t.Fatalf("stale callback mutated current wizard B: %+v", current)
	}
	_, currentQuoteID, ok := ParseQuoteCallback(QuoteCallbackDataForQuote("save", current.QuoteID))
	if !ok || !quoteCallbackMatchesPending(current, currentQuoteID) {
		t.Fatal("current wizard callback was rejected")
	}
}

func TestRestoreAndCancelWorkspaceQuoteEdit(t *testing.T) {
	store, quote, now := newActiveWorkspaceQuote(t)
	defer store.Close()
	ctx := context.Background()
	if _, err := store.BeginWorkspaceQuoteEdit(ctx, quote.ID, 77, "quote_edit_author", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceQuoteEdit(ctx, quote.ID, quote.Title, quote.Text, "Новый автор", "quote_edit_preview", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	pending := map[pendingTaskDateKey]pendingWorkspaceInput{}
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.ChatID = quote.TargetChatID
	cfg.Workspace.Topics.Inbox = 8
	if err := restoreWorkspaceQuoteDrafts(ctx, cfg, store, nil, pending, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	key := pendingTaskDateKey{chatID: cfg.Workspace.ChatID, threadID: 8, userID: 77}
	restored, ok := pending[key]
	if !ok || restored.Kind != "quote_edit_preview" || restored.Author != "Новый автор" || restored.QuoteID != quote.ID {
		t.Fatalf("restored edit = %+v, ok=%t", restored, ok)
	}
	if err := cancelWorkspaceQuoteWizard(ctx, store, restored, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Author != quote.Author || persisted.EditDeliveryStatus != "" || persisted.WizardStage != "" {
		t.Fatalf("cancelled edit changed quote = %+v", persisted)
	}
}

func TestWorkspaceQuoteOversizedEditDoesNotTouchTelegramOrSavedQuote(t *testing.T) {
	store, quote, now := newActiveWorkspaceQuote(t)
	defer store.Close()
	ctx := context.Background()
	if _, err := store.BeginWorkspaceQuoteEdit(ctx, quote.ID, 77, "quote_edit_text", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("🦉", quoteTelegramTextLimit/2+1)
	if err := store.UpdateWorkspaceQuoteEdit(ctx, quote.ID, quote.Title, oversized, quote.Author, "quote_edit_preview", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	fake := &fakeQuoteEditTelegram{}
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.ChatID = quote.TargetChatID
	cfg.Workspace.Topics.Experience = quote.TargetTopicID
	if _, err := applyWorkspaceQuoteEdit(ctx, cfg, store, fake, quote.ID, false, now.Add(3*time.Minute)); err == nil {
		t.Fatal("oversized edit was accepted")
	}
	if len(fake.edits) != 0 {
		t.Fatalf("oversized edit touched Telegram: %+v", fake.edits)
	}
	persisted, err := store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Text != quote.Text || persisted.EditDeliveryStatus != "pending" {
		t.Fatalf("oversized edit changed saved quote = %+v", persisted)
	}
}

func TestWorkspaceQuoteEditUpdatesSameMessageAndDynamicIndex(t *testing.T) {
	store, quote, now := newActiveWorkspaceQuote(t)
	defer store.Close()
	ctx := context.Background()
	if err := store.UpsertWorkspaceTopicIndex(ctx, quote.TargetChatID, quote.TargetTopicID, experienceQuoteIndexKey, 900, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginWorkspaceQuoteEdit(ctx, quote.ID, 77, "quote_edit_title", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceQuoteEdit(ctx, quote.ID, "Новое & имя", "Новый <текст>", "Автор & Co", "quote_edit_preview", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	fake := &fakeQuoteEditTelegram{}
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.ChatID = quote.TargetChatID
	cfg.Workspace.Topics.Experience = quote.TargetTopicID
	updated, err := applyWorkspaceQuoteEdit(ctx, cfg, store, fake, quote.ID, false, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != quote.ID || updated.TargetMessageID != quote.TargetMessageID || updated.Title != "Новое & имя" || updated.Status != "active" {
		t.Fatalf("updated quote = %+v", updated)
	}
	if len(fake.sends) != 0 || len(fake.edits) != 2 {
		t.Fatalf("telegram calls: sends=%+v edits=%+v", fake.sends, fake.edits)
	}
	if fake.edits[0].MessageID != quote.TargetMessageID || fake.edits[0].Text != "<b>Новое &amp; имя</b>\n\n<blockquote>Новый &lt;текст&gt;</blockquote>\n\n<i>Автор &amp; Co</i>" {
		t.Fatalf("target edit = %+v", fake.edits[0])
	}
	if fake.edits[1].MessageID != 900 || !strings.Contains(fake.edits[1].Text, "Новое &amp; имя — Автор &amp; Co") {
		t.Fatalf("index edit = %+v", fake.edits[1])
	}
	if len(fake.pins) != 1 || fake.pins[0].MessageID != 900 {
		t.Fatalf("index pins = %+v", fake.pins)
	}
	quotes, err := store.WorkspaceQuotes(ctx, []string{"active", "needs_review"}, 10)
	if err != nil || len(quotes) != 1 {
		t.Fatalf("quotes after edit = %+v, err=%v", quotes, err)
	}
}

func TestWorkspaceQuoteAmbiguousAndDeletedTargetEditsAreNotCommitted(t *testing.T) {
	t.Run("ambiguous", func(t *testing.T) {
		store, quote, now := newActiveWorkspaceQuote(t)
		defer store.Close()
		ctx := context.Background()
		prepareWorkspaceQuoteEdit(t, store, quote, "После сбоя", now)
		if err := store.UpsertWorkspaceTopicIndex(ctx, quote.TargetChatID, quote.TargetTopicID, experienceQuoteIndexKey, 900, now); err != nil {
			t.Fatal(err)
		}
		fake := &fakeQuoteEditTelegram{editErrors: map[int]error{quote.TargetMessageID: errors.New("connection reset")}}
		cfg := testWorkspaceLiveConfig()
		cfg.Workspace.ChatID = quote.TargetChatID
		cfg.Workspace.Topics.Experience = quote.TargetTopicID
		_, err := applyWorkspaceQuoteEdit(ctx, cfg, store, fake, quote.ID, false, now.Add(3*time.Minute))
		if !isQuoteEditOutcomeUnknown(err) {
			t.Fatalf("ambiguous error = %v", err)
		}
		persisted, err := store.WorkspaceQuoteByID(ctx, quote.ID)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.Text != quote.Text || persisted.EditText != "После сбоя" || persisted.EditDeliveryStatus != "unknown" {
			t.Fatalf("ambiguous edit = %+v", persisted)
		}
		if len(fake.edits) != 1 {
			t.Fatalf("ambiguous edit was silently retried: %+v", fake.edits)
		}
		delete(fake.editErrors, quote.TargetMessageID)
		updated, err := applyWorkspaceQuoteEdit(ctx, cfg, store, fake, quote.ID, true, now.Add(4*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if updated.Text != "После сбоя" || updated.EditDeliveryStatus != "" || len(fake.edits) != 3 || fake.edits[0].MessageID != quote.TargetMessageID || fake.edits[1].MessageID != quote.TargetMessageID || fake.edits[2].MessageID != 900 {
			t.Fatalf("explicit retry result=%+v edits=%+v", updated, fake.edits)
		}
	})

	t.Run("deleted target", func(t *testing.T) {
		store, quote, now := newActiveWorkspaceQuote(t)
		defer store.Close()
		ctx := context.Background()
		prepareWorkspaceQuoteEdit(t, store, quote, "Не применено", now)
		fake := &fakeQuoteEditTelegram{editErrors: map[int]error{quote.TargetMessageID: errors.New("Bot API editMessageText failed: Bad Request: message to edit not found")}}
		cfg := testWorkspaceLiveConfig()
		cfg.Workspace.ChatID = quote.TargetChatID
		cfg.Workspace.Topics.Experience = quote.TargetTopicID
		_, err := applyWorkspaceQuoteEdit(ctx, cfg, store, fake, quote.ID, false, now.Add(3*time.Minute))
		if err == nil || isQuoteEditOutcomeUnknown(err) {
			t.Fatalf("deleted-target error = %v", err)
		}
		persisted, err := store.WorkspaceQuoteByID(ctx, quote.ID)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.Text != quote.Text || persisted.EditDeliveryStatus != "pending" {
			t.Fatalf("deleted-target edit = %+v", persisted)
		}
		if len(fake.edits) != 1 {
			t.Fatalf("deleted target was retried: %+v", fake.edits)
		}
	})
}

func TestWorkspaceQuoteCommitFailureAfterTelegramEditBecomesUnknown(t *testing.T) {
	store, quote, now := newActiveWorkspaceQuote(t)
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	prepareWorkspaceQuoteEdit(t, store, quote, "Telegram уже принял", now)
	fake := &fakeQuoteEditTelegram{editHook: cancel}
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.ChatID = quote.TargetChatID
	cfg.Workspace.Topics.Experience = quote.TargetTopicID

	_, err := applyWorkspaceQuoteEdit(ctx, cfg, store, fake, quote.ID, false, now.Add(3*time.Minute))
	if !isQuoteEditOutcomeUnknown(err) {
		t.Fatalf("commit-after-edit error = %v", err)
	}
	persisted, loadErr := store.WorkspaceQuoteByID(context.Background(), quote.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.Text != quote.Text || persisted.EditText != "Telegram уже принял" || persisted.EditDeliveryStatus != "unknown" {
		t.Fatalf("ambiguous commit state = %+v", persisted)
	}
	if len(fake.edits) != 1 || fake.edits[0].MessageID != quote.TargetMessageID {
		t.Fatalf("Telegram edit calls = %+v", fake.edits)
	}
}

func TestWorkspaceQuoteAcceptsManuallyVerifiedAmbiguousEditWithoutResend(t *testing.T) {
	store, quote, now := newActiveWorkspaceQuote(t)
	defer store.Close()
	ctx := context.Background()
	prepareWorkspaceQuoteEdit(t, store, quote, "Уже видно в Telegram", now)
	if err := store.MarkWorkspaceQuoteEditing(ctx, quote.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspaceQuoteEditUnknown(ctx, quote.ID, "timeout", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkspaceTopicIndex(ctx, quote.TargetChatID, quote.TargetTopicID, experienceQuoteIndexKey, 900, now); err != nil {
		t.Fatal(err)
	}
	fake := &fakeQuoteEditTelegram{}
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.ChatID = quote.TargetChatID
	cfg.Workspace.Topics.Experience = quote.TargetTopicID
	updated, err := acceptWorkspaceQuoteEdit(ctx, cfg, store, fake, quote.ID, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Text != "Уже видно в Telegram" || updated.EditDeliveryStatus != "" {
		t.Fatalf("accepted edit = %+v", updated)
	}
	if len(fake.sends) != 0 || len(fake.edits) != 1 || fake.edits[0].MessageID != 900 || len(fake.pins) != 1 {
		t.Fatalf("accept did not refresh existing index: sends=%+v edits=%+v pins=%+v", fake.sends, fake.edits, fake.pins)
	}
}

func TestWorkspaceQuoteMissingIndexRecoveryNeverSendsDuplicateIndexOrReeditsQuote(t *testing.T) {
	store, quote, now := newActiveWorkspaceQuote(t)
	defer store.Close()
	ctx := context.Background()
	prepareWorkspaceQuoteEdit(t, store, quote, "Применено без индекса", now)
	fake := &fakeQuoteEditTelegram{}
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.ChatID = quote.TargetChatID
	cfg.Workspace.Topics.Experience = quote.TargetTopicID
	updated, err := applyWorkspaceQuoteEdit(ctx, cfg, store, fake, quote.ID, false, now.Add(3*time.Minute))
	if !isQuoteEditIndexPending(err) {
		t.Fatalf("missing-index edit error = %v", err)
	}
	if updated.Text != "Применено без индекса" || updated.EditDeliveryStatus != "index_pending" {
		t.Fatalf("missing-index updated quote = %+v", updated)
	}
	for attempt := 0; attempt < 2; attempt++ {
		wizards, loadErr := store.WorkspaceQuoteDrafts(ctx)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if recoverErr := recoverWorkspaceQuoteIndexPending(ctx, cfg, store, fake, wizards, now.Add(time.Duration(4+attempt)*time.Minute)); recoverErr == nil {
			t.Fatal("missing tracked index unexpectedly recovered")
		}
	}
	if len(fake.edits) != 1 || fake.edits[0].MessageID != quote.TargetMessageID {
		t.Fatalf("recovery re-edited quote or another message: %+v", fake.edits)
	}
	if len(fake.sends) != 0 || len(fake.pins) != 0 {
		t.Fatalf("missing-index recovery created duplicate: sends=%+v pins=%+v", fake.sends, fake.pins)
	}
}

func TestWorkspaceQuoteRestartRecoversIndexPendingWithoutReeditingQuote(t *testing.T) {
	store, quote, now := newActiveWorkspaceQuote(t)
	defer store.Close()
	ctx := context.Background()
	prepareWorkspaceQuoteEdit(t, store, quote, "Уже применено до рестарта", now)
	if err := store.MarkWorkspaceQuoteEditing(ctx, quote.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Simulate a process crash after Telegram accepted the target edit and the
	// DB committed the content, but before the dynamic index was refreshed.
	if err := store.CommitWorkspaceQuoteEdit(ctx, quote.ID, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkspaceTopicIndex(ctx, quote.TargetChatID, quote.TargetTopicID, experienceQuoteIndexKey, 900, now); err != nil {
		t.Fatal(err)
	}
	wizards, err := store.WorkspaceQuoteDrafts(ctx)
	if err != nil || len(wizards) != 1 || wizards[0].EditDeliveryStatus != "index_pending" {
		t.Fatalf("restart candidates=%+v err=%v", wizards, err)
	}
	fake := &fakeQuoteEditTelegram{}
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.ChatID = quote.TargetChatID
	cfg.Workspace.Topics.Experience = quote.TargetTopicID
	if err := recoverWorkspaceQuoteIndexPending(ctx, cfg, store, fake, wizards, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(fake.edits) != 1 || fake.edits[0].MessageID != 900 || len(fake.sends) != 0 {
		t.Fatalf("restart recovery calls: edits=%+v sends=%+v", fake.edits, fake.sends)
	}
	persisted, err := store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Text != "Уже применено до рестарта" || persisted.EditDeliveryStatus != "" {
		t.Fatalf("restart-recovered quote = %+v", persisted)
	}
}

func TestWorkspaceQuoteRestartMarksInFlightEditUnknownWithoutRetry(t *testing.T) {
	store, quote, now := newActiveWorkspaceQuote(t)
	defer store.Close()
	ctx := context.Background()
	prepareWorkspaceQuoteEdit(t, store, quote, "Неизвестный исход", now)
	if err := store.MarkWorkspaceQuoteEditing(ctx, quote.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.ChatID = quote.TargetChatID
	cfg.Workspace.Topics.Inbox = 8
	pending := map[pendingTaskDateKey]pendingWorkspaceInput{}
	if err := restoreWorkspaceQuoteDrafts(ctx, cfg, store, nil, pending, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Text != quote.Text || persisted.EditText != "Неизвестный исход" || persisted.EditDeliveryStatus != "unknown" || len(pending) != 0 {
		t.Fatalf("restarted in-flight edit = %+v pending=%+v", persisted, pending)
	}
}

func TestResolveWorkspaceQuoteRejectsInvalidArchivedAndNotFound(t *testing.T) {
	store, quote, _ := newActiveWorkspaceQuote(t)
	defer store.Close()
	ctx := context.Background()
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.ChatID = quote.TargetChatID
	resolved, err := resolveWorkspaceQuote(ctx, cfg, store, strconv.FormatInt(quote.ID, 10))
	if err != nil || resolved.ID != quote.ID {
		t.Fatalf("resolve id = %+v, err=%v", resolved, err)
	}
	resolved, err = resolveWorkspaceQuote(ctx, cfg, store, workspaceMessageLink(quote.TargetChatID, quote.TargetTopicID, quote.TargetMessageID))
	if err != nil || resolved.ID != quote.ID {
		t.Fatalf("resolve link = %+v, err=%v", resolved, err)
	}
	for _, ref := range []string{"not-a-ref", "999999"} {
		if _, err := resolveWorkspaceQuote(ctx, cfg, store, ref); err == nil {
			t.Fatalf("invalid ref %q resolved", ref)
		}
	}
	archived, err := store.CreateWorkspaceQuote(ctx, sqlitestore.WorkspaceQuote{Text: "Архив", SourceChatID: quote.SourceChatID, SourceMessageID: quote.SourceMessageID + 1}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveWorkspaceQuoteDraft(ctx, archived.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	archived, err = store.WorkspaceQuoteByID(ctx, archived.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateEditableWorkspaceQuote(archived); err == nil {
		t.Fatal("archived quote is editable")
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

func TestExperienceQuoteIndexStaysWithinTelegramUTF16Limit(t *testing.T) {
	quotes := make([]sqlitestore.WorkspaceQuote, 0, 100)
	for index := 0; index < 100; index++ {
		quotes = append(quotes, sqlitestore.WorkspaceQuote{
			ID: int64(index + 1), Text: strings.Repeat("🙂", 70), Author: strings.Repeat("🦉", 20), Status: "active",
			TargetChatID: -1004301779750, TargetTopicID: 16, TargetMessageID: 700 + index,
		})
	}
	text := renderExperienceQuoteIndexFromQuotes(quotes)
	units, err := telegramHTMLUTF16Len(text)
	if err != nil {
		t.Fatal(err)
	}
	if units > quoteIndexTextLimit {
		t.Fatalf("quote index has %d UTF-16 units", units)
	}
}

func TestQuoteHelpDocumentsInboxOnlyFlow(t *testing.T) {
	if !strings.Contains(InboxHelpMessageText(), "/quote") || !strings.Contains(InboxHelpMessageText(), "/search") || !strings.Contains(ExperienceHelpMessageText(), "только из <b>Inbox</b>") {
		t.Fatalf("quote help is incomplete: inbox=%q experience=%q", InboxHelpMessageText(), ExperienceHelpMessageText())
	}
	commandHelp := ExperienceQuoteCommandHelpText()
	for _, command := range []string{"/quote</code>", "/quote show", "/quote edit ID|ссылка", "/quote help", "/quote edit retry", "/quote edit accept"} {
		if !strings.Contains(commandHelp, command) {
			t.Fatalf("experience command help missing %q: %s", command, commandHelp)
		}
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

func TestSeedDocumentIndexesCanSelectOnlyQuote(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := testWorkspaceLiveConfig()
	cfg.Workspace.BotToken = "test-token"
	result, err := SeedWorkspaceDocumentIndexes(context.Background(), cfg, store, SeedDocumentIndexesOptions{DryRun: true, Type: "quote"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Type != "quote" || result.Items[0].TopicID != cfg.Workspace.Topics.Experience {
		t.Fatalf("quote-only seed = %+v", result.Items)
	}
}

type fakeQuoteEditTelegram struct {
	edits      []nest.EditMessageTextRequest
	sends      []nest.SendMessageRequest
	pins       []nest.PinChatMessageRequest
	editErrors map[int]error
	editHook   func()
	nextID     int
}

func (f *fakeQuoteEditTelegram) EditMessageText(_ context.Context, request nest.EditMessageTextRequest) error {
	f.edits = append(f.edits, request)
	if f.editHook != nil {
		f.editHook()
	}
	return f.editErrors[request.MessageID]
}

func (f *fakeQuoteEditTelegram) SendMessageResult(_ context.Context, request nest.SendMessageRequest) (nest.Message, error) {
	f.sends = append(f.sends, request)
	if f.nextID == 0 {
		f.nextID = 1000
	}
	message := nest.Message{MessageID: f.nextID}
	f.nextID++
	return message, nil
}

func (f *fakeQuoteEditTelegram) PinChatMessage(_ context.Context, request nest.PinChatMessageRequest) error {
	f.pins = append(f.pins, request)
	return nil
}

func TestRefreshPublishedWorkspaceQuoteFormatEditsExistingMessageOnce(t *testing.T) {
	store, quote, now := newLegacyPublishedWorkspaceQuote(t)
	defer store.Close()
	ctx := context.Background()
	fake := &fakeQuoteEditTelegram{}
	updated, err := refreshPublishedWorkspaceQuoteFormat(ctx, store, fake, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated != 1 || len(fake.edits) != 1 {
		t.Fatalf("updated=%d edits=%d", updated, len(fake.edits))
	}
	if got := fake.edits[0]; got.ChatID != quote.TargetChatID || got.MessageID != quote.TargetMessageID || !strings.Contains(got.Text, "<i>Старый автор</i>") {
		t.Fatalf("format edit = %+v", got)
	}
	stored, err := store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FormatVersion != currentWorkspaceQuoteFormatVersion {
		t.Fatalf("format version = %d", stored.FormatVersion)
	}
	updated, err = refreshPublishedWorkspaceQuoteFormat(ctx, store, fake, now.Add(2*time.Minute))
	if err != nil || updated != 0 || len(fake.edits) != 1 {
		t.Fatalf("second refresh: updated=%d edits=%d err=%v", updated, len(fake.edits), err)
	}
}

func newLegacyPublishedWorkspaceQuote(t *testing.T) (*sqlitestore.Store, sqlitestore.WorkspaceQuote, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sova.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	_, err = db.Exec(`
CREATE TABLE workspace_quotes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL DEFAULT '', text TEXT NOT NULL, author TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'draft', wizard_user_id INTEGER NOT NULL DEFAULT 0,
    wizard_stage TEXT NOT NULL DEFAULT '', delivery_status TEXT NOT NULL DEFAULT 'draft',
    delivery_error TEXT NOT NULL DEFAULT '', edit_title TEXT NOT NULL DEFAULT '',
    edit_text TEXT NOT NULL DEFAULT '', edit_author TEXT NOT NULL DEFAULT '',
    edit_delivery_status TEXT NOT NULL DEFAULT '', edit_delivery_error TEXT NOT NULL DEFAULT '',
    source_chat_id INTEGER NOT NULL, source_message_id INTEGER NOT NULL,
    source_link TEXT NOT NULL DEFAULT '', target_chat_id INTEGER NOT NULL DEFAULT 0,
    target_topic_id INTEGER NOT NULL DEFAULT 0, target_message_id INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL, updated_at TEXT NOT NULL, published_at TEXT,
    UNIQUE(source_chat_id, source_message_id)
);
INSERT INTO workspace_quotes(
    title, text, author, status, delivery_status, source_chat_id, source_message_id, source_link,
    target_chat_id, target_topic_id, target_message_id, created_at, updated_at, published_at
) VALUES ('Старое название', 'Старый текст', 'Старый автор', 'active', 'sent',
    -1004301779750, 501, 'https://t.me/c/4301779750/8/501',
    -1004301779750, 16, 700, ?, ?, ?)`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	quote, err := store.WorkspaceQuoteByID(context.Background(), 1)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, quote, now
}

func newActiveWorkspaceQuote(t *testing.T) (*sqlitestore.Store, sqlitestore.WorkspaceQuote, time.Time) {
	t.Helper()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	quote, err := store.CreateWorkspaceQuote(ctx, sqlitestore.WorkspaceQuote{
		Title: "Старое название", Text: "Старый текст", Author: "Старый автор", Status: "draft",
		SourceChatID: -1004301779750, SourceMessageID: 501, SourceLink: "https://t.me/c/4301779750/8/501",
	}, now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.MarkWorkspaceQuoteSending(ctx, quote.ID, now.Add(time.Second)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.PublishWorkspaceQuote(ctx, quote.ID, -1004301779750, 16, 700, now.Add(2*time.Second)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	quote, err = store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, quote, now
}

func prepareWorkspaceQuoteEdit(t *testing.T, store *sqlitestore.Store, quote sqlitestore.WorkspaceQuote, text string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.BeginWorkspaceQuoteEdit(ctx, quote.ID, 77, "quote_edit_text", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceQuoteEdit(ctx, quote.ID, quote.Title, text, quote.Author, "quote_edit_preview", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
}
