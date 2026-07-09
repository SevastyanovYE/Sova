package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

const (
	geminiGenerateEndpoint   = "https://generativelanguage.googleapis.com/v1beta"
	telegramPublishSoftLimit = 3800
)

type NotePublishProvider interface {
	FormatNote(ctx context.Context, request NotePublishRequest) (NotePublishResult, error)
}

type NotePublishRequest struct {
	Title    string
	Parts    []sqlitestore.WorkspaceDocumentPart
	Revision string
}

type NotePublishResult struct {
	Messages []string
}

type mockNotePublishProvider struct{}

func NewNotePublishProvider(cfg config.Config) NotePublishProvider {
	if strings.TrimSpace(cfg.Gemini.APIKey) != "" {
		model := strings.TrimSpace(cfg.Gemini.Model)
		if model == "" {
			model = config.DefaultGeminiModel
		}
		return geminiNotePublishProvider{
			apiKey:         strings.TrimSpace(cfg.Gemini.APIKey),
			model:          model,
			fallbackModels: cfg.Gemini.FallbackModels,
			endpoint:       geminiGenerateEndpoint,
			httpClient:     &http.Client{Timeout: 90 * time.Second},
		}
	}
	return mockNotePublishProvider{}
}

type geminiNotePublishProvider struct {
	apiKey         string
	model          string
	fallbackModels []string
	endpoint       string
	httpClient     *http.Client
}

type geminiAPIError struct {
	Model      string
	StatusCode int
	Status     string
	Message    string
}

func (err geminiAPIError) Error() string {
	var parts []string
	if strings.TrimSpace(err.Model) != "" {
		parts = append(parts, "model "+err.Model)
	}
	if err.StatusCode != 0 {
		parts = append(parts, "status "+strconv.Itoa(err.StatusCode))
	}
	if strings.TrimSpace(err.Status) != "" {
		parts = append(parts, err.Status)
	}
	if strings.TrimSpace(err.Message) != "" {
		parts = append(parts, compactWorkspaceLine(err.Message, 300))
	}
	return "gemini generateContent " + strings.Join(parts, ": ")
}

