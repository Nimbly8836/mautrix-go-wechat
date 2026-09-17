package matrixas

import (
	"context"
	"testing"
)

func TestSolitaireDisplayAliasMergePrefersAcknowledgedEvent(t *testing.T) {
	s, rec, recorder := solitaireDeliveryHarness(t)
	newOnly := deliveryTestMessage(rec, "new:9007199254740995")
	newOnly.Solitaire.Reference = `{"NewMsgID":9007199254740995}`
	if err := s.deliverSolitaireToPortal(context.Background(), rec, newOnly); err != nil {
		t.Fatal(err)
	}
	recorder.mu.Lock()
	recorder.firstFailure, recorder.failureRequest = "forbidden", 2
	recorder.mu.Unlock()
	legacy := deliveryTestMessage(rec, "msg:456")
	legacy.ID, legacy.Solitaire.Reference = "456", `{"MsgID":456}`
	if err := s.deliverSolitaireToPortal(context.Background(), rec, legacy); err == nil {
		t.Fatal("expected the independent legacy-only display attempt to fail")
	}
	// Identity was unknowable earlier, but the next callback connects both
	// reservations. The existing successful ack must win over the failed one.
	both := deliveryTestMessage(rec, "new:9007199254740995", "msg:456")
	if err := s.deliverSolitaireToPortal(context.Background(), rec, both); err != nil {
		t.Fatal(err)
	}
	if err := s.deliverSolitaireToPortal(context.Background(), rec, legacy); err != nil {
		t.Fatal(err)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.requests) != 2 || len(recorder.events) != 1 {
		t.Fatalf("ignored acknowledged alias when identities converged: requests=%v events=%v", recorder.requests, recorder.events)
	}
}

func TestSolitaireDisplayRecoveryKeepsOriginalMatrixSender(t *testing.T) {
	s, rec, recorder := solitaireDeliveryHarness(t)
	s.cfg.Homeserver.Domain = "example.com"
	s.cfg.Appservice.ID = "wechat"
	recorder.mu.Lock()
	recorder.denyRegistration, recorder.firstFailure = true, "missing event ID"
	recorder.mu.Unlock()
	m := deliveryTestMessage(rec, "new:9007199254740995", "msg:456")
	m.Sender = "wxid_friend"
	// Registration failure falls back to the bot. The Matrix write commits,
	// but returns no usable ack, so the delivery remains pending.
	if err := s.deliverSolitaireToPortal(context.Background(), rec, m); err == nil {
		t.Fatal("expected unknown Matrix result")
	}
	recorder.mu.Lock()
	recorder.denyRegistration = false
	recorder.mu.Unlock()
	// The ghost is now available; retrying under that different sender would
	// use a different Matrix transaction namespace and create another event.
	s.markPuppetJoined(rec.RoomID, m.Sender)
	if err := s.deliverSolitaireToPortal(context.Background(), rec, m); err != nil {
		t.Fatal(err)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.requests) != 2 || recorder.requests[0] != recorder.requests[1] || recorder.senders[0] != recorder.senders[1] || len(recorder.events) != 1 {
		t.Fatalf("changed identity across unacknowledged display: transactions=%v senders=%v events=%v", recorder.requests, recorder.senders, recorder.events)
	}
}
