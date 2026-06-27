package indexes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func Rebuild(ctx context.Context, cfg config.Config, store *sqlitestore.Store, generatedAt time.Time) error {
	runs, err := store.RecentRuns(ctx, 20)
	if err != nil {
		return err
	}
	if err := WriteRunsIndex(cfg, runs, generatedAt); err != nil {
		return err
	}
	candidates, err := store.RecentCalendarCandidates(ctx, 20)
	if err != nil {
		return err
	}
	if err := WriteCalendarIndex(cfg, candidates, generatedAt); err != nil {
		return err
	}
	calls, err := store.RecentModelCalls(ctx, 40)
	if err != nil {
		return err
	}
	return WriteQwenPerformanceIndex(cfg, calls, generatedAt)
}

func WriteRunsIndex(cfg config.Config, runs []sqlitestore.Run, generatedAt time.Time) error {
	path := filepath.Join(cfg.StateDir, "index", "runs.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	location := mustLocation(cfg.Timezone)
	var b strings.Builder
	b.WriteString("# Overview Runs\n\n")
	b.WriteString("Generated: ")
	b.WriteString(generatedAt.In(location).Format(time.RFC3339))
	b.WriteString("\n\n")
	if len(runs) == 0 {
		b.WriteString("No overview runs yet.\n")
		return os.WriteFile(path, []byte(b.String()), 0o600)
	}
	for _, run := range runs {
		b.WriteString("- `")
		b.WriteString(strconv.FormatInt(run.ID, 10))
		b.WriteString("` `")
		b.WriteString(run.Status)
		b.WriteString("` trigger=`")
		b.WriteString(run.Trigger)
		b.WriteString("` started=`")
		b.WriteString(run.StartedAt.In(location).Format("2006-01-02 15:04:05 MST"))
		b.WriteString("`")
		if run.FinishedAt != nil {
			b.WriteString(" finished=`")
			b.WriteString(run.FinishedAt.In(location).Format("2006-01-02 15:04:05 MST"))
			b.WriteString("`")
		}
		if run.Summary != "" {
			b.WriteString(" summary=\"")
			b.WriteString(compact(run.Summary, 180))
			b.WriteString("\"")
		}
		if run.Error != "" {
			b.WriteString(" error=\"")
			b.WriteString(compact(run.Error, 180))
			b.WriteString("\"")
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func WriteCalendarIndex(cfg config.Config, candidates []sqlitestore.CalendarCandidate, generatedAt time.Time) error {
	path := filepath.Join(cfg.StateDir, "index", "calendar.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	location := mustLocation(cfg.Timezone)
	var b strings.Builder
	b.WriteString("# Calendar Index\n\n")
	b.WriteString("Generated: ")
	b.WriteString(generatedAt.In(location).Format(time.RFC3339))
	b.WriteString("\n\n")
	b.WriteString("Calendar events must be created only after approval in the Nest Calendar topic.\n")
	if cfg.GoogleCalendarID == "" {
		b.WriteString("Google Calendar target calendar ID is not configured.\n")
	}
	if len(candidates) == 0 {
		b.WriteString("\nNo calendar candidates indexed yet.\n")
		return os.WriteFile(path, []byte(b.String()), 0o600)
	}
	b.WriteString("\n")
	for _, candidate := range candidates {
		b.WriteString("- `")
		b.WriteString(strconv.FormatInt(candidate.ID, 10))
		b.WriteString("` `")
		b.WriteString(candidate.Status)
		b.WriteString("` ")
		b.WriteString(compact(candidate.Title, 140))
		b.WriteString(" start=`")
		b.WriteString(candidate.StartAt.In(location).Format("2006-01-02 15:04 MST"))
		b.WriteString("`")
		if candidate.SourceLink != "" {
			b.WriteString(" source=")
			b.WriteString(candidate.SourceLink)
		}
		if candidate.CalendarEventID != "" {
			b.WriteString(" calendar_event_id=`")
			b.WriteString(candidate.CalendarEventID)
			b.WriteString("`")
		}
		if candidate.Error != "" {
			b.WriteString(" error=\"")
			b.WriteString(compact(candidate.Error, 180))
			b.WriteString("\"")
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func WriteQwenPerformanceIndex(cfg config.Config, calls []sqlitestore.ModelCall, generatedAt time.Time) error {
	path := QwenPerformanceIndexPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	location := mustLocation(cfg.Timezone)
	var b strings.Builder
	b.WriteString("# Qwen Performance\n\n")
	b.WriteString("Generated: ")
	b.WriteString(generatedAt.In(location).Format(time.RFC3339))
	b.WriteString("\n\n")
	b.WriteString("This index stores only compact model-call metrics, not Telegram text or prompts.\n\n")
	if len(calls) == 0 {
		b.WriteString("No Qwen calls recorded yet.\n")
		return os.WriteFile(path, []byte(b.String()), 0o600)
	}
	for _, call := range calls {
		b.WriteString("- run=`")
		b.WriteString(strconv.FormatInt(call.RunID, 10))
		b.WriteString("` stage=`")
		b.WriteString(call.Stage)
		b.WriteString("` batch=")
		b.WriteString(strconv.Itoa(call.BatchIndex))
		b.WriteString(" messages=")
		b.WriteString(strconv.Itoa(call.InputMessages))
		b.WriteString(" chars=")
		b.WriteString(strconv.Itoa(call.InputChars))
		b.WriteString(" duration_ms=")
		b.WriteString(strconv.FormatInt(call.DurationMillis, 10))
		b.WriteString(" success=")
		b.WriteString(strconv.FormatBool(call.Success))
		if call.Fallbacks > 0 {
			b.WriteString(" fallbacks=")
			b.WriteString(strconv.Itoa(call.Fallbacks))
		}
		if call.Model != "" {
			b.WriteString(" model=`")
			b.WriteString(call.Model)
			b.WriteString("`")
		}
		if call.Error != "" {
			b.WriteString(" error=\"")
			b.WriteString(compact(call.Error, 180))
			b.WriteString("\"")
		}
		b.WriteString(" at=`")
		b.WriteString(call.CreatedAt.In(location).Format("2006-01-02 15:04:05 MST"))
		b.WriteString("`\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "\"", "'")
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

func RunsIndexPath(cfg config.Config) string {
	return filepath.Join(cfg.StateDir, "index", "runs.md")
}

func CalendarIndexPath(cfg config.Config) string {
	return filepath.Join(cfg.StateDir, "index", "calendar.md")
}

func QwenPerformanceIndexPath(cfg config.Config) string {
	return filepath.Join(cfg.StateDir, "index", "qwen-performance.md")
}

func Summary(cfg config.Config) string {
	return fmt.Sprintf("updated %s, %s, and %s",
		RunsIndexPath(cfg), CalendarIndexPath(cfg), QwenPerformanceIndexPath(cfg))
}
