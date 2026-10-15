package wechat

import (
	"context"
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

func deferredFileXML(length string) string {
	return `<msg><appmsg appid="app-private"><title>document.pdf</title><type>6</type><des>ignored summary</des><appattach><totallen>` + length + `</totallen><attachid>attach-private</attachid><aeskey>aes-private</aeskey><fileext>pdf</fileext></appattach></appmsg><fromusername>wxid_friend</fromusername></msg>`
}

func convertDeferredFile(t *testing.T, c *Client, ctx context.Context, local bool, raw, sender, chat, to string, self bool) *remoteapi.InboundMessage {
	t.Helper()
	var ev remoteapi.Event
	var ok bool
	if local {
		ev, ok = c.convertLocal(ctx, localMessage{MsgID: 1, NewMsgID: 2, MsgType: wxApp, Content: raw, ContentText: "[文件] summary", MsgSource: "<msgsource>private</msgsource>", SenderUserName: sender, Talker: chat, ToUserName: to, IsSendBySelf: self, IsGroup: strings.HasSuffix(chat, "@chatroom")})
	} else {
		ev, ok = c.convertInbound(ctx, rawMessage{MsgID: 1, NewMsgID: 2, MsgType: wxApp, Content: syncStringDoc{Str: raw}, ContentText: "[文件] summary", MsgSource: "<msgsource>private</msgsource>", SenderUserName: sender, Talker: chat, FromUserName: syncStringDoc{Str: chat}, ToUserName: syncStringDoc{Str: to}, IsSendBySelf: self, IsGroup: strings.HasSuffix(chat, "@chatroom")})
	}
	if !ok || ev.Message == nil {
		t.Fatal("missing converted message")
	}
	return ev.Message
}

func TestDeferredFileMetadataAndIndependentContext(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%t", local), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var req downloadFileReq
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Wxid != "wxid_self" || req.AttachId != "attach-private" || req.Section.DataLen != 9 {
					t.Errorf("wrong request: %#v", req)
				}
				w.Header().Set("Content-Type", "application/pdf")
				_, _ = w.Write([]byte("%PDF-test"))
			}))
			defer server.Close()
			c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			callbackCtx, cancel := context.WithCancel(context.Background())
			m := convertDeferredFile(t, c, callbackCtx, local, deferredFileXML("9"), "wxid_friend", "wxid_friend", "wxid_self", false)
			cancel()
			if requests.Load() != 0 {
				t.Fatal("download occurred during conversion")
			}
			if m.Type != remoteapi.MsgFile || m.Media == nil || m.Media.FileName != "document.pdf" || m.Media.MimeType != "application/pdf" || m.Media.Size != 9 || len(m.Media.Data) != 0 || m.FetchMedia == nil {
				t.Fatalf("missing deferred metadata: %#v", m)
			}
			public, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"attach-private", "aes-private", "app-private", "appattach", "msgsource"} {
				if strings.Contains(string(public), secret) {
					t.Fatalf("private data leaked: %s", secret)
				}
			}
			media, err := m.FetchMedia(context.Background())
			if err != nil || media == nil || string(media.Data) != "%PDF-test" || media.Size != 9 {
				t.Fatalf("fetch after callback cancellation: media=%#v err=%v", media, err)
			}
			if requests.Load() != 1 || len(m.Media.Data) != 0 {
				t.Fatal("fetch mutated pending message or wrong request count")
			}
		})
	}
}

