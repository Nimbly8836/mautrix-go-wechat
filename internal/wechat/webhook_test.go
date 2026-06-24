package wechat

import (
	"context"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestHandleWebhookEmitsSyncMessages(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	payload := []byte(`{"Code":0,"Success":true,"Data":{"Messages":[{"MsgId":1,"NewMsgId":2,"MsgType":1,"Content":{"Str":"hello"},"FromUserName":{"Str":"wxid_friend"},"ToUserName":{"Str":"wxid_self"},"CreateTime":1710000000}]}}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		if ev.Kind != remoteapi.EventMessage || ev.Message == nil {
			t.Fatalf("expected message event, got %#v", ev)
		}
		if ev.Message.Chat != "wxid_friend" || ev.Message.Sender != "wxid_friend" || ev.Message.Text != "hello" {
			t.Fatalf("unexpected message: %#v", ev.Message)
		}
	default:
		t.Fatalf("expected webhook to emit an event")
	}
}

func TestHandleWebhookAcceptsDirectSyncResponse(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	payload := []byte(`{"AddMsgs":[{"MsgId":1,"NewMsgId":2,"MsgType":1,"Content":{"Str":"direct"},"FromUserName":{"Str":"wxid_friend"},"ToUserName":{"Str":"wxid_self"},"CreateTime":1710000000}]}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		if ev.Message == nil || ev.Message.Text != "direct" {
			t.Fatalf("unexpected event: %#v", ev)
		}
	default:
		t.Fatalf("expected webhook to emit an event")
	}
}
