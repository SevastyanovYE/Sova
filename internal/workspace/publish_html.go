package workspace

import (
	"fmt"
	stdhtml "html"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
)

var publishAllowedHTMLTags = map[string]struct{}{
	"b":          {},
	"i":          {},
	"blockquote": {},
}

// validatePublishHTML applies the deliberately small Telegram HTML contract
// used by Publish. Telegram counts the decoded message text in UTF-16 code
// units, not the serialized markup, so length validation is kept separate.
func validatePublishHTML(value string) error {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(value))
	var stack []string
	for {
		tokenType := tokenizer.Next()
		raw := string(tokenizer.Raw())
		switch tokenType {
		case xhtml.ErrorToken:
			if err := tokenizer.Err(); err != nil && err != io.EOF {
				return fmt.Errorf("parse Telegram HTML: %w", err)
			}
			if len(stack) > 0 {
				return fmt.Errorf("unclosed Telegram HTML tag <%s>", stack[len(stack)-1])
			}
			return nil
		case xhtml.StartTagToken:
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
			if _, ok := publishAllowedHTMLTags[name]; !ok {
				return fmt.Errorf("Telegram HTML tag <%s> is not allowed in publish output", name)
			}
			if len(token.Attr) > 0 {
				return fmt.Errorf("Telegram HTML attributes are not allowed on <%s>", name)
			}
			stack = append(stack, name)
		case xhtml.EndTagToken:
			name := strings.ToLower(tokenizer.Token().Data)
			if len(stack) == 0 || stack[len(stack)-1] != name {
				return fmt.Errorf("unbalanced Telegram HTML closing tag </%s>", name)
			}
			stack = stack[:len(stack)-1]
		case xhtml.TextToken:
			if err := validateTelegramHTMLText(raw); err != nil {
				return err
			}
		case xhtml.SelfClosingTagToken:
			return fmt.Errorf("self-closing Telegram HTML tags are not allowed")
		case xhtml.CommentToken, xhtml.DoctypeToken:
			return fmt.Errorf("comments and doctypes are not allowed in Telegram HTML")
		}
	}
}

func validateTelegramHTMLText(raw string) error {
	for index := 0; index < len(raw); {
		switch raw[index] {
		case '<', '>':
			return fmt.Errorf("literal %q must be escaped in Telegram HTML", raw[index])
		case '&':
			end := strings.IndexByte(raw[index+1:], ';')
			if end < 0 {
				return fmt.Errorf("unescaped ampersand in Telegram HTML")
			}
			end += index + 1
			entity := raw[index+1 : end]
			if err := validateTelegramHTMLEntity(entity); err != nil {
				return err
			}
			index = end + 1
		default:
			index++
		}
	}
	return nil
}

func validateTelegramHTMLEntity(entity string) error {
	switch entity {
	case "lt", "gt", "amp", "quot":
		return nil
	}
	base := 10
	digits := ""
	if strings.HasPrefix(entity, "#x") || strings.HasPrefix(entity, "#X") {
		base = 16
		digits = entity[2:]
	} else if strings.HasPrefix(entity, "#") {
		digits = entity[1:]
	}
	if digits == "" {
		return fmt.Errorf("unsupported Telegram HTML entity &%s;", entity)
	}
	value, err := strconv.ParseInt(digits, base, 32)
	if err != nil || value <= 0 || value > utf8.MaxRune || !utf8.ValidRune(rune(value)) {
		return fmt.Errorf("invalid numeric Telegram HTML entity &%s;", entity)
	}
	return nil
}

func publishHTMLUTF16Len(value string) (int, error) {
	if err := validatePublishHTML(value); err != nil {
		return 0, err
	}
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
			total += utf16StringLen(tokenizer.Token().Data)
		}
	}
}

func splitLongHTML(value string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("HTML split limit must be positive")
	}
	if err := validatePublishHTML(value); err != nil {
		return nil, err
	}
	tokenizer := xhtml.NewTokenizer(strings.NewReader(value))
	var (
		out          []string
		current      strings.Builder
		currentUnits int
		openTags     []string
		hasText      bool
	)
	reopen := func() {
		for _, tag := range openTags {
			current.WriteString("<" + tag + ">")
		}
	}
	flush := func() error {
		if !hasText {
			return nil
		}
		for i := len(openTags) - 1; i >= 0; i-- {
			current.WriteString("</" + openTags[i] + ">")
		}
		part := strings.TrimSpace(current.String())
		if part == "" {
			return nil
		}
		units, err := publishHTMLUTF16Len(part)
		if err != nil {
			return err
		}
		if units > limit {
			return fmt.Errorf("balanced Telegram HTML chunk exceeds %d UTF-16 code units", limit)
		}
		out = append(out, part)
		current.Reset()
		currentUnits = 0
		hasText = false
		reopen()
		return nil
	}
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			if err := tokenizer.Err(); err != nil && err != io.EOF {
				return nil, err
			}
			if err := flush(); err != nil {
				return nil, err
			}
			return out, nil
		case xhtml.StartTagToken:
			name := strings.ToLower(tokenizer.Token().Data)
			current.WriteString("<" + name + ">")
			openTags = append(openTags, name)
		case xhtml.EndTagToken:
			name := strings.ToLower(tokenizer.Token().Data)
			current.WriteString("</" + name + ">")
			openTags = openTags[:len(openTags)-1]
		case xhtml.TextToken:
			runes := []rune(tokenizer.Token().Data)
			for len(runes) > 0 {
				available := limit - currentUnits
				if available <= 0 {
					if err := flush(); err != nil {
						return nil, err
					}
					continue
				}
				n := fittingUTF16Runes(runes, available)
				if n == 0 {
					if err := flush(); err != nil {
						return nil, err
					}
					continue
				}
				if n < len(runes) {
					n = preferredPublishSplit(runes, n)
				}
				piece := string(runes[:n])
				current.WriteString(stdhtml.EscapeString(piece))
				currentUnits += utf16StringLen(piece)
				if strings.TrimSpace(piece) != "" {
					hasText = true
				}
				runes = runes[n:]
				if len(runes) > 0 {
					if err := flush(); err != nil {
						return nil, err
					}
				}
			}
		}
	}
}

func fittingUTF16Runes(runes []rune, limit int) int {
	used := 0
	for index, r := range runes {
		units := 1
		if r > 0xffff {
			units = 2
		}
		if used+units > limit {
			return index
		}
		used += units
	}
	return len(runes)
}

func preferredPublishSplit(runes []rune, maximum int) int {
	if maximum <= 1 {
		return maximum
	}
	// Prefer paragraph, line and word boundaries, but do not create a very
	// small chunk merely because an early boundary exists.
	floor := maximum / 2
	for _, separator := range []rune{'\n', ' '} {
		for index := maximum - 1; index >= floor; index-- {
			if runes[index] == separator {
				return index + 1
			}
		}
	}
	return maximum
}

func utf16StringLen(value string) int {
	total := 0
	for _, r := range value {
		if r > 0xffff {
			total += 2
		} else {
			total++
		}
	}
	return total
}
