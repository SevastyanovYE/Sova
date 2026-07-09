package workspace

import (
	"context"
	"html"
	"strconv"
	"strings"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type NotePublishProvider interface {
	FormatNote(ctx context.Context, request NotePublishRequest) (NotePublishResult, error)
}

type NotePublishRequest struct {
	Title    string
	Parts    []sqlitestore.WorkspaceDocumentPart
	Revision string
}

type NotePublishResult struct {
	Messages []string
}

type mockNotePublishProvider struct{}

func NewNotePublishProvider(cfg config.Config) NotePublishProvider {
	return mockNotePublishProvider{}
}

func (mockNotePublishProvider) FormatNote(ctx context.Context, request NotePublishRequest) (NotePublishResult, error) {
	var b strings.Builder
	b.WriteString("💎 <b>")
	b.WriteString(html.EscapeString(strings.TrimSpace(request.Title)))
	b.WriteString("</b>\n\n")
	for _, part := range request.Parts {
		title := documentPartTitle(part, "Часть "+strconv.Itoa(part.PartNo))
		if len(request.Parts) > 1 {
			b.WriteString("<b>")
			b.WriteString(html.EscapeString(title))
			b.WriteString("</b>\n")
		}
		text := strings.TrimSpace(part.Text)
		if text == "" {
			text = "[media]"
		}
		b.WriteString(html.EscapeString(text))
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(request.Revision) != "" {
		b.WriteString("<blockquote>Mock preview пересобран с учётом правки, без добавления новых фактов.</blockquote>")
	}
	return NotePublishResult{Messages: []string{strings.TrimSpace(b.String())}}, nil
}
