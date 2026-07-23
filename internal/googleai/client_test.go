package googleai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerateContentUsesHeaderAndStructuredOutput(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got != "secret-key" {
			t.Fatalf("key header = %q", got)
		}
		if strings.Contains(r.URL.RawQuery, "secret-key") {
			t.Fatal("API key leaked into URL")
		}
		if !strings.Contains(r.URL.Path, "/models/gemini-test:generateContent") {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"content":      map[string]any{"parts": []any{map[string]any{"text": `{"ok":true}`}}},
				"finishReason": "STOP",
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 12, "candidatesTokenCount": 3, "totalTokenCount": 15},
		})
	}))
	defer server.Close()

	client := NewWithOptions("secret-key", Options{BaseURL: server.URL, HTTPClient: server.Client()})
	response, err := client.GenerateContent(context.Background(), GenerateRequest{
		Model: "gemini-test", SystemPrompt: "system", UserPrompt: "user",
		ResponseSchema: map[string]any{"type": "object"}, Temperature: 0,
		MaxOutputTokens: 4096, ThinkingLevel: "minimal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != `{"ok":true}` || response.FinishReason != "STOP" || response.TotalTokens != 15 {
		t.Fatalf("response = %+v", response)
	}
	config := body["generationConfig"].(map[string]any)
	if config["temperature"].(float64) != 0 || config["maxOutputTokens"].(float64) != 4096 {
		t.Fatalf("generation config = %#v", config)
	}
	if _, ok := config["responseJsonSchema"]; !ok {
		t.Fatalf("response schema missing: %#v", config)
	}
	if _, ok := config["thinkingConfig"]; !ok {
		t.Fatalf("thinking config missing: %#v", config)
	}
}

func TestGenerateContentRedactsKeyAndClassifies404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"status":"NOT_FOUND","message":"model unavailable; key=secret-key"}}`))
	}))
	defer server.Close()

	client := NewWithOptions("secret-key", Options{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := client.GenerateContent(context.Background(), GenerateRequest{Model: "missing", UserPrompt: "test"})
	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("error = %v", err)
	}
	class, status, disable, tryNext := ClassifyError(err)
	if class != ErrorModelUnavailable || status != 404 || !disable || !tryNext {
		t.Fatalf("classification = %q %d %t %t", class, status, disable, tryNext)
	}
}

func TestClassifyErrorStopsForAuthAndBadRequest(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest} {
		class, _, _, tryNext := ClassifyError(&APIError{StatusCode: status})
		if tryNext {
			t.Fatalf("status %d should stop route", status)
		}
		if class != ErrorAuthentication && class != ErrorInvalidRequest {
			t.Fatalf("status %d class = %q", status, class)
		}
	}
}

func TestClassifyErrorSkipsModelSpecificUnsupported400(t *testing.T) {
	class, status, disable, tryNext := ClassifyError(&APIError{
		StatusCode: http.StatusBadRequest, Status: "INVALID_ARGUMENT",
		Message: "responseJsonSchema is not supported for this model",
	})
	if class != ErrorModelUnavailable || status != 400 || !disable || !tryNext {
		t.Fatalf("classification = %q %d %t %t", class, status, disable, tryNext)
	}
}

func TestGenerateContentTreatsSafetyFinishAsTerminalSafetyError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{
				map[string]any{
					"finishReason": "SAFETY",
					"content":      map[string]any{"parts": []any{}},
				},
			},
		})
	}))
	defer server.Close()
	client := NewWithOptions("key", Options{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := client.GenerateContent(context.Background(), GenerateRequest{Model: "gemini-test", UserPrompt: "test"})
	class, _, _, tryNext := ClassifyError(err)
	if class != ErrorSafety || tryNext {
		t.Fatalf("error=%v classification=%q try_next=%t", err, class, tryNext)
	}
}
