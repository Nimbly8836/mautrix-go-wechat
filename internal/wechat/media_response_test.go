package wechat

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestMediaBytesFromJSONFindsNestedBase64Fields(t *testing.T) {
	want := []byte("image-bytes")
	raw, _ := json.Marshal(map[string]any{
		"file": map[string]any{
			"data": map[string]any{
				"fileData": base64.StdEncoding.EncodeToString(want),
			},
		},
	})
	got, _, err := mediaBytesFromJSON(raw)
	if err != nil {
		t.Fatalf("mediaBytesFromJSON returned error: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("unexpected bytes: %q", got)
	}
}

func TestMediaBytesFromJSONDecodesNumericByteArray(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"buffer": []any{float64(65), float64(66), float64(67)},
	})
	got, _, err := mediaBytesFromJSON(raw)
	if err != nil {
		t.Fatalf("mediaBytesFromJSON returned error: %v", err)
	}
	if string(got) != "ABC" {
		t.Fatalf("unexpected bytes: %q", got)
	}
}
