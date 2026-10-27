package wechat

import (
	"context"
	"testing"
)

func TestConvertInboundRecognizesChatRoomInviteSystemMessage(t *testing.T) {
	client := &Client{wxid: "wxid_self"}
	event, ok := client.convertInbound(context.Background(), rawMessage{
		MsgId: 11, NewMsgId: 22, MsgType: 10000, FromUserName: syncStringDoc{Str: "wxid_inviter"},
		Content:     syncStringDoc{Str: `"Jonathan🤴🏼"邀请你加入群聊"NGA线上交流2.8"，进入可查看详情。` + "\n" + syntheticInviteURL},
		ContentText: `"Jonathan🤴🏼"邀请你加入群聊"NGA线上交流2.8"，进入可查看详情。` + "\n" + syntheticInviteURL,
		CreateTime:  1700000000,
	})
	if !ok || event.Message == nil || event.Message.ChatInvite == nil {
		t.Fatalf("converted event = %#v, ok=%v", event, ok)
	}
	if event.Message.ChatInvite.URL != syntheticInviteURL || event.Message.ChatInvite.ChatName != "NGA线上交流2.8" || event.Message.ChatInvite.Inviter != "Jonathan🤴🏼" {
		t.Fatalf("invite metadata = %#v", event.Message.ChatInvite)
	}
	if event.Message.Extra["chat_invite"] != true {
		t.Fatalf("missing chat_invite marker: %#v", event.Message.Extra)
	}
}
