package gcalendar

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const calendarEventsScope = "https://www.googleapis.com/auth/calendar.events"

const oauthCallbackTimeout = 5 * time.Minute

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

type oauthCallback struct {
	code string
	err  error
}

func Login(ctx context.Context, cfg config.Config, _ io.Reader, out io.Writer) error {
	oauthConfig, err := loadOAuthConfig(cfg)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start Google OAuth callback: %w", err)
	}
	defer listener.Close()
	oauthConfig.RedirectURL = "http://" + listener.Addr().String() + "/oauth2/callback"
	state, err := randomOAuthState()
	if err != nil {
		return err
	}
	callbackCh := make(chan oauthCallback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/callback", oauthCallbackHandler(state, callbackCh))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	authURL := oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	if _, err := fmt.Fprintln(out, "If the Google app is in Testing, add this account under Google Auth Platform > Audience > Test users."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "Approve Google Calendar access in your browser:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, authURL); err != nil {
		return err
	}
	if err := openBrowser(authURL); err != nil {
		_, _ = fmt.Fprintln(out, "Could not open the browser automatically; open the URL above.")
	}
	var callback oauthCallback
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(oauthCallbackTimeout):
		return fmt.Errorf("Google OAuth callback timed out")
	case serveErr := <-serveErrCh:
		if serveErr != nil && serveErr != http.ErrServerClosed {
			return fmt.Errorf("serve Google OAuth callback: %w", serveErr)
		}
		return fmt.Errorf("Google OAuth callback server stopped")
	case callback = <-callbackCh:
	}
	if callback.err != nil {
		return callback.err
	}
	token, err := oauthConfig.Exchange(ctx, callback.code)
	if err != nil {
		return fmt.Errorf("exchange Google OAuth code: %w", err)
	}
	if err := saveToken(cfg.GoogleToken, token); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Google Calendar token saved.")
	return nil
}

func oauthCallbackHandler(expectedState string, callbackCh chan<- oauthCallback) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("state") != expectedState {
			http.Error(w, "Invalid OAuth state. Return to Sova and try again.", http.StatusBadRequest)
			return
		}
		result := oauthCallback{code: strings.TrimSpace(request.URL.Query().Get("code"))}
		if oauthErr := strings.TrimSpace(request.URL.Query().Get("error")); oauthErr != "" {
			result.err = fmt.Errorf("Google OAuth authorization failed: %s", oauthErr)
		} else if result.code == "" {
			result.err = fmt.Errorf("Google OAuth callback did not contain an authorization code")
		}
		select {
		case callbackCh <- result:
		default:
		}
		if result.err != nil {
			http.Error(w, result.err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "Google Calendar authorization complete. You can close this tab.")
	}
}

func randomOAuthState() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate Google OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func openBrowser(target string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{target}
	case "linux":
		command, args = "xdg-open", []string{target}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		return fmt.Errorf("unsupported browser launcher on %s", runtime.GOOS)
	}
	return exec.Command(command, args...).Start()
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
