package workspace

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/semanticsearch"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestSearchResultsStayWithinOneTelegramHTMLMessage(t *testing.T) {
	cfg := config.Config{Timezone: "Europe/Moscow"}
	results := make([]semanticsearch.SearchResult, 0, 10)
	for index := 0; index < 10; index++ {
		results = append(results, semanticsearch.SearchResult{Document: sqlitestore.SearchDocument{
			Scope: "legacy", MessageID: index + 1, MessageDate: time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC),
			Text: strings.Repeat("🙂", 220), SourceLink: fmt.Sprintf("https://t.me/c/2498436858/%d", index+1),
		}})
	}
	text := formatWorkspaceSearchResults(cfg, strings.Repeat("🦉", 4000), results)
	units, err := telegramHTMLUTF16Len(text)
	if err != nil {
		t.Fatal(err)
	}
	if units > workspaceTelegramSafeTextLimit {
		t.Fatalf("search response has %d UTF-16 units", units)
	}
	if strings.Count(text, "🦉") > 150 {
		t.Fatalf("search query was not bounded: %d owl runes", strings.Count(text, "🦉"))
	}
}
