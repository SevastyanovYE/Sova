package gcalendar

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const calendarEventsScope = "https://www.googleapis.com/auth/calendar.events"

type Event struct {
	Title       string
	StartAt     time.Time
	EndAt       time.Time
	Timezone    string
	Location    string
	Description string
}

type CreatedEvent struct {
	ID       string
	HTMLLink string
}

func Login(ctx context.Context, cfg config.Config, in io.Reader, out io.Writer) error {
	oauthConfig, err := loadOAuthConfig(cfg)
	if err != nil {
		return err
	}
	authURL := oauthConfig.AuthCodeURL("sova-local", oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	if _, err := fmt.Fprintln(out, "Open this URL and approve Google Calendar access:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, authURL); err != nil {
		return err
	}
	if _, err := fmt.Fprint(out, "Paste authorization code: "); err != nil {
		return err
	}
	code, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("authorization code is required")
	}
	token, err := oauthConfig.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchange Google OAuth code: %w", err)
	}
	if err := saveToken(cfg.GoogleToken, token); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Google Calendar token saved.")
	return nil
}

func CreateEvent(ctx context.Context, cfg config.Config, event Event) (CreatedEvent, error) {
	if strings.TrimSpace(cfg.GoogleCalendarID) == "" {
		return CreatedEvent{}, fmt.Errorf("SOVA_GOOGLE_CALENDAR_ID is not configured")
	}
	if strings.TrimSpace(event.Title) == "" {
		return CreatedEvent{}, fmt.Errorf("calendar event title is required")
	}
	if event.StartAt.IsZero() || event.EndAt.IsZero() || !event.EndAt.After(event.StartAt) {
		return CreatedEvent{}, fmt.Errorf("calendar event requires a valid start/end")
	}
	oauthConfig, err := loadOAuthConfig(cfg)
	if err != nil {
		return CreatedEvent{}, err
	}
	token, err := loadToken(cfg.GoogleToken)
	if err != nil {
		return CreatedEvent{}, err
	}
	source := oauthConfig.TokenSource(ctx, token)
	refreshed, err := source.Token()
	if err != nil {
		return CreatedEvent{}, fmt.Errorf("refresh Google OAuth token: %w", err)
	}
	if refreshed.AccessToken != token.AccessToken || !refreshed.Expiry.Equal(token.Expiry) {
		_ = saveToken(cfg.GoogleToken, refreshed)
	}
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(refreshed))

	payload := googleEventPayload(event)
	body, err := json.Marshal(payload)
	if err != nil {
		return CreatedEvent{}, err
	}
	endpoint := "https://www.googleapis.com/calendar/v3/calendars/" + url.PathEscape(cfg.GoogleCalendarID) + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return CreatedEvent{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return CreatedEvent{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return CreatedEvent{}, err
	}
	if resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized {
			return CreatedEvent{}, fmt.Errorf("Google Calendar is unauthorized; run `sova google-login`")
		}
		return CreatedEvent{}, fmt.Errorf("Google Calendar create event returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var created struct {
		ID       string `json:"id"`
		HTMLLink string `json:"htmlLink"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		return CreatedEvent{}, fmt.Errorf("parse Google Calendar create event response: %w", err)
	}
	if created.ID == "" {
		return CreatedEvent{}, fmt.Errorf("Google Calendar create event response has empty id")
	}
	return CreatedEvent{ID: created.ID, HTMLLink: created.HTMLLink}, nil
}

func loadOAuthConfig(cfg config.Config) (*oauth2.Config, error) {
	if strings.TrimSpace(cfg.GoogleCredentials) == "" {
		return nil, fmt.Errorf("Google OAuth credentials path is not configured")
	}
	data, err := os.ReadFile(cfg.GoogleCredentials)
	if err != nil {
		return nil, fmt.Errorf("read Google OAuth credentials: %w", err)
	}
	oauthConfig, err := google.ConfigFromJSON(data, calendarEventsScope)
	if err != nil {
		return nil, fmt.Errorf("parse Google OAuth credentials: %w", err)
	}
	return oauthConfig, nil
}

func loadToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Google token missing; run `sova google-login`")
		}
		return nil, fmt.Errorf("read Google token: %w", err)
	}
	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("parse Google token: %w", err)
	}
	return &token, nil
}

func saveToken(path string, token *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func googleEventPayload(event Event) map[string]any {
	timezone := strings.TrimSpace(event.Timezone)
	if timezone == "" {
		timezone = "Europe/Moscow"
	}
	return map[string]any{
		"summary":     event.Title,
		"location":    event.Location,
		"description": event.Description,
		"start": map[string]string{
			"dateTime": event.StartAt.Format(time.RFC3339),
			"timeZone": timezone,
		},
		"end": map[string]string{
			"dateTime": event.EndAt.Format(time.RFC3339),
			"timeZone": timezone,
		},
		"reminders": map[string]any{
			"useDefault": false,
			"overrides": []map[string]any{
				{"method": "popup", "minutes": 10080},
				{"method": "popup", "minutes": 4320},
				{"method": "popup", "minutes": 1440},
				{"method": "popup", "minutes": 60},
			},
		},
	}
}
