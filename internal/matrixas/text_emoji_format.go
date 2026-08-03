package matrixas

import (
	"context"
	"html"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func (s *Server) resolvePortalBuiltinEmoji(ctx context.Context, room id.RoomID) map[string]id.ContentURIString {
	if s == nil || room == "" {
		return map[string]id.ContentURIString{}
	}
	s.lockImagePack()
	defer s.packMu.Unlock()
	return s.resolvePortalBuiltinEmojiLocked(ctx, room)
}

func (s *Server) resolvePortalBuiltinEmojiLocked(ctx context.Context, room id.RoomID) map[string]id.ContentURIString {
	state := s.packState()
	stableCtx, cancelStable := s.matrixOperationContext(ctx)
	stableRaw, stableErr := state.Load(stableCtx, room, event.StateImagePack, builtinEmojiStateKey)
	cancelStable()
	compatCtx, cancelCompat := s.matrixOperationContext(ctx)
	compatRaw, compatErr := state.Load(compatCtx, room, event.StateUnstableImagePack, "")
	cancelCompat()

	stable := map[string]packImage{}
	if stableErr == nil {
		stable = parsePackImages(stableRaw)
	}
	compat := map[string]packImage{}
	if compatErr == nil {
		compat = parsePackImages(compatRaw)
	}

	resolved := make(map[string]id.ContentURIString)
	for _, entry := range textemoji.Entries() {
		if image, ok := stable[entry.Shortcode]; ok {
			resolved[entry.Shortcode] = image.URL
		} else if image, ok := compat[entry.Shortcode]; ok {
			resolved[entry.Shortcode] = image.URL
		}
	}
	return resolved
}

func formatInlineWechatEmoji(body string, images map[string]id.ContentURIString) (string, bool) {
	var formatted strings.Builder
	last := 0
	used := false
	textemoji.ScanShortcodes(body, func(start, end int, entry textemoji.Entry) {
		appendEscapedWechatText(&formatted, body, last, start)
		uri, ok := images[entry.Shortcode]
		validated, valid := validMXC(string(uri))
		if !ok || !valid {
			appendEscapedWechatText(&formatted, body, start, end)
		} else {
			shortcode := html.EscapeString(":" + entry.Shortcode + ":")
			formatted.WriteString(`<img data-mx-emoticon src="`)
			formatted.WriteString(html.EscapeString(string(validated)))
			formatted.WriteString(`" alt="`)
			formatted.WriteString(shortcode)
			formatted.WriteString(`" title="`)
			formatted.WriteString(shortcode)
			formatted.WriteString(`" height="32">`)
			used = true
		}
		last = end
	})
	appendEscapedWechatText(&formatted, body, last, len(body))
	if !used {
		return "", false
	}
	return formatted.String(), true
}

func appendEscapedWechatText(out *strings.Builder, body string, start, end int) {
	for index := start; index < end; {
		switch body[index] {
		case '\r':
			out.WriteString("<br>")
			index++
			if index < end && body[index] == '\n' {
				index++
			}
		case '\n':
			out.WriteString("<br>")
			index++
		case '\t':
			out.WriteString("&nbsp;&nbsp;&nbsp;&nbsp;")
			index++
		case ' ':
			runEnd := index + 1
			for runEnd < end && body[runEnd] == ' ' {
				runEnd++
			}
			if runEnd-index == 1 && hasVisibleTextBeforeOnLine(body, index) && hasVisibleTextAfterOnLine(body, runEnd) {
				out.WriteByte(' ')
			} else {
				for spaces := index; spaces < runEnd; spaces++ {
					out.WriteString("&nbsp;")
				}
			}
			index = runEnd
		default:
			textEnd := index + 1
			for textEnd < end && !isWechatHTMLWhitespace(body[textEnd]) {
				textEnd++
			}
			out.WriteString(html.EscapeString(strings.ToValidUTF8(body[index:textEnd], "\uFFFD")))
			index = textEnd
		}
	}
}

func hasVisibleTextBeforeOnLine(body string, index int) bool {
	for index--; index >= 0; index-- {
		switch body[index] {
		case '\r', '\n':
			return false
		case ' ', '\t':
			continue
		default:
			return true
		}
	}
	return false
}

func hasVisibleTextAfterOnLine(body string, index int) bool {
	for ; index < len(body); index++ {
		switch body[index] {
		case '\r', '\n':
			return false
		case ' ', '\t':
			continue
		default:
			return true
		}
	}
	return false
}

func isWechatHTMLWhitespace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\r' || char == '\n'
}
