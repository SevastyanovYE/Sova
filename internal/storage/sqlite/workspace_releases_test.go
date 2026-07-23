package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceReleaseRequiresReceiptAndDeduplicatesReservation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 18, 0, 0, 0, time.UTC)
	receipt := WorkspaceDeploymentReceipt{Environment: "production", Version: "0.1.0", Commit: "abc123", Checks: "tests, doctors, smoke", VerifiedAt: now}
	if err := store.RecordWorkspaceDeploymentReceipt(ctx, receipt, now); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.WorkspaceDeploymentReceipt(ctx, "production", "0.1.0", "abc123")
	if err != nil || !ok || loaded.Checks != receipt.Checks {
		t.Fatalf("loaded=%+v ok=%v err=%v", loaded, ok, err)
	}
	reserved, created, err := store.ReserveWorkspaceReleaseAnnouncement(ctx, "production", "0.1.0", "abc123", now)
	if err != nil || !created || reserved.Status != "sending" {
		t.Fatalf("reserved=%+v created=%v err=%v", reserved, created, err)
	}
	again, created, err := store.ReserveWorkspaceReleaseAnnouncement(ctx, "production", "0.1.0", "abc123", now.Add(time.Minute))
	if err != nil || created || again.ID != reserved.ID {
		t.Fatalf("again=%+v created=%v err=%v", again, created, err)
	}
	if err := store.MarkWorkspaceReleaseAnnouncementSent(ctx, reserved.ID, 77, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	again, created, err = store.ReserveWorkspaceReleaseAnnouncement(ctx, "production", "0.1.0", "abc123", now.Add(2*time.Minute))
	if err != nil || created || again.Status != "sent" || again.MessageID != 77 {
		t.Fatalf("sent=%+v created=%v err=%v", again, created, err)
	}
	again, created, err = store.ReserveWorkspaceReleaseAnnouncement(ctx, "production", "0.1.0", "different-commit", now.Add(3*time.Minute))
	if err != nil || created || again.ID != reserved.ID || again.Commit != "abc123" {
		t.Fatalf("version dedupe=%+v created=%v err=%v", again, created, err)
	}
}
