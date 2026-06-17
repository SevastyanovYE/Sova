package gcalendar

import (
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
