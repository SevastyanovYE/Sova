package nest

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func testTCPDialError() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")}
}

func TestSplitMessageText(t *testing.T) {
	text := strings.Repeat("a", 10) + "\n" + strings.Repeat("b", 10) + "\n" + strings.Repeat("c", 10)
	parts := SplitMessageText(text, 15)
	if len(parts) != 3 {
		t.Fatalf("parts = %d: %#v", len(parts), parts)
	}
	for _, part := range parts {
		if TelegramTextUTF16Len(part) > 15 {
			t.Fatalf("part too long: %q", part)
		}
	}
	if strings.Join(parts, "") == "" {
		t.Fatal("split produced empty content")
	}
}

func TestSplitMessageTextUsesTelegramUTF16Units(t *testing.T) {
	text := strings.Repeat("🙂", 3000)
	parts := SplitMessageText(text, safeMessageLimit)
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	for _, part := range parts {
		if units := TelegramTextUTF16Len(part); units > safeMessageLimit {
			t.Fatalf("part has %d UTF-16 units", units)
		}
	}
	if got := strings.Join(parts, ""); got != text {
		t.Fatalf("split changed content: got %d runes, want %d", len([]rune(got)), len([]rune(text)))
	}
}

func TestRedactBotToken(t *testing.T) {
	token := "123:secret"
	input := `Post "https://api.telegram.org/bot123:secret/getUpdates": context deadline exceeded`
	got := redactBotToken(input, token)
	if strings.Contains(got, token) {
		t.Fatalf("token was not redacted: %s", got)
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("redaction marker missing: %s", got)
	}
}

func TestApplyMessagePreviewPolicy(t *testing.T) {
	payload := map[string]any{"text": "https://example.com"}
	applyMessagePreviewPolicy(payload)
	if got, ok := payload["disable_web_page_preview"].(bool); !ok || !got {
		t.Fatalf("disable_web_page_preview = %#v", payload["disable_web_page_preview"])
	}
}

func TestCallRetriesTCPDialFailureAndReturnsDefinitelyUnsent(t *testing.T) {
	const token = "123:secret"
	attempts := 0
	client := New(token)
	client.httpClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		return nil, testTCPDialError()
	})}
	client.dialRetryDelay = func(int) time.Duration { return 0 }

	err := client.call(context.Background(), "sendMessage", map[string]any{"text": "hello"}, &struct{}{})
	if attempts != telegramDialMaxAttempts {
		t.Fatalf("dial attempts = %d, want %d", attempts, telegramDialMaxAttempts)
	}
	if !IsDefinitelyUnsent(err) {
		t.Fatalf("error is not definitely-unsent: %T %v", err, err)
	}
	var unsent *DefinitelyUnsentError
	if !errors.As(err, &unsent) || unsent.Attempts() != telegramDialMaxAttempts {
		t.Fatalf("typed error = %#v", unsent)
	}
	if strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("error was not safely redacted: %v", err)
	}
}

func TestCallRetriesTCPDialFailureThenSucceeds(t *testing.T) {
	attempts := 0
	client := New("123:secret")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		if attempts < telegramDialMaxAttempts {
			return nil, testTCPDialError()
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Header:     make(http.Header),
		}, nil
	})}
	client.dialRetryDelay = func(int) time.Duration { return 0 }

	if err := client.call(context.Background(), "sendMessage", map[string]any{"text": "hello"}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if attempts != telegramDialMaxAttempts {
		t.Fatalf("dial attempts = %d, want %d", attempts, telegramDialMaxAttempts)
	}
}

func TestCallDoesNotRetryAfterConnectionStage(t *testing.T) {
	attempts := 0
	client := New("123:secret")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("TLS handshake timeout")
	})}
	client.dialRetryDelay = func(int) time.Duration { return 0 }

	err := client.call(context.Background(), "sendMessage", nil, &struct{}{})
	if err == nil || attempts != 1 {
		t.Fatalf("error=%v attempts=%d", err, attempts)
	}
	if IsDefinitelyUnsent(err) {
		t.Fatalf("post-connect failure was marked definitely-unsent: %v", err)
	}
}

func TestCallDialRetryBackoffHonorsContext(t *testing.T) {
	client := New("123:secret")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, testTCPDialError()
	})}
	client.dialRetryDelay = func(int) time.Duration { return time.Hour }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	err := client.call(ctx, "sendMessage", nil, &struct{}{})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("context cancellation took %s", elapsed)
	}
	if !IsDefinitelyUnsent(err) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%T %v", err, err)
	}
}

func TestCallReturnsTypedTelegramMessageNotFound(t *testing.T) {
	client := New("123:secret")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error_code":400,"description":"Bad Request: message to delete not found"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	err := client.DeleteMessage(context.Background(), -1001, 750)
	if !IsTelegramMessageNotFound(err) {
		t.Fatalf("delete error is not typed not-found: %T %v", err, err)
	}
	var apiErr *BotAPIError
	if !errors.As(err, &apiErr) || apiErr.Method != "deleteMessage" || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("typed Bot API error = %#v", apiErr)
	}
}
