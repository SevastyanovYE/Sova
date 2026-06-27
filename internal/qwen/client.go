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
	baseURL            string
	model              string
	httpClient         *http.Client
	numContext         int
	classifyNumPredict int
	eventNumPredict    int
	keepAlive          string
}

const (
	defaultRequestTimeout     = 90 * time.Second
	defaultNumContext         = 4096
	defaultClassifyNumPredict = 1024
	defaultEventNumPredict    = 1536
	defaultKeepAlive          = "2m"
)

func New(baseURL, model string) *Client {
	return &Client{
		baseURL:            strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		model:              strings.TrimSpace(model),
		httpClient:         &http.Client{Timeout: defaultRequestTimeout},
		numContext:         defaultNumContext,
		classifyNumPredict: defaultClassifyNumPredict,
		eventNumPredict:    defaultEventNumPredict,
		keepAlive:          defaultKeepAlive,
	}
}

func (c *Client) Model() string {
	return c.model
}

func (c *Client) ClassifyBatch(ctx context.Context, messages []MessageInput) (BatchResult, string, error) {
	result, raw, _, err := c.ClassifyBatchWithMetrics(ctx, messages)
	return result, raw, err
}

func (c *Client) ClassifyBatchWithMetrics(ctx context.Context, messages []MessageInput) (BatchResult, string, ResponseMetrics, error) {
	if c.baseURL == "" {
		return BatchResult{}, "", ResponseMetrics{}, fmt.Errorf("Ollama URL is empty")
	}
	if c.model == "" {
		return BatchResult{}, "", ResponseMetrics{}, fmt.Errorf("Ollama model is empty")
	}
	prompt, err := BuildPrompt(messages)
	if err != nil {
		return BatchResult{}, "", ResponseMetrics{}, err
	}
	result, raw, metrics, err := c.generate(ctx, prompt, responseSchema(), c.classifyNumPredict)
	if err != nil {
		return BatchResult{}, raw, metrics, err
	}
	var parsed BatchResult
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return BatchResult{}, result, metrics, fmt.Errorf("parse model JSON: %w", err)
	}
	if err := validateResult(messages, parsed); err != nil {
		return BatchResult{}, result, metrics, err
	}
	fillDecisionDefaults(messages, &parsed)
	return parsed, result, metrics, nil
}

func (c *Client) ExtractEvents(ctx context.Context, messages []EventInput, now time.Time, timezone string) (EventExtractionResult, string, error) {
	result, raw, _, err := c.ExtractEventsWithMetrics(ctx, messages, now, timezone)
	return result, raw, err
}

func (c *Client) ExtractEventsWithMetrics(ctx context.Context, messages []EventInput, now time.Time, timezone string) (EventExtractionResult, string, ResponseMetrics, error) {
	if c.baseURL == "" {
		return EventExtractionResult{}, "", ResponseMetrics{}, fmt.Errorf("Ollama URL is empty")
	}
	if c.model == "" {
		return EventExtractionResult{}, "", ResponseMetrics{}, fmt.Errorf("Ollama model is empty")
	}
	prompt, err := BuildEventPrompt(messages, now, timezone)
	if err != nil {
		return EventExtractionResult{}, "", ResponseMetrics{}, err
	}
	result, raw, metrics, err := c.generate(ctx, prompt, eventResponseSchema(), c.eventNumPredict)
	if err != nil {
		return EventExtractionResult{}, raw, metrics, err
	}
	var parsed EventExtractionResult
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return EventExtractionResult{}, result, metrics, fmt.Errorf("parse model JSON: %w", err)
	}
	if err := validateEventResult(messages, parsed); err != nil {
		return EventExtractionResult{}, result, metrics, err
	}
	return parsed, result, metrics, nil
}

func (c *Client) generate(ctx context.Context, prompt string, schema map[string]any, numPredict int) (string, string, ResponseMetrics, error) {
	body := map[string]any{
		"model":      c.model,
		"prompt":     prompt,
		"stream":     false,
		"think":      false,
		"format":     schema,
		"keep_alive": c.keepAlive,
		"options": map[string]any{
			"temperature": 0,
			"num_ctx":     c.numContext,
			"num_predict": numPredict,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", "", ResponseMetrics{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(encoded))
	if err != nil {
		return "", "", ResponseMetrics{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", ResponseMetrics{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return "", "", ResponseMetrics{}, err
	}
	if resp.StatusCode >= 300 {
		return "", "", ResponseMetrics{}, fmt.Errorf("Ollama returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var envelope struct {
		Model              string `json:"model"`
		Response           string `json:"response"`
		TotalDuration      int64  `json:"total_duration"`
		LoadDuration       int64  `json:"load_duration"`
		PromptEvalCount    int    `json:"prompt_eval_count"`
		PromptEvalDuration int64  `json:"prompt_eval_duration"`
		EvalCount          int    `json:"eval_count"`
		EvalDuration       int64  `json:"eval_duration"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return "", "", ResponseMetrics{}, fmt.Errorf("parse Ollama envelope: %w", err)
	}
	metrics := ResponseMetrics{
		Model:              envelope.Model,
		TotalDuration:      time.Duration(envelope.TotalDuration),
		LoadDuration:       time.Duration(envelope.LoadDuration),
		PromptEvalCount:    envelope.PromptEvalCount,
		PromptEvalDuration: time.Duration(envelope.PromptEvalDuration),
		EvalCount:          envelope.EvalCount,
		EvalDuration:       time.Duration(envelope.EvalDuration),
	}
	return envelope.Response, envelope.Response, metrics, nil
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
						"has_event":  map[string]any{"type": "boolean"},
					},
					"required":             []string{"id", "keep", "importance", "has_event"},
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
		return &IncompleteResultError{Kind: "decisions", Returned: len(seen), Expected: len(inputs)}
	}
	return nil
}

func fillDecisionDefaults(inputs []MessageInput, result *BatchResult) {
	byID := make(map[string]MessageInput, len(inputs))
	for _, input := range inputs {
		byID[input.ID] = input
	}
	for i, decision := range result.Decisions {
		input := byID[decision.ID]
		if strings.TrimSpace(decision.Reason) == "" {
			decision.Reason = defaultReason(decision)
		}
		if len(decision.Tags) == 0 {
			decision.Tags = defaultTags(input, decision)
		}
		result.Decisions[i] = decision
	}
}

func defaultReason(decision MessageDecision) string {
	if decision.HasEvent {
		return "найден учебный срок или событие"
	}
	if decision.Keep && decision.Importance >= 2 {
		return "учебно важное сообщение"
	}
	if decision.Keep {
		return "может быть полезно для учебного обзора"
	}
	return "шум или не относится к учебному обзору"
}

func defaultTags(input MessageInput, decision MessageDecision) []string {
	var tags []string
	if decision.HasEvent {
		tags = append(tags, "event")
	}
	if decision.Importance >= 3 {
		tags = append(tags, "urgent")
	}
	if strings.Contains(strings.ToLower(input.Kind), "media") || input.AttachmentCount > 0 {
		tags = append(tags, "attachment")
	}
	if decision.Keep {
		tags = append(tags, "study")
	}
	if len(tags) == 0 {
		tags = append(tags, "noise")
	}
	return tags
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
	return nil
}
