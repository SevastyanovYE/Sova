package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestValidateResultMarksMissingDecisionsIncomplete(t *testing.T) {
	err := validateResult(
		[]MessageInput{{ID: "a"}, {ID: "b"}},
		BatchResult{Decisions: []MessageDecision{{ID: "a"}}},
	)
	var incomplete *IncompleteResultError
	if !errors.As(err, &incomplete) || incomplete.Returned != 1 || incomplete.Expected != 2 {
		t.Fatalf("error = %#v", err)
	}
}

func TestClassifyBatchUsesBoundedOllamaOptionsAndFillsDefaults(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":                "qwen3:14b",
			"response":             `{"decisions":[{"id":"a","keep":true,"importance":2,"has_event":true}]}`,
			"total_duration":       1200000000,
			"prompt_eval_count":    40,
			"prompt_eval_duration": 100000000,
			"eval_count":           20,
			"eval_duration":        300000000,
		})
	}))
	defer server.Close()

	result, _, metrics, err := New(server.URL, "qwen3:14b").ClassifyBatchWithMetrics(
		context.Background(),
		[]MessageInput{{ID: "a", Kind: "message", Text: "Экзамен завтра"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if requestBody["think"] != false {
		t.Fatalf("think = %#v", requestBody["think"])
	}
	options, ok := requestBody["options"].(map[string]any)
	if !ok {
		t.Fatalf("options = %#v", requestBody["options"])
	}
	if options["num_ctx"].(float64) != defaultNumContext || options["num_predict"].(float64) != defaultClassifyNumPredict {
		t.Fatalf("options = %#v", options)
	}
	if len(result.Decisions) != 1 || result.Decisions[0].Reason == "" || len(result.Decisions[0].Tags) == 0 {
		t.Fatalf("decision defaults were not filled: %+v", result.Decisions)
	}
	if metrics.EvalCount != 20 || metrics.PromptEvalCount != 40 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

func TestBuildEventPrompt(t *testing.T) {
	prompt, err := BuildEventPrompt([]EventInput{{
		ID:         "telegram:100:42",
		SourceRef:  "telegram:channel:100",
		SourceLink: "https://t.me/c/100/42",
		Text:       "Экзамен завтра в 10:00 в 504",
	}}, time.Date(2026, 6, 17, 7, 0, 0, 0, time.UTC), "Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Return JSON only",
		"Europe/Moscow",
		"telegram:100:42",
		"RFC3339",
		"has_event",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("event prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestExtractEventsAcceptsPartialResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":    "qwen3:14b",
			"response": `{"events":[{"id":"a","has_event":true,"title":"Экзамен","start":"2026-06-18T10:00:00+03:00","end":"","location":"","description":"Экзамен","confidence":"medium","missing":[]}]}`,
		})
	}))
	defer server.Close()

	result, _, err := New(server.URL, "qwen3:14b").ExtractEvents(
		context.Background(),
		[]EventInput{{ID: "a", Text: "Экзамен 18 июня"}, {ID: "b", Text: "спасибо"}},
		time.Date(2026, 6, 17, 7, 0, 0, 0, time.UTC),
		"Europe/Moscow",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].ID != "a" {
		t.Fatalf("events = %+v", result.Events)
	}
}

func TestValidateEventResult(t *testing.T) {
	inputs := []EventInput{{ID: "a"}, {ID: "b"}}
	if err := validateEventResult(inputs, EventExtractionResult{Events: []EventCandidate{{ID: "a"}, {ID: "b"}}}); err != nil {
		t.Fatal(err)
	}
	if err := validateEventResult(inputs, EventExtractionResult{Events: []EventCandidate{{ID: "a"}}}); err != nil {
		t.Fatalf("partial event result should be accepted: %v", err)
	}
	if err := validateEventResult(inputs, EventExtractionResult{Events: []EventCandidate{{ID: "a"}, {ID: "x"}}}); err == nil {
		t.Fatal("expected unknown id validation error")
	}
	if err := validateEventResult(inputs, EventExtractionResult{Events: []EventCandidate{{ID: "a"}, {ID: "a"}}}); err == nil {
		t.Fatal("expected duplicate id validation error")
	}
}

func TestParseBatchSizesDefault(t *testing.T) {
	sizes, err := ParseBatchSizes("")
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) == 0 || sizes[0] != 8 {
		t.Fatalf("unexpected sizes: %v", sizes)
	}
}
