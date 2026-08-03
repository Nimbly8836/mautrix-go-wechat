package textemoji

import (
	"encoding/csv"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const approvedManifest = "../../../docs/superpowers/specs/2026-08-03-wechat-text-emoji-registry.csv"

func TestEntriesMatchApprovedManifest(t *testing.T) {
	want := readManifest(t, approvedManifest)
	got := Entries()
	if !slices.Equal(want, got) {
		t.Fatalf("registry mismatch: got %#v want %#v", got, want)
	}
}

func TestEntriesSatisfyRegistryInvariants(t *testing.T) {
	entries := Entries()
	if len(entries) != 109 {
		t.Fatalf("entry count = %d, want 109", len(entries))
	}

	chinese := make(map[string]int, len(entries))
	shortcodes := make(map[string]int, len(entries))
	filenames := make(map[string]int, len(entries))
	for i, entry := range entries {
		wantIndex := i + 1
		if entry.Index != wantIndex {
			t.Errorf("entry %d index = %d, want %d", i, entry.Index, wantIndex)
		}
		assertUnique(t, "Chinese", chinese, entry.Chinese, entry.Index)
		assertUnique(t, "shortcode", shortcodes, entry.Shortcode, entry.Index)
		assertUnique(t, "filename", filenames, entry.Filename, entry.Index)

		wantShortcode := CanonicalShortcode(entry.English)
		if entry.Shortcode != wantShortcode {
			t.Errorf("entry %d shortcode = %q, want CanonicalShortcode(%q) = %q", entry.Index, entry.Shortcode, entry.English, wantShortcode)
		}

		wantFilename := fmt.Sprintf("%03d_%s.webp", entry.Index, entry.Chinese)
		if entry.Filename != wantFilename {
			t.Errorf("entry %d filename = %q, want %q", entry.Index, entry.Filename, wantFilename)
		}
	}

	for i, left := range entries {
		for j := i + 1; j < len(entries); j++ {
			right := entries[j]
			if strings.EqualFold(left.English, right.English) {
				t.Errorf("duplicate case-folded English %q at indexes %d and %d", left.English, left.Index, right.Index)
			}
		}
		for j, right := range entries {
			if i != j && strings.EqualFold(left.Chinese, right.English) {
				t.Errorf("cross-entry cross-language collision: entry %d Chinese %q equals entry %d English %q", left.Index, left.Chinese, right.Index, right.English)
			}
		}
	}
}

func TestEntriesReturnsDefensiveCopy(t *testing.T) {
	first := Entries()
	first[0] = Entry{}

	second := Entries()
	if second[0] == (Entry{}) {
		t.Fatal("mutating Entries result changed the compiled registry")
	}
}

func TestEntriesCanonicalShortcode(t *testing.T) {
	tests := []struct {
		alias string
		want  string
	}{
		{alias: "Bah！R", want: "wechat_bah_r"},
		{alias: "Let Down", want: "wechat_let_down"},
		{alias: "Yeah!", want: "wechat_yeah"},
		{alias: "GoForIt", want: "wechat_goforit"},
	}
	for _, test := range tests {
		t.Run(test.alias, func(t *testing.T) {
			if got := CanonicalShortcode(test.alias); got != test.want {
				t.Fatalf("CanonicalShortcode(%q) = %q, want %q", test.alias, got, test.want)
			}
		})
	}
}

func assertUnique(t *testing.T, field string, seen map[string]int, value string, index int) {
	t.Helper()
	if previous, ok := seen[value]; ok {
		t.Errorf("duplicate %s %q at indexes %d and %d", field, value, previous, index)
		return
	}
	seen[value] = index
}

func readManifest(t *testing.T, path string) []Entry {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []string{"index", "chinese", "english", "shortcode", "filename"}
	if len(records) == 0 || !slices.Equal(records[0], wantHeader) {
		t.Fatalf("manifest header = %v, want %v", records[0], wantHeader)
	}

	entries := make([]Entry, 0, len(records)-1)
	for row, record := range records[1:] {
		if len(record) != 5 {
			t.Fatalf("manifest row %d has %d columns, want 5", row+2, len(record))
		}
		index, err := strconv.Atoi(record[0])
		if err != nil {
			t.Fatalf("manifest row %d index: %v", row+2, err)
		}
		entries = append(entries, Entry{
			Index:     index,
			Chinese:   record[1],
			English:   record[2],
			Shortcode: record[3],
			Filename:  record[4],
		})
	}
	return entries
}
