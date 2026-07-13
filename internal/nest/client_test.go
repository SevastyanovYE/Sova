package nest

import (
	"strings"
	"testing"
)

func TestSplitMessageText(t *testing.T) {
	text := strings.Repeat("a", 10) + "\n" + strings.Repeat("b", 10) + "\n" + strings.Repeat("c", 10)
	parts := SplitMessageText(text, 15)
	if len(parts) != 3 {
		t.Fatalf("parts = %d: %#v", len(parts), parts)
	}
	for _, part := range parts {
		if len([]rune(part)) > 15 {
			t.Fatalf("part too long: %q", part)
		}
	}
	if strings.Join(parts, "") == "" {
		t.Fatal("split produced empty content")
	}
}

func TestRedactBotToken(t *testing.T) {
	token := "123:secret"
	input := `Post "https://api.telegram.org/bot123:secret/getUpdates": context deadline exceeded`
	got := redactBotToken(input, token)
	if strings.Contains(got, token) {
		t.Fatalf("token was not redacted: %s", got)
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("redaction marker missing: %s", got)
	}
}

func TestApplyMessagePreviewPolicy(t *testing.T) {
	payload := map[string]any{"text": "https://example.com"}
	applyMessagePreviewPolicy(payload)
	if got, ok := payload["disable_web_page_preview"].(bool); !ok || !got {
		t.Fatalf("disable_web_page_preview = %#v", payload["disable_web_page_preview"])
	}
}
