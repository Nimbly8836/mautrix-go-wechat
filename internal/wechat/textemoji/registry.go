package textemoji

import "strings"

//go:generate go run ./cmd/generate -manifest ../../../docs/superpowers/specs/2026-08-03-wechat-text-emoji-registry.csv -output registry_generated.go

// Entry describes one built-in WeChat text emoji.
type Entry struct {
	Index     int
	Chinese   string
	English   string
	Shortcode string
	Filename  string
}

// Entries returns a copy of the canonical registry.
func Entries() []Entry {
	return append([]Entry(nil), generatedEntries...)
}

// CanonicalShortcode converts an English alias to its canonical shortcode.
func CanonicalShortcode(alias string) string {
	alias = strings.ToLower(strings.TrimSpace(alias))
	var out strings.Builder
	separator := false
	for _, r := range alias {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if separator && out.Len() > 0 {
				out.WriteByte('_')
			}
			separator = false
			out.WriteRune(r)
		} else {
			separator = true
		}
	}
	normalized := strings.Trim(out.String(), "_")
	if normalized == "" {
		return ""
	}
	return "wechat_" + normalized
}
