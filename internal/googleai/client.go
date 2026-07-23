package googleai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultBaseURL          = "https://generativelanguage.googleapis.com/v1beta"
	defaultMaxResponseBytes = int64(4 << 20)
)

type Client struct {
	apiKey           string
	baseURL          string
	httpClient       *http.Client
	maxResponseBytes int64
}

type Options struct {
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

type GenerateRequest struct {
	Model           string
	SystemPrompt    string
	UserPrompt      string
	ResponseSchema  map[string]any
	Temperature     float64
	MaxOutputTokens int
	ThinkingLevel   string
}

type GenerateResponse struct {
	Text         string
	Model        string
	FinishReason string
	PromptTokens int
	OutputTokens int
	TotalTokens  int
	Duration     time.Duration
}

type APIError struct {
	Model      string
	StatusCode int
	Status     string
	Message    string
	Safety     bool
}

func (e *APIError) Error() string {
	parts := make([]string, 0, 4)
	if e.Model != "" {
		parts = append(parts, "model="+e.Model)
	}
	if e.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("status=%d", e.StatusCode))
	}
	if e.Status != "" {
		parts = append(parts, "code="+e.Status)
	}
	if e.Message != "" {
		parts = append(parts, compact(e.Message, 300))
	}
	if len(parts) == 0 {
		return "Google API request failed"
	}
	return "Google API request failed: " + strings.Join(parts, "; ")
}

type ErrorClass string

const (
	ErrorUnknown          ErrorClass = "unknown"
	ErrorCanceled         ErrorClass = "canceled"
	ErrorDeadline         ErrorClass = "deadline"
	ErrorNetwork          ErrorClass = "network"
	ErrorAuthentication   ErrorClass = "authentication"
	ErrorInvalidRequest   ErrorClass = "invalid_request"
	ErrorModelUnavailable ErrorClass = "model_unavailable"
	ErrorRateLimit        ErrorClass = "rate_limit"
	ErrorServer           ErrorClass = "server"
	ErrorSafety           ErrorClass = "safety"
	ErrorInvalidResponse  ErrorClass = "invalid_response"
)

type ResponseError struct {
	Reason string
}

func (e *ResponseError) Error() string {
	return "invalid Google API response: " + compact(e.Reason, 240)
}

func New(apiKey string) *Client {
	return NewWithOptions(apiKey, Options{})
}

func NewWithOptions(apiKey string, options Options) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 90 * time.Second}
	}
	maxBytes := options.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	return &Client{
		apiKey: strings.TrimSpace(apiKey), baseURL: baseURL,
		httpClient: httpClient, maxResponseBytes: maxBytes,
	}
}

func (c *Client) GenerateContent(ctx context.Context, request GenerateRequest) (GenerateResponse, error) {
	model := normalizeModel(request.Model)
	if c == nil || strings.TrimSpace(c.apiKey) == "" {
		return GenerateResponse{}, &APIError{Model: model, StatusCode: http.StatusUnauthorized, Status: "MISSING_API_KEY", Message: "API key is not configured"}
	}
	if model == "" {
		return GenerateResponse{}, &APIError{StatusCode: http.StatusBadRequest, Status: "INVALID_ARGUMENT", Message: "model is required"}
	}
	if strings.TrimSpace(request.UserPrompt) == "" {
		return GenerateResponse{}, &APIError{Model: model, StatusCode: http.StatusBadRequest, Status: "INVALID_ARGUMENT", Message: "user prompt is required"}
	}

	body := generateRequest{
		SystemInstruction: content{Parts: []part{{Text: request.SystemPrompt}}},
		Contents:          []content{{Role: "user", Parts: []part{{Text: request.UserPrompt}}}},
		GenerationConfig: generationConfig{
			Temperature: request.Temperature, MaxOutputTokens: request.MaxOutputTokens,
			ResponseMimeType: "application/json", ResponseJSONSchema: request.ResponseSchema,
		},
	}
	if strings.TrimSpace(request.SystemPrompt) == "" {
		body.SystemInstruction = content{}
	}
	if strings.TrimSpace(request.ThinkingLevel) != "" && strings.HasPrefix(strings.ToLower(model), "gemini-") {
		body.GenerationConfig.ThinkingConfig = &thinkingConfig{ThinkingLevel: strings.TrimSpace(request.ThinkingLevel)}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("encode Google generateContent request: %w", err)
	}
	endpoint, err := url.Parse(c.baseURL + "/models/" + url.PathEscape(model) + ":generateContent")
	if err != nil {
		return GenerateResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return GenerateResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("x-goog-api-key", c.apiKey)

	started := time.Now()
	httpResponse, err := c.httpClient.Do(httpRequest)
	duration := time.Since(started)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return GenerateResponse{}, ctxErr
		}
		return GenerateResponse{}, fmt.Errorf("Google generateContent transport: %w", err)
	}
	defer httpResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, c.maxResponseBytes+1))
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("read Google generateContent response: %w", err)
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return GenerateResponse{}, &ResponseError{Reason: "response exceeded size limit"}
	}
	var envelope generateResponse
	decodeErr := json.Unmarshal(raw, &envelope)
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return GenerateResponse{}, c.apiError(model, httpResponse.StatusCode, envelope, raw)
	}
	if decodeErr != nil {
		return GenerateResponse{}, &ResponseError{Reason: "response envelope is not valid JSON"}
	}
	if envelope.Error != nil {
		return GenerateResponse{}, c.apiError(model, httpResponse.StatusCode, envelope, nil)
	}
	if strings.TrimSpace(envelope.PromptFeedback.BlockReason) != "" {
		return GenerateResponse{}, &APIError{Model: model, StatusCode: httpResponse.StatusCode, Status: envelope.PromptFeedback.BlockReason, Message: "prompt was blocked", Safety: true}
	}
	if reason := safetyFinishReason(envelope); reason != "" {
		return GenerateResponse{}, &APIError{Model: model, StatusCode: httpResponse.StatusCode, Status: reason, Message: "candidate was blocked", Safety: true}
	}
	text, finishReason := responseText(envelope)
	if text == "" {
		return GenerateResponse{}, &ResponseError{Reason: "model returned no text"}
	}
	return GenerateResponse{
		Text: text, Model: model, FinishReason: finishReason, Duration: duration,
		PromptTokens: envelope.UsageMetadata.PromptTokenCount,
		OutputTokens: envelope.UsageMetadata.CandidatesTokenCount,
		TotalTokens:  envelope.UsageMetadata.TotalTokenCount,
	}, nil
}

