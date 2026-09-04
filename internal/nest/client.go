package nest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/SevastyanovYE/Sova/internal/config"
)

const telegramMessageLimit = 4096
const safeMessageLimit = 3900
const defaultHTTPTimeout = 75 * time.Second
const defaultDialTimeout = 10 * time.Second
const telegramDialMaxAttempts = 3
const telegramDialInitialBackoff = 250 * time.Millisecond
const telegramDialMaxBackoff = time.Second

type Client struct {
	token          string
	httpClient     *http.Client
	dialRetryDelay func(int) time.Duration
}

// DefinitelyUnsentError reports a failure that happened while establishing the
// TCP connection, before an HTTP request could be sent. The stored message is
// already redacted and intentionally does not unwrap the original URL error,
// which may contain the Bot API token.
type DefinitelyUnsentError struct {
	message  string
	attempts int
	cause    error
}

type BotAPIError struct {
	Method      string
	StatusCode  int
	Description string
}

func (e *BotAPIError) Error() string {
	if e == nil {
		return ""
	}
	description := strings.TrimSpace(e.Description)
	if description == "" {
		description = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("Bot API %s returned HTTP %d: %s", e.Method, e.StatusCode, description)
}

func IsTelegramMessageNotFound(err error) bool {
	var apiErr *BotAPIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusBadRequest &&
			strings.Contains(strings.ToLower(apiErr.Description), "message to delete not found")
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "message to delete not found")
}

func IsBotAPIClientError(err error) bool {
	var apiErr *BotAPIError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}

func (e *DefinitelyUnsentError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *DefinitelyUnsentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Attempts returns the number of TCP dial attempts made before the request was
// abandoned.
func (e *DefinitelyUnsentError) Attempts() int {
	if e == nil {
		return 0
	}
	return e.attempts
}

// IsDefinitelyUnsent lets durable publishers safely retry failures that are
// known to have happened before Telegram could receive the HTTP request.
func IsDefinitelyUnsent(err error) bool {
	var target *DefinitelyUnsentError
	return errors.As(err, &target)
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
	MessageID            int             `json:"message_id"`
	MessageThreadID      int             `json:"message_thread_id"`
	Chat                 Chat            `json:"chat"`
	From                 *User           `json:"from,omitempty"`
	Date                 int64           `json:"date,omitempty"`
	EditDate             int64           `json:"edit_date,omitempty"`
	Text                 string          `json:"text"`
	Caption              string          `json:"caption,omitempty"`
	Photo                []PhotoSize     `json:"photo,omitempty"`
	Video                *Video          `json:"video,omitempty"`
	Voice                *Voice          `json:"voice,omitempty"`
	Audio                *Audio          `json:"audio,omitempty"`
	Document             *Document       `json:"document,omitempty"`
	ForwardOrigin        json.RawMessage `json:"forward_origin,omitempty"`
	ForwardFrom          *User           `json:"forward_from,omitempty"`
	ForwardFromChat      *Chat           `json:"forward_from_chat,omitempty"`
	ForwardFromMessageID int             `json:"forward_from_message_id,omitempty"`
	ReplyToMessage       *Message        `json:"reply_to_message,omitempty"`
}

type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int    `json:"file_size,omitempty"`
}

type Video struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

