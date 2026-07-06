package connector

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// fakeAPI is a second PuppetAPI implementation used to prove the connector is
// backend-agnostic: nothing in this package references internal/wechat.
type fakeAPI struct {
	events chan remoteapi.Event
	sent   []remoteapi.OutgoingMessage
}

func (f *fakeAPI) Connect(context.Context) error {
	f.events <- remoteapi.Event{Kind: remoteapi.EventConnected}
	f.events <- remoteapi.Event{Kind: remoteapi.EventMessage, Message: &remoteapi.InboundMessage{
		ID: "1", Chat: "c1", Sender: "u1", Type: remoteapi.MsgText, Text: "hi", Timestamp: time.Now(),
	}}
	close(f.events)
	return nil
}
func (f *fakeAPI) Disconnect()                       {}
func (f *fakeAPI) Events() <-chan remoteapi.Event    { return f.events }
func (f *fakeAPI) LoginFlows() []remoteapi.LoginFlow { return []remoteapi.LoginFlow{{ID: "fake"}} }
func (f *fakeAPI) CreateLogin(context.Context, string) (remoteapi.LoginProcess, error) {
	return nil, nil
}
func (f *fakeAPI) Logout(context.Context) error                   { return nil }
func (f *fakeAPI) IsLoggedIn(context.Context) (bool, error)       { return true, nil }
func (f *fakeAPI) Self(context.Context) (remoteapi.UserID, error) { return "me", nil }
func (f *fakeAPI) DumpSession() ([]byte, error)                   { return nil, nil }
func (f *fakeAPI) SendMessage(_ context.Context, _ remoteapi.ChatID, m remoteapi.OutgoingMessage) (remoteapi.MessageID, error) {
	f.sent = append(f.sent, m)
	return "sent", nil
}
func (f *fakeAPI) RedactMessage(context.Context, remoteapi.ChatID, remoteapi.MessageID) error {
	return nil
}
func (f *fakeAPI) GetContact(context.Context, remoteapi.UserID) (remoteapi.Contact, error) {
	return remoteapi.Contact{}, nil
}
func (f *fakeAPI) ListContacts(context.Context) ([]remoteapi.Contact, error) { return nil, nil }
func (f *fakeAPI) GetChat(context.Context, remoteapi.ChatID) (remoteapi.Chat, error) {
	return remoteapi.Chat{}, nil
}

type recordSink struct {
	messages  int32
	connected bool
}

func (s *recordSink) OnMessage(context.Context, *remoteapi.InboundMessage) {
	atomic.AddInt32(&s.messages, 1)
}
func (s *recordSink) OnRedaction(context.Context, *remoteapi.Redaction)   {}
func (s *recordSink) OnReceipt(context.Context, *remoteapi.Receipt)       {}
func (s *recordSink) OnContactUpdate(context.Context, *remoteapi.Contact) {}
func (s *recordSink) OnConnected(context.Context)                         { s.connected = true }
func (s *recordSink) OnDisconnected(context.Context, error)               {}

