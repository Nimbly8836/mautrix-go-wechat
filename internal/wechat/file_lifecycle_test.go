package wechat

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func lifecycleFileXML(kind int, length, target, md5sum string) string {
	return fmt.Sprintf(`<msg><appmsg appid="app-private"><title>dist.zip</title><type>%d</type><md5>%s</md5><appattach><totallen>%s</totallen><attachid>attach-private</attachid><cdnattachurl>cdn-private</cdnattachurl><aeskey>aes-private</aeskey><fileext>zip</fileext><uploadtoken>token-private</uploadtoken>%s</appattach></appmsg></msg>`, kind, md5sum, length, target)
}

func TestFileLifecyclePendingAndExplicitReplacement(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprint(local), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			for _, kind := range []int{74, 6} {
				raw := lifecycleFileXML(kind, "66076955", `<overwrite_newmsgid>9007199254740993</overwrite_newmsgid>`, "")
				m := convertDeferredFile(t, c, context.Background(), local, raw, "wxid_friend", "wxid_friend", "wxid_self", false)
				if m.Type != remoteapi.MsgFile || m.Text != "dist.zip" || m.Media == nil || m.Media.Size != 66076955 || len(m.Media.Data) != 0 {
					t.Fatalf("file metadata missing: %#v", m)
				}
				if m.FileTransfer == nil {
					t.Fatal("missing lifecycle metadata")
				}
				if kind == 74 {
					if !m.FileTransfer.Pending || m.FetchMedia != nil || m.FileTransfer.Replaces != "" || m.FileTransfer.TargetKey != "" {
						t.Fatal("placeholder must not fetch or replace")
					}
				} else if m.FetchMedia == nil || m.FileTransfer.Pending || m.FileTransfer.Replaces != "9007199254740993" || m.FileTransfer.TargetKey != "new:9007199254740993" {
					t.Fatal("missing explicit replacement")
				}
				public, _ := json.Marshal(m)
				for _, secret := range []string{"app-private", "attach-private", "cdn-private", "aes-private", "token-private", "appattach", "msgsource"} {
					if strings.Contains(string(public), secret) {
						t.Fatalf("public secret %s", secret)
					}
				}
			}
			if calls.Load() != 0 {
				t.Fatal("conversion performed HTTP")
			}
		})
	}
}

func TestFileLifecycleOnlyUniqueStructuralTarget(t *testing.T) {
	for _, tc := range []struct{ name, target, want string }{
		{"canonical", `<overwrite_newmsgid>000123</overwrite_newmsgid>`, "123"},
		{"missing", "", ""}, {"zero", `<overwrite_newmsgid>0</overwrite_newmsgid>`, ""},
		{"negative", `<overwrite_newmsgid>-1</overwrite_newmsgid>`, ""},
		{"overflow", `<overwrite_newmsgid>18446744073709551616</overwrite_newmsgid>`, ""},
		{"duplicate", `<overwrite_newmsgid>123</overwrite_newmsgid><overwrite_newmsgid>123</overwrite_newmsgid>`, ""},
		{"nested", `<wrapper><overwrite_newmsgid>123</overwrite_newmsgid></wrapper>`, ""},
		{"nested-value", `<overwrite_newmsgid><id>123</id></overwrite_newmsgid>`, ""},
		{"cdata", `<![CDATA[<overwrite_newmsgid>123</overwrite_newmsgid>]]>`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{wxid: "wxid_self"}
			m := convertDeferredFile(t, c, context.Background(), false, lifecycleFileXML(6, "9", tc.target, ""), "wxid_friend", "wxid_friend", "wxid_self", false)
			if tc.want == "" {
				if m.FileTransfer != nil && (m.FileTransfer.Replaces != "" || m.FileTransfer.TargetKey != "") {
					t.Fatal("guessed replacement")
				}
			} else if m.FileTransfer == nil || string(m.FileTransfer.Replaces) != tc.want || m.FileTransfer.TargetKey != "new:"+tc.want {
				t.Fatal("explicit target lost")
			}
		})
	}
}

