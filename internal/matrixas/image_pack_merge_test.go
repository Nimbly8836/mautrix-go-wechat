package matrixas

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestRawImagePackStateContract(t *testing.T) {
	var _ rawImagePackState = (*testRawImagePackState)(nil)
	var _ rawImagePackState = matrixRawImagePackState{}
}

func TestMatrixRawImagePackStateLoadsAndSavesRawJSON(t *testing.T) {
	loaded := json.RawMessage(`{"pack":{"extension":true},"images":{}}`)
	var saved json.RawMessage
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if strings.Contains(r.URL.Path, "missing") {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"missing"}`))
				return
			} else if strings.Contains(r.URL.Path, "forbidden") {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"forbidden"}`))
				return
			}
			_, _ = w.Write(loaded)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read state body: %v", err)
			}
			saved = append(json.RawMessage(nil), body...)
			_, _ = w.Write([]byte(`{"event_id":"$saved"}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer homeserver.Close()

	as := appservice.Create()
	as.Registration = &appservice.Registration{SenderLocalpart: "wechatbot", AppToken: "test-token"}
	as.HomeserverDomain = "example.org"
	if err := as.SetHomeserverURL(homeserver.URL); err != nil {
		t.Fatalf("SetHomeserverURL failed: %v", err)
	}
	if err := as.StateStore.SetMembership(context.Background(), "!room:example.org", as.BotMXID(), event.MembershipJoin); err != nil {
		t.Fatalf("seed bot membership: %v", err)
	}
	adapter := matrixRawImagePackState{server: &Server{as: as}}
	got, err := adapter.Load(context.Background(), "!room:example.org", event.StateImagePack, "wechat.test")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	assertJSONEqual(t, got, string(loaded))

	_, err = adapter.Load(context.Background(), "!room:example.org", event.StateImagePack, "missing")
	if !errors.Is(err, errImagePackStateNotFound) {
		t.Fatalf("missing Matrix state error = %v, want errImagePackStateNotFound", err)
	}
	_, err = adapter.Load(context.Background(), "!room:example.org", event.StateImagePack, "forbidden")
	if err == nil || errors.Is(err, errImagePackStateNotFound) {
		t.Fatalf("generic Matrix state error = %v, want non-sentinel error", err)
	}

	wantSaved := json.RawMessage(`{"pack":{"unknown":"kept"},"images":{"one":{"url":"mxc://example.org/one","extra":1}}}`)
	if err = adapter.Save(context.Background(), "!room:example.org", event.StateImagePack, "wechat.test", wantSaved); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	assertJSONEqual(t, saved, string(wantSaved))
}

type testRawImagePackState struct{}

func (*testRawImagePackState) Load(context.Context, id.RoomID, event.Type, string) (json.RawMessage, error) {
	return nil, nil
}

func (*testRawImagePackState) Save(context.Context, id.RoomID, event.Type, string, json.RawMessage) error {
	return nil
}

func TestImagePackMergePreservesRawCompatibilityFields(t *testing.T) {
	raw := json.RawMessage(`{
		"pack":{"display_name":"Legacy","org.example.metadata":true},
		"org.example.top":{"nested":[1,2,3]},
		"images":{
			"keep":{"url":"mxc://example.org/keep","body":"Keep","org.example.keep":"yes"},
			"update":{"url":"mxc://example.org/old","body":"Old","org.example.image":{"value":7}}
		}
	}`)

	merged, err := mergePackImages(raw, map[string]json.RawMessage{
		"update": json.RawMessage(`{"url":"mxc://example.org/new","body":"New"}`),
		"add":    json.RawMessage(`{"url":"mxc://example.org/add","body":"Added","org.example.new":9}`),
	})
	if err != nil {
		t.Fatalf("mergePackImages returned error: %v", err)
	}

	var got map[string]any
	if err = json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("merged state is invalid JSON: %v", err)
	}
	var want map[string]any
	_ = json.Unmarshal([]byte(`{
		"pack":{"display_name":"Legacy","org.example.metadata":true},
		"org.example.top":{"nested":[1,2,3]},
		"images":{
			"keep":{"url":"mxc://example.org/keep","body":"Keep","org.example.keep":"yes"},
			"update":{"url":"mxc://example.org/new","body":"New","org.example.image":{"value":7}},
			"add":{"url":"mxc://example.org/add","body":"Added","org.example.new":9}
		}
	}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged state mismatch\n got: %s\nwant: %#v", merged, want)
	}
}

