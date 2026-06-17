package nest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
)

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

type SendMessageRequest struct {
	ChatID          int64
	MessageThreadID int
	Text            string
	ParseMode       string
}

func New(token string) *Client {
	return &Client{
		token:      strings.TrimSpace(token),
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
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

func (c *Client) SendMessage(ctx context.Context, request SendMessageRequest) error {
	payload := map[string]any{
		"chat_id":           request.ChatID,
		"message_thread_id": request.MessageThreadID,
		"text":              request.Text,
	}
	if strings.TrimSpace(request.ParseMode) != "" {
		payload["parse_mode"] = request.ParseMode
	}
	var response struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := c.call(ctx, "sendMessage", payload, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("Bot API sendMessage failed: %s", response.Description)
	}
	return nil
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
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
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
