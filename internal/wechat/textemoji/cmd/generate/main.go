package main

import (
	"bytes"
	"encoding/csv"
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"strconv"
	"strings"
)

const entryCount = 109

type manifestEntry struct {
	Index     int
	Chinese   string
	English   string
	Shortcode string
	Filename  string
}

func main() {
	manifestPath := flag.String("manifest", "", "path to the approved CSV manifest")
	outputPath := flag.String("output", "", "path for the generated Go source")
	flag.Parse()
	if *manifestPath == "" || *outputPath == "" || flag.NArg() != 0 {
		fatalf("usage: generate -manifest PATH -output PATH")
	}

	manifest, err := os.Open(*manifestPath)
	if err != nil {
		fatalf("read manifest: %v", err)
	}
	defer manifest.Close()

	source, err := generate(manifest)
	if err != nil {
		fatalf("generate source: %v", err)
	}
	if err = os.WriteFile(*outputPath, source, 0o644); err != nil {
		fatalf("write output: %v", err)
	}
}

func generate(manifest io.Reader) ([]byte, error) {
	entries, err := readManifest(manifest)
	if err != nil {
		return nil, err
	}
	return generateSource(entries)
}

func readManifest(manifest io.Reader) ([]manifestEntry, error) {
	reader := csv.NewReader(manifest)
	reader.FieldsPerRecord = 5
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	wantHeader := []string{"index", "chinese", "english", "shortcode", "filename"}
	if len(records) == 0 || !equalStrings(records[0], wantHeader) {
		return nil, fmt.Errorf("header must be %q", wantHeader)
	}
	if len(records)-1 != entryCount {
		return nil, fmt.Errorf("got %d entries, want %d", len(records)-1, entryCount)
	}

	entries := make([]manifestEntry, 0, entryCount)
	for rowIndex, record := range records[1:] {
		line := rowIndex + 2
		expectedIndex := rowIndex + 1
		if record[0] != fmt.Sprintf("%03d", expectedIndex) {
			return nil, fmt.Errorf("row %d index %q is not sequential index %03d", line, record[0], expectedIndex)
		}
		index, err := strconv.Atoi(record[0])
		if err != nil {
			return nil, fmt.Errorf("row %d index: %w", line, err)
		}
		entry := manifestEntry{
			Index:     index,
			Chinese:   record[1],
			English:   record[2],
			Shortcode: record[3],
			Filename:  record[4],
		}
		fields := []struct {
			name  string
			value string
		}{
			{name: "chinese", value: entry.Chinese},
			{name: "english", value: entry.English},
			{name: "shortcode", value: entry.Shortcode},
			{name: "filename", value: entry.Filename},
		}
		for _, field := range fields {
			if field.value == "" {
				return nil, fmt.Errorf("row %d has empty %s", line, field.name)
			}
			if field.value != strings.TrimSpace(field.value) {
				return nil, fmt.Errorf("row %d %s has surrounding whitespace", line, field.name)
			}
		}
		entries = append(entries, entry)
	}
	if err := validateUnique(entries); err != nil {
		return nil, err
	}
	for rowIndex, entry := range entries {
		line := rowIndex + 2
		if want := canonicalShortcode(entry.English); entry.Shortcode != want {
			return nil, fmt.Errorf("row %d shortcode %q, want %q", line, entry.Shortcode, want)
		}
		if want := fmt.Sprintf("%03d_%s.webp", entry.Index, entry.Chinese); entry.Filename != want {
			return nil, fmt.Errorf("row %d filename %q, want %q", line, entry.Filename, want)
		}
	}
	return entries, nil
}

func validateUnique(entries []manifestEntry) error {
	chinese := make(map[string]int, len(entries))
	shortcodes := make(map[string]int, len(entries))
	filenames := make(map[string]int, len(entries))
	for _, entry := range entries {
		if err := addUnique("Chinese", chinese, entry.Chinese, entry.Index); err != nil {
			return err
		}
		if err := addUnique("shortcode", shortcodes, entry.Shortcode, entry.Index); err != nil {
			return err
		}
		if err := addUnique("filename", filenames, entry.Filename, entry.Index); err != nil {
			return err
		}
	}
	for i, left := range entries {
		for j := i + 1; j < len(entries); j++ {
			right := entries[j]
			if strings.EqualFold(left.English, right.English) {
				return fmt.Errorf("duplicate case-folded English %q at indexes %d and %d", left.English, left.Index, right.Index)
			}
		}
		for j, right := range entries {
			if i != j && strings.EqualFold(left.Chinese, right.English) {
				return fmt.Errorf("cross-entry cross-language collision: entry %d Chinese %q equals entry %d English %q", left.Index, left.Chinese, right.Index, right.English)
			}
		}
	}
	return nil
}

func addUnique(field string, seen map[string]int, value string, index int) error {
	if previous, ok := seen[value]; ok {
		return fmt.Errorf("duplicate %s %q at indexes %d and %d", field, value, previous, index)
	}
	seen[value] = index
	return nil
}

func canonicalShortcode(alias string) string {
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

func generateSource(entries []manifestEntry) ([]byte, error) {
	var source bytes.Buffer
	source.WriteString("// Code generated by go generate; DO NOT EDIT.\n\n")
	source.WriteString("package textemoji\n\n")
	source.WriteString("var generatedEntries = []Entry{\n")
	for _, entry := range entries {
		fmt.Fprintf(&source, "{Index: %d, Chinese: %q, English: %q, Shortcode: %q, Filename: %q},\n",
			entry.Index, entry.Chinese, entry.English, entry.Shortcode, entry.Filename)
	}
	source.WriteString("}\n")
	return format.Source(source.Bytes())
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "generate text emoji registry: "+format+"\n", args...)
	os.Exit(1)
}
