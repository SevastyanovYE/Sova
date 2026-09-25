package gcalendar

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestGoogleEventPayload(t *testing.T) {
	start := time.Date(2026, 6, 18, 10, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	payload := googleEventPayload(Event{
		ID:          "sovaevent123",
		Title:       "[ОММ] Экзамен",
		StartAt:     start,
		EndAt:       start.Add(2 * time.Hour),
		Timezone:    "Europe/Moscow",
		Location:    "504",
		Description: "Source link",
	})
	if payload["summary"] != "[ОММ] Экзамен" || payload["location"] != "504" {
		t.Fatalf("payload summary/location = %#v", payload)
	}
	if payload["id"] != "sovaevent123" {
		t.Fatalf("payload id = %#v", payload["id"])
	}
	reminders := payload["reminders"].(map[string]any)
	if reminders["useDefault"].(bool) {
		t.Fatal("expected custom reminders")
	}
	overrides := reminders["overrides"].([]map[string]any)
	want := []int{10080, 4320, 1440, 60}
	if len(overrides) != len(want) {
		t.Fatalf("reminders = %#v", overrides)
	}
	for i, minutes := range want {
		if overrides[i]["minutes"] != minutes {
			t.Fatalf("reminder %d = %#v", i, overrides[i])
		}
	}
}

func TestEventIDForCandidateIsStableAndGoogleCompatible(t *testing.T) {
	first := EventIDForCandidate(42)
	if first != EventIDForCandidate(42) || first == EventIDForCandidate(43) {
		t.Fatalf("event ids are not stable/unique: %q", first)
	}
	for _, r := range first {
		if !((r >= 'a' && r <= 'v') || (r >= '0' && r <= '9')) {
			t.Fatalf("event id contains unsupported rune %q: %q", r, first)
		}
	}
}

func TestCreateEventRecoversLostPostResponseByDeterministicID(t *testing.T) {
	eventID := EventIDForCandidate(7)
	postCalls := 0
	getCalls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.Method {
		case http.MethodPost:
			postCalls++
			return nil, errors.New("response lost after accept")
		case http.MethodGet:
			getCalls++
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"id":"` + eventID + `","htmlLink":"https://calendar.example/event"}`)), Header: make(http.Header)}, nil
		default:
			return nil, errors.New("unexpected method")
		}
	})}
	created, err := createEventWithClient(context.Background(), client, "https://calendar.example/calendars/main/events", Event{ID: eventID, Title: "Экзамен", StartAt: time.Now(), EndAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != eventID || postCalls != 1 || getCalls != 1 {
		t.Fatalf("created=%+v post=%d get=%d", created, postCalls, getCalls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestOAuthCallbackHandlerValidatesStateAndReturnsCode(t *testing.T) {
	callbacks := make(chan oauthCallback, 1)
	handler := oauthCallbackHandler("expected", callbacks)

	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/oauth2/callback?state=wrong&code=bad", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad state status = %d", bad.Code)
	}
	select {
	case callback := <-callbacks:
		t.Fatalf("bad state produced callback: %+v", callback)
	default:
	}

	good := httptest.NewRecorder()
	handler.ServeHTTP(good, httptest.NewRequest(http.MethodGet, "/oauth2/callback?state=expected&code=auth-code", nil))
	if good.Code != http.StatusOK || !strings.Contains(good.Body.String(), "authorization complete") {
		t.Fatalf("good response = %d %q", good.Code, good.Body.String())
	}
	callback := <-callbacks
	if callback.err != nil || callback.code != "auth-code" {
		t.Fatalf("callback = %+v", callback)
	}
}

func TestGoogleTokenErrorExplainsRecoveryWithoutProviderBody(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"invalid_grant", "sova google-login"},
		{"invalid_token", "sova google-login"},
		{"invalid_client", "OAuth-клиент"},
		{"unauthorized_client", "OAuth-клиент"},
		{"temporarily_unavailable", "позже"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			err := googleTokenError(&oauth2.RetrieveError{ErrorCode: tc.code, Body: []byte("private-refresh-token")})
			if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-refresh-token") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	err := googleTokenError(errors.New("oauth2: token expired and refresh token is not set"))
	if !strings.Contains(err.Error(), "sova google-login") {
		t.Fatalf("error = %v", err)
	}
	if !errors.Is(googleTokenError(context.Canceled), context.Canceled) {
		t.Fatal("lost context cancellation")
	}
}

func TestSaveTokenReplacesAtomicallyAndPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	want := &oauth2.Token{AccessToken: "test-access", RefreshToken: "test-refresh", Expiry: time.Now().UTC().Add(time.Hour)}
	if err := saveToken(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || !got.Expiry.Equal(want.Expiry) {
		t.Fatal("token did not round trip")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions = %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left: %v (%v)", entries, err)
	}
}