func ClassifyError(err error) (class ErrorClass, statusCode int, disableModel bool, tryNext bool) {
	if err == nil {
		return "", 0, false, false
	}
	if errors.Is(err, context.Canceled) {
		return ErrorCanceled, 0, false, false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorDeadline, 0, false, true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Safety {
			return ErrorSafety, apiErr.StatusCode, false, false
		}
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrorAuthentication, apiErr.StatusCode, false, false
		case http.StatusNotFound:
			return ErrorModelUnavailable, apiErr.StatusCode, true, true
		case http.StatusRequestTimeout:
			return ErrorDeadline, apiErr.StatusCode, false, true
		case http.StatusTooManyRequests:
			return ErrorRateLimit, apiErr.StatusCode, false, true
		case http.StatusBadRequest:
			if isUnsupportedModelError(apiErr) {
				return ErrorModelUnavailable, apiErr.StatusCode, true, true
			}
			return ErrorInvalidRequest, apiErr.StatusCode, false, false
		}
		if apiErr.StatusCode >= 500 {
			return ErrorServer, apiErr.StatusCode, false, true
		}
		return ErrorUnknown, apiErr.StatusCode, false, false
	}
	var responseErr *ResponseError
	if errors.As(err, &responseErr) {
		return ErrorInvalidResponse, 0, false, true
	}
	return ErrorNetwork, 0, false, true
}

func isUnsupportedModelError(apiErr *APIError) bool {
	value := strings.ToLower(apiErr.Status + " " + apiErr.Message)
	for _, token := range []string{"unsupported", "not supported", "does not support", "not available for model", "model is not available"} {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

func (c *Client) apiError(model string, statusCode int, envelope generateResponse, raw []byte) error {
	apiErr := &APIError{Model: model, StatusCode: statusCode}
	if envelope.Error != nil {
		apiErr.Status = strings.TrimSpace(envelope.Error.Status)
		apiErr.Message = strings.TrimSpace(envelope.Error.Message)
	}
	if apiErr.Message == "" && len(raw) > 0 {
		apiErr.Message = compact(string(raw), 300)
	}
	apiErr.Message = redact(apiErr.Message, c.apiKey)
	return apiErr
}

func normalizeModel(model string) string {
	model = strings.Trim(strings.TrimSpace(model), "/")
	return strings.TrimPrefix(model, "models/")
}

func responseText(response generateResponse) (string, string) {
	for _, candidate := range response.Candidates {
		var b strings.Builder
		for _, item := range candidate.Content.Parts {
			if strings.TrimSpace(item.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(item.Text)
		}
		if strings.TrimSpace(b.String()) != "" {
			return strings.TrimSpace(b.String()), strings.TrimSpace(candidate.FinishReason)
		}
	}
	return "", ""
}

func safetyFinishReason(response generateResponse) string {
	for _, candidate := range response.Candidates {
		reason := strings.ToUpper(strings.TrimSpace(candidate.FinishReason))
		for _, blocked := range []string{"SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY"} {
			if reason == blocked {
				return reason
			}
		}
	}
	return ""
}

func redact(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

type generateRequest struct {
	SystemInstruction content          `json:"systemInstruction,omitempty"`
	Contents          []content        `json:"contents"`
	GenerationConfig  generationConfig `json:"generationConfig"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts,omitempty"`
}

type part struct {
	Text string `json:"text"`
}

type thinkingConfig struct {
	ThinkingLevel string `json:"thinkingLevel"`
}

type generationConfig struct {
	Temperature        float64         `json:"temperature"`
	MaxOutputTokens    int             `json:"maxOutputTokens,omitempty"`
	ResponseMimeType   string          `json:"responseMimeType,omitempty"`
	ResponseJSONSchema map[string]any  `json:"responseJsonSchema,omitempty"`
	ThinkingConfig     *thinkingConfig `json:"thinkingConfig,omitempty"`
}

type generateResponse struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}
