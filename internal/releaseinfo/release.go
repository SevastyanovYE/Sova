package releaseinfo

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type DiffStats struct {
	Commits    int
	Files      int
	Insertions int
	Deletions  int
}

func LoadTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return value, nil
}

// ChangelogItems returns bullet text from the requested release section. The
// Unreleased section is accepted for a pre-tag dry run of the VERSION value.
func ChangelogItems(markdown, version string) ([]string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, fmt.Errorf("version is required")
	}
	wanted := "## [" + version + "]"
	inSection := false
	var items []string
	scanner := bufio.NewScanner(strings.NewReader(markdown))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "## [") {
			if inSection {
				break
			}
			inSection = strings.HasPrefix(line, wanted)
			continue
		}
		if inSection && strings.HasPrefix(line, "- ") {
			item := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if item != "" {
				items = append(items, item)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("changelog section %q has no release items", version)
	}
	return items, nil
}

func UnreleasedItems(markdown string) ([]string, error) {
	const marker = "## [Unreleased]"
	inSection := false
	var items []string
	scanner := bufio.NewScanner(strings.NewReader(markdown))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "## [") {
			if inSection {
				break
			}
			inSection = line == marker
			continue
		}
		if inSection && strings.HasPrefix(line, "- ") {
			item := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if item != "" {
				items = append(items, item)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("Unreleased changelog section has no release items")
	}
	return items, nil
}

func RenderAnnouncement(version string, items []string, stats DiffStats) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", fmt.Errorf("version is required")
	}
	if len(items) == 0 {
		return "", fmt.Errorf("release items are required")
	}
	var body strings.Builder
	fmt.Fprintf(&body, "🚀 Встречайте: Sova %s\n\n", version)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		fmt.Fprintf(&body, "— %s\n", item)
	}
	fmt.Fprintf(&body, "\n— итоговый diff: %d %s, %d %s, +%d %s и −%d %s",
		stats.Commits, russianCountLabel(stats.Commits, "коммит", "коммита", "коммитов"),
		stats.Files, russianCountLabel(stats.Files, "файл", "файла", "файлов"),
		stats.Insertions, russianCountLabel(stats.Insertions, "строка", "строки", "строк"),
		stats.Deletions, russianCountLabel(stats.Deletions, "строка", "строки", "строк"))
	return body.String(), nil
}

func russianCountLabel(value int, one, few, many string) string {
	if value < 0 {
		value = -value
	}
	if lastTwo := value % 100; lastTwo >= 11 && lastTwo <= 14 {
		return many
	}
	switch value % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	default:
		return many
	}
}

func ComputeGitStats(repoDir, base, head string) (DiffStats, error) {
	base, head = strings.TrimSpace(base), strings.TrimSpace(head)
	if base == "" || head == "" {
		return DiffStats{}, fmt.Errorf("base and head commits are required")
	}
	countRaw, err := gitOutput(repoDir, "rev-list", "--count", base+".."+head)
	if err != nil {
		return DiffStats{}, err
	}
	commits, err := strconv.Atoi(strings.TrimSpace(string(countRaw)))
	if err != nil {
		return DiffStats{}, fmt.Errorf("parse commit count: %w", err)
	}
	numstat, err := gitOutput(repoDir, "diff", "--numstat", base, head)
	if err != nil {
		return DiffStats{}, err
	}
	stats := DiffStats{Commits: commits}
	scanner := bufio.NewScanner(bytes.NewReader(numstat))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 3 {
			continue
		}
		stats.Files++
		if fields[0] != "-" {
			value, parseErr := strconv.Atoi(fields[0])
			if parseErr != nil {
				return DiffStats{}, fmt.Errorf("parse insertions for %s: %w", fields[2], parseErr)
			}
			stats.Insertions += value
		}
		if fields[1] != "-" {
			value, parseErr := strconv.Atoi(fields[1])
			if parseErr != nil {
				return DiffStats{}, fmt.Errorf("parse deletions for %s: %w", fields[2], parseErr)
			}
			stats.Deletions += value
		}
	}
	if err := scanner.Err(); err != nil {
		return DiffStats{}, err
	}
	return stats, nil
}

func RepoFile(repoDir, name string) string {
	return filepath.Join(repoDir, name)
}

func VerifyTag(repoDir, tag, commit string) error {
	tag, commit = strings.TrimSpace(tag), strings.TrimSpace(commit)
	if tag == "" || commit == "" {
		return fmt.Errorf("tag and commit are required")
	}
	tagCommit, err := gitOutput(repoDir, "rev-parse", tag+"^{commit}")
	if err != nil {
		return fmt.Errorf("release tag %s is missing or invalid: %w", tag, err)
	}
	wantedCommit, err := gitOutput(repoDir, "rev-parse", commit+"^{commit}")
	if err != nil {
		return fmt.Errorf("release commit %s is invalid: %w", commit, err)
	}
	if strings.TrimSpace(string(tagCommit)) != strings.TrimSpace(string(wantedCommit)) {
		return fmt.Errorf("release tag %s does not point at commit %s", tag, commit)
	}
	return nil
}

func gitOutput(repoDir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}
