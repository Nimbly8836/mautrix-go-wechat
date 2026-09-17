package matrixas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestSolitaireReplyResultFallbacksDoNotFabricateOrResend(t *testing.T) {
	for _, name := range []string{"MissingXML", "MissingID", "Uncertain", "MismatchedID", "WrongAccount", "MatrixFailure"} {
		t.Run(name, func(t *testing.T) {
			s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
			s.portals.path = filepath.Join(t.TempDir(), "portals.json")
			m := deliveryTestMessage(rec, "new:9007199254740995", "msg:456")
			result := remoteapi.SolitaireResult{ID: m.ID, Message: m}
			api := &solitaireTestAPI{account: "wxid_self", joinResult: &result}
			s.api = api
			wantNotice := "未取得完整接龙"
			switch name {
			case "MissingXML":
				result.Message = nil
			case "MissingID":
				result.ID = ""
				wantNotice = "接龙未确认成功"
			case "Uncertain":
				api.joinError = errors.New("结果未知")
				wantNotice = "没有自动重发"
			case "MismatchedID":
				result.ID = "wrong"
			case "WrongAccount":
				m.Solitaire.Account = "wxid_other"
				wantNotice = "展示未确认"
			case "MatrixFailure":
				wantNotice = "展示未确认"
			}
			var mu sync.Mutex
			var notices []string
			textAttempts := 0
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/event/$source") {
					_ = json.NewEncoder(w).Encode(map[string]any{"event_id": "$source", "room_id": rec.RoomID, "sender": s.as.BotMXID(), "type": "m.room.message", "content": map[string]any{"msgtype": "m.text", "body": "#接龙\n1. 旧条目", solitaireSourceKey: remoteapi.SolitaireSource{Account: "wxid_self", Chat: rec.ChatID, Reference: `{"MsgID":123}`}}})
					return
				}
				if !strings.Contains(r.URL.Path, "/send/m.room.message/") {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				var content event.MessageEventContent
				if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
					t.Error(err)
				}
				if content.MsgType == event.MsgText {
					textAttempts++
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"denied"}`))
					return
				}
				notices = append(notices, content.Body)
				_, _ = w.Write([]byte(`{"event_id":"$notice"}`))
			}))
			defer hs.Close()
			if err := s.as.SetHomeserverURL(hs.URL); err != nil {
				t.Fatal(err)
			}
			evt := solitaireReplyEvent(rec, "!wx jl 新条目")
			s.handleMatrixEvent(context.Background(), evt)
			s.handleMatrixEvent(context.Background(), evt)
			mu.Lock()
			defer mu.Unlock()
			wantAttempts := 0
			if name == "MatrixFailure" {
				wantAttempts = 1
			}
			if len(api.joined) != 1 || textAttempts != wantAttempts || len(notices) != 2 || !strings.Contains(notices[0], wantNotice) || !strings.Contains(notices[1], "不会重复发送") {
				t.Fatalf("fabricated/resubmitted result or incorrect notice: calls=%d texts=%d notices=%v", len(api.joined), textAttempts, notices)
			}
		})
	}
}
