package indexes

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestWriteRunsIndex(t *testing.T) {
	stateDir := t.TempDir()
	cfg := config.Config{StateDir: stateDir, Timezone: "Europe/Moscow"}
	started := time.Date(2026, 6, 17, 7, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	err := WriteRunsIndex(cfg, []sqlitestore.Run{{
		ID:         2,
		Trigger:    "manual",
		Status:     "success",
		StartedAt:  started,
		FinishedAt: &finished,
		Summary:    "telegram sync completed; no new messages",
	}}, started)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(RunsIndexPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"# Overview Runs",
		"`2` `success` trigger=`manual`",
		"summary=\"telegram sync completed; no new messages\"",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("runs index missing %q:\n%s", want, content)
		}
	}
}

func TestWriteCalendarIndex(t *testing.T) {
	stateDir := t.TempDir()
	cfg := config.Config{StateDir: stateDir, Timezone: "Europe/Moscow", GoogleCalendarID: "primary"}
	start := time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC)
	if err := WriteCalendarIndex(cfg, []sqlitestore.CalendarCandidate{{
		ID:         1,
		Status:     "pending",
		Title:      "[ОММ] Экзамен",
		StartAt:    start,
		EndAt:      start.Add(time.Hour),
		SourceLink: "https://t.me/c/100/42",
	}}, time.Date(2026, 6, 17, 7, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(CalendarIndexPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"# Calendar Index",
		"only after approval",
		"`1` `pending` [ОММ] Экзамен",
		"https://t.me/c/100/42",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("calendar index missing %q:\n%s", want, content)
		}
	}
}
