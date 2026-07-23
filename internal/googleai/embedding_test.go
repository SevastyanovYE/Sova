package googleai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbedContentUsesHeaderAndDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "embedding-secret" {
			t.Fatalf("key header = %q", r.Header.Get("x-goog-api-key"))
		}
		if strings.Contains(r.URL.RawQuery, "embedding-secret") || r.URL.Path != "/models/gemini-embedding-2:embedContent" {
			t.Fatalf("unexpected request URL %q", r.URL.String())
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["output_dimensionality"] != float64(3) {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": map[string]any{"values": []float32{1, 2, 3}}})
	}))
	defer server.Close()

	client := NewWithOptions("embedding-secret", Options{BaseURL: server.URL, HTTPClient: server.Client()})
	response, err := client.EmbedContent(context.Background(), EmbedRequest{Model: "gemini-embedding-2", Text: "task: search result | query: owl", OutputDimensions: 3})
	if err != nil {
		t.Fatal(err)
	}
	if response.Model != "gemini-embedding-2" || len(response.Values) != 3 {
		t.Fatalf("response = %+v", response)
	}
}

func TestEmbedContentRejectsWrongDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"embedding":{"values":[1]}}`))
	}))
	defer server.Close()
	client := NewWithOptions("key", Options{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := client.EmbedContent(context.Background(), EmbedRequest{Model: "gemini-embedding-2", Text: "text", OutputDimensions: 2})
	if err == nil || !strings.Contains(err.Error(), "expected 2") {
		t.Fatalf("error = %v", err)
	}
}

func TestBatchEmbedContentsKeepsInputsSeparate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "embedding-secret" {
			t.Fatalf("key header = %q", r.Header.Get("x-goog-api-key"))
		}
		if r.URL.Path != "/models/gemini-embedding-2:batchEmbedContents" {
			t.Fatalf("unexpected request URL %q", r.URL.String())
		}
		var body struct {
			Requests []struct {
				Model            string `json:"model"`
				OutputDimensions int    `json:"output_dimensionality"`
				Content          struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Requests) != 2 || body.Requests[0].Model != "models/gemini-embedding-2" || body.Requests[0].OutputDimensions != 3 || body.Requests[0].Content.Parts[0].Text != "one" {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": []any{
			map[string]any{"values": []float32{1, 0, 0}},
			map[string]any{"values": []float32{0, 1, 0}},
		}})
	}))
	defer server.Close()

	client := NewWithOptions("embedding-secret", Options{BaseURL: server.URL, HTTPClient: server.Client()})
	response, err := client.BatchEmbedContents(context.Background(), BatchEmbedRequest{
		Model: "gemini-embedding-2", Texts: []string{"one", "two"}, OutputDimensions: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Model != "gemini-embedding-2" || len(response.Values) != 2 || len(response.Values[1]) != 3 {
		t.Fatalf("response = %+v", response)
	}
}

func TestBatchEmbedContentsRejectsIncompleteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"embeddings":[{"values":[1,0]}]}`))
	}))
	defer server.Close()
	client := NewWithOptions("key", Options{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := client.BatchEmbedContents(context.Background(), BatchEmbedRequest{
		Model: "gemini-embedding-2", Texts: []string{"one", "two"}, OutputDimensions: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "expected 2") {
		t.Fatalf("error = %v", err)
	}
}
