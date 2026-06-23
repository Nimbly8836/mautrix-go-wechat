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

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// Connector owns one logged-in backend client and translates between the
// neutral event stream and whatever Matrix-facing sink you plug in.
type Connector struct {
	protocol string
	api      remoteapi.PuppetAPI
	sink     EventSink
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
			c.sink.OnMessage(ctx, ev.Message)
		}
	case remoteapi.EventRedaction:
		if ev.Redaction != nil {
			c.sink.OnRedaction(ctx, ev.Redaction)
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
