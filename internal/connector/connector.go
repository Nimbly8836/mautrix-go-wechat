// Package connector glues the protocol-neutral remoteapi.PuppetAPI to the rest
// of the bridge. The bridge core depends on THIS package and on remoteapi, but
// never imports internal/wechat (or any concrete protocol) directly. Protocol
// selection happens at runtime via the registry.
//
// When you adopt maunium.net/go/mautrix bridgev2, this is where you implement
// bridgev2.NetworkConnector by delegating to the PuppetAPI. The structure below
// is framework-agnostic so it compiles and is testable on its own; see
// docs/BRIDGEV2.md for how each PuppetAPI method maps onto bridgev2 callbacks.
package connector

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// One process-wide order survives connector/account replacement. Sinks keep
// only short-lived arrival windows; restarting the process clears both sides.
var messageDeliveryOrder atomic.Uint64

// Connector owns one logged-in backend client and translates between the
// neutral event stream and whatever Matrix-facing sink you plug in.
type Connector struct {
	protocol  string
	api       remoteapi.PuppetAPI
	sink      EventSink
	pendingMu sync.Mutex
	pending   map[remoteapi.ChatID]map[chan struct{}]struct{}
}

// EventSink receives neutral events translated from the backend. In a real
// bridge this is implemented by your bridgev2 portal/login glue; in tests it
// can be a recorder. Keeping it an interface means the Connector has zero
// knowledge of Matrix.
type EventSink interface {
	OnMessage(ctx context.Context, m *remoteapi.InboundMessage)
	OnRedaction(ctx context.Context, r *remoteapi.Redaction)
	OnReceipt(ctx context.Context, r *remoteapi.Receipt)
	OnContactUpdate(ctx context.Context, c *remoteapi.Contact)
	OnConnected(ctx context.Context)
	OnDisconnected(ctx context.Context, err error)
}

// New builds a Connector for the named protocol using the given config.
// Switching protocols is a one-line config change: pass "qq" instead of
// "wechat" once a qq Factory is registered.
func New(protocol string, cfg remoteapi.Config, sink EventSink) (*Connector, error) {
	factory, err := remoteapi.Get(protocol)
	if err != nil {
		return nil, err
	}
	api, err := factory.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("connector: build %s client: %w", protocol, err)
	}
	return &Connector{protocol: protocol, api: api, sink: sink}, nil
}

// API exposes the underlying client for outbound operations (send, redact,
// directory lookups) that the Matrix side initiates.
func (c *Connector) API() remoteapi.PuppetAPI { return c.api }

// Protocol returns the active protocol name.
func (c *Connector) Protocol() string { return c.protocol }

// Run connects the backend and pumps its event stream into the sink until the
// context is cancelled or the stream closes.
func (c *Connector) Run(ctx context.Context) error {
	if err := c.api.Connect(ctx); err != nil {
		return err
	}
	defer c.api.Disconnect()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-c.api.Events():
			if !ok {
				return nil
			}
			c.dispatch(ctx, ev)
		}
	}
}

func (c *Connector) dispatch(ctx context.Context, ev remoteapi.Event) {
	switch ev.Kind {
	case remoteapi.EventMessage:
		if ev.Message != nil {
			msg := *ev.Message
			msg.DeliveryOrder = messageDeliveryOrder.Add(1)
			c.dispatchAsync(ctx, msg.Chat, msg.Reply != nil || msg.FetchMedia != nil, func() { c.dispatchMessage(ctx, &msg) })
		}
	case remoteapi.EventRedaction:
		if ev.Redaction != nil {
			recall := ev.Redaction
			c.dispatchAsync(ctx, recall.Chat, true, func() { c.sink.OnRedaction(ctx, recall) })
		}
	case remoteapi.EventReceipt:
		if ev.Receipt != nil {
			c.sink.OnReceipt(ctx, ev.Receipt)
		}
	case remoteapi.EventContactUpdate:
		if ev.Contact != nil {
			c.sink.OnContactUpdate(ctx, ev.Contact)
		}
	case remoteapi.EventConnected:
		c.sink.OnConnected(ctx)
	case remoteapi.EventDisconnected:
		c.sink.OnDisconnected(ctx, ev.Error)
	}
}

// Ordinary messages remain concurrent. Related events and deferred files wait for earlier
// deliveries in their chat, so a slow Matrix acknowledgement cannot make a
// valid reply/recall look like an unknown target. Waiting never blocks the pump
// or another chat, and cancellation never waits for a stuck sink.
func (c *Connector) dispatchAsync(ctx context.Context, chat remoteapi.ChatID, related bool, handle func()) {
	c.pendingMu.Lock()
	if c.pending == nil {
		c.pending = make(map[remoteapi.ChatID]map[chan struct{}]struct{})
	}
	active := c.pending[chat]
	if active == nil {
		active = make(map[chan struct{}]struct{})
		c.pending[chat] = active
	}
	var before []chan struct{}
	if related {
		for pending := range active {
			before = append(before, pending)
		}
	}
	done := make(chan struct{})
	active[done] = struct{}{}
	c.pendingMu.Unlock()
	go func() {
		defer func() {
			c.pendingMu.Lock()
			delete(active, done)
			if len(active) == 0 {
				delete(c.pending, chat)
			}
			close(done)
			c.pendingMu.Unlock()
			if recovered := recover(); recovered != nil {
				log.Printf("connector: recovered panic dispatching related event chat=%s: %v", chat, recovered)
			}
		}()
		for _, pending := range before {
			select {
			case <-pending:
			case <-ctx.Done():
				return
			}
		}
		if ctx.Err() == nil {
			handle()
		}
	}()
}

func (c *Connector) dispatchMessage(ctx context.Context, msg *remoteapi.InboundMessage) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("connector: recovered panic while dispatching inbound message chat=%s sender=%s id=%s: %v", msg.Chat, msg.Sender, msg.ID, r)
		}
	}()
	c.sink.OnMessage(ctx, msg)
}
