package nest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
)

const telegramMessageLimit = 4096
const safeMessageLimit = 3900
const defaultHTTPTimeout = 75 * time.Second

type Client struct {
	token      string
	httpClient *http.Client
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
	IsForum  bool   `json:"is_forum,omitempty"`
}

type Message struct {
	MessageID       int    `json:"message_id"`
	MessageThreadID int    `json:"message_thread_id"`
	Chat            Chat   `json:"chat"`
	From            *User  `json:"from,omitempty"`
	Text            string `json:"text"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

type SendMessageRequest struct {
	ChatID          int64
	MessageThreadID int
	Text            string
	ParseMode       string
	ReplyMarkup     *InlineKeyboardMarkup
}

type EditMessageTextRequest struct {
	ChatID      int64
	MessageID   int
	Text        string
	ParseMode   string
	ReplyMarkup *InlineKeyboardMarkup
}

type ForumTopic struct {
	MessageThreadID   int    `json:"message_thread_id"`
	Name              string `json:"name"`
	IconColor         int    `json:"icon_color,omitempty"`
	IconCustomEmojiID string `json:"icon_custom_emoji_id,omitempty"`
}

type CreateForumTopicRequest struct {
	ChatID            int64
	Name              string
	IconColor         int
	IconCustomEmojiID string
}

func New(token string) *Client {
	return &Client{
		token:      strings.TrimSpace(token),
		httpClient: &http.Client{Timeout: defaultHTTPTimeout, Transport: telegramTransport()},
	}
}

func telegramTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = dialer.DialContext
	return transport
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var response struct {
		OK          bool   `json:"ok"`
		Result      User   `json:"result"`
		Description string `json:"description"`
	}
	if err := c.call(ctx, "getMe", nil, &response); err != nil {
		return User{}, err
	}
	if !response.OK {
		return User{}, fmt.Errorf("Bot API getMe failed: %s", response.Description)
	}
	return response.Result, nil
}

func (c *Client) GetChat(ctx context.Context, chatID int64) (Chat, error) {
	var response struct {
		OK          bool   `json:"ok"`
		Result      Chat   `json:"result"`
		Description string `json:"description"`
	}
	if err := c.call(ctx, "getChat", map[string]any{"chat_id": chatID}, &response); err != nil {
		return Chat{}, err
	}
	if !response.OK {
		return Chat{}, fmt.Errorf("Bot API getChat failed: %s", response.Description)
	}
	return response.Result, nil
}

func (c *Client) SendMessage(ctx context.Context, request SendMessageRequest) error {
	_, err := c.SendMessageResult(ctx, request)
	return err
}

func (c *Client) SendMessageResult(ctx context.Context, request SendMessageRequest) (Message, error) {
	payload := map[string]any{
		"chat_id":           request.ChatID,
		"message_thread_id": request.MessageThreadID,
		"text":              request.Text,
	}
	if strings.TrimSpace(request.ParseMode) != "" {
		payload["parse_mode"] = request.ParseMode
	}
	if request.ReplyMarkup != nil {
		payload["reply_markup"] = request.ReplyMarkup
	}
	var response struct {
		OK          bool    `json:"ok"`
		Result      Message `json:"result"`
		Description string  `json:"description"`
	}
	if err := c.call(ctx, "sendMessage", payload, &response); err != nil {
		return Message{}, err
	}
	if !response.OK {
		return Message{}, fmt.Errorf("Bot API sendMessage failed: %s", response.Description)
	}
	return response.Result, nil
}

func (c *Client) CreateForumTopic(ctx context.Context, request CreateForumTopicRequest) (ForumTopic, error) {
	payload := map[string]any{
		"chat_id": request.ChatID,
		"name":    request.Name,
	}
	if request.IconColor != 0 {
		payload["icon_color"] = request.IconColor
	}
	if strings.TrimSpace(request.IconCustomEmojiID) != "" {
		payload["icon_custom_emoji_id"] = request.IconCustomEmojiID
	}
	var response struct {
		OK          bool       `json:"ok"`
		Result      ForumTopic `json:"result"`
		Description string     `json:"description"`
	}
	if err := c.call(ctx, "createForumTopic", payload, &response); err != nil {
		return ForumTopic{}, err
	}
	if !response.OK {
		return ForumTopic{}, fmt.Errorf("Bot API createForumTopic failed: %s", response.Description)
	}
	return response.Result, nil
}

func (c *Client) EditMessageText(ctx context.Context, request EditMessageTextRequest) error {
	payload := map[string]any{
		"chat_id":    request.ChatID,
		"message_id": request.MessageID,
		"text":       request.Text,
	}
	if strings.TrimSpace(request.ParseMode) != "" {
		payload["parse_mode"] = request.ParseMode
	}
	if request.ReplyMarkup != nil {
		payload["reply_markup"] = request.ReplyMarkup
	}
	var response struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := c.call(ctx, "editMessageText", payload, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("Bot API editMessageText failed: %s", response.Description)
	}
	return nil
}

func (c *Client) GetUpdates(ctx context.Context, offset int, timeoutSeconds int) ([]Update, error) {
	payload := map[string]any{
		"offset":          offset,
		"timeout":         timeoutSeconds,
		"allowed_updates": []string{"message", "callback_query"},
	}
	var response struct {
		OK          bool     `json:"ok"`
		Result      []Update `json:"result"`
		Description string   `json:"description"`
	}
	if err := c.call(ctx, "getUpdates", payload, &response); err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, fmt.Errorf("Bot API getUpdates failed: %s", response.Description)
	}
	return response.Result, nil
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	payload := map[string]any{"callback_query_id": id}
	if strings.TrimSpace(text) != "" {
		payload["text"] = text
	}
	var response struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := c.call(ctx, "answerCallbackQuery", payload, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("Bot API answerCallbackQuery failed: %s", response.Description)
	}
	return nil
}

func (c *Client) SendLongMessage(ctx context.Context, request SendMessageRequest) error {
	parts := SplitMessageText(request.Text, safeMessageLimit)
	for _, part := range parts {
		next := request
		next.Text = part
		if err := c.SendMessage(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

func SplitMessageText(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if limit <= 0 || limit > telegramMessageLimit {
		limit = safeMessageLimit
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var parts []string
	for len(runes) > 0 {
		end := limit
		if end > len(runes) {
			end = len(runes)
		}
		split := end
		for i := end - 1; i > 0 && end-i < 600; i-- {
			if runes[i] == '\n' {
				split = i + 1
				break
			}
		}
		part := strings.TrimSpace(string(runes[:split]))
		if part != "" {
			parts = append(parts, part)
		}
		runes = runes[split:]
	}
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}

func CheckTopics(cfg config.Config) error {
	if cfg.NestTopics.Digest == cfg.NestTopics.Chat ||
		cfg.NestTopics.Calendar == cfg.NestTopics.Chat ||
		cfg.NestTopics.Status == cfg.NestTopics.Chat {
		return fmt.Errorf("Nest Chat topic must be separate from automated output topics")
	}
	if cfg.NestTopics.Digest == 0 || cfg.NestTopics.Calendar == 0 ||
		cfg.NestTopics.Status == 0 || cfg.NestTopics.Chat == 0 {
		return fmt.Errorf("all Nest topic IDs must be configured")
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, payload any, out any) error {
	if c.token == "" {
		return fmt.Errorf("Bot API token is empty")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(method), body)
	if err != nil {
		return fmt.Errorf("build Bot API %s request: %s", method, redactBotToken(err.Error(), c.token))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Bot API %s request failed: %s", method, redactBotToken(err.Error(), c.token))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Bot API %s returned %s: %s", method, resp.Status, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse Bot API %s response: %w", method, err)
	}
	return nil
}

func (c *Client) url(method string) string {
	return "https://api.telegram.org/bot" + c.token + "/" + method
}

func redactBotToken(value, token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return value
	}
	return strings.ReplaceAll(value, token, "<redacted>")
}
