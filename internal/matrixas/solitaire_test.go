package matrixas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type solitaireTestAPI struct {
	remoteapi.PuppetAPI
	account    remoteapi.UserID
	joined     []remoteapi.SolitaireSource
	entries    []string
	texts      []remoteapi.OutgoingMessage
	joinError  error
	joinResult *remoteapi.SolitaireResult
}

func (a *solitaireTestAPI) Self(context.Context) (remoteapi.UserID, error) {
	return a.account, nil
}

func (a *solitaireTestAPI) JoinSolitaire(_ context.Context, source remoteapi.SolitaireSource, content string) (remoteapi.SolitaireResult, error) {
	a.joined = append(a.joined, source)
	a.entries = append(a.entries, content)
	if a.joinResult != nil {
		return *a.joinResult, a.joinError
	}
	return remoteapi.SolitaireResult{ID: "456"}, a.joinError
}

func (a *solitaireTestAPI) SendMessage(_ context.Context, _ remoteapi.ChatID, msg remoteapi.OutgoingMessage) (remoteapi.MessageID, error) {
	a.texts = append(a.texts, msg)
	return "text", nil
}

func solitaireReplyEvent(rec portalRecord, body string) *event.Event {
	evt := portalTextEvent(rec, body)
	evt.ID = "$command"
	evt.Content.AsMessage().RelatesTo = (&event.RelatesTo{}).SetReplyTo("$source")
	return evt
}

