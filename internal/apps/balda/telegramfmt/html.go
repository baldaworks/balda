package telegramfmt

import (
	gohtml "html"
	"strings"
	"unicode"
)

const (
	htmlTagBlockquote = "blockquote"
	htmlTagPre        = "pre"
)

// HTML escapes unsafe raw text while preserving Telegram-supported HTML tags.
func HTML(text string) string {
	var out strings.Builder
	stack := make([]string, 0, 8)
	for i := 0; i < len(text); {
		switch text[i] {
		case '<':
			if i+1 >= len(text) || !isHTMLTagStart(text[i+1]) {
				out.WriteString("&lt;")
				i++
				continue
			}
			end := findHTMLTagEnd(text[i:])
			if end < 0 {
				out.WriteString("&lt;")
				i++
				continue
			}
			raw := text[i : i+end+1]
			parent := ""
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			if tag, name, closing, ok := telegramHTMLTag(raw, parent); ok {
				if closing {
					if len(stack) > 0 && stack[len(stack)-1] == name {
						stack = stack[:len(stack)-1]
						out.WriteString(tag)
					} else {
						out.WriteString(gohtml.EscapeString(raw))
					}
				} else {
					out.WriteString(tag)
					if !strings.HasSuffix(tag, "/>") {
						stack = append(stack, name)
					}
				}
			} else {
				out.WriteString(gohtml.EscapeString(raw))
			}
			i += end + 1
		case '&':
			if entity, ok := validHTMLEntity(text[i:]); ok {
				out.WriteString(entity)
				i += len(entity)
			} else {
				out.WriteString("&amp;")
				i++
			}
		default:
			next := nextHTMLSpecial(text[i:])
			out.WriteString(gohtml.EscapeString(text[i : i+next]))
			i += next
		}
	}
	return out.String()
}

// HTMLPlainText derives a deterministic parse-mode-free fallback from model
// HTML. Tag-shaped input is removed before entities are decoded so raw and
// entity-encoded markup cannot become active or visible markup in fallback.
func HTMLPlainText(text string) string {
	text = gohtml.UnescapeString(text)
	var out strings.Builder
	for i := 0; i < len(text); {
		if text[i] != '<' || i+1 >= len(text) || !isHTMLTagStart(text[i+1]) {
			out.WriteByte(text[i])
			i++
			continue
		}
		end := findHTMLTagEnd(text[i:])
		if end < 0 {
			break
		}
		if isHTMLBlockTag(text[i:i+end+1]) && out.Len() > 0 {
			out.WriteByte('\n')
		}
		i += end + 1
	}
	return strings.TrimSpace(gohtml.UnescapeString(out.String()))
}

func isHTMLBlockTag(raw string) bool {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">"))
	body = strings.TrimPrefix(body, "/")
	name, _ := splitHTMLTagName(strings.TrimSpace(body))
	switch strings.ToLower(strings.TrimSuffix(name, "/")) {
	case htmlTagBlockquote, "br", "div", "h1", "h2", "h3", "h4", "h5", "h6", "li", "p", htmlTagPre:
		return true
	default:
		return false
	}
}

func telegramHTMLTag(raw, parent string) (tag string, name string, closing bool, ok bool) {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">"))
	if body == "" {
		return "", "", false, false
	}
	if strings.HasPrefix(body, "/") {
		name = strings.ToLower(strings.TrimSpace(body[1:]))
		if !isTelegramHTMLTag(name) {
			return "", "", false, false
		}
		return "</" + name + ">", name, true, true
	}

	selfClosing := strings.HasSuffix(body, "/")
	if selfClosing {
		body = strings.TrimSpace(strings.TrimSuffix(body, "/"))
	}
	name, rest := splitHTMLTagName(body)
	name = strings.ToLower(name)
	if !isTelegramHTMLTag(name) {
		return "", "", false, false
	}

	attrs, ok := telegramHTMLAttrs(name, rest, parent)
	if !ok {
		return "", "", false, false
	}
	if selfClosing {
		return "<" + name + attrs + "/>", name, false, true
	}
	return "<" + name + attrs + ">", name, false, true
}

func splitHTMLTagName(body string) (name string, rest string) {
	for i, r := range body {
		if unicode.IsSpace(r) {
			return body[:i], strings.TrimSpace(body[i:])
		}
	}
	return body, ""
}

func isTelegramHTMLTag(name string) bool {
	switch name {
	case "b", "strong", "i", "em", "u", "ins", "s", "strike", "del", "tg-spoiler", "a", "code", htmlTagPre, htmlTagBlockquote, "tg-emoji", "tg-time", "span":
		return true
	default:
		return false
	}
}

