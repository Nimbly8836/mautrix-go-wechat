package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestJoinSolitaireExactSourceAndResults(t *testing.T) {
	const largeID int64 = 9007199254740993
	tests := []struct {
		name     string
		response string
		wantID   remoteapi.MessageID
	}{
		{"Success", `{"Code":0,"Success":true,"Data":{"Xml":"generated","SendResult":{"BaseResponse":{"ret":0},"newMsgId":9007199254740995}}}`, "9007199254740995"},
		{"LegacyResultID", `{"Code":0,"Success":true,"Data":{"SendResult":{"BaseResponse":{"ret":0},"msgId":123}}}`, "123"},
		{"MissingLocalMessage", `{"Code":-8,"Success":false,"Message":"消息尚未落库","Data":null}`, ""},
		{"NonzeroCodeDespiteSuccess", `{"Code":-8,"Success":true,"Data":{"SendResult":{"BaseResponse":{"ret":0},"msgId":123}}}`, ""},
		{"MissingCode", `{"Success":true,"Data":{"SendResult":{"BaseResponse":{"ret":0},"msgId":123}}}`, ""},
		{"MissingSuccess", `{"Code":0,"Data":{"SendResult":{"BaseResponse":{"ret":0},"msgId":123}}}`, ""},
		{"ProtocolFailure", `{"Code":0,"Success":true,"Data":{"SendResult":{"BaseResponse":{"ret":-2},"msgId":123}}}`, ""},
		{"MissingRet", `{"Code":0,"Success":true,"Data":{"SendResult":{"BaseResponse":{},"msgId":123}}}`, ""},
		{"MissingID", `{"Code":0,"Success":true,"Data":{"SendResult":{"BaseResponse":{"ret":0}}}}`, ""},
		{"MissingSendResult", `{"Code":0,"Success":true,"Data":{"Xml":"generated"}}`, ""},
		{"InvalidJSON", `not json`, ""},
		{"NoBridgeLoginRetry", `{"Code":-13,"Success":false,"Message":"login expired"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/Msg/SendSolitaire" || r.Method != http.MethodPost {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body) != 5 || string(body["Wxid"]) != `"wxid_self"` || string(body["ToWxid"]) != `"group@chatroom"` || string(body["Content"]) != `"张三，1份\n不加糖 :wechat_smile:"` || string(body["MsgID"]) != "123" || string(body["NewMsgID"]) != "9007199254740993" {
					t.Errorf("wrong fields, altered content or rounded ID: %s", body)
				}
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			client := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			source := newSolitaireSource("wxid_self", "group@chatroom", 123, largeID)
			// Matrix persists custom metadata in generic maps: IDs must survive.
			raw, _ := json.Marshal(source)
			var generic map[string]any
			if err := json.Unmarshal(raw, &generic); err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(generic)
			if err := json.Unmarshal(raw, source); err != nil {
				t.Fatal(err)
			}
			got, err := client.JoinSolitaire(context.Background(), *source, "张三，1份\n不加糖 :wechat_smile:")
			if got.Message != nil {
				t.Fatalf("invalid/missing XML fabricated a chain: %#v", got.Message)
			}
			if got.ID != tt.wantID || (err != nil) != (tt.wantID == "") || calls != 1 {
				t.Fatalf("id=%q err=%v calls=%d; want id=%q calls=1", got.ID, err, calls, tt.wantID)
			}
		})
	}
}

func TestJoinSolitaireRejectsBadSourceBeforeSending(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	for _, tt := range []struct {
		name, account, chat, reference, content string
	}{
		{"OtherAccount", "wxid_other", "group@chatroom", `{"MsgID":123}`, "entry"},
		{"NoChat", "wxid_self", "", `{"MsgID":123}`, "entry"},
		{"EmptyEntry", "wxid_self", "group@chatroom", `{"MsgID":123}`, " \n"},
		{"NoID", "wxid_self", "group@chatroom", `{}`, "entry"},
		{"BadJSON", "wxid_self", "group@chatroom", `not-json`, "entry"},
		{"OverflowShortID", "wxid_self", "group@chatroom", `{"MsgID":2147483648}`, "entry"},
		{"OverflowLongID", "wxid_self", "group@chatroom", `{"NewMsgID":9223372036854775808}`, "entry"},
		{"UnknownField", "wxid_self", "group@chatroom", `{"MsgID":123,"Title":"wrong"}`, "entry"},
		{"MultipleObjects", "wxid_self", "group@chatroom", `{"MsgID":123}{}`, "entry"},
		{"OversizedReference", "wxid_self", "group@chatroom", strings.Repeat("x", 257), "entry"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.JoinSolitaire(context.Background(), remoteapi.SolitaireSource{Account: remoteapi.UserID(tt.account), Chat: remoteapi.ChatID(tt.chat), Reference: tt.reference}, tt.content)
			if err == nil || calls != 0 {
				t.Fatalf("err=%v calls=%d; expected local rejection", err, calls)
			}
		})
	}
}

func TestJoinSolitaireNeverFollowsRedirectsOrRetriesHTTPErrors(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls, redirected := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/replayed" {
					redirected++
					_, _ = w.Write([]byte(`{"Code":0,"Success":true}`))
					return
				}
				calls++
				w.Header().Set("Location", "/replayed")
				w.WriteHeader(status)
			}))
			defer server.Close()
			client := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			source := newSolitaireSource("wxid_self", "group@chatroom", 123, 0)
			_, err := client.JoinSolitaire(context.Background(), *source, "entry")
			if err == nil || calls != 1 || redirected != 0 {
				t.Fatalf("err=%v calls=%d redirected=%d; expected one attempt", err, calls, redirected)
			}
			if client.t.hc.CheckRedirect != nil {
				t.Fatal("changed redirect policy for unrelated APIs")
			}
		})
	}
}
