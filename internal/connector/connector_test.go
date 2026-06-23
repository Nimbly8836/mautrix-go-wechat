package connector

import (
	"context"
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
	messages  int
	connected bool
}

func (s *recordSink) OnMessage(context.Context, *remoteapi.InboundMessage) { s.messages++ }
func (s *recordSink) OnRedaction(context.Context, *remoteapi.Redaction)    {}
func (s *recordSink) OnReceipt(context.Context, *remoteapi.Receipt)        {}
func (s *recordSink) OnContactUpdate(context.Context, *remoteapi.Contact)  {}
func (s *recordSink) OnConnected(context.Context)                          { s.connected = true }
func (s *recordSink) OnDisconnected(context.Context, error)                {}

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
	if sink.messages != 1 {
		t.Errorf("expected 1 message, got %d", sink.messages)
	}
}

type fakeFactory struct{}

func (fakeFactory) Name() string { return "fake-test" }
func (fakeFactory) New(remoteapi.Config) (remoteapi.PuppetAPI, error) {
	return &fakeAPI{events: make(chan remoteapi.Event, 8)}, nil
}

var _ remoteapi.PuppetAPI = (*fakeAPI)(nil)
