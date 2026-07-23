package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceQuoteLifecycleAndSourceReview(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	quote, err := store.CreateWorkspaceQuote(ctx, WorkspaceQuote{
		Title:        "  Разум и страсти ",
		Text:         " reason is the slave of the passions ",
		Author:       " David Hume, 1739 (c) ",
		SourceChatID: -1004301779750, SourceMessageID: 501,
		SourceLink: "https://t.me/c/4301779750/8/501",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if quote.Status != "draft" || quote.Title != "Разум и страсти" || quote.Author != "David Hume, 1739 (c)" {
		t.Fatalf("draft quote = %+v", quote)
	}
	if err := store.MarkWorkspaceQuoteSending(ctx, quote.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishWorkspaceQuote(ctx, quote.ID, -1004301779750, 16, 700, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	active, err := store.WorkspaceQuotes(ctx, []string{"active", "needs_review"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].TargetMessageID != 700 || active[0].PublishedAt == nil {
		t.Fatalf("active quotes = %+v", active)
	}

	changed, err := store.MarkWorkspaceQuoteSourceNeedsReview(ctx, quote.SourceChatID, quote.SourceMessageID, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Status != "needs_review" {
		t.Fatalf("changed quotes = %+v", changed)
	}
	changed, err = store.MarkWorkspaceQuoteSourceNeedsReview(ctx, quote.SourceChatID, quote.SourceMessageID, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("second review transition returned %+v", changed)
	}
	persisted, err := store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != "needs_review" || persisted.TargetTopicID != 16 {
		t.Fatalf("persisted quote = %+v", persisted)
	}
}

func TestWorkspaceQuoteValidationAndUniqueSource(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := store.CreateWorkspaceQuote(ctx, WorkspaceQuote{SourceChatID: 1, SourceMessageID: 2}, now); err == nil {
		t.Fatal("expected empty quote text to fail")
	}
	quote := WorkspaceQuote{Text: "Цитата", SourceChatID: 1, SourceMessageID: 2}
	if _, err := store.CreateWorkspaceQuote(ctx, quote, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkspaceQuote(ctx, quote, now); err == nil {
		t.Fatal("expected duplicate source to fail")
	}
	if _, err := store.WorkspaceQuotes(ctx, []string{"unknown"}, 10); err == nil {
		t.Fatal("expected invalid status to fail")
	}
}

func TestWorkspaceQuoteDraftCanBeArchivedOnWizardCancel(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	quote, err := store.CreateWorkspaceQuote(ctx, WorkspaceQuote{Text: "Цитата", SourceChatID: 1, SourceMessageID: 3}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveWorkspaceQuoteDraft(ctx, quote.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	quote, err = store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if quote.Status != "archived" {
		t.Fatalf("cancelled draft status = %q", quote.Status)
	}
}

func TestWorkspaceQuoteWizardDraftAndAmbiguousDeliveryAreDurable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	quote, err := store.CreateWorkspaceQuote(ctx, WorkspaceQuote{
		Text: "Persist me", SourceChatID: -1001, SourceMessageID: 9,
		WizardUserID: 77, WizardStage: "quote_author",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceQuoteDraft(ctx, quote.ID, "Title", "Author", "quote_preview", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	drafts, err := store.WorkspaceQuoteDrafts(ctx)
	if err != nil || len(drafts) != 1 || drafts[0].WizardUserID != 77 || drafts[0].WizardStage != "quote_preview" {
		t.Fatalf("drafts=%+v err=%v", drafts, err)
	}
	if err := store.MarkWorkspaceQuoteSending(ctx, quote.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspaceQuoteDeliveryUnknown(ctx, quote.ID, "connection reset", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.WorkspaceQuoteByID(ctx, quote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.DeliveryStatus != "unknown" || persisted.WizardStage != "" {
		t.Fatalf("ambiguous quote=%+v", persisted)
	}
	if err := store.MarkWorkspaceQuoteSending(ctx, quote.ID, now.Add(4*time.Minute)); err == nil {
		t.Fatal("ambiguous delivery must not be resent automatically")
	}
}
