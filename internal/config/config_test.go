package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	t.Setenv("SOVA_OVERVIEW_COOLDOWN", "")
	t.Setenv("SOVA_TELEGRAM_SESSION_PATH", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OverviewCooldown != 15*time.Minute {
		t.Fatalf("cooldown = %v", cfg.OverviewCooldown)
	}
	if cfg.OllamaModel != "qwen3:14b" {
		t.Fatalf("model = %q", cfg.OllamaModel)
	}
	if err := cfg.ValidateFoundation(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsTelegramDesktopSession(t *testing.T) {
	cfg := Config{
		Timezone:            "Europe/Moscow",
		DatabasePath:        ".state/sova.db",
		OverviewCooldown:    15 * time.Minute,
		DailyRunTime:        "08:00",
		TelegramSessionPath: "/tmp/Telegram Desktop/tdata",
	}
	if err := cfg.ValidateFoundation(); err == nil {
		t.Fatal("expected Telegram Desktop session rejection")
	}
}

func TestCommandsOnlyFromStatusTopic(t *testing.T) {
	cfg := Config{NestTopics: TopicIDs{Status: 10, Chat: 20}}
	if !cfg.IsCommandTopic(10) {
		t.Fatal("status topic should accept commands")
	}
	if cfg.IsCommandTopic(20) {
		t.Fatal("chat topic should not accept text commands")
	}
}

func TestNestReadyRequiresChatTopic(t *testing.T) {
	cfg := Config{
		NestBotToken: "token",
		NestChatID:   -100123,
		NestTopics:   TopicIDs{Digest: 1, Calendar: 2, Status: 3},
	}
	if cfg.NestReady() {
		t.Fatal("Nest should not be ready without Chat topic")
	}
	cfg.NestTopics.Chat = 4
	if !cfg.NestReady() {
		t.Fatal("Nest should be ready with all four topics")
	}
}

func TestNestTelegramSourcesAreSeparatedFromLegacyTelegramSources(t *testing.T) {
	t.Setenv("SOVA_NEST_TELEGRAM_ALLOWED_CHATS", "@study;https://t.me/study2")
	t.Setenv("SOVA_TELEGRAM_ALLOWED_CHATS", "@personal")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"@study", "https://t.me/study2"}
	if len(cfg.NestTelegramAllowedChats) != len(want) {
		t.Fatalf("nest sources = %#v", cfg.NestTelegramAllowedChats)
	}
	for i := range want {
		if cfg.NestTelegramAllowedChats[i] != want[i] {
			t.Fatalf("nest source %d = %q, want %q", i, cfg.NestTelegramAllowedChats[i], want[i])
		}
	}
}

func TestWorkspaceAndControlConfigured(t *testing.T) {
	cfg := Config{
		Workspace: WorkspaceConfig{
			BotToken: "token",
			ChatID:   -100200,
			Topics: WorkspaceTopicIDs{
				Inbox: 1, Tasks: 2, Notes: 3, Experience: 4,
				Useful: 5, Templates: 6, Collections: 7,
			},
		},
		Control: ControlConfig{
			BotToken: "token",
			ChatID:   -100300,
			Topics: ControlTopicIDs{
				Status: 1, Errors: 2, Runs: 3, Review: 4,
				TestLab: 5, Workspace: 6, Nest: 7, Ideas: 8,
			},
		},
	}
	if !cfg.WorkspaceConfigured() {
		t.Fatal("Workspace should be configured with all seven topic IDs")
	}
	if !cfg.ControlConfigured() {
		t.Fatal("Control should be configured with all eight topic IDs")
	}
	cfg.Workspace.Topics.Collections = 0
	if cfg.WorkspaceConfigured() {
		t.Fatal("Workspace should require Collections topic")
	}
	cfg.Control.Topics.Ideas = 0
	if cfg.ControlConfigured() {
		t.Fatal("Control should require Ideas topic")
	}
}

func TestLoadDotEnvDoesNotOverrideEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("SOVA_TEST_VALUE=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOVA_TEST_VALUE", "from-env")
	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("SOVA_TEST_VALUE"); got != "from-env" {
		t.Fatalf("value = %q", got)
	}
}
