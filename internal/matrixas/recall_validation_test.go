package matrixas

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWechatRecallRejectsUnsafeXML(t *testing.T) {
	const valid = `<sysmsg type="revokemsg"><revokemsg><session>group@chatroom</session><newmsgid>456</newmsgid><replacemsg>群成员甲撤回了一条消息</replacemsg></revokemsg></sysmsg>`
	for _, tc := range []struct{ name, raw string }{
		{"duplicate target", strings.Replace(valid, "<newmsgid>456</newmsgid>", "<newmsgid>456</newmsgid><newmsgid>456</newmsgid>", 1)},
		{"nested target", strings.Replace(valid, "<newmsgid>456</newmsgid>", "<quote><newmsgid>456</newmsgid></quote>", 1)},
		{"namespaced target", strings.Replace(valid, "<newmsgid>456</newmsgid>", `<x:newmsgid xmlns:x="urn:test">456</x:newmsgid>`, 1)},
		{"duplicate recall", strings.Replace(valid, "</sysmsg>", "<revokemsg><newmsgid>456</newmsgid></revokemsg></sysmsg>", 1)},
		{"multiple roots", valid + valid},
		{"trailing garbage", valid + "garbage"},
		{"malformed end", strings.TrimSuffix(valid, "</sysmsg>")},
		{"DTD", "<!DOCTYPE sysmsg>" + valid},
		{"zero", strings.Replace(valid, ">456<", ">0<", 1)},
		{"invalid new ID cannot use legacy", strings.Replace(valid, "<newmsgid>456</newmsgid>", "<newmsgid>bad</newmsgid><oldmsgid>123</oldmsgid>", 1)},
		{"wrong session", strings.Replace(valid, "group@chatroom", "another@chatroom", 1)},
		{"sender mismatch", "wxid_untrusted:\n" + valid},
		{"oversized", strings.Replace(valid, "</revokemsg>", "<extra>"+strings.Repeat("x", 1<<20)+"</extra></revokemsg>", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newMessageRelationBridge(t)
			b.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
			b.callback(t, 124, 789, 10002, tc.raw, "[系统消息]")
			messages, redacted := b.snapshot()
			if len(redacted) != 0 {
				t.Fatalf("unsafe XML selected a deletion target: %v", redacted)
			}
			if len(messages) != 2 || strings.Contains(messages[1].Content.Body, "<") || !strings.Contains(messages[1].Content.Body, "撤回") {
				t.Fatalf("unsafe XML lost its safe notice or leaked XML: %+v", messages)
			}
		})
	}
}

func TestWechatRecallRawXMLNeverBecomesSenderOrFallback(t *testing.T) {
	t.Run("colon in replacement is not an envelope", func(t *testing.T) {
		b := newMessageRelationBridge(t)
		b.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
		raw := strings.Replace(relationXML(10002, "456", "group@chatroom"), "群成员甲撤回了一条消息", "小明:\n撤回了一条消息", 1)
		b.callbackPayload(t, map[string]any{"AddMsgs": []any{map[string]any{"MsgId": 124, "NewMsgId": 789, "MsgType": 10002, "Content": map[string]string{"Str": raw}, "ContentText": "[系统消息]", "FromUserName": "group@chatroom", "ToUserName": "wxid_self", "CreateTime": 1710000001}}})
		messages, redacted := b.snapshot()
		if len(redacted) != 1 || redacted[0] != messages[0].ID || messages[1].Content.Body != "小明:\n撤回了一条消息" {
			t.Fatalf("XML text was mistaken for a group sender envelope: redacted=%v messages=%+v", redacted, messages)
		}
	})
	t.Run("malformed source and rendered envelope", func(t *testing.T) {
		b := newMessageRelationBridge(t)
		b.callback(t, 124, 789, 10002, "<sysmsg><revokemsg>", "wxid_other:\n<sysmsg><revokemsg><newmsgid>456</newmsgid></revokemsg></sysmsg>")
		messages, redacted := b.snapshot()
		if len(messages) != 1 || len(redacted) != 0 || strings.Contains(messages[0].Content.Body, "<") {
			t.Fatalf("recall fallback leaked XML: %+v", messages)
		}
	})
}

func TestWechatRecallLegacyIDsAndLiteralNotice(t *testing.T) {
	for _, field := range []string{"oldmsgid", "msgid"} {
		t.Run(field, func(t *testing.T) {
			b := newMessageRelationBridge(t)
			b.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
			raw := fmt.Sprintf(`<sysmsg type="revokemsg"><revokemsg><session>group@chatroom</session><%s>123</%s><replacemsg><![CDATA[小明 &amp; 小红撤回了一条消息🙂]]></replacemsg></revokemsg></sysmsg>`, field, field)
			b.callback(t, 124, 789, 10002, raw, "[系统消息]")
			messages, redacted := b.snapshot()
			if len(redacted) != 1 || redacted[0] != messages[0].ID || messages[1].Content.Body != "小明 &amp; 小红撤回了一条消息🙂" {
				t.Fatalf("legacy recall or literal CDATA was lost: redacted=%v messages=%+v", redacted, messages)
			}
		})
	}
}

func TestWechatRecallReportsUnconfirmedMatrixDeletion(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprintf("forbidden_%t", denied), func(t *testing.T) {
			b := newMessageRelationBridge(t)
			b.missingRedactionAck = !denied
			if denied {
				b.redactionStatus = http.StatusForbidden
			}
			b.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
			b.callback(t, 124, 789, 10002, relationXML(10002, "456", "group@chatroom"), "[系统消息]")
			messages, _ := b.snapshot()
			if len(messages) != 2 || !strings.Contains(messages[1].Content.Body, "Matrix 原消息撤回未确认") {
				t.Fatalf("uncertain Matrix response was reported as successful: %+v", messages)
			}
		})
	}
}

func TestWechatRecallTimeoutStillShowsHonestNotice(t *testing.T) {
	b := newMessageRelationBridge(t)
	b.redactionTimeout = true
	b.server.matrixOperationTimeout = 250 * time.Millisecond
	b.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
	b.callback(t, 124, 789, 10002, relationXML(10002, "456", "group@chatroom"), "[系统消息]")
	messages, _ := b.snapshot()
	if len(messages) != 2 || !strings.Contains(messages[1].Content.Body, "Matrix 原消息撤回未确认") {
		t.Fatalf("redaction timeout swallowed its user-facing notice: %+v", messages)
	}
}

func TestWechatRepeatedRecallKeepsMatrixTransaction(t *testing.T) {
	b := newMessageRelationBridge(t)
	b.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
	b.callback(t, 124, 789, 10002, relationXML(10002, "456", "group@chatroom"), "[系统消息]")
	b.callback(t, 125, 790, 10002, relationXML(10002, "456", "group@chatroom"), "[系统消息]")
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.redactionTxns) != 2 || b.redactionTxns[0] == "" || b.redactionTxns[0] != b.redactionTxns[1] || b.redactionSenders[0] != b.redactionSenders[1] {
		t.Fatalf("repeated recall lost stable Matrix identity: txns=%v senders=%v", b.redactionTxns, b.redactionSenders)
	}
}