type geminiGenerateRequest struct {
	SystemInstruction geminiContent          `json:"systemInstruction"`
	Contents          []geminiContent        `json:"contents"`
	GenerationConfig  geminiGenerationConfig `json:"generationConfig"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationConfig struct {
	Temperature      float64 `json:"temperature,omitempty"`
	ResponseMimeType string  `json:"responseMimeType,omitempty"`
}

type geminiGenerateResponse struct {
	Candidates     []geminiCandidate `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error *struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

type geminiCandidate struct {
	Content struct {
		Parts []geminiPart `json:"parts"`
	} `json:"content"`
	FinishReason string `json:"finishReason"`
}

type geminiPublishPayload struct {
	Messages []string `json:"messages"`
}

func (provider geminiNotePublishProvider) FormatNote(ctx context.Context, request NotePublishRequest) (NotePublishResult, error) {
	if strings.TrimSpace(provider.apiKey) == "" {
		return mockNotePublishProvider{}.FormatNote(ctx, request)
	}
	client := provider.httpClient
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	body, err := json.Marshal(geminiGenerateRequest{
		SystemInstruction: geminiContent{Parts: []geminiPart{{Text: notePublishSystemPrompt()}}},
		Contents: []geminiContent{{
			Role:  "user",
			Parts: []geminiPart{{Text: notePublishUserPrompt(request)}},
		}},
		GenerationConfig: geminiGenerationConfig{
			Temperature:      0.35,
			ResponseMimeType: "application/json",
		},
	})
	if err != nil {
		return NotePublishResult{}, err
	}

	models := provider.candidateModels()
	var lastErr error
	var temporaryFailures []string
	for i, model := range models {
		result, err := provider.formatNoteWithModel(ctx, client, body, model)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isGeminiTemporaryError(err) {
			return NotePublishResult{}, err
		}
		temporaryFailures = append(temporaryFailures, model)
		if i+1 >= len(models) {
			break
		}
	}
	if len(temporaryFailures) > 0 {
		return NotePublishResult{}, fmt.Errorf("Gemini временно перегружен или недоступен; попробуй публикацию позже. Модели уже пробовала: %s. Последняя ошибка: %w", strings.Join(temporaryFailures, ", "), lastErr)
	}
	return NotePublishResult{}, lastErr
}

func (provider geminiNotePublishProvider) formatNoteWithModel(ctx context.Context, client *http.Client, body []byte, model string) (NotePublishResult, error) {
	endpoint, err := provider.generateURL(model)
	if err != nil {
		return NotePublishResult{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return NotePublishResult{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := client.Do(httpRequest)
	if err != nil {
		return NotePublishResult{}, fmt.Errorf("gemini generateContent model %s: %w", model, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return NotePublishResult{}, err
	}
	var parsed geminiGenerateResponse
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &parsed)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return NotePublishResult{}, geminiErrorFromResponse(model, response.StatusCode, parsed, raw)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return NotePublishResult{}, geminiErrorFromResponse(model, response.StatusCode, parsed, raw)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return NotePublishResult{}, fmt.Errorf("decode gemini response: %w", err)
		}
	}
	if strings.TrimSpace(parsed.PromptFeedback.BlockReason) != "" {
		return NotePublishResult{}, fmt.Errorf("gemini blocked prompt: %s", parsed.PromptFeedback.BlockReason)
	}
	text := geminiResponseText(parsed)
	payload, err := parseGeminiPublishPayload(text)
	if err != nil {
		return NotePublishResult{}, err
	}
	messages := normalizePublishMessages(payload.Messages)
	if len(messages) == 0 {
		return NotePublishResult{}, fmt.Errorf("gemini returned no publish messages")
	}
	return NotePublishResult{Messages: messages}, nil
}

func (provider geminiNotePublishProvider) candidateModels() []string {
	candidates := append([]string{provider.model}, provider.fallbackModels...)
	out := make([]string, 0, len(candidates)+1)
	seen := map[string]struct{}{}
	for _, model := range candidates {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	if len(out) == 0 {
		out = append(out, config.DefaultGeminiModel)
	}
	return out
}

func (provider geminiNotePublishProvider) generateURL(model string) (string, error) {
	base := strings.TrimRight(provider.endpoint, "/")
	model = strings.Trim(strings.TrimSpace(model), "/")
	model = strings.TrimPrefix(model, "models/")
	if model == "" {
		model = config.DefaultGeminiModel
	}
	endpoint, err := url.Parse(base + "/models/" + url.PathEscape(model) + ":generateContent")
	if err != nil {
		return "", err
	}
	values := endpoint.Query()
	values.Set("key", provider.apiKey)
	endpoint.RawQuery = values.Encode()
	return endpoint.String(), nil
}

func geminiErrorFromResponse(model string, statusCode int, parsed geminiGenerateResponse, raw []byte) error {
	apiErr := geminiAPIError{Model: model, StatusCode: statusCode}
	if parsed.Error != nil {
		apiErr.Status = strings.TrimSpace(parsed.Error.Status)
		apiErr.Message = strings.TrimSpace(parsed.Error.Message)
	}
	if apiErr.Message == "" {
		apiErr.Message = compactWorkspaceLine(string(raw), 300)
	}
	return apiErr
}

func isGeminiTemporaryError(err error) bool {
	var apiErr geminiAPIError
	if !errors.As(err, &apiErr) {
		lower := strings.ToLower(err.Error())
		return strings.Contains(lower, "timeout") ||
			strings.Contains(lower, "temporar") ||
			strings.Contains(lower, "try again") ||
			strings.Contains(lower, "overload") ||
			strings.Contains(lower, "busy")
	}
	switch apiErr.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	status := strings.ToUpper(apiErr.Status)
	for _, token := range []string{"RESOURCE_EXHAUSTED", "UNAVAILABLE", "DEADLINE_EXCEEDED", "ABORTED"} {
		if strings.Contains(status, token) {
			return true
		}
	}
	lower := strings.ToLower(apiErr.Message)
	for _, token := range []string{"overload", "busy", "high demand", "try again", "temporar", "quota", "resource exhausted", "unavailable", "deadline"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func geminiResponseText(response geminiGenerateResponse) string {
	var b strings.Builder
	for _, candidate := range response.Candidates {
		for _, part := range candidate.Content.Parts {
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(part.Text)
		}
		if b.Len() > 0 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func parseGeminiPublishPayload(text string) (geminiPublishPayload, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return geminiPublishPayload{}, fmt.Errorf("gemini returned empty text")
	}
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)
	}
	if !strings.HasPrefix(text, "{") {
		start := strings.Index(text, "{")
		end := strings.LastIndex(text, "}")
		if start >= 0 && end > start {
			text = text[start : end+1]
		}
	}
	var payload geminiPublishPayload
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return geminiPublishPayload{}, fmt.Errorf("decode gemini publish JSON: %w", err)
	}
	return payload, nil
}

func normalizePublishMessages(messages []string) []string {
	var out []string
	for _, message := range messages {
		message = strings.TrimSpace(message)
		if message == "" {
			continue
		}
		out = append(out, splitPublishMessage(message)...)
	}
	return out
}

func splitPublishMessage(message string) []string {
	message = strings.TrimSpace(message)
	if len([]rune(message)) <= telegramPublishSoftLimit {
		return []string{message}
	}
	paragraphs := strings.Split(message, "\n\n")
	var out []string
	var current strings.Builder
	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		addedLen := len([]rune(paragraph))
		if current.Len() > 0 {
			addedLen += 2
		}
		if current.Len() > 0 && len([]rune(current.String()))+addedLen > telegramPublishSoftLimit {
			out = append(out, strings.TrimSpace(current.String()))
			current.Reset()
		}
		if len([]rune(paragraph)) > telegramPublishSoftLimit {
			if current.Len() > 0 {
				out = append(out, strings.TrimSpace(current.String()))
				current.Reset()
			}
			out = append(out, splitLongRunes(paragraph, telegramPublishSoftLimit)...)
			continue
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(paragraph)
	}
	if current.Len() > 0 {
		out = append(out, strings.TrimSpace(current.String()))
	}
	return out
}

func splitLongRunes(value string, limit int) []string {
	runes := []rune(value)
	var out []string
	for len(runes) > 0 {
		n := limit
		if len(runes) < n {
			n = len(runes)
		}
		out = append(out, strings.TrimSpace(string(runes[:n])))
		runes = runes[n:]
	}
	return out
}

func notePublishSystemPrompt() string {
	return strings.TrimSpace(`Ты редактор личного Workspace. Нужно превратить исходную заметку в аккуратный материал для Telegram topic "Полезное".

Правила:
- Пиши по-русски, если исходник на русском.
- Не добавляй новых фактов, ссылок, обещаний или выводов, которых нет в исходнике.
- Минимально меняй смысл и авторскую интонацию; исправляй только явные шероховатости.
- Можно структурировать, озаглавить, разбить на несколько Telegram-сообщений.
- Используй только простой Telegram HTML: <b>, <i>, <blockquote>. Не используй Markdown.
- Не добавляй source links в видимый текст.
- Верни только валидный JSON без пояснений: {"messages":["..."]}.
- Каждый элемент messages должен быть готовым Telegram HTML сообщением и желательно короче 3500 символов.`)
}

func notePublishUserPrompt(request NotePublishRequest) string {
	var b strings.Builder
	b.WriteString("Название заметки: ")
	b.WriteString(strings.TrimSpace(request.Title))
	b.WriteString("\n\nЧасти заметки в исходном порядке:\n")
	for _, part := range request.Parts {
		fmt.Fprintf(&b, "\n[%d] %s\n", part.PartNo, documentPartTitle(part, "Часть "+strconv.Itoa(part.PartNo)))
		text := strings.TrimSpace(part.Text)
		if text == "" {
			text = "[media without text]"
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	if strings.TrimSpace(request.Revision) != "" {
		b.WriteString("\nПравка пользователя к предыдущему preview:\n")
		b.WriteString(strings.TrimSpace(request.Revision))
		b.WriteString("\n")
	}
	return b.String()
}

func (mockNotePublishProvider) FormatNote(ctx context.Context, request NotePublishRequest) (NotePublishResult, error) {
	var b strings.Builder
	b.WriteString("💎 <b>")
	b.WriteString(html.EscapeString(strings.TrimSpace(request.Title)))
	b.WriteString("</b>\n\n")
	for _, part := range request.Parts {
		title := documentPartTitle(part, "Часть "+strconv.Itoa(part.PartNo))
		if len(request.Parts) > 1 {
			b.WriteString("<b>")
			b.WriteString(html.EscapeString(title))
			b.WriteString("</b>\n")
		}
		text := strings.TrimSpace(part.Text)
		if text == "" {
			text = "[media]"
		}
		b.WriteString(html.EscapeString(text))
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(request.Revision) != "" {
		b.WriteString("<blockquote>Mock preview пересобран с учётом правки, без добавления новых фактов.</blockquote>")
	}
	return NotePublishResult{Messages: []string{strings.TrimSpace(b.String())}}, nil
}
