package workspace

import (
	"io"
	"strings"
	"unicode/utf16"

	xhtml "golang.org/x/net/html"
)

const workspaceTelegramSafeTextLimit = 3900

func truncatePlainUTF16(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || utf16Length(value) <= limit {
		return value
	}
	runes := []rune(value)
	used := 0
	end := 0
	reserve := utf16.RuneLen('…')
	for end < len(runes) {
		next := utf16.RuneLen(runes[end])
		if used+next+reserve > limit {
			break
		}
		used += next
		end++
	}
	return strings.TrimSpace(string(runes[:end])) + "…"
}

func telegramHTMLFits(value string, limit int) bool {
	units, err := telegramHTMLUTF16Len(value)
	return err == nil && units <= limit
}

func telegramHTMLUTF16Len(value string) (int, error) {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(value))
	total := 0
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			if err := tokenizer.Err(); err != nil && err != io.EOF {
				return 0, err
			}
			return total, nil
		case xhtml.TextToken:
			total += utf16Length(tokenizer.Token().Data)
		}
	}
}
