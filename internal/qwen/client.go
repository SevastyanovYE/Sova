package qwen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

func New(baseURL, model string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		model:      strings.TrimSpace(model),
		httpClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

func (c *Client) ClassifyBatch(ctx context.Context, messages []MessageInput) (BatchResult, string, error) {
	if c.baseURL == "" {
		return BatchResult{}, "", fmt.Errorf("Ollama URL is empty")
	}
	if c.model == "" {
		return BatchResult{}, "", fmt.Errorf("Ollama model is empty")
	}
	prompt, err := BuildPrompt(messages)
	if err != nil {
		return BatchResult{}, "", err
	}
	body := map[string]any{
		"model":       c.model,
		"prompt":      prompt,
		"stream":      false,
		"temperature": 0,
		"format":      responseSchema(),
		"options": map[string]any{
			"temperature": 0,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return BatchResult{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(encoded))
	if err != nil {
		return BatchResult{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return BatchResult{}, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return BatchResult{}, "", err
	}
	if resp.StatusCode >= 300 {
		return BatchResult{}, "", fmt.Errorf("Ollama returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var envelope struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return BatchResult{}, "", fmt.Errorf("parse Ollama envelope: %w", err)
	}
	var result BatchResult
	if err := json.Unmarshal([]byte(envelope.Response), &result); err != nil {
		return BatchResult{}, envelope.Response, fmt.Errorf("parse model JSON: %w", err)
	}
	if err := validateResult(messages, result); err != nil {
		return BatchResult{}, envelope.Response, err
	}
	return result, envelope.Response, nil
}

func (c *Client) ExtractEvents(ctx context.Context, messages []EventInput, now time.Time, timezone string) (EventExtractionResult, string, error) {
	if c.baseURL == "" {
		return EventExtractionResult{}, "", fmt.Errorf("Ollama URL is empty")
	}
	if c.model == "" {
		return EventExtractionResult{}, "", fmt.Errorf("Ollama model is empty")
	}
	prompt, err := BuildEventPrompt(messages, now, timezone)
	if err != nil {
		return EventExtractionResult{}, "", err
	}
	body := map[string]any{
		"model":       c.model,
		"prompt":      prompt,
		"stream":      false,
		"temperature": 0,
		"format":      eventResponseSchema(),
		"options": map[string]any{
			"temperature": 0,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return EventExtractionResult{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(encoded))
	if err != nil {
		return EventExtractionResult{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return EventExtractionResult{}, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return EventExtractionResult{}, "", err
	}
	if resp.StatusCode >= 300 {
		return EventExtractionResult{}, "", fmt.Errorf("Ollama returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var envelope struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return EventExtractionResult{}, "", fmt.Errorf("parse Ollama envelope: %w", err)
	}
	var result EventExtractionResult
	if err := json.Unmarshal([]byte(envelope.Response), &result); err != nil {
		return EventExtractionResult{}, envelope.Response, fmt.Errorf("parse model JSON: %w", err)
	}
	if err := validateEventResult(messages, result); err != nil {
		return EventExtractionResult{}, envelope.Response, err
	}
	return result, envelope.Response, nil
}

func responseSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"decisions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":         map[string]any{"type": "string"},
						"keep":       map[string]any{"type": "boolean"},
						"importance": map[string]any{"type": "integer", "minimum": 0, "maximum": 3},
						"reason":     map[string]any{"type": "string"},
						"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"has_event":  map[string]any{"type": "boolean"},
					},
					"required":             []string{"id", "keep", "importance", "reason", "tags", "has_event"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"decisions"},
		"additionalProperties": false,
	}
}

func eventResponseSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"events": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":          map[string]any{"type": "string"},
						"has_event":   map[string]any{"type": "boolean"},
						"title":       map[string]any{"type": "string"},
						"start":       map[string]any{"type": "string"},
						"end":         map[string]any{"type": "string"},
						"location":    map[string]any{"type": "string"},
						"description": map[string]any{"type": "string"},
						"confidence":  map[string]any{"type": "string"},
						"missing":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
					"required":             []string{"id", "has_event", "title", "start", "end", "location", "description", "confidence", "missing"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"events"},
		"additionalProperties": false,
	}
}

func validateResult(inputs []MessageInput, result BatchResult) error {
	allowed := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		allowed[input.ID] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, decision := range result.Decisions {
		if _, ok := allowed[decision.ID]; !ok {
			return fmt.Errorf("model returned unknown id %q", decision.ID)
		}
		if _, ok := seen[decision.ID]; ok {
			return fmt.Errorf("model returned duplicate id %q", decision.ID)
		}
		seen[decision.ID] = struct{}{}
		if decision.Importance < 0 || decision.Importance > 3 {
			return fmt.Errorf("importance out of range for %q", decision.ID)
		}
	}
	if len(seen) != len(inputs) {
		return fmt.Errorf("model returned %d decisions for %d inputs", len(seen), len(inputs))
	}
	return nil
}

func validateEventResult(inputs []EventInput, result EventExtractionResult) error {
	allowed := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		allowed[input.ID] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, event := range result.Events {
		if _, ok := allowed[event.ID]; !ok {
			return fmt.Errorf("model returned unknown event id %q", event.ID)
		}
		if _, ok := seen[event.ID]; ok {
			return fmt.Errorf("model returned duplicate event id %q", event.ID)
		}
		seen[event.ID] = struct{}{}
	}
	if len(seen) != len(inputs) {
		return fmt.Errorf("model returned %d event candidates for %d inputs", len(seen), len(inputs))
	}
	return nil
}
