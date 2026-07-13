package wechat

import (
	"context"
	"testing"
	"time"

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

func TestHandleWebhookIgnoresTransientLoggedOutEnvelope(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 1), done: make(chan struct{})}
	payload := []byte(`{"Code":-13,"Success":false,"Message":"用户可能退出"}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook should ignore transient logged-out envelope, got error: %v", err)
	}
	select {
	case ev := <-c.Events():
		t.Fatalf("expected no event for transient logged-out envelope, got %#v", ev)
	default:
	}
}

func TestConnectInPushModeSkipsLoginProbe(t *testing.T) {
	c := &Client{events: make(chan remoteapi.Event, 1), done: make(chan struct{}), receiveMode: "push"}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("push Connect should not require login probe: %v", err)
	}
	select {
	case ev := <-c.Events():
		if ev.Kind != remoteapi.EventConnected {
			t.Fatalf("expected connected event, got %#v", ev)
		}
	default:
		t.Fatalf("expected connected event")
	}
}

func TestConnectInPushModeDoesNotBlockWhenEventQueueIsFull(t *testing.T) {
	events := make(chan remoteapi.Event, 1)
	events <- remoteapi.Event{Kind: remoteapi.EventMessage, Message: &remoteapi.InboundMessage{
		ID: "queued-message", Chat: "wxid_friend", Text: "hello",
	}}
	c := &Client{events: events, done: make(chan struct{}), receiveMode: "push"}

	connectDone := make(chan struct{})
	released := make(chan remoteapi.Event, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			released <- <-events
		case <-connectDone:
		}
	}()

	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("push Connect should not require login probe: %v", err)
	}
	close(connectDone)
	<-done

	select {
	case <-released:
		t.Fatal("push Connect blocked while the event queue was full")
	default:
	}

	ev := <-events
	if ev.Kind != remoteapi.EventMessage || ev.Message == nil ||
		ev.Message.ID != "queued-message" || ev.Message.Chat != "wxid_friend" || ev.Message.Text != "hello" {
		t.Fatalf("queued event was replaced or dropped: %#v", ev)
	}
}
