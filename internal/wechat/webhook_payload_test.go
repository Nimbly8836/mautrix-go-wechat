package wechat

import (
	"context"
	"strconv"
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

func TestHandleWebhookDropsLastMessageOp(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	payload := []byte(`{"MsgID":1,"NewMsgID":2,"MsgType":9,"ContentText":"<msg>\n<op id='1'>\n<username>wxid_friend</username>\n<name>lastMessage</name>\n<arg>{\"messageSvrId\": \"2014035913214263335\"}</arg>\n</op>\n</msg>","Talker":"wxid_friend","SenderUserName":"wxid_self","FromUserName":"wxid_self","ToUserName":"wxid_friend","CreateTime":1710000000}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		t.Fatalf("expected lastMessage op to be dropped, got %#v", ev)
	default:
	}
}

func TestHandleWebhookStripsLastMessageOpsAroundText(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	payload := []byte(`{"MsgID":1,"NewMsgID":2,"MsgType":1,"ContentText":"<msg>\n<op id='1'>\n<username>wxid_friend</username>\n<name>lastMessage</name>\n<arg>{\"messageSvrId\":\"1\"}</arg>\n</op>\n</msg>\n好耶\n<msg>\n<op id='1'>\n<username>wxid_friend</username>\n<name>lastMessage</name>\n<arg>{\"messageSvrId\":\"1\"}</arg>\n</op>\n</msg>","Talker":"wxid_friend","SenderUserName":"wxid_friend","CreateTime":1710000000}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		if ev.Message == nil || ev.Message.Text != "好耶" {
			t.Fatalf("expected only real text after stripping XML, got %#v", ev)
		}
	default:
		t.Fatalf("expected webhook to emit stripped real text")
	}
}

func TestHandleWebhookDeduplicatesSameMessage(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	payload := []byte(`{"Code":0,"Success":true,"Data":{"AddMsgs":[{"MsgID":1,"NewMsgID":2,"MsgType":1,"ContentText":"dup","Talker":"wxid_friend","SenderUserName":"wxid_friend","CreateTime":1710000000}],"Messages":[{"MsgID":1,"NewMsgID":2,"MsgType":1,"ContentText":"dup","Talker":"wxid_friend","SenderUserName":"wxid_friend","CreateTime":1710000000}]}}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	count := 0
	for {
		select {
		case <-c.Events():
			count++
		default:
			if count != 1 {
				t.Fatalf("expected one emitted message after dedupe, got %d", count)
			}
			return
		}
	}
}

func TestHandleWebhookImageXMLDoesNotLeakAsBody(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	imageXML := `<?xml version="1.0"?>
<msg>
    <img aeskey="2c5b695f08c0921a5e58539172b3a14c" cdnmidimgurl="305f020100044b" length="398191" md5="60c9d0aff18afffb4478f948c96b661b" />
</msg>`
	payload := []byte(`{"MsgID":1,"NewMsgID":2,"MsgType":3,"ContentText":` + strconv.Quote(imageXML) + `,"PushContent":"","Talker":"wxid_friend","SenderUserName":"wxid_friend","CreateTime":1710000000}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		if ev.Message == nil {
			t.Fatalf("expected message event, got %#v", ev)
		}
		if ev.Message.Type != remoteapi.MsgImage {
			t.Fatalf("expected image message, got type=%d event=%#v", ev.Message.Type, ev)
		}
		if ev.Message.Text != "[image]" {
			t.Fatalf("expected image placeholder instead of raw XML, got %q", ev.Message.Text)
		}
	default:
		t.Fatalf("expected webhook to emit image message")
	}
}
