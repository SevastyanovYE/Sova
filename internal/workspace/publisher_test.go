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
}