func telegramHTMLAttrs(name, raw, parent string) (string, bool) {
	attrs := parseHTMLAttrs(raw)
	switch name {
	case "a":
		if href, ok := attrs["href"]; ok && strings.TrimSpace(href) != "" {
			return ` href="` + gohtml.EscapeString(href) + `"`, true
		}
	case "code":
		if class, ok := attrs["class"]; ok && parent == htmlTagPre && strings.HasPrefix(class, "language-") {
			return ` class="` + gohtml.EscapeString(class) + `"`, true
		}
		return "", true
	case htmlTagBlockquote:
		if _, ok := attrs["expandable"]; ok {
			return " expandable", true
		}
		return "", true
	case "span":
		if class, ok := attrs["class"]; ok && class == "tg-spoiler" {
			return ` class="tg-spoiler"`, true
		}
		return "", false
	case "tg-emoji":
		if emojiID, ok := attrs["emoji-id"]; ok && strings.TrimSpace(emojiID) != "" {
			return ` emoji-id="` + gohtml.EscapeString(emojiID) + `"`, true
		}
	case "tg-time":
		unix, ok := attrs["unix"]
		if !ok || strings.TrimSpace(unix) == "" {
			return "", false
		}
		out := ` unix="` + gohtml.EscapeString(unix) + `"`
		if format, ok := attrs["format"]; ok && strings.TrimSpace(format) != "" {
			out += ` format="` + gohtml.EscapeString(format) + `"`
		}
		return out, true
	default:
		return "", true
	}
	return "", false
}

func parseHTMLAttrs(raw string) map[string]string {
	attrs := make(map[string]string)
	for len(raw) > 0 {
		raw = strings.TrimLeftFunc(raw, unicode.IsSpace)
		if raw == "" {
			return attrs
		}
		key, rest := splitHTMLAttrKey(raw)
		if key == "" {
			return attrs
		}
		raw = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if !strings.HasPrefix(raw, "=") {
			attrs[strings.ToLower(key)] = ""
			continue
		}
		raw = strings.TrimLeftFunc(raw[1:], unicode.IsSpace)
		value, rest := splitHTMLAttrValue(raw)
		attrs[strings.ToLower(key)] = value
		raw = rest
	}
	return attrs
}

func splitHTMLAttrKey(raw string) (key string, rest string) {
	for i, r := range raw {
		if unicode.IsSpace(r) || r == '=' {
			return raw[:i], raw[i:]
		}
	}
	return raw, ""
}

func splitHTMLAttrValue(raw string) (value string, rest string) {
	if raw == "" {
		return "", ""
	}
	quote := raw[0]
	if quote == '"' || quote == '\'' {
		end := strings.IndexByte(raw[1:], quote)
		if end < 0 {
			return raw[1:], ""
		}
		return raw[1 : end+1], raw[end+2:]
	}
	for i, r := range raw {
		if unicode.IsSpace(r) {
			return raw[:i], raw[i:]
		}
	}
	return raw, ""
}

func validHTMLEntity(text string) (string, bool) {
	end := strings.IndexByte(text, ';')
	if end < 0 {
		return "", false
	}
	entity := text[:end+1]
	switch entity {
	case "&lt;", "&gt;", "&amp;", "&quot;":
		return entity, true
	}
	if strings.HasPrefix(entity, "&#x") || strings.HasPrefix(entity, "&#X") {
		if len(entity) <= 4 {
			return "", false
		}
		for _, r := range entity[3 : len(entity)-1] {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return "", false
			}
		}
		return entity, true
	}
	if strings.HasPrefix(entity, "&#") && len(entity) > 3 {
		for _, r := range entity[2 : len(entity)-1] {
			if r < '0' || r > '9' {
				return "", false
			}
		}
		return entity, true
	}
	return "", false
}

func nextHTMLSpecial(text string) int {
	for i, r := range text {
		if r == '<' || r == '&' {
			return i
		}
	}
	return len(text)
}

func isHTMLTagStart(b byte) bool {
	return b == '/' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func findHTMLTagEnd(text string) int {
	var quote byte
	for i := 1; i < len(text); i++ {
		switch text[i] {
		case '\'', '"':
			switch quote {
			case 0:
				quote = text[i]
			case text[i]:
				quote = 0
			}
		case '>':
			if quote == 0 {
				return i
			}
		}
	}
	return -1
}
