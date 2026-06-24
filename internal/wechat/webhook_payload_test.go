package wechat

import (
	"context"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestHandleWebhookAcceptsSwaggerStringDocShape(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	payload := []byte(`{"Code":0,"Success":true,"Data":{"Messages":[{"MsgId":1,"NewMsgId":2,"MsgType":1,"Content":{"string":"hello"},"FromUserName":{"string":"wxid_friend"},"ToUserName":{"string":"wxid_self"},"CreateTime":1710000000}]}}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		if ev.Message == nil || ev.Message.Text != "hello" || ev.Message.Chat != "wxid_friend" {
			t.Fatalf("unexpected event: %#v", ev)
		}
	default:
		t.Fatalf("expected webhook to emit an event")
	}
}

func TestHandleWebhookAcceptsContentTextMessages(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	payload := []byte(`{"Code":0,"Success":true,"Data":{"Messages":[{"MsgID":1,"NewMsgID":2,"MsgType":1,"ContentText":"hello local","Talker":"wxid_friend","SenderUserName":"wxid_friend","FromUserName":"wxid_friend","ToUserName":"wxid_self","CreateTime":1710000000}]}}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		if ev.Message == nil || ev.Message.Text != "hello local" || ev.Message.Chat != "wxid_friend" {
			t.Fatalf("unexpected event: %#v", ev)
		}
	default:
		t.Fatalf("expected webhook to emit an event")
	}
}

func TestHandleWebhookAcceptsEnvelopeDataLocalMessage(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	payload := []byte(`{"Code":0,"Success":true,"Data":{"MsgID":1,"NewMsgID":2,"MsgType":1,"ContentText":"data local","Talker":"wxid_friend","SenderUserName":"wxid_friend","FromUserName":"wxid_friend","ToUserName":"wxid_self","CreateTime":1710000000}}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		if ev.Message == nil || ev.Message.Text != "data local" || ev.Message.Chat != "wxid_friend" {
			t.Fatalf("unexpected event: %#v", ev)
		}
	default:
		t.Fatalf("expected webhook to emit an event")
	}
}

func TestHandleWebhookAcceptsDirectLocalMessage(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	payload := []byte(`{"MsgID":1,"NewMsgID":2,"MsgType":1,"ContentText":"direct local","Talker":"wxid_friend","SenderUserName":"wxid_friend","FromUserName":"wxid_friend","ToUserName":"wxid_self","CreateTime":1710000000}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		if ev.Message == nil || ev.Message.Text != "direct local" || ev.Message.Chat != "wxid_friend" {
			t.Fatalf("unexpected event: %#v", ev)
		}
	default:
		t.Fatalf("expected webhook to emit an event")
	}
}
