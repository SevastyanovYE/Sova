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

func TestGeminiNotePublishProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/models/gemini-test:generateContent") {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "test-key" {
			t.Fatalf("missing api key")
		}
		var request geminiGenerateRequest
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
						"text": "{\"messages\":[\"<b>Готово</b>\\n\\nПервая часть\"]}"
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
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"messages\":[\"ok\"]}"}]}}]}`))
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
	if len(paths) != 2 {
		t.Fatalf("paths = %#v", paths)
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