func TestFileCDNProducerResponseAndValidation(t *testing.T) {
	data := []byte("zip-bytes")
	sum := fmt.Sprintf("%x", md5.Sum(data))
	for _, tc := range []struct {
		name, length, hash, response string
		wantErr                      bool
		calls                        int32
	}{
		{"actual-producer", "9", sum, `{"Code":0,"Success":true,"Message":"成功","Data":{"Image":"` + base64.StdEncoding.EncodeToString(data) + `"}}`, false, 1},
		{"without-hash", "9", "", `{"Code":0,"Success":true,"Data":{"Image":"emlwLWJ5dGVz"}}`, false, 1},
		{"nested-image", "9", strings.ToUpper(sum), `{"Code":0,"Success":true,"Data":{"Image":{"Image":"emlwLWJ5dGVz"}}}`, false, 1},
		{"wrong-hash", "9", strings.Repeat("0", 32), `{"Code":0,"Success":true,"Data":{"Image":"emlwLWJ5dGVz"}}`, true, 1},
		{"invalid-hash", "9", "bad", `{"Code":0,"Success":true,"Data":{"Image":"emlwLWJ5dGVz"}}`, true, 1},
		{"short", "10", "", `{"Code":0,"Success":true,"Data":{"Image":"emlwLWJ5dGVz"}}`, true, 1},
		{"long", "8", "", `{"Code":0,"Success":true,"Data":{"Image":"emlwLWJ5dGVz"}}`, true, 1},
		{"metadata-not-bytes", "9", "", `{"Code":0,"Success":true,"Data":{"FullMd5":"emlwLWJ5dGVz","CdnDataUrl":"emlwLWJ5dGVz"}}`, true, 1},
		{"failure-no-fallback", "9", "", `{"Code":-8,"Success":false,"Message":"cdn-private aes-private"}`, true, 1},
		{"nonzero-code", "9", "", `{"Code":-8,"Success":true,"Data":{"Image":"emlwLWJ5dGVz"}}`, true, 1},
		{"response-cap", "9", "", strings.Repeat("x", (1<<20)+100), true, 1},
		{"over-limit", "67108865", "", "", true, 0},
		{"zero", "0", "", "", true, 0},
		{"actual-large-size", "66076955", "", `{"Code":0,"Success":true,"Data":{"Image":"emlwLWJ5dGVz"}}`, true, 1},
		{"exact-cap-size", "67108864", "", `{"Code":0,"Success":true,"Data":{"Image":"emlwLWJ5dGVz"}}`, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/Tools/CdnDownloadRecordItem" {
					t.Errorf("wrong endpoint: %s", r.URL.Path)
				}
				var req cdnDownloadRecordReq
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Wxid != "wxid_self" || req.DataType != 8 || req.IsThumb != 0 || fmt.Sprint(req.DataSize) != tc.length || req.CdnDataUrl != "cdn-private" || req.CdnDataKey != "aes-private" || req.FullMd5 != tc.hash {
					t.Errorf("incorrect CDN request: %#v", req)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			m := convertDeferredFile(t, c, context.Background(), false, lifecycleFileXML(6, tc.length, "", tc.hash), "wxid_friend", "wxid_friend", "wxid_self", false)
			if m.FetchMedia == nil {
				t.Fatal("missing fetch")
			}
			if calls.Load() != 0 {
				t.Fatal("callback downloaded")
			}
			media, err := m.FetchMedia(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetch error=%v wantErr=%v", err, tc.wantErr)
			}
			if err != nil && (strings.Contains(err.Error(), "cdn-private") || strings.Contains(err.Error(), "aes-private")) {
				t.Fatal("error exposes credentials")
			}
			if !tc.wantErr && (media == nil || string(media.Data) != string(data) || media.Size != 9) {
				t.Fatalf("wrong media: %#v", media)
			}
			if calls.Load() != tc.calls {
				t.Fatalf("requests=%d expected=%d", calls.Load(), tc.calls)
			}
		})
	}
}

func TestFileCDNCallerDeadlineOverridesGlobalTimeout(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(40 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Code":0,"Success":true,"Data":{"Image":"emlwLWJ5dGVz"}}`)
	}))
	defer server.Close()
	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	c.t.hc.Timeout = time.Millisecond
	original := c.t.hc
	m := convertDeferredFile(t, c, context.Background(), false, lifecycleFileXML(6, "9", "", ""), "wxid_friend", "wxid_friend", "wxid_self", false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.FetchMedia(ctx); err != nil {
		t.Fatalf("bulk request inherited short global timeout: %v", err)
	}
	if c.t.hc != original || c.t.hc.Timeout != time.Millisecond {
		t.Fatal("shared client mutated")
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := m.FetchMedia(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled fetch: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("canceled fetch performed HTTP")
	}
}

func TestFileLegacyRejectsProtocolFailureAndMetadata(t *testing.T) {
	for _, response := range []string{
		`{"Success":true,"Code":0,"Data":{"BaseResponse":{"ret":-2,"errMsg":{"string":"private"}},"dataLen":0,"data":{"len":0,"buffer":""},"attachId":"emlwLWJ5dGVz"}}`,
		`{"Success":true,"Code":0,"Data":{"BaseResponse":{"ret":-2},"dataLen":9,"data":{"buffer":"emlwLWJ5dGVz"}}}`,
		`{"Success":true,"Code":0,"Data":{"BaseResponse":{"ret":0},"dataLen":0,"data":{"buffer":"emlwLWJ5dGVz"}}}`,
		`{"Success":true,"Code":0,"Data":{"BaseResponse":{},"dataLen":9,"data":{"buffer":"emlwLWJ5dGVz"}}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, response)
		}))
		c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
		m := convertDeferredFile(t, c, context.Background(), false, deferredFileXML("9"), "wxid_friend", "wxid_friend", "wxid_self", false)
		if media, err := m.FetchMedia(context.Background()); err == nil || media != nil {
			t.Errorf("accepted protocol failure: %s", response)
		}
		server.Close()
	}
}
