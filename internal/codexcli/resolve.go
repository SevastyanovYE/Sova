package codexcli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var macAppCandidates = []string{
	"/Applications/Codex.app/Contents/Resources/codex",
	"~/Applications/Codex.app/Contents/Resources/codex",
}

func Resolve(configuredPath string) (string, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath != "" {
		return resolveCandidate(configuredPath)
	}
	if path, err := exec.LookPath("codex"); err == nil {
		return path, nil
	}
	for _, candidate := range macAppCandidates {
		path, err := resolveCandidate(candidate)
		if err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("Codex CLI not found; install Codex or set SOVA_CODEX_PATH")
}

func resolveCandidate(candidate string) (string, error) {
	candidate = expandHome(strings.TrimSpace(candidate))
	if candidate == "" {
		return "", fmt.Errorf("Codex CLI path is empty")
	}
	if !strings.ContainsRune(candidate, os.PathSeparator) {
		path, err := exec.LookPath(candidate)
		if err != nil {
			return "", fmt.Errorf("Codex CLI %q not found", candidate)
		}
		return path, nil
	}
	path, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve Codex CLI path: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("Codex CLI %q is unavailable: %w", path, err)
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("Codex CLI %q is not executable", path)
	}
	return path, nil
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}
