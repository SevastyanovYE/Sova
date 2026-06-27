package gcalendar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGoogleEventPayload(t *testing.T) {
	start := time.Date(2026, 6, 18, 10, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	payload := googleEventPayload(Event{
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