type Audio struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	Performer    string `json:"performer,omitempty"`
	Title        string `json:"title,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
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
	EditedMessage *Message       `json:"edited_message"`
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

type CopyMessageRequest struct {
	ChatID          int64
	MessageThreadID int
	FromChatID      int64
	MessageID       int
}

type ForwardMessageRequest struct {
	ChatID          int64
	MessageThreadID int
	FromChatID      int64
	MessageID       int
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

type PinChatMessageRequest struct {
	ChatID              int64
	MessageID           int
	DisableNotification bool
}

func New(token string) *Client {
	return &Client{
		token:          strings.TrimSpace(token),
		httpClient:     &http.Client{Timeout: defaultHTTPTimeout, Transport: telegramTransport()},
		dialRetryDelay: telegramDialRetryDelay,
	}
}

func telegramTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: defaultDialTimeout, KeepAlive: 30 * time.Second}
	transport.DialContext = dialer.DialContext
	return transport
}

func telegramDialRetryDelay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	delay := telegramDialInitialBackoff
	for attempt := 1; attempt < failures && delay < telegramDialMaxBackoff; attempt++ {
		delay *= 2
	}
	if delay > telegramDialMaxBackoff {
		return telegramDialMaxBackoff
	}
	return delay
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
	applyMessagePreviewPolicy(payload)
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

func (c *Client) CopyMessage(ctx context.Context, request CopyMessageRequest) (Message, error) {
	payload := map[string]any{
		"chat_id":      request.ChatID,
		"from_chat_id": request.FromChatID,
		"message_id":   request.MessageID,
	}
	if request.MessageThreadID != 0 {
		payload["message_thread_id"] = request.MessageThreadID
	}
	var response struct {
		OK          bool    `json:"ok"`
		Result      Message `json:"result"`
		Description string  `json:"description"`
	}
	if err := c.call(ctx, "copyMessage", payload, &response); err != nil {
		return Message{}, err
	}
	if !response.OK {
		return Message{}, fmt.Errorf("Bot API copyMessage failed: %s", response.Description)
	}
	return response.Result, nil
}

func (c *Client) ForwardMessage(ctx context.Context, request ForwardMessageRequest) (Message, error) {
	payload := map[string]any{
		"chat_id":      request.ChatID,
		"from_chat_id": request.FromChatID,
		"message_id":   request.MessageID,
	}
	if request.MessageThreadID != 0 {
		payload["message_thread_id"] = request.MessageThreadID
	}
	var response struct {
		OK          bool    `json:"ok"`
		Result      Message `json:"result"`
		Description string  `json:"description"`
	}
	if err := c.call(ctx, "forwardMessage", payload, &response); err != nil {
		return Message{}, err
	}
	if !response.OK {
		return Message{}, fmt.Errorf("Bot API forwardMessage failed: %s", response.Description)
	}
	return response.Result, nil
}

func (c *Client) EditMessageText(ctx context.Context, request EditMessageTextRequest) error {
	payload := map[string]any{
		"chat_id":    request.ChatID,
		"message_id": request.MessageID,
		"text":       request.Text,
	}
	applyMessagePreviewPolicy(payload)
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

func applyMessagePreviewPolicy(payload map[string]any) {
	if payload == nil {
		return
	}
	payload["disable_web_page_preview"] = true
}

func (c *Client) PinChatMessage(ctx context.Context, request PinChatMessageRequest) error {
	payload := map[string]any{
		"chat_id":    request.ChatID,
		"message_id": request.MessageID,
	}
	if request.DisableNotification {
		payload["disable_notification"] = true
	}
	var response struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := c.call(ctx, "pinChatMessage", payload, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("Bot API pinChatMessage failed: %s", response.Description)
	}
	return nil
}

func (c *Client) UnpinAllForumTopicMessages(ctx context.Context, chatID int64, messageThreadID int) error {
	payload := map[string]any{
		"chat_id":           chatID,
		"message_thread_id": messageThreadID,
	}
	var response struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := c.call(ctx, "unpinAllForumTopicMessages", payload, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("Bot API unpinAllForumTopicMessages failed: %s", response.Description)
	}
	return nil
}

func (c *Client) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	var response struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := c.call(ctx, "deleteMessage", payload, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("Bot API deleteMessage failed: %s", response.Description)
	}
	return nil
}

func (c *Client) GetUpdates(ctx context.Context, offset int, timeoutSeconds int) ([]Update, error) {
	payload := map[string]any{
		"offset":          offset,
		"timeout":         timeoutSeconds,
		"allowed_updates": []string{"message", "edited_message", "callback_query"},
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
	if TelegramTextUTF16Len(text) <= limit {
		return []string{text}
	}
	var parts []string
	for len(runes) > 0 {
		end := 0
		units := 0
		for end < len(runes) {
			next := utf16.RuneLen(runes[end])
			if units+next > limit {
				break
			}
			units += next
			end++
		}
		if end == 0 {
			end = 1
		}
		split := end
		searchedUnits := 0
		for i := end - 1; i > 0 && searchedUnits < 600; i-- {
			searchedUnits += utf16.RuneLen(runes[i])
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

// TelegramTextUTF16Len returns the unit count used by Telegram's message
// limits. Non-BMP runes consume two UTF-16 code units.
func TelegramTextUTF16Len(text string) int {
	return len(utf16.Encode([]rune(text)))
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
	var encoded []byte
	if payload != nil {
		var err error
		encoded, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	var resp *http.Response
	for attempt := 1; attempt <= telegramDialMaxAttempts; attempt++ {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(method), body)
		if err != nil {
			return fmt.Errorf("build Bot API %s request: %s", method, redactBotToken(err.Error(), c.token))
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err = c.httpClient.Do(req)
		if err == nil {
			break
		}
		if !isTCPDialFailure(err) {
			return fmt.Errorf("Bot API %s request failed: %s", method, redactBotToken(err.Error(), c.token))
		}
		if attempt == telegramDialMaxAttempts || ctx.Err() != nil {
			return definitelyUnsentError(method, attempt, err, ctx.Err(), c.token)
		}
		delay := telegramDialRetryDelay(attempt)
		if c.dialRetryDelay != nil {
			delay = c.dialRetryDelay(attempt)
		}
		if err := waitForDialRetry(ctx, delay); err != nil {
			return definitelyUnsentError(method, attempt, err, err, c.token)
		}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var envelope struct {
			Description string `json:"description"`
		}
		_ = json.Unmarshal(data, &envelope)
		if strings.TrimSpace(envelope.Description) == "" {
			envelope.Description = strings.TrimSpace(string(data))
		}
		envelope.Description = redactBotToken(envelope.Description, c.token)
		return &BotAPIError{Method: method, StatusCode: resp.StatusCode, Description: envelope.Description}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse Bot API %s response: %w", method, err)
	}
	return nil
}

func isTCPDialFailure(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial" && strings.HasPrefix(strings.ToLower(opErr.Net), "tcp")
}

func waitForDialRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func definitelyUnsentError(method string, attempts int, dialErr, cause error, token string) error {
	detail := "TCP dial failed"
	if dialErr != nil {
		detail = redactBotToken(dialErr.Error(), token)
	}
	return &DefinitelyUnsentError{
		message:  fmt.Sprintf("Bot API %s request definitely not sent after %d TCP dial attempt(s): %s", method, attempts, detail),
		attempts: attempts,
		cause:    cause,
	}
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
