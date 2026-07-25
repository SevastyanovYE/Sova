package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOverviewPublicationClaimsAndRecoversWithoutDuplicate(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 0)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := store.EnsureOverviewPublication(ctx, run.ID, "digest", 0, "hash-a", now)
	if err != nil || publication.Status != "pending" {
		t.Fatalf("ensure=%+v err=%v", publication, err)
	}
	again, err := store.EnsureOverviewPublication(ctx, run.ID, "digest", 0, "hash-a", now)
	if err != nil || again.ID != publication.ID {
		t.Fatalf("second ensure=%+v err=%v", again, err)
	}
	if _, err := store.EnsureOverviewPublication(ctx, run.ID, "digest", 0, "changed", now); err == nil {
		t.Fatal("changed content hash accepted")
	}
	claimed, ok, err := store.ClaimOverviewPublication(ctx, publication.ID, now.Add(time.Second))
	if err != nil || !ok || claimed.Status != "sending" || claimed.Attempts != 1 {
		t.Fatalf("claim=%+v ok=%t err=%v", claimed, ok, err)
	}
	recovered, err := store.RecoverInterruptedOverviewPublications(ctx, run.ID, now.Add(2*time.Second))
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	claimed, ok, err = store.ClaimOverviewPublication(ctx, publication.ID, now.Add(3*time.Second))
	if err != nil || ok || claimed.Status != "unknown" || claimed.Attempts != 1 {
		t.Fatalf("claim after recovery=%+v ok=%t err=%v", claimed, ok, err)
	}
}

func TestOverviewPublicationConfirmedFailureCanRetryAndSentCannot(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 0)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := store.EnsureOverviewPublication(ctx, run.ID, "calendar", 42, "hash-b", now)
	if err != nil {
		t.Fatal(err)
	}
	publication, ok, err := store.ClaimOverviewPublication(ctx, publication.ID, now)
	if err != nil || !ok {
		t.Fatalf("first claim ok=%t err=%v", ok, err)
	}
	if err := store.MarkOverviewPublicationRetry(ctx, publication.ID, "confirmed rejection", now); err != nil {
		t.Fatal(err)
	}
	publication, ok, err = store.ClaimOverviewPublication(ctx, publication.ID, now.Add(time.Second))
	if err != nil || !ok || publication.Attempts != 2 {
		t.Fatalf("retry claim=%+v ok=%t err=%v", publication, ok, err)
	}
	if err := store.MarkOverviewPublicationSent(ctx, publication.ID, -100, 7, 99, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	publication, ok, err = store.ClaimOverviewPublication(ctx, publication.ID, now.Add(3*time.Second))
	if err != nil || ok || publication.Status != "sent" || publication.MessageID != 99 {
		t.Fatalf("sent claim=%+v ok=%t err=%v", publication, ok, err)
	}
}

func TestUnknownOverviewPublicationRequiresExplicitResolution(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	run, err := store.TryStartOverview(ctx, "manual", now, 0)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := store.EnsureOverviewPublication(ctx, run.ID, "digest", 0, "hash", now)
	if err != nil {
		t.Fatal(err)
	}
	publication, claimed, err := store.ClaimOverviewPublication(ctx, publication.ID, now)
	if err != nil || !claimed {
		t.Fatalf("claim=%+v claimed=%t err=%v", publication, claimed, err)
	}
	if err := store.MarkOverviewPublicationUnknown(ctx, publication.ID, "ambiguous", now); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveUnknownOverviewPublication(ctx, run.ID, "digest", 0, "sent", -100, 2, 77, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	rows, err := store.OverviewPublicationsByRun(ctx, run.ID)
	if err != nil || len(rows) != 1 || rows[0].Status != "sent" || rows[0].MessageID != 77 {
		t.Fatalf("resolved=%+v err=%v", rows, err)
	}
	if err := store.ResolveUnknownOverviewPublication(ctx, run.ID, "digest", 0, "retry", 0, 0, 0, now); err == nil {
		t.Fatal("already resolved publication changed again")
	}
}
