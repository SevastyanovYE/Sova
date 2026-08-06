package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyOverviewEnabledDefaultsOnAndPersists(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "sova.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	enabled, err := store.DailyOverviewEnabled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("new databases must preserve the existing enabled-by-default scheduler behavior")
	}

	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	if err := store.SetDailyOverviewEnabled(ctx, false, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enabled, err = store.DailyOverviewEnabled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("disabled setting did not survive reopening the database")
	}

	if err := store.SetDailyOverviewEnabled(ctx, true, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	enabled, err = store.DailyOverviewEnabled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("daily overview setting was not re-enabled")
	}
}

func TestDailyOverviewEnabledRejectsInvalidPersistedValue(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO nest_settings(key, value, updated_at)
		VALUES (?, ?, ?)
	`, dailyOverviewEnabledKey, "maybe", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	enabled, err := store.DailyOverviewEnabled(ctx)
	if err == nil {
		t.Fatal("expected invalid state to fail closed")
	}
	if enabled {
		t.Fatal("invalid state must not enable the scheduler")
	}
}
