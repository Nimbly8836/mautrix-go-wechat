package wechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestSendMessageVideoCallsSendVideo(t *testing.T) {
	videoBytes := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p'}
	var sawSendVideo bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Msg/SendVideo":
			sawSendVideo = true
			var req sendVideoReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode SendVideo request: %v", err)
			}
			if req.Wxid != "wxid_self" || req.ToWxid != "wxid_friend" {
				t.Fatalf("unexpected SendVideo recipient: %#v", req)
			}
			if req.Base64 != base64.StdEncoding.EncodeToString(videoBytes) {
				t.Fatalf("unexpected video base64: %q", req.Base64)
			}
			if req.PlayLength != 7 {
				t.Fatalf("expected play length 7, got %d", req.PlayLength)
			}
			_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{"NewMsgId":789}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	msgID, err := c.SendMessage(context.Background(), "wxid_friend", remoteapi.OutgoingMessage{
		Type:  remoteapi.MsgVideo,
		Text:  "clip.mp4",
		Media: &remoteapi.Media{Data: videoBytes, MimeType: "video/mp4", FileName: "clip.mp4", Size: int64(len(videoBytes))},
		Extra: map[string]any{"play_length": 7},
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	if msgID != "789" {
		t.Fatalf("unexpected msg id: %q", msgID)
	}
	if !sawSendVideo {
		t.Fatalf("expected /Msg/SendVideo to be called")
	}
}
