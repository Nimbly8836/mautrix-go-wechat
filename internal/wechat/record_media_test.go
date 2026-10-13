package wechat

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

//go:embed testdata/record-video.mp4
var recordVideoBytes []byte

func TestMergedRecordDownloadsVideoWithoutForwarding(t *testing.T) {
	for _, envelope := range []bool{false, true} {
		t.Run(fmt.Sprint(envelope), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/Tools/CdnDownloadRecordItem" {
					t.Errorf("unexpected API (must never forward records): %s", r.URL.Path)
					http.Error(w, "unexpected", 500)
					return
				}
				var req cdnDownloadRecordReq
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Wxid != "wxid_self" || req.DataId != "video-id" || req.CdnDataKey != "video-key" || req.CdnDataUrl != "video-cdn" || req.DataSize != 12 || req.IsThumb != 0 || req.DataType != 4 {
					t.Errorf("wrong download request: %#v", req)
				}
				if envelope {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"Success": true, "Data": map[string]any{"data": map[string]any{"Buffer": base64.StdEncoding.EncodeToString(recordVideoBytes)}}})
				} else {
					w.Header().Set("Content-Type", "video/mp4")
					_, _ = w.Write(recordVideoBytes)
				}
			}))
			defer srv.Close()
			c := &Client{wxid: "wxid_self", t: newTransport(srv.URL), events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
			payload, _ := json.Marshal(map[string]any{"MsgType": 49, "MsgID": 1, "NewMsgID": 2, "FromUserName": "group@chatroom", "ToUserName": "wxid_self", "Content": "wxid_friend:\n" + mergedRecordXML, "ContentText": "[聊天记录]"})
			if err := c.HandleWebhook(t.Context(), payload); err != nil {
				t.Fatal(err)
			}
			select {
			case ev := <-c.Events():
				if ev.Message == nil || ev.Message.Record == nil || len(ev.Message.Record.Items) != 2 {
					t.Fatalf("missing record: %+v", ev.Message)
				}
				if ev.Message.Sender != "wxid_friend" {
					t.Fatalf("spoofed sender: %s", ev.Message.Sender)
				}
				item := ev.Message.Record.Items[1]
				if requests != 1 || item.Media == nil || !bytes.Equal(item.Media.Data, recordVideoBytes) || item.Media.MimeType != "video/mp4" || item.MediaStatus != "" {
					t.Fatalf("bad downloaded video: requests=%d item=%+v", requests, item)
				}
				for _, secret := range []string{"video-key", "video-cdn", "sourceheadurl", "support.weixin.qq.com"} {
					if strings.Contains(ev.Message.Text, secret) {
						t.Errorf("leaked %q", secret)
					}
				}
			default:
				t.Fatal("no event")
			}
		})
	}
}

func TestMergedRecordMediaFailuresKeepTranscript(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		body              []byte
		status            int
	}{
		{"expired", "application/json", []byte(`{"Success":false,"Message":"SECRET-CDN-KEY"}`), 200},
		{"truncated", "video/mp4", recordVideoBytes[:5], 200},
		{"metadata-only", "application/json", []byte(`{"Success":true,"Data":{"md5":"YWJjZGVmZ2hpamts","url":"SECRET"}}`), 200},
		{"missing-endpoint", "text/plain", []byte("not supported"), 404},
		{"oversized", "application/octet-stream", bytes.Repeat([]byte{'a'}, (1<<20)+100), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer srv.Close()
			c := &Client{wxid: "wxid_self", t: newTransport(srv.URL)}
			ev, ok := c.convertLocal(t.Context(), localMessage{MsgType: 49, Talker: "wxid_friend", Content: mergedRecordXML})
			if !ok || ev.Message.Record == nil {
				t.Fatal("lost bundle")
			}
			item := ev.Message.Record.Items[1]
			if item.Media != nil || item.MediaStatus == "" || !strings.Contains(ev.Message.Text, "没有视频（完整正文）") || strings.Contains(ev.Message.Text, "SECRET") {
				t.Fatalf("invalid failure fallback: %+v", ev.Message)
			}
		})
	}
}

func TestRecordDownloadLimitsDoNotRequest(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; http.Error(w, "unexpected", 500) }))
	defer srv.Close()
	c := &Client{wxid: "wxid_self", t: newTransport(srv.URL)}
	for _, tc := range []struct {
		size, key string
		budget    recordBudget
		cancel    bool
	}{
		{"-1", "key", recordBudget{}, false},
		{"bad", "key", recordBudget{}, false},
		{"16777217", "key", recordBudget{}, false},
		{"12", "", recordBudget{}, false},
		{"12", "key", recordBudget{downloads: 8}, false},
		{"12", "key", recordBudget{bytes: maxRecordTotalBytes - 1}, false},
		{"12", "key", recordBudget{}, true},
	} {
		ctx, cancel := context.WithCancel(t.Context())
		if tc.cancel {
			cancel()
		}
		media, status := c.downloadRecordMedia(ctx, recordXMLItem{Size: tc.size, Key: tc.key, URL: "cdn"}, &tc.budget)
		cancel()
		if media != nil || status == "" {
			t.Fatalf("limit not honored: %+v", tc)
		}
	}
	if requests != 0 {
		t.Fatalf("sent %d disallowed requests", requests)
	}
}
