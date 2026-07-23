package workspace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type testGeminiGenerateRequest struct {
	Contents []struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"contents"`
}

func TestGeminiNotePublishProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/models/gemini-test:generateContent") {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "test-key" || r.URL.Query().Get("key") != "" {
			t.Fatalf("missing api key")
		}
		var request testGeminiGenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Contents) != 1 || !strings.Contains(request.Contents[0].Parts[0].Text, "Первая часть") {
			t.Fatalf("request = %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{
						"text": "{\"messages\":[{\"html\":\"<b>Готово</b>\\n\\nПервая часть\",\"source_part_ids\":[\"p1\"]}]}"
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	provider := geminiNotePublishProvider{
		apiKey:     "test-key",
		model:      "gemini-test",
		endpoint:   server.URL,
		httpClient: server.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := provider.FormatNote(ctx, NotePublishRequest{
		Title: "Тест",
		Parts: []sqlitestore.WorkspaceDocumentPart{{PartNo: 1, Title: "Начало", Text: "Первая часть"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 || result.Messages[0] != "<b>Готово</b>\n\nПервая часть" {
		t.Fatalf("messages = %#v", result.Messages)
	}
	if result.Model != "gemini-test" {
		t.Fatalf("model = %q", result.Model)
	}
}

func TestGeminiNotePublishProviderFallsBackOnTemporaryErrors(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.Contains(r.URL.Path, "/models/gemini-primary:generateContent"):
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"status":"UNAVAILABLE","message":"model is overloaded"}}`))
		case strings.Contains(r.URL.Path, "/models/gemini-fallback:generateContent"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"messages\":[{\"html\":\"ok\",\"source_part_ids\":[\"p1\"]}]}"}]}}]}`))
		default:
			t.Fatalf("unexpected path = %s", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := geminiNotePublishProvider{
		apiKey:         "test-key",
		model:          "gemini-primary",
		fallbackModels: []string{"gemini-fallback"},
		endpoint:       server.URL,
		httpClient:     server.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := provider.FormatNote(ctx, NotePublishRequest{
		Title: "Тест",
		Parts: []sqlitestore.WorkspaceDocumentPart{{PartNo: 1, Title: "Начало", Text: "Первая часть"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 || result.Messages[0] != "ok" {
		t.Fatalf("messages = %#v", result.Messages)
	}
	if result.Model != "gemini-fallback" {
		t.Fatalf("model = %q", result.Model)
	}
	if !strings.Contains(result.RouteSummary, "gemini-primary:server:503") {
		t.Fatalf("route summary = %q", result.RouteSummary)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestGeminiNotePublishProviderFallsBackOnMissingModel(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"status":"NOT_FOUND","message":"model is not found"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"messages\":[{\"html\":\"ok\",\"source_part_ids\":[\"p1\"]}]}"}]}}]}`))
	}))
	defer server.Close()
	provider := geminiNotePublishProvider{apiKey: "test-key", model: "missing", fallbackModels: []string{"available"}, endpoint: server.URL, httpClient: server.Client()}
	result, err := provider.FormatNote(context.Background(), NotePublishRequest{Parts: []sqlitestore.WorkspaceDocumentPart{{PartNo: 1, Text: "text"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "available" || calls != 2 {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
}

func TestPublishCoverageRequiredOnlyWithoutRevision(t *testing.T) {
	payload := geminiPublishPayload{Messages: []geminiPublishMessage{{HTML: "ok", SourcePartIDs: []string{"p1"}}}}
	request := NotePublishRequest{Parts: []sqlitestore.WorkspaceDocumentPart{{PartNo: 1}, {PartNo: 2}}}
	if err := validatePublishCoverage(request, payload); err == nil {
		t.Fatal("expected incomplete coverage error")
	}
	request.Revision = "Удали вторую часть"
	if err := validatePublishCoverage(request, payload); err != nil {
		t.Fatalf("free revision should allow omitted parts: %v", err)
	}
}

func TestSplitPublishHTMLKeepsTagsBalanced(t *testing.T) {
	message := "<blockquote>" + strings.Repeat("текст &amp; ", 900) + "</blockquote>"
	parts, err := splitPublishMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 2 {
		t.Fatalf("parts=%d, want split", len(parts))
	}
	for _, part := range parts {
		units, err := publishHTMLUTF16Len(part)
		if err != nil {
			t.Fatal(err)
		}
		if units > telegramPublishMessageLimit {
			t.Fatalf("chunk too long: %d UTF-16 units", units)
		}
		if err := validatePublishHTML(part); err != nil {
			t.Fatalf("invalid chunk: %v\n%s", err, part)
		}
	}
}

func TestValidatePublishHTMLRejectsUnsupportedOrUnbalancedTags(t *testing.T) {
	for _, value := range []string{
		"<a href=\"https://example.com\">x</a>",
		"<b>broken</i>",
		"Tom & Jerry",
		"&copy;",
		"1 < 2",
		"1 > 0",
		"&#xD800;",
	} {
		if err := validatePublishHTML(value); err == nil {
			t.Fatalf("expected invalid HTML for %q", value)
		}
	}
	for _, value := range []string{
		"Tom &amp; Jerry",
		"&lt;b&gt;not a tag&lt;/b&gt;",
		"&#128512; &#x1F642; &quot;ok&quot;",
	} {
		if err := validatePublishHTML(value); err != nil {
			t.Fatalf("valid Telegram HTML rejected for %q: %v", value, err)
		}
	}
}

func TestSplitPublishHTMLCountsTelegramUTF16TextNotMarkup(t *testing.T) {
	exact := "<b>" + strings.Repeat("🙂", telegramPublishMessageLimit/2) + "</b>"
	parts, err := splitPublishMessage(exact)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("exact UTF-16 limit split into %d messages", len(parts))
	}
	entities := strings.Repeat("&amp;", telegramPublishMessageLimit)
	parts, err = splitPublishMessage(entities)
	if err != nil || len(parts) != 1 {
		t.Fatalf("decoded entities should count as one unit each: parts=%d err=%v", len(parts), err)
	}
	over := "<blockquote>" + strings.Repeat("🙂", telegramPublishMessageLimit/2+1) + "</blockquote>"
	parts, err = splitPublishMessage(over)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("over-limit emoji text split into %d messages, want 2", len(parts))
	}
	for _, part := range parts {
		units, err := publishHTMLUTF16Len(part)
		if err != nil || units > telegramPublishMessageLimit {
			t.Fatalf("part units=%d err=%v", units, err)
		}
	}
}

func TestFreeRevisionPreviewIsMarkedButStoredTextStaysPublishable(t *testing.T) {
	messages, err := preparePublishPreviewMessages([]string{strings.Repeat("🙂", 2100)}, "добавь вывод")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) < 2 {
		t.Fatalf("revision reserve should split long message, got %d", len(messages))
	}
	for index, message := range messages {
		if strings.Contains(message, "Свободная ревизия") {
			t.Fatal("stored/final text contains preview-only revision notice")
		}
		display, err := publishPreviewDisplayText(message, "добавь вывод", index == len(messages)-1)
		if err != nil {
			t.Fatal(err)
		}
		units, _ := publishHTMLUTF16Len(display)
		if units > telegramPublishMessageLimit {
			t.Fatalf("display preview units=%d", units)
		}
		if index == len(messages)-1 && !strings.Contains(display, "Свободная ревизия") {
			t.Fatal("last free-revision preview is not visibly marked")
		}
	}
}

func TestGeminiNotePublishProviderDoesNotFallbackOnBadRequest(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"status":"INVALID_ARGUMENT","message":"bad request"}}`))
	}))
	defer server.Close()

	provider := geminiNotePublishProvider{
		apiKey:         "test-key",
		model:          "gemini-primary",
		fallbackModels: []string{"gemini-fallback"},
		endpoint:       server.URL,
		httpClient:     server.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := provider.FormatNote(ctx, NotePublishRequest{
		Title: "Тест",
		Parts: []sqlitestore.WorkspaceDocumentPart{{PartNo: 1, Title: "Начало", Text: "Первая часть"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
