package doctor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
)

type Check struct {
	Name    string
	Status  string
	Message string
}

func Run(ctx context.Context, cfg config.Config) []Check {
	checks := []Check{
		commandCheck("go", "go"),
		commandCheck("sqlite3", "sqlite3"),
		commandCheck("ffmpeg", "ffmpeg"),
		commandCheck("tesseract", "tesseract"),
		commandCheck("codex", "codex"),
		commandCheck("ollama", "ollama"),
		pathParentCheck("database", cfg.DatabasePath),
		sessionPathCheck(cfg.TelegramSessionPath),
	}
	checks = append(checks,
		configuredCheck("telegram_credentials", cfg.TelegramAppID != 0 && cfg.TelegramAppHash != "" && cfg.TelegramPhone != "", "set Telegram app ID, hash, and phone"),
		configuredCheck("telegram_sources", len(cfg.TelegramAllowedChats) > 0, "set at least one allowlisted Telegram source"),
		configuredCheck("nest", cfg.NestReady(), "set bot token, Nest chat ID, and all four topic IDs"),
		configuredCheck("google_calendar", cfg.GoogleCalendarID != "" && fileExists(cfg.GoogleCredentials), "set calendar ID and OAuth Desktop credentials"),
		configuredCheck("google_calendar_token", fileExists(cfg.GoogleToken), "run `sova google-login` after setting OAuth credentials"),
	)
	checks = append(checks, ollamaCheck(ctx, cfg))
	return checks
}

func commandCheck(name, command string) Check {
	path, err := exec.LookPath(command)
	if err != nil {
		return Check{Name: name, Status: "missing", Message: command + " not found"}
	}
	return Check{Name: name, Status: "ok", Message: path}
}

func configuredCheck(name string, ready bool, missingMessage string) Check {
	if ready {
		return Check{Name: name, Status: "ok", Message: "configured"}
	}
	return Check{Name: name, Status: "needs_input", Message: missingMessage}
}

func pathParentCheck(name, path string) Check {
	parent := "."
	if index := strings.LastIndex(path, string(os.PathSeparator)); index >= 0 {
		parent = path[:index]
	}
	if parent == "" {
		parent = "."
	}
	return Check{Name: name, Status: "ok", Message: "parent=" + parent}
}

func sessionPathCheck(path string) Check {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "telegram desktop") || strings.Contains(lower, "tdata") {
		return Check{Name: "telegram_session_path", Status: "error", Message: "Telegram Desktop session path is forbidden"}
	}
	return Check{Name: "telegram_session_path", Status: "ok", Message: path}
}

func ollamaCheck(ctx context.Context, cfg config.Config) Check {
	if _, err := exec.LookPath("ollama"); err != nil {
		return Check{Name: "ollama_model", Status: "missing", Message: "install Ollama and run: ollama run " + cfg.OllamaModel}
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(cfg.OllamaURL, "/")+"/api/tags", nil)
	if err != nil {
		return Check{Name: "ollama_model", Status: "error", Message: err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Check{Name: "ollama_model", Status: "missing", Message: "Ollama is not reachable at " + cfg.OllamaURL}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return Check{Name: "ollama_model", Status: "error", Message: resp.Status}
	}
	return Check{Name: "ollama_model", Status: "ok", Message: "Ollama reachable; required model=" + cfg.OllamaModel}
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
