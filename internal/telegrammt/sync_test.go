package telegrammt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestWriteTelegramRecentIndex(t *testing.T) {
	stateDir := t.TempDir()
	cfg := config.Config{StateDir: stateDir, Timezone: "Europe/Moscow"}
	err := WriteTelegramRecentIndex(cfg, []sqlitestore.TelegramRecentMessage{
		{
			SourceRef:   "telegram:channel:100",
			SourceTitle: "Study Chat",
			ChatID:      100,
			MessageID:   42,
			Date:        time.Date(2026, 6, 17, 7, 30, 0, 0, time.UTC),
			Kind:        "message",
			Text:        "Экзамен завтра в 10:00\nаудитория 504",
			SourceLink:  "https://t.me/study/42",
		},
	}, time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(stateDir, "index", "telegram-recent.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"# Recent Telegram Content",
		"## Study Chat",
		"[#42](https://t.me/study/42)",
		"Экзамен завтра в 10:00 аудитория 504",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("index missing %q:\n%s", want, content)
		}
	}
}
