package wechat

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestSendResponseProductionContract(t *testing.T) {
	for _, id := range []string{"9007199254740993", "18446744073709551615"} {
		t.Run(id, func(t *testing.T) {
			data := fmt.Sprintf(`{"BaseResponse":{"ret":0,"errMsg":{"string":""}},"Count":1,"List":[{"Ret":0,"ToUsetName":{"string":"recipient"},"MsgId":123,"NewMsgId":%s,"ClientMsgid":18446744073709551615,"Createtime":1700000000,"servertime":1700000001,"Type":1}]}`, id)
			result, err, calls := sendResponseFixture(t, `{"Success":true,"Data":`+data+`}`)
			if err != nil || calls != 1 || string(result.ID) != id || !reflect.DeepEqual(result.Keys, []string{"new:" + id, "msg:123", "id:" + id}) {
				t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
			}
		})
	}
}

func sendResponseFixture(t *testing.T, envelope string) (remoteapi.MessageSendResult, error, int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/Msg/SendTxt" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(envelope))
	}))
	defer srv.Close()
	c := &Client{wxid: "self", t: newTransport(srv.URL)}
	result, err := c.SendMessageWithResult(t.Context(), "recipient", remoteapi.OutgoingMessage{Type: remoteapi.MsgText, Text: "hello"})
	return result, err, calls
}

func TestSendResponseRejectsUncertainAcknowledgementWithoutRetry(t *testing.T) {
	for _, data := range []string{
		`null`, `{}`, `{"MsgId":0,"NewMsgId":0}`, `{"MsgId":-1}`, `{"NewMsgId":-1,"MsgId":123}`,
		`{"BaseResponse":{"ret":-13},"NewMsgId":123}`,
		`{"Ret":-2,"NewMsgId":123}`,
		`{"BaseResponse":{"ret":0},"Ret":-2,"NewMsgId":123}`,
		`{"BaseResponse":{},"NewMsgId":123}`,
		`{"BaseResponse":{"ret":0},"Count":1}`,
		`{"BaseResponse":{"ret":0},"Count":1,"List":[]}`,
		`{"BaseResponse":{"ret":0},"Count":1,"List":[{"Ret":0,"MsgId":1},{"Ret":0,"MsgId":2}]}`,
		`{"BaseResponse":{"ret":0},"Count":2,"List":[{"Ret":0,"MsgId":1}]}`,
		`{"BaseResponse":{"ret":0},"List":[{"Ret":0,"MsgId":1}]}`,
		`{"Count":1,"List":[{"Ret":0,"MsgId":1}]}`,
		`{"BaseResponse":{"ret":-13},"Count":1,"List":[{"Ret":0,"MsgId":1}]}`,
		`{"BaseResponse":{"ret":0},"Count":1,"List":[{"Ret":-13,"MsgId":1}]}`,
		`{"BaseResponse":{"ret":0},"Count":1,"List":[{"MsgId":1}]}`,
		`{"BaseResponse":{"ret":0},"Count":1,"List":[{"Ret":0}]}`,
		`{"BaseResponse":{"ret":0},"Count":1,"List":[null]}`,
		`{"BaseResponse":{"ret":0},"Count":1,"List":null,"MsgId":123}`,
		`{"BaseResponse":{"ret":0},"Count":1,"List":[{"Ret":0,"MsgId":1}],"MsgId":2}`,
	} {
		t.Run(data, func(t *testing.T) {
			result, err, calls := sendResponseFixture(t, `{"Success":true,"Data":`+data+`}`)
			if err == nil || result.ID != "" || calls != 1 {
				t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
			}
		})
	}
	t.Run("missing Data", func(t *testing.T) {
		result, err, calls := sendResponseFixture(t, `{"Success":true}`)
		if err == nil || result.ID != "" || calls != 1 {
			t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
		}
	})
}

func TestSendResponseExactFieldsAndSignedSolitaireGuard(t *testing.T) {
	var ack sendResp
	if err := json.Unmarshal([]byte(`{"BaseResponse":{"ret":0},"Count":1,"List":[{"Ret":0,"MsgId":18446744073709551615,"NewMsgId":18446744073709551615,"ClientMsgid":18446744073709551615,"Createtime":1700000000}]}`), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.MsgId != math.MaxUint64 || ack.NewMsgId != math.MaxUint64 || ack.ClientMsgId != "18446744073709551615" || ack.CreateTime != 1700000000 {
		t.Fatalf("lossy ack: %+v", ack)
	}
	payload := `<msg><appmsg><title>1. hello</title><type>53</type></appmsg></msg>`
	if sentSolitaireMessage("self", "room@chatroom", payload, ack) != nil {
		t.Fatal("unsigned IDs must not wrap into signed solitaire references")
	}
	if msgID(ack) != "18446744073709551615" {
		t.Fatal("confirmed send ID must remain intact")
	}
}

func TestSendResponseEmbeddedBaseResponseValidation(t *testing.T) {
	// This is the existing sendAppXML wrapper. An embedded UnmarshalJSON is
	// promoted, so its sibling field cannot be relied on for validation.
	for _, ret := range []int{0, -13} {
		var response struct {
			sendResp
			BaseResponse *struct {
				Ret int `json:"ret"`
			} `json:"BaseResponse"`
		}
		err := json.Unmarshal([]byte(fmt.Sprintf(`{"BaseResponse":{"ret":%d},"MsgId":123}`, ret)), &response)
		if (err == nil) != (ret == 0) {
			t.Fatalf("ret=%d err=%v", ret, err)
		}
	}
}

func TestSendResponseFlatCompatibility(t *testing.T) {
	for _, data := range []string{
		`{"BaseResponse":{"ret":0},"MsgId":123,"NewMsgId":9007199254740993,"ClientMsgId":"client"}`,
		`{"MsgId":123,"NewMsgId":9007199254740993,"ClientMsgId":12345}`,
		`{"MsgId":123,"NewMsgId":9007199254740993}`,
	} {
		result, err, calls := sendResponseFixture(t, `{"Success":true,"Data":`+data+`}`)
		if err != nil || result.ID != "9007199254740993" || calls != 1 {
			t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
		}
	}
}