func TestSolitaireReplyGuardsAndDurableDedup(t *testing.T) {
	for _, name := range []string{"Success", "UppercaseJL", "LegacyChineseAlias", "SolitaireAlias", "OtherBot", "LegacyReplyFallback", "Multiline", "CustomPrefix", "OrdinaryReply", "PermissionDenied", "MissingReply", "ThreadFallback", "Edit", "Notice", "NoCommandID", "WrongRoom", "OmittedRoomID", "WrongEventID", "WrongSender", "WrongAccount", "WrongChat", "NoMetadata", "Redacted", "Unreadable", "PersistenceFailure", "UncertainResult"} {
		t.Run(name, func(t *testing.T) {
			api := &solitaireTestAPI{account: "wxid_self"}
			s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
			s.api = api
			s.portals.path = filepath.Join(t.TempDir(), "portals.json")
			source := remoteapi.SolitaireSource{Account: "wxid_self", Chat: rec.ChatID, Reference: `{"MsgID":123,"NewMsgID":9007199254740993}`}
			original := &event.Event{ID: "$source", RoomID: rec.RoomID, Sender: s.as.BotMXID(), Type: event.EventMessage}
			evt := solitaireReplyEvent(rec, "!wx jl 张三，1份 :wechat_smile:")
			wantJoin, wantText := 0, 0
			wantEntry := "张三，1份 :wechat_smile:"
			switch name {
			case "Success":
				wantJoin = 1
			case "UppercaseJL":
				wantJoin = 1
				evt.Content.AsMessage().Body = "!wx JL " + wantEntry
			case "LegacyChineseAlias":
				wantJoin = 1
				evt.Content.AsMessage().Body = "!wx 接龙 " + wantEntry
			case "SolitaireAlias":
				wantJoin = 1
				evt.Content.AsMessage().Body = "!wx solitaire " + wantEntry
			case "OtherBot":
				wantJoin = 1
				evt.Sender = "@daily_bot:example.com"
				s.cfg.Bridge.Permissions = map[string]string{evt.Sender.String(): "user"}
			case "LegacyReplyFallback":
				wantJoin = 1
				evt.Content.AsMessage().Body = "> <@wechatbot:example.com> #接龙\n> 1. 小明\n\n!wx jl " + wantEntry
			case "Multiline":
				wantJoin = 1
				wantEntry += "\n不加糖"
				evt.Content.AsMessage().Body = "!wx jl\n" + wantEntry
			case "CustomPrefix":
				wantJoin = 1
				s.cfg.Bridge.CommandPrefix = "!custom"
				evt.Content.AsMessage().Body = "!custom jl " + wantEntry
			case "OrdinaryReply":
				// Not a join command. Without a general message-ID mapping,
				// reject rather than sending a plain-text imitation of a reply.
				evt.Content.AsMessage().Body = "张三，1份"
			case "PermissionDenied":
				s.cfg.Bridge.Permissions = map[string]string{evt.Sender.String(): "relay"}
			case "MissingReply":
				evt.Content.AsMessage().RelatesTo = nil
			case "ThreadFallback":
				evt.Content.AsMessage().RelatesTo.Type = event.RelThread
				evt.Content.AsMessage().RelatesTo.IsFallingBack = true
			case "Edit":
				evt.Content.AsMessage().RelatesTo.Type = event.RelReplace
				evt.Content.AsMessage().RelatesTo.EventID = "$edited"
			case "Notice":
				evt.Content.AsMessage().MsgType = event.MsgNotice
			case "NoCommandID":
				evt.ID = ""
			case "WrongRoom":
				original.RoomID = "!other:example.com"
			case "OmittedRoomID":
				// The GET request is already scoped to the current room.
				wantJoin = 1
				original.RoomID = ""
			case "WrongEventID":
				original.ID = "$other"
			case "WrongSender":
				original.Sender = "@mallory:example.com"
			case "WrongAccount":
				source.Account = "wxid_other"
			case "WrongChat":
				source.Chat = "other@chatroom"
			case "Redacted":
				original.Unsigned.RedactedBecause = &event.Event{}
			case "PersistenceFailure":
				s.portals.path = ""
			case "UncertainResult":
				wantJoin = 1
				api.joinError = errors.New("结果未知")
			}
			original.Content = event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "#接龙"}}
			if name != "NoMetadata" {
				original.Content.Raw = map[string]any{solitaireSourceKey: source}
			}
			var notices []string
			fetches := 0
			homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					fetches++
					if !strings.Contains(r.URL.Path, "/rooms/"+rec.RoomID.String()+"/event/$source") {
						t.Errorf("unscoped target lookup: %s", r.URL)
					}
					if name == "Unreadable" {
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"forbidden"}`))
						return
					}
					_ = json.NewEncoder(w).Encode(original)
					return
				}
				if strings.Contains(r.URL.Path, "/send/m.room.message/") {
					var content event.MessageEventContent
					if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
						t.Error(err)
					}
					notices = append(notices, content.Body)
				}
				_, _ = w.Write([]byte(`{"event_id":"$notice"}`))
			}))
			defer homeserver.Close()
			if err := s.as.SetHomeserverURL(homeserver.URL); err != nil {
				t.Fatal(err)
			}
			s.handleMatrixEvent(context.Background(), evt)
			if len(api.joined) != wantJoin || len(api.texts) != wantText {
				t.Fatalf("join calls=%d text calls=%d notices=%v; want join=%d text=%d", len(api.joined), len(api.texts), notices, wantJoin, wantText)
			}
			if wantJoin == 1 {
				if api.joined[0] != source || api.entries[0] != wantEntry {
					t.Fatalf("wrong source/entry: %#v %q", api.joined[0], api.entries[0])
				}
				// Duplicate delivery, then a fresh Server with no in-memory state.
				s.handleMatrixEvent(context.Background(), evt)
				restarted, _ := newPortalMessageTestServer(t, &portalMessageAPI{})
				restarted.api = api
				restarted.cfg.Bridge.CommandPrefix = s.cfg.Bridge.CommandPrefix
				restarted.portals.path = s.portals.path
				if err := restarted.as.SetHomeserverURL(homeserver.URL); err != nil {
					t.Fatal(err)
				}
				restarted.handleMatrixEvent(context.Background(), evt)
				if len(api.joined) != 1 || !strings.Contains(strings.Join(notices, "\n"), "不会重复发送") {
					t.Fatalf("command replayed after duplicate/restart: calls=%d notices=%v", len(api.joined), notices)
				}
			} else if wantText == 0 && len(notices) == 0 {
				t.Fatal("rejection had no user-facing notice")
			}
			if name == "PermissionDenied" && fetches != 0 {
				t.Fatal("unauthorized sender fetched a target message")
			}
			if name == "UncertainResult" && !strings.Contains(strings.Join(notices, "\n"), "没有自动重发") {
				t.Fatalf("missing uncertainty warning: %v", notices)
			}
		})
	}
}

func TestSolitaireMetadataSurvivesMatrixJSON(t *testing.T) {
	source := &remoteapi.SolitaireSource{Account: "wxid_self", Chat: "group@chatroom", Reference: `{"NewMsgID":9007199254740993}`}
	content := solitaireMatrixContent(&event.MessageEventContent{MsgType: event.MsgText, Body: "#接龙\n1. 小明"}, source, "!custom")
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Body   string                    `json:"body"`
		Source remoteapi.SolitaireSource `json:"com.leat.wechat.solitaire_source"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != *source || got.Body != "#接龙\n1. 小明\n\nReply: !custom jl <text>" {
		t.Fatalf("metadata/title/hint lost: %#v", got)
	}
}

func TestSolitaireCommandHintsUseJL(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "portal help", body: "!custom help"},
		{name: "missing reply", body: "!custom jl 张三，1份"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
			s.cfg.Bridge.CommandPrefix = "!custom"
			messages := capturePortalMatrixMessages(t, s)
			s.handleMatrixEvent(context.Background(), portalTextEvent(rec, tc.body))
			select {
			case msg := <-messages:
				if !strings.Contains(msg.Body, "!custom jl ") || strings.Contains(msg.Body, "!custom 接龙") {
					t.Fatalf("hint does not use the short command: %q", msg.Body)
				}
			default:
				t.Fatal("missing command hint")
			}
		})
	}
}

var _ remoteapi.SolitaireAPI = (*solitaireTestAPI)(nil)
