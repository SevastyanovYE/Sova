package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type Check struct {
	Name    string
	Status  string
	Message string
}

func Run(ctx context.Context, cfg config.Config) []Check {
	checks := []Check{
		databaseCheck(ctx, cfg.DatabasePath),
		directoryWriteCheck("state_directory", cfg.StateDir),
		directoryWriteCheck("heartbeat_directory", filepath.Dir(cfg.HeartbeatPath)),
		sessionPathCheck(cfg.TelegramSessionPath),
	}
	checks = append(checks,
		configuredCheck("telegram_credentials", cfg.TelegramAppID != 0 && cfg.TelegramAppHash != "" && cfg.TelegramPhone != "", "set Telegram app ID, hash, and phone"),
		configuredCheck("nest_telegram_sources", len(cfg.NestTelegramAllowedChats) > 0, "set SOVA_NEST_TELEGRAM_ALLOWED_CHATS to at least one Sova Nest study source"),
		configuredCheck("nest", cfg.NestReady(), "set bot token, Nest chat ID, and all four topic IDs"),
		configuredCheck("nest_google_models", cfg.Gemini.APIKey != "" && len(cfg.NestGoogleModels) > 0, "set SOVA_GEMINI_API_KEY and SOVA_NEST_GOOGLE_MODELS"),
		configuredCheck("digest_google_models", cfg.Gemini.APIKey != "" && strings.TrimSpace(cfg.Gemini.Model) != "", "set SOVA_GEMINI_API_KEY and SOVA_GEMINI_MODEL"),
		searchConfigCheck(cfg),
		configuredCheck("workspace_audit", cfg.WorkspaceAuditConfigured(), "set SOVA_WORKSPACE_LEGACY_SOURCE plus Telegram app ID/hash"),
		configuredCheck("workspace_group", cfg.WorkspaceConfigured(), "set Workspace bot token, InSync v1.0 chat ID, and all Workspace topic IDs"),
		configuredCheck("control_group", cfg.ControlConfigured(), "set Control bot token, chat ID, and all Control topic IDs"),
		configuredCheck("google_calendar_id", cfg.GoogleCalendarID != "", "set SOVA_GOOGLE_CALENDAR_ID"),
		configuredCheck("google_oauth_credentials", fileExists(cfg.GoogleCredentials), "place OAuth Desktop client JSON at "+cfg.GoogleCredentials),
		configuredCheck("google_calendar_token", fileExists(cfg.GoogleToken), "run `sova google-login` after setting OAuth credentials"),
	)
	return checks
}

func databaseCheck(ctx context.Context, path string) Check {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Check{Name: "database", Status: "needs_input", Message: "existing SQLite database is missing at " + path}
		}
		return Check{Name: "database", Status: "error", Message: err.Error()}
	}
	if !info.Mode().IsRegular() {
		return Check{Name: "database", Status: "error", Message: "SQLite path is not a regular file: " + path}
	}
	if err := sqlitestore.QuickCheckFile(ctx, path); err != nil {
		return Check{Name: "database", Status: "error", Message: err.Error()}
	}
	return Check{Name: "database", Status: "ok", Message: "SQLite quick_check ok"}
}

func searchConfigCheck(cfg config.Config) Check {
	if !cfg.Search.Enabled {
		return Check{Name: "semantic_search", Status: "ok", Message: "disabled until full index is ready"}
	}
	if cfg.Gemini.APIKey == "" || cfg.Search.LegacyChatID == 0 || cfg.Search.EmbeddingModel != config.DefaultSearchEmbeddingModel {
		return Check{Name: "semantic_search", Status: "needs_input", Message: "set the primary key, legacy chat ID, and gemini-embedding-2 before enabling search"}
	}
	return Check{Name: "semantic_search", Status: "ok", Message: "configured; fallback key is optional"}
}

func configuredCheck(name string, ready bool, missingMessage string) Check {
	if ready {
		return Check{Name: name, Status: "ok", Message: "configured"}
	}
	return Check{Name: name, Status: "needs_input", Message: missingMessage}
}

func directoryWriteCheck(name, path string) Check {
	if strings.TrimSpace(path) == "" {
		return Check{Name: name, Status: "error", Message: "directory path is empty"}
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return Check{Name: name, Status: "error", Message: err.Error()}
	}
	temporary, err := os.CreateTemp(path, ".sova-doctor-*")
	if err != nil {
		return Check{Name: name, Status: "error", Message: "directory is not writable: " + err.Error()}
	}
	temporaryPath := temporary.Name()
	if closeErr := temporary.Close(); closeErr != nil {
		_ = os.Remove(temporaryPath)
		return Check{Name: name, Status: "error", Message: closeErr.Error()}
	}
	if err := os.Remove(temporaryPath); err != nil {
		return Check{Name: name, Status: "error", Message: "cannot remove write probe: " + err.Error()}
	}
	return Check{Name: name, Status: "ok", Message: path}
}

func sessionPathCheck(path string) Check {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "telegram desktop") || strings.Contains(lower, "tdata") {
		return Check{Name: "telegram_session_path", Status: "error", Message: "Telegram Desktop session path is forbidden"}
	}
	if !fileExists(path) {
		return Check{Name: "telegram_session_path", Status: "needs_input", Message: "dedicated MTProto session file is missing at " + path}
	}
	return Check{Name: "telegram_session_path", Status: "ok", Message: path}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func Format(checks []Check) string {
	var builder strings.Builder
	for _, check := range checks {
		fmt.Fprintf(&builder, "%-24s %-12s %s\n", check.Name, check.Status, check.Message)
	}
	return builder.String()
}
