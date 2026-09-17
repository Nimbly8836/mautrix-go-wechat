package matrixas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type solitaireMatrixRecorder struct {
	mu               sync.Mutex
	requests         []string
	events           map[string]id.EventID
	firstFailure     string
	failureRequest   int
	denyRegistration bool
	senders          []string
}

func solitaireDeliveryHarness(t *testing.T) (*Server, portalRecord, *solitaireMatrixRecorder) {
	t.Helper()
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	s.api = &solitaireTestAPI{account: "wxid_self"}
	recorder := &solitaireMatrixRecorder{events: make(map[string]id.EventID), failureRequest: 1}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.URL.Path, "/send/m.room.message/") {
			if strings.HasSuffix(r.URL.Path, "/register") && recorder.denyRegistration {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"registration denied"}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
			return
		}
		txn := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		sender := r.URL.Query().Get("user_id")
		recorder.requests = append(recorder.requests, txn)
		recorder.senders = append(recorder.senders, sender)
		if recorder.firstFailure == "forbidden" && len(recorder.requests) == recorder.failureRequest {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"denied"}`))
			return
		}
		identity := sender + "|" + txn
		eventID, ok := recorder.events[identity]
		if !ok {
			eventID = id.EventID(fmt.Sprintf("$sent-%d", len(recorder.events)+1))
			recorder.events[identity] = eventID
		}
		if recorder.firstFailure == "missing event ID" && len(recorder.requests) == recorder.failureRequest {
			// Simulate a committed Matrix transaction with an unusable ack.
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"event_id": eventID})
	}))
	t.Cleanup(hs.Close)
	if err := s.as.SetHomeserverURL(hs.URL); err != nil {
		t.Fatal(err)
	}
	return s, rec, recorder
}

func deliveryTestMessage(rec portalRecord, keys ...string) *remoteapi.InboundMessage {
	return &remoteapi.InboundMessage{
		ID: "9007199254740995", Chat: rec.ChatID, Sender: "wxid_self", Type: remoteapi.MsgApp,
		Text: "#接龙\n1. 小明\n2. 张三，1份", DeduplicationKeys: keys,
		Solitaire: &remoteapi.SolitaireSource{Account: "wxid_self", Chat: rec.ChatID, Reference: `{"MsgID":456,"NewMsgID":9007199254740995}`},
		Extra:     map[string]any{"raw_msg_id": int64(456)},
	}
}

func TestSolitaireDisplayConcurrentDeliveryAndAliasLearning(t *testing.T) {
	s, rec, recorder := solitaireDeliveryHarness(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			m := deliveryTestMessage(rec, "new:9007199254740995", "msg:456")
			if err := s.deliverSolitaireToPortal(context.Background(), rec, m); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	for _, keys := range [][]string{{"msg:456"}, {"new:9007199254740995"}} {
		if err := s.deliverSolitaireToPortal(context.Background(), rec, deliveryTestMessage(rec, keys...)); err != nil {
			t.Fatal(err)
		}
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.requests) != 1 || len(recorder.events) != 1 {
		t.Fatalf("duplicate display: %#v", recorder)
	}
}

func TestSolitaireDisplayLearnsPreviouslyMissingAlias(t *testing.T) {
	for _, firstKey := range []string{"msg:456", "new:9007199254740995"} {
		t.Run(firstKey, func(t *testing.T) {
			s, rec, recorder := solitaireDeliveryHarness(t)
			for _, keys := range [][]string{{firstKey}, {"new:9007199254740995", "msg:456"}, {"msg:456"}, {"new:9007199254740995"}} {
				if err := s.deliverSolitaireToPortal(context.Background(), rec, deliveryTestMessage(rec, keys...)); err != nil {
					t.Fatal(err)
				}
			}
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			if len(recorder.requests) != 1 {
				t.Fatalf("did not learn shared alias: %v", recorder.requests)
			}
		})
	}
}

func TestSolitaireDisplayMatrixFailureDoesNotPoisonDelivery(t *testing.T) {
	for _, failure := range []string{"forbidden", "missing event ID"} {
		t.Run(failure, func(t *testing.T) {
			s, rec, recorder := solitaireDeliveryHarness(t)
			recorder.firstFailure = failure
			m := deliveryTestMessage(rec, "new:9007199254740995", "msg:456")
			if err := s.deliverSolitaireToPortal(context.Background(), rec, m); err == nil {
				t.Fatal("Matrix failure/unknown ack was treated as displayed")
			}
			// A later callback can complete display via a shared legacy alias.
			echo := deliveryTestMessage(rec, "msg:456")
			echo.ID = "456"
			echo.Solitaire.Reference = `{"MsgID":456}`
			if err := s.deliverSolitaireToPortal(context.Background(), rec, echo); err != nil {
				t.Fatal(err)
			}
			if err := s.deliverSolitaireToPortal(context.Background(), rec, m); err != nil {
				t.Fatal(err)
			}
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			if len(recorder.requests) != 2 || recorder.requests[0] != recorder.requests[1] || len(recorder.events) != 1 {
				t.Fatalf("failed reservation prevented recovery or changed transaction: %#v", recorder)
			}
		})
	}
}

func TestSolitaireDisplayIdentityScopesAndUnrelatedMessages(t *testing.T) {
	for _, variation := range []string{"namespace", "different ID same body", "chat", "room", "account", "old account rejected"} {
		t.Run(variation, func(t *testing.T) {
			s, rec, recorder := solitaireDeliveryHarness(t)
			m := deliveryTestMessage(rec, "new:456")
			if err := s.deliverSolitaireToPortal(context.Background(), rec, m); err != nil {
				t.Fatal(err)
			}
			next := deliveryTestMessage(rec, "new:456")
			wantErr := false
			switch variation {
			case "namespace":
				next.DeduplicationKeys = []string{"msg:456"}
			case "different ID same body":
				next.DeduplicationKeys = []string{"new:457"}
			case "chat":
				rec.ChatID = "other@chatroom"
				next.Chat, next.Solitaire.Chat = rec.ChatID, rec.ChatID
			case "room":
				rec.RoomID = "!other:example.com"
			case "account":
				s.api.(*solitaireTestAPI).account = "wxid_other"
				next.Sender, next.Solitaire.Account = "wxid_other", "wxid_other"
			case "old account rejected":
				s.api.(*solitaireTestAPI).account = "wxid_other"
				wantErr = true
			}
			if err := s.deliverSolitaireToPortal(context.Background(), rec, next); (err != nil) != wantErr {
				t.Fatalf("error=%v wantErr=%v", err, wantErr)
			}
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			want := 2
			if wantErr {
				want = 1
			}
			if len(recorder.requests) != want || len(recorder.events) != want {
				t.Fatalf("wrong identity scope: requests=%v events=%v", recorder.requests, recorder.events)
			}
		})
	}
}

func TestSolitaireDisplayCacheBoundAndStableTransaction(t *testing.T) {
	s, rec, _ := solitaireDeliveryHarness(t)
	s.solitaireDeliveryMu.Lock()
	defer s.solitaireDeliveryMu.Unlock()
	m := deliveryTestMessage(rec, "new:9007199254740995", "msg:456")
	first := s.solitaireDeliveryFor(rec, m)
	for i := range maxSolitaireDeliveries {
		s.solitaireDeliveryFor(rec, deliveryTestMessage(rec, fmt.Sprintf("new:other-%d", i), fmt.Sprintf("msg:other-%d", i)))
	}
	if len(s.solitaireDeliveryOrder) != maxSolitaireDeliveries || len(s.solitaireDeliveries) != maxSolitaireDeliveries*2 || s.solitaireDeliveries[first.keys[0]] != nil {
		t.Fatal("display cache grew without bound or failed to evict aliases")
	}
	afterEviction := s.solitaireDeliveryFor(rec, m)
	fresh := &Server{}
	afterRestart := fresh.solitaireDeliveryFor(rec, m)
	if first.transactionID != afterEviction.transactionID || first.transactionID != afterRestart.transactionID {
		t.Fatal("same primary identity did not retain its Matrix transaction ID")
	}
}
