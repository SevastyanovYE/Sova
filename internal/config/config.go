package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultStateDir        = ".state"
	defaultDatabasePath    = ".state/sova.db"
	defaultCooldown        = 15 * time.Minute
	defaultTimezone        = "Europe/Moscow"
	defaultDailyRunTime    = "08:00"
	defaultTelegramSession = ".sessions/sova-user.json"
	defaultOllamaURL       = "http://127.0.0.1:11434"
	defaultOllamaModel     = "qwen3:14b"
	defaultGoogleCredsPath = ".secrets/google-calendar-client.json"
	defaultGoogleTokenPath = ".secrets/google-calendar-token.json"
)

type TopicIDs struct {
	Digest   int
	Calendar int
	Status   int
	Chat     int
}

type Config struct {
	Timezone             string
	StateDir             string
	DatabasePath         string
	OverviewCooldown     time.Duration
	DailyRunTime         string
	TelegramAppID        int
	TelegramAppHash      string
	TelegramPhone        string
	TelegramSessionPath  string
	TelegramAllowedChats []string
	NestBotToken         string
	NestChatID           int64
	NestTopics           TopicIDs
	OllamaURL            string
	OllamaModel          string
	GoogleCredentials    string
	GoogleToken          string
	GoogleCalendarID     string
}

func Load() (Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return Config{}, err
	}
	cooldown, err := durationEnv("SOVA_OVERVIEW_COOLDOWN", defaultCooldown)
	if err != nil {
		return Config{}, err
	}
	appID, err := intEnv("SOVA_TELEGRAM_APP_ID")
	if err != nil {
		return Config{}, err
	}
	nestChatID, err := int64Env("SOVA_NEST_CHAT_ID")
	if err != nil {
		return Config{}, err
	}
	digestID, err := intEnv("SOVA_NEST_TOPIC_DIGEST_ID")
	if err != nil {
		return Config{}, err
	}
	calendarID, err := intEnv("SOVA_NEST_TOPIC_CALENDAR_ID")
	if err != nil {
		return Config{}, err
	}
	statusID, err := intEnv("SOVA_NEST_TOPIC_STATUS_ID")
	if err != nil {
		return Config{}, err
	}
	chatID, err := intEnv("SOVA_NEST_TOPIC_CHAT_ID")
	if err != nil {
		return Config{}, err
	}

	stateDir := valueOrDefault("SOVA_STATE_DIR", defaultStateDir)
	return Config{
		Timezone:             valueOrDefault("SOVA_TIMEZONE", defaultTimezone),
		StateDir:             stateDir,
		DatabasePath:         valueOrDefault("SOVA_DATABASE_PATH", filepath.Join(stateDir, "sova.db")),
		OverviewCooldown:     cooldown,
		DailyRunTime:         valueOrDefault("SOVA_DAILY_RUN_TIME", defaultDailyRunTime),
		TelegramAppID:        appID,
		TelegramAppHash:      strings.TrimSpace(os.Getenv("SOVA_TELEGRAM_APP_HASH")),
		TelegramPhone:        strings.TrimSpace(os.Getenv("SOVA_TELEGRAM_PHONE")),
		TelegramSessionPath:  valueOrDefault("SOVA_TELEGRAM_SESSION_PATH", defaultTelegramSession),
		TelegramAllowedChats: splitList(os.Getenv("SOVA_TELEGRAM_ALLOWED_CHATS")),
		NestBotToken:         strings.TrimSpace(os.Getenv("SOVA_NEST_BOT_TOKEN")),
		NestChatID:           nestChatID,
		NestTopics: TopicIDs{
			Digest: digestID, Calendar: calendarID, Status: statusID, Chat: chatID,
		},
		OllamaURL:         valueOrDefault("SOVA_OLLAMA_URL", defaultOllamaURL),
		OllamaModel:       valueOrDefault("SOVA_OLLAMA_MODEL", defaultOllamaModel),
		GoogleCredentials: valueOrDefault("SOVA_GOOGLE_CREDENTIALS_PATH", defaultGoogleCredsPath),
		GoogleToken:       valueOrDefault("SOVA_GOOGLE_TOKEN_PATH", defaultGoogleTokenPath),
		GoogleCalendarID:  strings.TrimSpace(os.Getenv("SOVA_GOOGLE_CALENDAR_ID")),
	}, nil
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid %s line %q", path, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("empty key in %s", path)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set %s from %s: %w", key, path, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func (c Config) ValidateFoundation() error {
	if c.OverviewCooldown < time.Minute {
		return fmt.Errorf("overview cooldown must be at least 1 minute")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", c.Timezone, err)
	}
	if _, err := time.Parse("15:04", c.DailyRunTime); err != nil {
		return fmt.Errorf("invalid daily run time %q: use HH:MM", c.DailyRunTime)
	}
	if strings.TrimSpace(c.DatabasePath) == "" {
		return fmt.Errorf("database path is required")
	}
	if filepath.Clean(c.TelegramSessionPath) == "." {
		return fmt.Errorf("Telegram session path is required")
	}
	lower := strings.ToLower(c.TelegramSessionPath)
	if strings.Contains(lower, "telegram desktop") || strings.Contains(lower, "tdata") {
		return fmt.Errorf("Telegram Desktop sessions are forbidden; use a dedicated Sova session path")
	}
	return nil
}

func (c Config) NestReady() bool {
	return c.NestBotToken != "" && c.NestChatID != 0 &&
		c.NestTopics.Digest > 0 && c.NestTopics.Calendar > 0 &&
		c.NestTopics.Status > 0 && c.NestTopics.Chat > 0
}

func (c Config) IsCommandTopic(threadID int) bool {
	return threadID == c.NestTopics.Chat
}

func valueOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	return parsed, nil
}

func intEnv(key string) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func int64Env(key string) (int64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func splitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