func TestImagePackMergeDeletesNilReplacement(t *testing.T) {
	merged, err := mergePackImages(json.RawMessage(`{"images":{"remove":{"url":"mxc://example.org/remove"},"keep":{"url":"mxc://example.org/keep"}},"extra":1}`), map[string]json.RawMessage{
		"remove": nil,
	})
	if err != nil {
		t.Fatalf("mergePackImages returned error: %v", err)
	}
	assertJSONEqual(t, merged, `{"images":{"keep":{"url":"mxc://example.org/keep"}},"extra":1}`)
}

func TestImagePackMergeTreatsMalformedOrNonObjectStateAsEmptyObject(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`not-json`), json.RawMessage(`[]`), json.RawMessage(`null`), json.RawMessage(`"text"`)} {
		t.Run(string(raw), func(t *testing.T) {
			merged, err := mergePackImages(raw, map[string]json.RawMessage{
				"new": json.RawMessage(`{"url":"mxc://example.org/new"}`),
			})
			if err != nil {
				t.Fatalf("mergePackImages returned error: %v", err)
			}
			assertJSONEqual(t, merged, `{"images":{"new":{"url":"mxc://example.org/new"}}}`)
		})
	}
}

func TestImagePackMergeRejectsMalformedReplacement(t *testing.T) {
	if _, err := mergePackImages(json.RawMessage(`{"untouched":true}`), map[string]json.RawMessage{"bad": json.RawMessage(`{`)}); err == nil {
		t.Fatal("expected malformed replacement error")
	}
}

func TestImagePackParseReturnsOnlyObjectImagesWithValidMXC(t *testing.T) {
	got := parsePackImages(json.RawMessage(`{"images":{
		"good":{"url":"mxc://example.org/media_1","body":"Label","org.example.extra":true},
		"no_body":{"url":"mxc://example.org/media-2"},
		"bad_url":{"url":"https://example.org/image.png","body":"Bad"},
		"not_object":"mxc://example.org/nope",
		"null":null
	}}`))
	want := map[string]packImage{
		"good":    {URL: id.ContentURIString("mxc://example.org/media_1"), Body: "Label"},
		"no_body": {URL: id.ContentURIString("mxc://example.org/media-2")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePackImages() = %#v, want %#v", got, want)
	}

	for _, raw := range []json.RawMessage{nil, json.RawMessage(`bad`), json.RawMessage(`[]`), json.RawMessage(`{"images":[]}`)} {
		if parsed := parsePackImages(raw); len(parsed) != 0 {
			t.Fatalf("parsePackImages(%s) = %#v, want empty", raw, parsed)
		}
	}
}

func TestImagePackValidMXCAcceptsMatrixRFC3986Names(t *testing.T) {
	valid := []string{
		"mxc://example.org/media_1",
		"mxc://matrix.example.org:8448/a-b_C9",
		"mxc://matrix_server.example/media~name",
		"mxc://matrix%2Dserver.example/media",
		"mxc://matrix!$&'()*+,;=.example/media",
		"mxc://example.org:65535/a-z_A.Z~9!$&'()*+,;=:@%2F",
		"mxc://[2001:db8::1]/media",
		"mxc://[2001:db8::1]:8448/media",
	}
	for _, raw := range valid {
		got, ok := validMXC(raw)
		if !ok || got != id.ContentURIString(raw) {
			t.Errorf("validMXC(%q) = (%q, %v), want (%q, true)", raw, got, ok, raw)
		}
	}
}

func TestImagePackValidMXCRejectsInvalidAuthorityAndMediaID(t *testing.T) {
	invalid := []string{
		"", "https://example.org/media", "mxc://example.org", "mxc:///media", "mxc://example.org/",
		"mxc://user@example.org/media", "mxc://example.org?query/media", "mxc://example.org#fragment/media",
		"mxc://bad%2.example/media", "mxc://bad%ZZ.example/media",
		"mxc://example.org:/media", "mxc://example.org:0/media", "mxc://example.org:65536/media", "mxc://example.org:bad/media",
		"mxc://2001:db8::1/media", "mxc://[2001:db8::1/media", "mxc://[not-ipv6]/media",
		"mxc://example.org/a/b", "mxc://example.org/a?download=1", "mxc://example.org/a#fragment",
		"mxc://example.org/bad%2", "mxc://example.org/bad%ZZ", "mxc://example.org/has space",
		" mxc://example.org/media", "mxc://example.org/media ",
	}
	for _, raw := range invalid {
		if got, ok := validMXC(raw); ok || got != "" {
			t.Errorf("validMXC(%q) = (%q, %v), want (\"\", false)", raw, got, ok)
		}
	}
}

func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid got JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid want JSON %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}
