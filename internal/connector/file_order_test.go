package connector

import (
	"context"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type fileOrderSink struct {
	recordSink
	first, file, other chan *remoteapi.InboundMessage
	release            chan struct{}
}

func (s *fileOrderSink) OnMessage(ctx context.Context, m *remoteapi.InboundMessage) {
	switch m.ID {
	case "caption":
		s.first <- m
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	case "file":
		s.file <- m
	case "other":
		s.other <- m
	}
}

func TestDeferredFileWaitsForCaptionWithoutBlockingOtherChat(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &fileOrderSink{first: make(chan *remoteapi.InboundMessage, 1), file: make(chan *remoteapi.InboundMessage, 1), other: make(chan *remoteapi.InboundMessage, 1), release: make(chan struct{})}
	c := &Connector{sink: s}
	caption := &remoteapi.InboundMessage{ID: "caption", Chat: "chat", Type: remoteapi.MsgText}
	file := &remoteapi.InboundMessage{ID: "file", Chat: "chat", Type: remoteapi.MsgFile, FetchMedia: func(context.Context) (*remoteapi.Media, error) { return nil, nil }}
	c.dispatch(ctx, remoteapi.Event{Kind: remoteapi.EventMessage, Message: caption})
	first := <-s.first
	c.dispatch(ctx, remoteapi.Event{Kind: remoteapi.EventMessage, Message: file})
	c.dispatch(ctx, remoteapi.Event{Kind: remoteapi.EventMessage, Message: &remoteapi.InboundMessage{ID: "other", Chat: "other"}})
	var other *remoteapi.InboundMessage
	select {
	case other = <-s.other:
	case <-time.After(time.Second):
		t.Fatal("other chat blocked")
	}
	select {
	case <-s.file:
		t.Fatal("file overtook unacknowledged caption")
	case <-time.After(20 * time.Millisecond):
	}
	close(s.release)
	select {
	case got := <-s.file:
		if first.DeliveryOrder == 0 || first.DeliveryOrder >= got.DeliveryOrder || got.DeliveryOrder >= other.DeliveryOrder {
			t.Fatal("delivery order lost")
		}
	case <-time.After(time.Second):
		t.Fatal("file did not resume")
	}
	if caption.DeliveryOrder != 0 || file.DeliveryOrder != 0 {
		t.Fatal("connector mutated producer-owned messages")
	}
}
