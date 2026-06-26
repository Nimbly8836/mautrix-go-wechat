package wechat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestSendMessageFileUploadsBinaryThenSendsAppMessage(t *testing.T) {
	var sawUpload bool
	var sawSendApp bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Tools/UploadFileBinary":
			sawUpload = true
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("ParseMultipartForm: %v", err)
			}
			if got := r.FormValue("wxid"); got != "wxid_self" {
				t.Fatalf("unexpected wxid: %q", got)
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("missing file part: %v", err)
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			if string(data) != "zip-bytes" || header.Filename != "archive.zip" {
				t.Fatalf("unexpected upload file: name=%q data=%q", header.Filename, data)
			}
			_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{"mediaId":"media-123","totalLen":9}}`))
		case "/Msg/SendApp":
			sawSendApp = true
			var req sendAppReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode SendApp request: %v", err)
			}
			if req.Wxid != "wxid_self" || req.ToWxid != "wxid_friend" || req.Type != 6 {
				t.Fatalf("unexpected SendApp request: %#v", req)
			}
			for _, want := range []string{"<type>6</type>", "<title>archive.zip</title>", "<totallen>9</totallen>", "<attachid>media-123</attachid>", "<fileext>zip</fileext>"} {
				if !strings.Contains(req.Xml, want) {
					t.Fatalf("SendApp XML missing %s: %s", want, req.Xml)
				}
			}
			_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{"NewMsgId":123}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	msgID, err := c.SendMessage(context.Background(), "wxid_friend", remoteapi.OutgoingMessage{
		Type:  remoteapi.MsgFile,
		Text:  "archive.zip",
		Media: &remoteapi.Media{Data: []byte("zip-bytes"), MimeType: "application/zip", FileName: "archive.zip", Size: 9},
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	if msgID != "123" {
		t.Fatalf("unexpected msg id: %q", msgID)
	}
	if !sawUpload || !sawSendApp {
		t.Fatalf("expected upload and SendApp calls, saw upload=%v sendApp=%v", sawUpload, sawSendApp)
	}
}
