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
