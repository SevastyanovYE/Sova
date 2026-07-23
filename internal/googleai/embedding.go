package googleai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type EmbedRequest struct {
	Model            string
	Text             string
	OutputDimensions int
}

type EmbedResponse struct {
	Values   []float32
	Model    string
	Duration time.Duration
}

type BatchEmbedRequest struct {
	Model            string
	Texts            []string
	OutputDimensions int
}

type BatchEmbedResponse struct {
	Values   [][]float32
	Model    string
	Duration time.Duration
}

// EmbedContent sends one text input to the official Gemini embedContent REST API.
// Gemini Embedding 2 aggregates multiple parts into one vector, so callers keep
// one document or one query per request.
func (c *Client) EmbedContent(ctx context.Context, request EmbedRequest) (EmbedResponse, error) {
	model := normalizeModel(request.Model)
	if c == nil || strings.TrimSpace(c.apiKey) == "" {
		return EmbedResponse{}, &APIError{Model: model, StatusCode: http.StatusUnauthorized, Status: "MISSING_API_KEY", Message: "API key is not configured"}
	}
	if model == "" || strings.TrimSpace(request.Text) == "" || request.OutputDimensions <= 0 {
		return EmbedResponse{}, &APIError{Model: model, StatusCode: http.StatusBadRequest, Status: "INVALID_ARGUMENT", Message: "model, text, and output dimensions are required"}
	}
	body := struct {
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
		OutputDimensions int `json:"output_dimensionality"`
	}{OutputDimensions: request.OutputDimensions}
	body.Content.Parts = []part{{Text: request.Text}}
	encoded, err := json.Marshal(body)
	if err != nil {
		return EmbedResponse{}, fmt.Errorf("encode Google embedContent request: %w", err)
	}
	endpoint, err := url.Parse(c.baseURL + "/models/" + url.PathEscape(model) + ":embedContent")
	if err != nil {
		return EmbedResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return EmbedResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("x-goog-api-key", c.apiKey)

	started := time.Now()
	httpResponse, err := c.httpClient.Do(httpRequest)
	duration := time.Since(started)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return EmbedResponse{}, ctxErr
		}
		return EmbedResponse{}, fmt.Errorf("Google embedContent transport: %w", err)
	}
	defer httpResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, c.maxResponseBytes+1))
	if err != nil {
		return EmbedResponse{}, fmt.Errorf("read Google embedContent response: %w", err)
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return EmbedResponse{}, &ResponseError{Reason: "response exceeded size limit"}
	}
	var envelope embedResponse
	decodeErr := json.Unmarshal(raw, &envelope)
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		var generated generateResponse
		_ = json.Unmarshal(raw, &generated)
		return EmbedResponse{}, c.apiError(model, httpResponse.StatusCode, generated, raw)
	}
	if decodeErr != nil {
		return EmbedResponse{}, &ResponseError{Reason: "response envelope is not valid JSON"}
	}
	if len(envelope.Embedding.Values) != request.OutputDimensions {
		return EmbedResponse{}, &ResponseError{Reason: fmt.Sprintf("model returned %d dimensions, expected %d", len(envelope.Embedding.Values), request.OutputDimensions)}
	}
	return EmbedResponse{Values: envelope.Embedding.Values, Model: model, Duration: duration}, nil
}

// BatchEmbedContents generates one independent vector per text through the
// synchronous batch endpoint. Inputs remain separate EmbedContentRequest
// objects so Gemini Embedding 2 does not aggregate them into one vector.
func (c *Client) BatchEmbedContents(ctx context.Context, request BatchEmbedRequest) (BatchEmbedResponse, error) {
	model := normalizeModel(request.Model)
	if c == nil || strings.TrimSpace(c.apiKey) == "" {
		return BatchEmbedResponse{}, &APIError{Model: model, StatusCode: http.StatusUnauthorized, Status: "MISSING_API_KEY", Message: "API key is not configured"}
	}
	if model == "" || len(request.Texts) == 0 || len(request.Texts) > 100 || request.OutputDimensions <= 0 {
		return BatchEmbedResponse{}, &APIError{Model: model, StatusCode: http.StatusBadRequest, Status: "INVALID_ARGUMENT", Message: "model, 1-100 texts, and output dimensions are required"}
	}
	type batchItem struct {
		Model   string `json:"model"`
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
		OutputDimensions int `json:"output_dimensionality"`
	}
	body := struct {
		Requests []batchItem `json:"requests"`
	}{Requests: make([]batchItem, 0, len(request.Texts))}
	for _, text := range request.Texts {
		if strings.TrimSpace(text) == "" {
			return BatchEmbedResponse{}, &APIError{Model: model, StatusCode: http.StatusBadRequest, Status: "INVALID_ARGUMENT", Message: "batch texts must not be empty"}
		}
		item := batchItem{Model: "models/" + model, OutputDimensions: request.OutputDimensions}
		item.Content.Parts = []part{{Text: text}}
		body.Requests = append(body.Requests, item)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return BatchEmbedResponse{}, fmt.Errorf("encode Google batchEmbedContents request: %w", err)
	}
	endpoint, err := url.Parse(c.baseURL + "/models/" + url.PathEscape(model) + ":batchEmbedContents")
	if err != nil {
		return BatchEmbedResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return BatchEmbedResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("x-goog-api-key", c.apiKey)

	started := time.Now()
	httpResponse, err := c.httpClient.Do(httpRequest)
	duration := time.Since(started)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return BatchEmbedResponse{}, ctxErr
		}
		return BatchEmbedResponse{}, fmt.Errorf("Google batchEmbedContents transport: %w", err)
	}
	defer httpResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, c.maxResponseBytes+1))
	if err != nil {
		return BatchEmbedResponse{}, fmt.Errorf("read Google batchEmbedContents response: %w", err)
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return BatchEmbedResponse{}, &ResponseError{Reason: "response exceeded size limit"}
	}
	var envelope batchEmbedResponse
	decodeErr := json.Unmarshal(raw, &envelope)
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		var generated generateResponse
		_ = json.Unmarshal(raw, &generated)
		return BatchEmbedResponse{}, c.apiError(model, httpResponse.StatusCode, generated, raw)
	}
	if decodeErr != nil {
		return BatchEmbedResponse{}, &ResponseError{Reason: "response envelope is not valid JSON"}
	}
	if len(envelope.Embeddings) != len(request.Texts) {
		return BatchEmbedResponse{}, &ResponseError{Reason: fmt.Sprintf("model returned %d embeddings, expected %d", len(envelope.Embeddings), len(request.Texts))}
	}
	values := make([][]float32, len(envelope.Embeddings))
	for index, embedding := range envelope.Embeddings {
		if len(embedding.Values) != request.OutputDimensions {
			return BatchEmbedResponse{}, &ResponseError{Reason: fmt.Sprintf("embedding %d returned %d dimensions, expected %d", index, len(embedding.Values), request.OutputDimensions)}
		}
		values[index] = embedding.Values
	}
	return BatchEmbedResponse{Values: values, Model: model, Duration: duration}, nil
}

type embedResponse struct {
	Embedding struct {
		Values []float32 `json:"values"`
	} `json:"embedding"`
}

type batchEmbedResponse struct {
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
}