func TestConnectorPumpsEvents(t *testing.T) {
	remoteapi.Register(fakeFactory{})
	sink := &recordSink{}
	conn, err := New("fake-test", remoteapi.Config{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sink.connected {
		t.Error("expected OnConnected")
	}
	deadline := time.After(time.Second)
	for atomic.LoadInt32(&sink.messages) != 1 {
		select {
		case <-deadline:
			t.Errorf("expected 1 message, got %d", atomic.LoadInt32(&sink.messages))
			return
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

type fakeFactory struct{}

func (fakeFactory) Name() string { return "fake-test" }
func (fakeFactory) New(remoteapi.Config) (remoteapi.PuppetAPI, error) {
	return &fakeAPI{events: make(chan remoteapi.Event, 8)}, nil
}

type blockingAPI struct {
	events chan remoteapi.Event
}

func (f *blockingAPI) Connect(context.Context) error {
	f.events <- remoteapi.Event{Kind: remoteapi.EventMessage, Message: &remoteapi.InboundMessage{ID: "1", Chat: "c1", Sender: "u1", Type: remoteapi.MsgText, Text: "first"}}
	f.events <- remoteapi.Event{Kind: remoteapi.EventMessage, Message: &remoteapi.InboundMessage{ID: "2", Chat: "c1", Sender: "u2", Type: remoteapi.MsgText, Text: "second"}}
	close(f.events)
	return nil
}
func (f *blockingAPI) Disconnect()                       {}
func (f *blockingAPI) Events() <-chan remoteapi.Event    { return f.events }
func (f *blockingAPI) LoginFlows() []remoteapi.LoginFlow { return nil }
func (f *blockingAPI) CreateLogin(context.Context, string) (remoteapi.LoginProcess, error) {
	return nil, nil
}
func (f *blockingAPI) Logout(context.Context) error                   { return nil }
func (f *blockingAPI) IsLoggedIn(context.Context) (bool, error)       { return true, nil }
func (f *blockingAPI) Self(context.Context) (remoteapi.UserID, error) { return "me", nil }
func (f *blockingAPI) DumpSession() ([]byte, error)                   { return nil, nil }
func (f *blockingAPI) SendMessage(context.Context, remoteapi.ChatID, remoteapi.OutgoingMessage) (remoteapi.MessageID, error) {
	return "", nil
}
func (f *blockingAPI) RedactMessage(context.Context, remoteapi.ChatID, remoteapi.MessageID) error {
	return nil
}
func (f *blockingAPI) GetContact(context.Context, remoteapi.UserID) (remoteapi.Contact, error) {
	return remoteapi.Contact{}, nil
}
func (f *blockingAPI) ListContacts(context.Context) ([]remoteapi.Contact, error) { return nil, nil }
func (f *blockingAPI) GetChat(context.Context, remoteapi.ChatID) (remoteapi.Chat, error) {
	return remoteapi.Chat{}, nil
}

type blockingFactory struct{}

func (blockingFactory) Name() string { return "blocking-test" }
func (blockingFactory) New(remoteapi.Config) (remoteapi.PuppetAPI, error) {
	return &blockingAPI{events: make(chan remoteapi.Event, 2)}, nil
}

type blockingSink struct {
	releaseFirst chan struct{}
	secondSeen   chan struct{}
	calls        int32
}

func (s *blockingSink) OnMessage(_ context.Context, m *remoteapi.InboundMessage) {
	call := atomic.AddInt32(&s.calls, 1)
	if call == 1 && m.Text == "first" {
		<-s.releaseFirst
		return
	}
	if m.Text == "second" {
		close(s.secondSeen)
	}
}
func (s *blockingSink) OnRedaction(context.Context, *remoteapi.Redaction)   {}
func (s *blockingSink) OnReceipt(context.Context, *remoteapi.Receipt)       {}
func (s *blockingSink) OnContactUpdate(context.Context, *remoteapi.Contact) {}
func (s *blockingSink) OnConnected(context.Context)                         {}
func (s *blockingSink) OnDisconnected(context.Context, error)               {}

func TestConnectorDoesNotBlockEventPumpOnSlowMessageSink(t *testing.T) {
	remoteapi.Register(blockingFactory{})
	sink := &blockingSink{releaseFirst: make(chan struct{}), secondSeen: make(chan struct{})}
	conn, err := New("blocking-test", remoteapi.Config{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- conn.Run(context.Background()) }()
	select {
	case <-sink.secondSeen:
	case <-time.After(200 * time.Millisecond):
		close(sink.releaseFirst)
		t.Fatalf("second inbound message was blocked by slow first message handler")
	}
	close(sink.releaseFirst)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("connector did not stop after event stream closed")
	}
}

var _ remoteapi.PuppetAPI = (*fakeAPI)(nil)
var _ remoteapi.PuppetAPI = (*blockingAPI)(nil)
