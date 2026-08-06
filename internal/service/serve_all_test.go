package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestRunComponentsCancelsSiblingAndWritesHeartbeat(t *testing.T) {
	heartbeatPath := filepath.Join(t.TempDir(), "health", "heartbeat.json")
	siblingStopped := make(chan struct{})
	err := runComponents(context.Background(), heartbeatPath,
		func(context.Context) error { return context.DeadlineExceeded },
		func(ctx context.Context) error {
			<-ctx.Done()
			close(siblingStopped)
			return ctx.Err()
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runComponents error = %v", err)
	}
	select {
	case <-siblingStopped:
	default:
		t.Fatal("sibling component was not stopped")
	}
	if _, err := os.Stat(heartbeatPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("heartbeat was not removed after exit: %v", err)
	}
}

func TestRunComponentsTreatsNilReturnAsUnexpected(t *testing.T) {
	heartbeatPath := filepath.Join(t.TempDir(), "health", "heartbeat.json")
	err := runComponents(context.Background(), heartbeatPath, func(context.Context) error { return nil })
	if err == nil || err.Error() != "service component stopped unexpectedly" {
		t.Fatalf("runComponents error = %v", err)
	}
}

func TestHealthCheckVerifiesLiveHeartbeatAndSQLite(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DatabasePath:  filepath.Join(dir, "sova.db"),
		HeartbeatPath: filepath.Join(dir, "health", "heartbeat.json"),
	}
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	now := time.Now().UTC()
	if err := writeHeartbeat(cfg.HeartbeatPath, heartbeat{PID: os.Getpid(), StartedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := HealthCheck(context.Background(), cfg, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := HealthCheck(context.Background(), cfg, now.Add(3*time.Minute)); err == nil {
		t.Fatal("stale heartbeat was accepted")
	}
}
