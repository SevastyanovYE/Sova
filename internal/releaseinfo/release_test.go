package releaseinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestChangelogItemsStopsAtNextRelease(t *testing.T) {
	items, err := ChangelogItems("# Changelog\n\n## [0.2.0]\n\n### Added\n- Один\n### Fixed\n- Два\n\n## [0.1.0]\n- Старое\n", "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(items, "|"); got != "Один|Два" {
		t.Fatalf("unexpected items: %s", got)
	}
}

func TestVerifyTagRequiresExactCommit(t *testing.T) {
	repo := t.TempDir()
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "config", "user.email", "sova@example.invalid")
	runGitTest(t, repo, "config", "user.name", "Sova Test")
	path := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "file.txt")
	runGitTest(t, repo, "commit", "-m", "one")
	first := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	runGitTest(t, repo, "tag", "v0.1.0")
	if err := VerifyTag(repo, "v0.1.0", first); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "file.txt")
	runGitTest(t, repo, "commit", "-m", "two")
	if err := VerifyTag(repo, "v0.1.0", "HEAD"); err == nil {
		t.Fatal("tag pointing at another commit was accepted")
	}
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func TestRenderAnnouncement(t *testing.T) {
	text, err := RenderAnnouncement("0.1.0", []string{"Добавили поиск"}, DiffStats{Commits: 3, Files: 4, Insertions: 25, Deletions: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"🚀 Встречайте: Sova 0.1.0", "— Добавили поиск", "3 коммита, 4 файла, +25 строк и −2 строки"} {
		if !strings.Contains(text, want) {
			t.Fatalf("announcement %q does not contain %q", text, want)
		}
	}
}

func TestRussianCountLabel(t *testing.T) {
	tests := []struct {
		value int
		want  string
	}{{1, "коммит"}, {2, "коммита"}, {5, "коммитов"}, {11, "коммитов"}, {21, "коммит"}, {24, "коммита"}}
	for _, test := range tests {
		if got := russianCountLabel(test.value, "коммит", "коммита", "коммитов"); got != test.want {
			t.Fatalf("label(%d) = %q, want %q", test.value, got, test.want)
		}
	}
}
