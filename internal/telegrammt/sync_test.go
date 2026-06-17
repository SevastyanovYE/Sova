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
		{
			SourceRef:   "telegram:channel:100",
			SourceTitle: "Study Chat",
			ChatID:      100,
			MessageID:   43,
			Date:        time.Date(2026, 6, 17, 7, 31, 0, 0, time.UTC),
			Kind:        "message",
			MediaType:   "messageMediaPhoto",
			SourceLink:  "https://t.me/study/43",
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
		"[#43](https://t.me/study/43): \\[messageMediaPhoto\\]",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("index missing %q:\n%s", want, content)
		}
	}
}

func TestSourceMessageLink(t *testing.T) {
	tests := []struct {
		name      string
		source    sqlitestore.TelegramSource
		messageID int
		want      string
	}{
		{
			name:      "public username",
			source:    sqlitestore.TelegramSource{PeerKind: "channel", ChatID: 3000967982, Username: "important_biofiz"},
			messageID: 223,
			want:      "https://t.me/important_biofiz/223",
		},
		{
			name:      "private channel internal id",
			source:    sqlitestore.TelegramSource{PeerKind: "channel", ChatID: 2922087105},
			messageID: 13154,
			want:      "https://t.me/c/2922087105/13154",
		},
		{
			name:      "plain chat has no public link",
			source:    sqlitestore.TelegramSource{PeerKind: "chat", ChatID: 100},
			messageID: 42,
			want:      "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourceMessageLink(tt.source, tt.messageID); got != tt.want {
				t.Fatalf("sourceMessageLink() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAppendTelegramRawJSONLAppendOnly(t *testing.T) {
	stateDir := t.TempDir()
	cfg := config.Config{StateDir: stateDir}
	path := filepath.Join(stateDir, "raw", "telegram", "messages.jsonl")

	if err := appendTelegramRawJSONL(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty append created raw file or unexpected stat error: %v", err)
	}

	firstBatch := []sqlitestore.TelegramMessage{
		{MessageID: 1, RawJSON: `{"message_id":1}`},
		{MessageID: 2, RawJSON: `{"message_id":2}`},
	}
	if err := appendTelegramRawJSONL(cfg, firstBatch); err != nil {
		t.Fatal(err)
	}
	if got := rawJSONLLineCount(t, path); got != 2 {
		t.Fatalf("raw line count after first batch = %d", got)
	}
	if err := appendTelegramRawJSONL(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if got := rawJSONLLineCount(t, path); got != 2 {
		t.Fatalf("raw line count after empty append = %d", got)
	}
	if err := appendTelegramRawJSONL(cfg, []sqlitestore.TelegramMessage{{MessageID: 3, RawJSON: `{"message_id":3}`}}); err != nil {
		t.Fatal(err)
	}
	if got := rawJSONLLineCount(t, path); got != 3 {
		t.Fatalf("raw line count after second batch = %d", got)
	}
}

func rawJSONLLineCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}