func TestDeferredFileSenderAndAccountBinding(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, group := range []bool{false, true} {
			t.Run(fmt.Sprintf("local=%t/group=%t", local, group), func(t *testing.T) {
				var requests atomic.Int32
				chat, sender, raw, self := "wxid_self", "wxid_self", strings.ReplaceAll(deferredFileXML("9"), "wxid_friend", "wxid_self"), true
				wantUser := "wxid_peer"
				if group {
					chat, sender, raw, self, wantUser = "group@chatroom", "wxid_friend", "wxid_friend:\n"+deferredFileXML("9"), false, "wxid_friend"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					var req downloadFileReq
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					if req.UserName != wantUser {
						t.Errorf("download user=%s want=%s", req.UserName, wantUser)
					}
					w.Header().Set("Content-Type", "application/pdf")
					_, _ = w.Write([]byte("%PDF-test"))
				}))
				defer server.Close()
				c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
				m := convertDeferredFile(t, c, context.Background(), local, raw, sender, chat, "wxid_peer", self)
				if m.FetchMedia == nil || m.Type != remoteapi.MsgFile {
					t.Fatalf("file not deferred: %#v", m)
				}
				if _, err := m.FetchMedia(context.Background()); err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				c.wxid = "wxid_other"
				c.mu.Unlock()
				if media, err := m.FetchMedia(context.Background()); err == nil || media != nil {
					t.Fatal("account change allowed stale attachment fetch")
				}
				if requests.Load() != 1 {
					t.Fatal("stale account made network request")
				}
			})
		}
	}
}

func TestDeferredFileRejectsMismatchedGroupPrefix(t *testing.T) {
	c := &Client{wxid: "wxid_self"}
	for _, local := range []bool{false, true} {
		m := convertDeferredFile(t, c, context.Background(), local, "wxid_imposter:\n"+deferredFileXML("9"), "wxid_friend", "group@chatroom", "wxid_self", false)
		if m.FetchMedia != nil {
			if media, err := m.FetchMedia(context.Background()); err == nil || media != nil {
				t.Fatal("unauthorized sender prefix accepted")
			}
		}
		if strings.Contains(m.Text, "attach-private") || strings.Contains(m.Text, "aes-private") {
			t.Fatal("invalid source exposed XML")
		}
	}
}

func TestDeferredFileCallerCancellationStopsHTTP(t *testing.T) {
	started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	m := convertDeferredFile(t, c, context.Background(), false, deferredFileXML("9"), "wxid_friend", "wxid_friend", "wxid_self", false)
	if m.FetchMedia == nil {
		t.Fatal("missing deferred fetch")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := m.FetchMedia(ctx); result <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("HTTP did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wrong cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fetch ignored cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("HTTP request not canceled")
	}
	if m.Type != remoteapi.MsgFile || m.Media.FileName != "document.pdf" {
		t.Fatal("cancellation lost metadata")
	}
}

func TestDeferredFileRejectsInvalidLengthsAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name, length               string
		size, status, wantRequests int
	}{
		{"oversize", "67108865", 9, 200, 0},
		{"overflow", "9223372036854775808", 9, 200, 0},
		{"negative", "-1", 9, 200, 0},
		{"zero", "0", 9, 200, 0},
		{"short", "9", 8, 200, 1},
		{"long", "9", 10, 200, 1},
		{"truncated-last-chunk", "65537", 65536, 200, 2},
		{"http-error", "9", 9, 500, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(tc.status)
				if n == 2 {
					return
				}
				_, _ = w.Write([]byte(strings.Repeat("x", tc.size)))
			}))
			defer server.Close()
			c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			m := convertDeferredFile(t, c, context.Background(), false, deferredFileXML(tc.length), "wxid_friend", "wxid_friend", "wxid_self", false)
			if m.FetchMedia == nil || m.Type != remoteapi.MsgFile || m.Media == nil || m.Media.FileName != "document.pdf" {
				t.Fatalf("error path lost file identity: %#v", m)
			}
			if media, err := m.FetchMedia(context.Background()); err == nil || media != nil {
				t.Fatalf("invalid response accepted: %#v %v", media, err)
			}
			if requests.Load() != int32(tc.wantRequests) {
				t.Fatalf("requests=%d want=%d", requests.Load(), tc.wantRequests)
			}
			if m.Type != remoteapi.MsgFile || m.Media.FileName != "document.pdf" {
				t.Fatal("failure lost metadata")
			}
		})
	}
}
