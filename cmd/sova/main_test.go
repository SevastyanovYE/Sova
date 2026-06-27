package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/qwen"
)

func TestBenchmarkModelsAlwaysIncludes14B(t *testing.T) {
	models := benchmarkModels("gemma3:4b,qwen3:8b,qwen3:14b")
	want := []string{"qwen3:14b", "gemma3:4b", "qwen3:8b"}
	if len(models) != len(want) {
		t.Fatalf("models = %v", models)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models = %v, want %v", models, want)
		}
	}
}

func TestNestTopicIntroRequests(t *testing.T) {
	cfg := config.Config{
		NestChatID: -1001,
		NestTopics: config.TopicIDs{
			Chat: 2, Digest: 4, Calendar: 6, Status: 8,
		},
	}
	requests := nestTopicIntroRequests(cfg)
	if len(requests) != 4 {
		t.Fatalf("requests = %d", len(requests))
	}
	threads := []int{2, 4, 6, 8}
	for i, request := range requests {
		if request.ChatID != cfg.NestChatID || request.MessageThreadID != threads[i] {
			t.Fatalf("request[%d] target = %+v", i, request)
		}
	}
	if requests[0].ReplyMarkup == nil || !strings.Contains(requests[0].Text, "/run") {
		t.Fatalf("chat intro = %+v", requests[0])
	}
	for i, request := range requests {
		if request.ParseMode != "HTML" {
			t.Fatalf("request[%d] parse mode = %q", i, request.ParseMode)
		}
	}
	if !strings.Contains(requests[2].Text, "Изменить дату") || !strings.Contains(requests[2].Text, "2026-06-28") {
		t.Fatalf("calendar intro = %q", requests[2].Text)
	}
}

func TestWriteQwenBenchmarkIndex(t *testing.T) {
	cfg := config.Config{StateDir: t.TempDir(), Timezone: "Europe/Moscow"}
	path, err := writeQwenBenchmarkIndex(cfg, 6, ".state/artifacts/qwen-benchmark-test.jsonl", []qwen.CalibrationResult{
		{Model: "qwen3:14b", BatchSize: 8, JSONValid: true, DurationMillis: 1000, Kept: 2, Important: 1, Events: 1},
		{Model: "qwen3:14b", BatchSize: 16, Error: "context deadline exceeded", DurationMillis: 90000},
		{Model: "gemma3:4b", BatchSize: 8, JSONValid: true, DurationMillis: 700, Kept: 1},
	}, time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"# Qwen Benchmark", "`qwen3:14b`", "`gemma3:4b`", "| `qwen3:14b` | 2 | 1 | 1 | 91000 | 2 | 1 | 1 |"} {
		if !strings.Contains(content, want) {
			t.Fatalf("benchmark index missing %q:\n%s", want, content)
		}
	}
}

func TestScoreQwenEval(t *testing.T) {
	labels := []qwenEvalLabel{
		{ID: "a", ExpectedKeep: true, ExpectedImportance: 2, ExpectedHasEvent: true},
		{ID: "b", ExpectedKeep: false, ExpectedImportance: 0, ExpectedHasEvent: false},
		{ID: "c", ExpectedKeep: true, ExpectedImportance: 3, ExpectedHasEvent: false},
	}
	predictions := map[string]qwen.MessageDecision{
		"a": {ID: "a", Keep: true, Importance: 2, HasEvent: false},
		"b": {ID: "b", Keep: true, Importance: 2, HasEvent: true},
	}
	var result qwenEvalResult
	scoreQwenEval(labels, predictions, &result)
	if result.ExpectedKeep != 2 || result.ExpectedImportant != 2 || result.ExpectedEvents != 1 {
		t.Fatalf("expected counts = %+v", result)
	}
	if result.KeepTP != 1 || result.KeepFP != 1 || result.KeepFN != 1 {
		t.Fatalf("keep counts = %+v", result)
	}
	if result.ImportantTP != 1 || result.ImportantFP != 1 || result.ImportantFN != 1 {
		t.Fatalf("important counts = %+v", result)
	}
	if result.EventTP != 0 || result.EventFP != 1 || result.EventFN != 1 {
		t.Fatalf("event counts = %+v", result)
	}
	if result.MissingDecisions != 1 {
		t.Fatalf("missing decisions = %d", result.MissingDecisions)
	}
}
