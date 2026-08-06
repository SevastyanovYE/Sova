package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/controller"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
	"github.com/SevastyanovYE/Sova/internal/workspace"
)

const (
	heartbeatInterval = 30 * time.Second
	heartbeatMaxAge   = 2 * time.Minute
)

type heartbeat struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type component func(context.Context) error

// ServeAll runs the Nest and Workspace controllers in one process. This avoids
// duplicating the Go runtime on memory-constrained managed hosting while
// preserving each controller's existing SQLite connection and behavior.
func ServeAll(ctx context.Context, cfg config.Config) error {
	nestComponent := func(ctx context.Context) error {
		return controller.Serve(ctx, cfg)
	}
	workspaceComponent := func(ctx context.Context) error {
		store, err := sqlitestore.Open(cfg.DatabasePath)
		if err != nil {
			return fmt.Errorf("open Workspace store: %w", err)
		}
		defer store.Close()
		return workspace.Serve(ctx, cfg, store)
	}
	return runComponents(ctx, cfg.HeartbeatPath, nestComponent, workspaceComponent)
}

func runComponents(ctx context.Context, heartbeatPath string, components ...component) error {
	if len(components) == 0 {
		return fmt.Errorf("at least one service component is required")
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	startedAt := time.Now().UTC()
	if err := writeHeartbeat(heartbeatPath, heartbeat{PID: os.Getpid(), StartedAt: startedAt, UpdatedAt: startedAt}); err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(heartbeatPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Printf("sova serve-all: heartbeat cleanup failed: %v\n", err)
		}
	}()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		heartbeatLoop(childCtx, heartbeatPath, startedAt)
	}()

	errorsByComponent := make(chan error, len(components))
	for _, runComponent := range components {
		runComponent := runComponent
		go func() { errorsByComponent <- runComponent(childCtx) }()
	}

	var firstUnexpected error
	for range components {
		componentErr := <-errorsByComponent
		if componentErr == nil && childCtx.Err() == nil {
			componentErr = fmt.Errorf("service component stopped unexpectedly")
		}
		if componentErr != nil && !errors.Is(componentErr, context.Canceled) && firstUnexpected == nil {
			firstUnexpected = componentErr
		}
		cancel()
	}
	<-heartbeatDone
	if firstUnexpected != nil {
		return firstUnexpected
	}
	return ctx.Err()
}

func heartbeatLoop(ctx context.Context, path string, startedAt time.Time) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := writeHeartbeat(path, heartbeat{PID: os.Getpid(), StartedAt: startedAt, UpdatedAt: now.UTC()}); err != nil {
				fmt.Printf("sova serve-all: heartbeat update failed: %v\n", err)
			}
		}
	}
}

func writeHeartbeat(path string, value heartbeat) error {
	if path == "" {
		return fmt.Errorf("heartbeat path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create heartbeat directory: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".heartbeat-*")
	if err != nil {
		return fmt.Errorf("create heartbeat temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect heartbeat temporary file: %w", err)
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("write heartbeat: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close heartbeat: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace heartbeat: %w", err)
	}
	return nil
}

// HealthCheck performs local, non-billable checks only. It never calls Gemini
// or Telegram.
func HealthCheck(ctx context.Context, cfg config.Config, now time.Time) error {
	data, err := os.ReadFile(cfg.HeartbeatPath)
	if err != nil {
		return fmt.Errorf("read heartbeat: %w", err)
	}
	var value heartbeat
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode heartbeat: %w", err)
	}
	if value.PID <= 0 {
		return fmt.Errorf("heartbeat PID is invalid")
	}
	if age := now.UTC().Sub(value.UpdatedAt); age < 0 || age > heartbeatMaxAge {
		return fmt.Errorf("heartbeat is stale: updated %s", value.UpdatedAt.UTC().Format(time.RFC3339))
	}
	if err := sqlitestore.QuickCheckFile(ctx, cfg.DatabasePath); err != nil {
		return err
	}
	return nil
}
