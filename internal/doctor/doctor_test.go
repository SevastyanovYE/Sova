package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestRunUsesEmbeddedRuntimeChecksNotBuildTools(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "sessions", "sova-user.json")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		StateDir:            filepath.Join(dir, "state"),
		DatabasePath:        filepath.Join(dir, "state", "sova.db"),
		HeartbeatPath:       filepath.Join(dir, "state", "health", "heartbeat.json"),
		TelegramSessionPath: sessionPath,
	}
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	checks := Run(context.Background(), cfg)
	for _, check := range checks {
		switch check.Name {
		case "go", "sqlite3", "ffmpeg", "tesseract", "codex":
			t.Fatalf("production doctor still requires build/local tool %q", check.Name)
		}
	}
	if checks[0].Name != "database" || checks[0].Status != "ok" {
		t.Fatalf("database check = %+v", checks[0])
	}
}

func TestRunDoesNotCreateMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "missing", "sova.db")
	checks := Run(context.Background(), config.Config{
		StateDir:            filepath.Join(dir, "state"),
		DatabasePath:        databasePath,
		HeartbeatPath:       filepath.Join(dir, "state", "health", "heartbeat.json"),
		TelegramSessionPath: filepath.Join(dir, "missing-session.json"),
	})
	if checks[0].Name != "database" || checks[0].Status != "needs_input" {
		t.Fatalf("database check = %+v", checks[0])
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("doctor created missing database: %v", err)
	}
}
