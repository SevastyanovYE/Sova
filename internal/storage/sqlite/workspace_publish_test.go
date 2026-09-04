package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspacePublishPreviewSurvivesRestartAndCallbackIsMessageBound(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sova.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := createPublishTestDocument(t, store)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	run, err := store.CreateWorkspacePublishRun(ctx, doc.ID, "Свободная правка", -1001, 11, 701, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkspacePublishPreview(ctx, run.ID, "gemini-test", []string{"one", "two"}, now); err != nil {
		t.Fatal(err)
	}
	preview, err := store.WorkspacePublishMessages(ctx, run.ID, "preview")
	if err != nil {
		t.Fatal(err)
	}
	for index, item := range preview {
		if err := store.MarkWorkspacePublishMessageSending(ctx, item.ID, -1001, 11, now); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkWorkspacePublishMessageSent(ctx, item.ID, 801+index, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ActivateWorkspacePublishPreview(ctx, run.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recovered, ok, err := store.WorkspacePublishRunForCallback(ctx, doc.ID, 802)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || recovered.ID != run.ID || recovered.Revision != "Свободная правка" || recovered.Model != "gemini-test" {
		t.Fatalf("recovered=%+v ok=%v", recovered, ok)
	}
	if _, ok, err := store.WorkspacePublishRunForCallback(ctx, doc.ID, 801); err != nil || ok {
		t.Fatalf("non-final preview callback must be stale: ok=%v err=%v", ok, err)
	}
}

func TestWorkspacePublishPartialPreviewCannotReplaceOldPreview(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := createPublishTestDocument(t, store)
	now := time.Now().UTC()
	old := createActivePublishPreview(t, store, doc.ID, 901, now)

	newRun, err := store.CreateWorkspacePublishRun(ctx, doc.ID, "new", -1001, 11, 0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkspacePublishPreview(ctx, newRun.ID, "gemini", []string{"first", "second"}, now); err != nil {
		t.Fatal(err)
	}
	items, _ := store.WorkspacePublishMessages(ctx, newRun.ID, "preview")
	if err := store.MarkWorkspacePublishMessageSending(ctx, items[0].ID, -1001, 11, now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspacePublishMessageSent(ctx, items[0].ID, 902, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateWorkspacePublishPreview(ctx, newRun.ID, now); err == nil {
		t.Fatal("partially sent replacement must not become active")
	}
	if active, ok, err := store.WorkspacePublishRunForCallback(ctx, doc.ID, 901); err != nil || !ok || active.ID != old.ID {
		t.Fatalf("old preview lost after partial replacement: active=%+v ok=%v err=%v", active, ok, err)
	}
}

func TestInterruptedWorkspacePublishFailureMakesSendingUnknownAndCancelsPending(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := createPublishTestDocument(t, store)
	now := time.Now().UTC()
	run, err := store.CreateWorkspacePublishRun(ctx, doc.ID, "", -1001, 11, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkspacePublishPreview(ctx, run.ID, "gemini", []string{"one", "two"}, now); err != nil {
		t.Fatal(err)
	}
	items, err := store.WorkspacePublishMessages(ctx, run.ID, "preview")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspacePublishMessageSending(ctx, items[0].ID, -1001, 11, now); err != nil {
		t.Fatal(err)
	}
	interrupted, err := store.InterruptedWorkspacePublishRuns(ctx, 10)
	if err != nil || len(interrupted) != 1 || interrupted[0].Status != "preview_sending" {
		t.Fatalf("interrupted=%+v err=%v", interrupted, err)
	}
	if err := store.FailWorkspacePublishRun(ctx, run.ID, "restart recovery", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	failed, err := store.WorkspacePublishRunByID(ctx, run.ID)
	if err != nil || failed.Status != "failed" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	statuses := map[int]string{}
	for _, item := range failed.Messages {
		statuses[item.Position] = item.Status
	}
	if statuses[1] != "unknown" || statuses[2] != "cancelled" {
		t.Fatalf("statuses=%v, want unknown/cancelled", statuses)
	}
	interrupted, err = store.InterruptedWorkspacePublishRuns(ctx, 10)
	if err != nil || len(interrupted) != 0 {
		t.Fatalf("terminal run remained interrupted: %+v err=%v", interrupted, err)
	}
}

func TestWorkspacePublishReplacementMakesOldButtonStale(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := createPublishTestDocument(t, store)
	now := time.Now().UTC()
	old := createActivePublishPreview(t, store, doc.ID, 1001, now)
	newRun := createActivePublishPreview(t, store, doc.ID, 1002, now.Add(time.Second))
	if _, ok, err := store.WorkspacePublishRunForCallback(ctx, doc.ID, 1001); err != nil || ok {
		t.Fatalf("old callback remained actionable: ok=%v err=%v", ok, err)
	}
	if active, ok, err := store.WorkspacePublishRunForCallback(ctx, doc.ID, 1002); err != nil || !ok || active.ID != newRun.ID {
		t.Fatalf("new callback unavailable: active=%+v ok=%v err=%v", active, ok, err)
	}
	old, err = store.WorkspacePublishRunByID(ctx, old.ID)
	if err != nil || old.Status != "superseded" {
		t.Fatalf("old=%+v err=%v", old, err)
	}
}

func TestWorkspaceManualPublishPreviewBlocksBaseAndPublishesOnlyManualText(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := createPublishTestDocument(t, store)
	now := time.Now().UTC()
	base := createActivePublishPreviewWithTexts(t, store, doc.ID, []string{"AI text"}, 1050, now)
	if err := store.BeginWorkspaceManualPublishEdit(ctx, base.ID, 77, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveWorkspacePublishRun(ctx, base.ID, now); err == nil {
		t.Fatal("base preview was approved while manual text was awaited")
	}
	waiting, err := store.AwaitingWorkspaceManualPublishInputs(ctx, 10)
	if err != nil || len(waiting) != 1 || waiting[0].ManualEditorUserID != 77 {
		t.Fatalf("manual waits=%+v err=%v", waiting, err)
	}
	child, err := store.CreateWorkspaceManualPublishPreview(ctx, base.ID, 77, 2001, []string{"Manual text"}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != "preview_sending" || child.Model != "manual" || len(child.Messages) != 1 || child.Messages[0].Text != "Manual text" {
		t.Fatalf("manual child=%+v", child)
	}
	preview := child.Messages[0]
	if err := store.MarkWorkspacePublishMessageSending(ctx, preview.ID, -1001, 11, now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspacePublishMessageSent(ctx, preview.ID, 1051, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateWorkspacePublishPreview(ctx, child.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.WorkspacePublishRunForCallback(ctx, doc.ID, 1050); err != nil || ok {
		t.Fatalf("base callback remained active: ok=%v err=%v", ok, err)
	}
	approved, err := store.ApproveWorkspacePublishRun(ctx, child.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	var finals []WorkspacePublishMessage
	for _, message := range approved.Messages {
		if message.Kind == "final" {
			finals = append(finals, message)
		}
	}
	if len(finals) != 1 || finals[0].Text != "Manual text" {
		t.Fatalf("manual finals=%+v", finals)
	}
}

func TestArchiveWorkspaceUsefulPublicationClosesProvenance(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	doc := createPublishTestDocument(t, store)
	if err := store.UpdateWorkspaceDocumentTarget(ctx, doc.ID, -1001, 18, 750, &now, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceDocumentStatus(ctx, doc.ID, "published", now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkspaceDerivedMessage(ctx, WorkspaceDerivedMessage{
		SourceChatID: doc.SourceChatID, SourceMessageID: doc.SourceMessageID, DerivedType: "legacy_migration_migrate_part_1",
		DerivedChatID: -1001, DerivedTopicID: 18, DerivedMessageID: 750, Status: "published",
	}, now); err != nil {
		t.Fatal(err)
	}
	legacyIDs, err := store.WorkspaceUsefulLegacyMessageIDsForDocument(ctx, doc.ID, -1001, 18)
	if err != nil || len(legacyIDs) != 1 || legacyIDs[0] != 750 {
		t.Fatalf("legacy message IDs=%v err=%v", legacyIDs, err)
	}
	if err := store.ArchiveWorkspaceUsefulPublication(ctx, doc.ID, -1001, 18, []int{750}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	doc, err = store.WorkspaceDocumentByID(ctx, doc.ID)
	if err != nil || doc.Status != "archived" {
		t.Fatalf("archived doc=%+v err=%v", doc, err)
	}
	derived, err := store.WorkspaceDerivedMessagesBySource(ctx, doc.SourceChatID, doc.SourceMessageID, "legacy_migration_", []string{"closed"}, 10)
	if err != nil || len(derived) != 1 || derived[0].DerivedMessageID != 750 {
		t.Fatalf("closed provenance=%+v err=%v", derived, err)
	}
}

func TestWorkspacePublishApprovalIsIdempotentAndUnknownIsNotResent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := createPublishTestDocument(t, store)
	now := time.Now().UTC()
	run := createActivePublishPreviewWithTexts(t, store, doc.ID, []string{"one", "two", "three"}, 1100, now)
	if _, err := store.ApproveWorkspacePublishRun(ctx, run.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveWorkspacePublishRun(ctx, run.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	finals, err := store.WorkspacePublishMessages(ctx, run.ID, "final")
	if err != nil || len(finals) != 3 {
		t.Fatalf("finals=%+v err=%v", finals, err)
	}
	if err := store.MarkWorkspacePublishMessageSending(ctx, finals[0].ID, -1001, 22, now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspacePublishMessageSent(ctx, finals[0].ID, 1201, now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspacePublishMessageSending(ctx, finals[1].ID, -1001, 22, now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspacePublishMessageUnknown(ctx, finals[1].ID, "read response: EOF", now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkspacePublishMessageSending(ctx, finals[1].ID, -1001, 22, now); err == nil {
		t.Fatal("unknown message must never be claimed for resend")
	}
	if err := store.MarkWorkspacePublishRunFinalizing(ctx, run.ID, now); err == nil {
		t.Fatal("run with unknown and pending final messages must not finalize")
	}
	resumable, err := store.ResumableWorkspacePublishRuns(ctx, 10)
	if err != nil || len(resumable) != 1 {
		t.Fatalf("resumable=%+v err=%v", resumable, err)
	}
	var pending, unknown int
	for _, item := range resumable[0].Messages {
		if item.Kind != "final" {
			continue
		}
		switch item.Status {
		case "pending":
			pending++
		case "unknown":
			unknown++
		}
	}
	if pending != 1 || unknown != 1 {
		t.Fatalf("pending=%d unknown=%d messages=%+v", pending, unknown, resumable[0].Messages)
	}
}

func TestWorkspacePublishConfirmedFragmentsResumeAtFinalizationAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sova.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := createPublishTestDocument(t, store)
	now := time.Now().UTC()
	run := createActivePublishPreviewWithTexts(t, store, doc.ID, []string{"one", "two"}, 1300, now)
	if _, err := store.ApproveWorkspacePublishRun(ctx, run.ID, now); err != nil {
		t.Fatal(err)
	}
	finals, err := store.WorkspacePublishMessages(ctx, run.ID, "final")
	if err != nil {
		t.Fatal(err)
	}
	for index, item := range finals {
		if err := store.MarkWorkspacePublishMessageSending(ctx, item.ID, -1001, 22, now); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkWorkspacePublishMessageSent(ctx, item.ID, 1400+index, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkWorkspacePublishRunFinalizing(ctx, run.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.ResumableWorkspacePublishRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "finalizing" {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	for _, item := range runs[0].Messages {
		if item.Kind == "final" && item.Status != "sent" {
			t.Fatalf("confirmed final changed across restart: %+v", item)
		}
	}
	if err := store.CompleteWorkspacePublishRun(ctx, run.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	completed, err := store.WorkspacePublishRunByID(ctx, run.ID)
	if err != nil || completed.Status != "completed" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func createPublishTestDocument(t *testing.T, store *Store) WorkspaceDocument {
	t.Helper()
	doc, err := store.CreateWorkspaceDocument(context.Background(), WorkspaceDocument{
		Type:   "note",
		Status: "active",
		Title:  "Durable publish",
	}, WorkspaceDocumentPart{
		SourceChatID:    -1001,
		SourceMessageID: 77,
		Text:            "Source text",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func createActivePublishPreview(t *testing.T, store *Store, documentID int64, messageID int, now time.Time) WorkspacePublishRun {
	t.Helper()
	return createActivePublishPreviewWithTexts(t, store, documentID, []string{"preview"}, messageID, now)
}

func createActivePublishPreviewWithTexts(t *testing.T, store *Store, documentID int64, texts []string, firstMessageID int, now time.Time) WorkspacePublishRun {
	t.Helper()
	ctx := context.Background()
	run, err := store.CreateWorkspacePublishRun(ctx, documentID, "", -1001, 11, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkspacePublishPreview(ctx, run.ID, "gemini", texts, now); err != nil {
		t.Fatal(err)
	}
	items, err := store.WorkspacePublishMessages(ctx, run.ID, "preview")
	if err != nil {
		t.Fatal(err)
	}
	for index, item := range items {
		if err := store.MarkWorkspacePublishMessageSending(ctx, item.ID, -1001, 11, now); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkWorkspacePublishMessageSent(ctx, item.ID, firstMessageID+index, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ActivateWorkspacePublishPreview(ctx, run.ID, now); err != nil {
		t.Fatal(err)
	}
	run, err = store.WorkspacePublishRunByID(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}
