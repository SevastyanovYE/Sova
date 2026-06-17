package qwen

import (
	"testing"
)

func TestBuildPromptRequiresIDs(t *testing.T) {
	if _, err := BuildPrompt([]MessageInput{{Text: "привет"}}); err == nil {
		t.Fatal("expected missing id error")
	}
}

func TestValidateResultRejectsUnknownID(t *testing.T) {
	err := validateResult(
		[]MessageInput{{ID: "a"}},
		BatchResult{Decisions: []MessageDecision{{ID: "b"}}},
	)
	if err == nil {
		t.Fatal("expected unknown id error")
	}
}

func TestParseBatchSizesDefault(t *testing.T) {
	sizes, err := ParseBatchSizes("")
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) == 0 || sizes[0] != 4 {
		t.Fatalf("unexpected sizes: %v", sizes)
	}
}
