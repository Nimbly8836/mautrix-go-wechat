package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

const syntheticInviteURL = "https://support.weixin.qq.com/cgi-bin/mmsupport-bin/addchatroombyinvite?ticket=Aw2BG%2BMRgOpUiS1l7SCkXQ%3D%3D"

func TestParseChatRoomInviteKeepsURLPrivateAndExtractsDisplayPrefix(t *testing.T) {
	invite, ok := parseChatRoomInvite(`[Hayek]
	邀请你加入群聊
	"Jonathan🤴🏼"邀请你加入群聊"NGA线上交流2.8"，进入可查看详情。
	` + syntheticInviteURL)
	if !ok || invite.URL != syntheticInviteURL {
		t.Fatalf("invite = %#v, ok=%v", invite, ok)
	}
	if invite.Inviter == "" {
		t.Fatalf("inviter was not extracted")
	}
	if strings.Contains(invite.Inviter, "ticket") || strings.Contains(invite.ChatName, "ticket") {
		t.Fatal("invite capability leaked into display metadata")
	}
}

func TestParseChatRoomInviteRejectsUntrustedURLs(t *testing.T) {
	for _, raw := range []string{
		"http://support.weixin.qq.com/cgi-bin/mmsupport-bin/addchatroombyinvite?ticket=x",
		"https://evil.example/cgi-bin/mmsupport-bin/addchatroombyinvite?ticket=x",
		"https://support.weixin.qq.com/cgi-bin/mmsupport-bin/addchatroombyinvite",
		"https://support.weixin.qq.com/cgi-bin/mmsupport-bin/addchatroombyinvite?ticket=x#fragment",
		"https://support.weixin.qq.com/cgi-bin/mmsupport-bin/addchatroombyinvite?ticket=x&next=https://evil.example",
	} {
		if _, ok := parseChatRoomInvite(raw); ok {
			t.Fatalf("untrusted invite accepted: %s", raw)
		}
	}
}

func TestAcceptChatRoomInviteUsesConsentToJoinAndValidatesResult(t *testing.T) {
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Group/ConsentToJoin" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"Success":true,"Code":0,"Data":"weixin://jump/mainframe/123@chatroom"}`))
	}))
	defer server.Close()
	client := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	chat, err := client.AcceptChatRoomInvite(context.Background(), syntheticInviteURL)
	if err != nil || chat != remoteapi.ChatID("123@chatroom") {
		t.Fatalf("chat=%q err=%v", chat, err)
	}
	if body["Wxid"] != "wxid_self" || body["Url"] != syntheticInviteURL {
		t.Fatalf("request body = %#v", body)
	}
}

func TestAcceptChatRoomInviteRejectsInvalidResultWithoutPublishingChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Success":true,"Code":0,"Data":"not-a-chat"}`))
	}))
	defer server.Close()
	client := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	if chat, err := client.AcceptChatRoomInvite(context.Background(), syntheticInviteURL); err == nil || chat != "" {
		t.Fatalf("invalid result accepted: chat=%q err=%v", chat, err)
	}
}
