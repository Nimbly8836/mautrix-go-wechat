package textemoji

import "strings"

const maxTokenPayloadBytes = 64

var (
	entriesByChinese   = make(map[string]Entry, len(generatedEntries))
	entriesByEnglish   = make(map[string]Entry, len(generatedEntries))
	entriesByShortcode = make(map[string]Entry, len(generatedEntries))
)

func init() {
	for _, entry := range generatedEntries {
		entriesByChinese[entry.Chinese] = entry
		entriesByEnglish[strings.ToLower(entry.English)] = entry
		entriesByShortcode[entry.Shortcode] = entry
	}
}

// ToShortcodes replaces known bracketed WeChat aliases with canonical shortcodes.
func ToShortcodes(text string) string {
	var out strings.Builder
	out.Grow(len(text))

	bracketDepth := 0
	for pos := 0; pos < len(text); {
		if bracketDepth > 0 {
			current := text[pos]
			out.WriteByte(current)
			switch current {
			case '[':
				bracketDepth++
			case ']':
				bracketDepth--
			case '\n', '\r':
				bracketDepth = 0
			}
			pos++
			continue
		}

		if text[pos] != '[' {
			out.WriteByte(text[pos])
			pos++
			continue
		}

		end, payload, complete := scanBracketToken(text, pos)
		if !complete {
			out.WriteByte(text[pos])
			bracketDepth = 1
			pos++
			continue
		}

		if entry, ok := entryForAlias(payload); ok {
			out.WriteByte(':')
			out.WriteString(entry.Shortcode)
			out.WriteByte(':')
		} else {
			out.WriteString(text[pos:end])
		}
		pos = end
	}

	return out.String()
}

// ToChineseTokens replaces complete known canonical shortcodes with Chinese tokens.
func ToChineseTokens(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	last := 0
	ScanShortcodes(text, func(start, end int, entry Entry) {
		out.WriteString(text[last:start])
		out.WriteByte('[')
		out.WriteString(entry.Chinese)
		out.WriteByte(']')
		last = end
	})
	out.WriteString(text[last:])
	return out.String()
}

// ScanShortcodes yields the byte offsets and registry entry for each complete
// known canonical shortcode. End offsets are exclusive.
func ScanShortcodes(text string, yield func(start, end int, entry Entry)) {
	if yield == nil {
		return
	}
	for start := 0; start < len(text); start++ {
		if text[start] != ':' {
			continue
		}
		end, entry, known := scanShortcodeToken(text, start)
		if !known {
			continue
		}
		yield(start, end, entry)
		start = end - 1
	}
}

func entryForAlias(alias string) (Entry, bool) {
	if entry, ok := entriesByChinese[alias]; ok {
		return entry, true
	}
	entry, ok := entriesByEnglish[strings.ToLower(alias)]
	if !ok || !strings.EqualFold(alias, entry.English) {
		return Entry{}, false
	}
	return entry, true
}

func scanShortcodeToken(text string, start int) (end int, entry Entry, known bool) {
	payloadStart := start + 1
	for pos := payloadStart; pos < len(text); pos++ {
		payloadBytes := pos - payloadStart
		if payloadBytes > maxTokenPayloadBytes {
			return 0, Entry{}, false
		}
		switch text[pos] {
		case '\n', '\r':
			return 0, Entry{}, false
		case ':':
			entry, known := entriesByShortcode[text[payloadStart:pos]]
			return pos + 1, entry, known
		}
	}
	return 0, Entry{}, false
}

func scanBracketToken(text string, start int) (end int, payload string, complete bool) {
	payloadStart := start + 1
	for pos := payloadStart; pos < len(text); pos++ {
		payloadBytes := pos - payloadStart
		if payloadBytes > maxTokenPayloadBytes {
			return 0, "", false
		}
		switch text[pos] {
		case '\n', '\r', '[':
			return 0, "", false
		case ']':
			return pos + 1, text[payloadStart:pos], true
		}
	}
	return 0, "", false
}
