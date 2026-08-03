package textemoji

import (
	"strings"
	"testing"
	"unicode"
)

func TestMappingAllAliasesToShortcodes(t *testing.T) {
	for _, entry := range Entries() {
		want := ":" + entry.Shortcode + ":"
		aliases := []struct {
			name  string
			alias string
		}{
			{name: "Chinese", alias: entry.Chinese},
			{name: "English", alias: entry.English},
			{name: "EnglishLower", alias: strings.ToLower(entry.English)},
			{name: "EnglishUpper", alias: strings.ToUpper(entry.English)},
			{name: "EnglishMixed", alias: mixedCase(entry.English)},
		}
		for _, alias := range aliases {
			t.Run(entry.Shortcode+"/"+alias.name, func(t *testing.T) {
				input := "[" + alias.alias + "]"
				if got := ToShortcodes(input); got != want {
					t.Fatalf("ToShortcodes(%q) = %q, want %q", input, got, want)
				}
			})
		}
	}
}

func TestMappingAllShortcodesToChinese(t *testing.T) {
	for _, entry := range Entries() {
		t.Run(entry.Shortcode, func(t *testing.T) {
			input := ":" + entry.Shortcode + ":"
			want := "[" + entry.Chinese + "]"
			if got := ToChineseTokens(input); got != want {
				t.Fatalf("ToChineseTokens(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestMappingAdjacentMixedAndUnknownTokens(t *testing.T) {
	input := "前[微笑][SMILE]中[Let dOwN][unknown]后"
	want := "前:wechat_smile::wechat_smile:中:wechat_let_down:[unknown]后"
	if got := ToShortcodes(input); got != want {
		t.Fatalf("ToShortcodes(%q) = %q, want %q", input, got, want)
	}

	reverseInput := "前:wechat_smile::wechat_cry:中:wechat_unknown:后"
	reverseWant := "前[微笑][大哭]中:wechat_unknown:后"
	if got := ToChineseTokens(reverseInput); got != reverseWant {
		t.Fatalf("ToChineseTokens(%q) = %q, want %q", reverseInput, got, reverseWant)
	}
}

func TestMappingPreservesMalformedBracketTokens(t *testing.T) {
	overlong := "[" + strings.Repeat("界", 22) + "]" // 66 UTF-8 payload bytes.
	tests := []string{
		"[[Smile]]",
		"[outer[Smile]]",
		"[Smile",
		"]Smile[",
		overlong,
		"[" + strings.Repeat("a", 65) + "[Smile]]",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if got := ToShortcodes(input); got != input {
				t.Fatalf("ToShortcodes malformed input changed bytes: got %q want %q", got, input)
			}
		})
	}
}

func TestMappingDoesNotScanBracketsAcrossNewlines(t *testing.T) {
	input := "[Smile\n] [Smile]\r[微笑\nSmile]"
	want := "[Smile\n] :wechat_smile:\r[微笑\nSmile]"
	if got := ToShortcodes(input); got != want {
		t.Fatalf("ToShortcodes(%q) = %q, want %q", input, got, want)
	}
}

func TestMappingReplacesOutboundURLAndCodeLikeProse(t *testing.T) {
	input := "https://example.test/:wechat_smile:/x `:wechat_cry:` code=:wechat_wink:"
	want := "https://example.test/[微笑]/x `[大哭]` code=[调皮]"
	if got := ToChineseTokens(input); got != want {
		t.Fatalf("ToChineseTokens(%q) = %q, want %q", input, got, want)
	}
}

func TestMappingPreservesUnknownInputByteForByte(t *testing.T) {
	input := string([]byte{'a', 0xff, '[', 'N', 'o', 'p', 'e', ']', 0x00, ':', 'w', 'e', 'c', 'h', 'a', 't', '_', 'N', 'O', 'P', 'E', ':', 0xfe})
	if got := ToShortcodes(input); got != input {
		t.Fatalf("ToShortcodes changed unknown bytes: got %x want %x", []byte(got), []byte(input))
	}
	if got := ToChineseTokens(input); got != input {
		t.Fatalf("ToChineseTokens changed unknown bytes: got %x want %x", []byte(got), []byte(input))
	}
}

func TestScanShortcodesReportsKnownCanonicalByteOffsets(t *testing.T) {
	input := "前:wechat_smile::wechat_unknown:\n:wechat_cry:后"
	type match struct {
		start int
		end   int
		entry Entry
	}
	var got []match
	ScanShortcodes(input, func(start, end int, entry Entry) {
		got = append(got, match{start: start, end: end, entry: entry})
	})

	entries := Entries()
	if len(got) != 2 {
		t.Fatalf("ScanShortcodes yielded %d matches, want 2: %#v", len(got), got)
	}
	wantStarts := []int{len("前"), len("前:wechat_smile::wechat_unknown:\n")}
	wantEntries := []Entry{entries[0], entries[9]}
	for i := range got {
		if got[i].start != wantStarts[i] {
			t.Errorf("match %d start = %d, want %d", i, got[i].start, wantStarts[i])
		}
		wantEnd := wantStarts[i] + len(":"+wantEntries[i].Shortcode+":")
		if got[i].end != wantEnd {
			t.Errorf("match %d end = %d, want %d", i, got[i].end, wantEnd)
		}
		if got[i].entry != wantEntries[i] {
			t.Errorf("match %d entry = %#v, want %#v", i, got[i].entry, wantEntries[i])
		}
	}
}

func TestScanShortcodesRequiresCompleteKnownCanonicalTokens(t *testing.T) {
	overlong := ":wechat_" + strings.Repeat("a", 65) + ":"
	input := strings.Join([]string{
		":wechat_smile",
		"wechat_smile:",
		":WECHAT_SMILE:",
		":wechat_unknown:",
		":wechat_smile\n:",
		overlong,
	}, "|")
	var matches int
	ScanShortcodes(input, func(_, _ int, _ Entry) { matches++ })
	if matches != 0 {
		t.Fatalf("ScanShortcodes yielded %d malformed or unknown tokens, want 0", matches)
	}
	if got := ToChineseTokens(input); got != input {
		t.Fatalf("ToChineseTokens changed malformed or unknown input: got %q want %q", got, input)
	}
}

func mixedCase(input string) string {
	var out strings.Builder
	upper := true
	for _, r := range input {
		if unicode.IsLetter(r) {
			if upper {
				r = unicode.ToUpper(r)
			} else {
				r = unicode.ToLower(r)
			}
			upper = !upper
		}
		out.WriteRune(r)
	}
	return out.String()
}
