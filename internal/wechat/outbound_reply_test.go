package wechat

import (
	"encoding/xml"
	"fmt"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestNativeReplyXMLMapsNeutralTypesExplicitly(t *testing.T) {
	for kind, want := range map[remoteapi.MessageType]int{remoteapi.MsgText: 1, remoteapi.MsgImage: 3, remoteapi.MsgAudio: 34, remoteapi.MsgVideo: 43, remoteapi.MsgFile: 49, remoteapi.MsgLocation: 48, remoteapi.MsgContactCard: 42, remoteapi.MsgLink: 49, remoteapi.MsgEmoji: 47, remoteapi.MsgApp: 49} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			reply := &remoteapi.Reply{Type: kind, Target: "9007199254740993", TargetKey: "new:9007199254740993", Sender: "wxid_author", SenderName: "A&B <name>", Text: "text & ]]> <appmsg>"}
			payload, err := buildNativeReplyXML("wxid_friend", "reply & <tag>", reply)
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Title string `xml:"appmsg>title"`
				Kind  int    `xml:"appmsg>type"`
				Ref   struct {
					Kind   int    `xml:"type"`
					From   string `xml:"fromusr"`
					Member string `xml:"chatusr"`
					Text   string `xml:"content"`
					Name   string `xml:"displayname"`
				} `xml:"appmsg>refermsg"`
			}
			if err = xml.Unmarshal([]byte(payload), &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.Kind != 57 || parsed.Ref.Kind != want || parsed.Ref.From != "wxid_author" || parsed.Ref.Member != "" || parsed.Ref.Text != reply.Text || parsed.Ref.Name != reply.SenderName || parsed.Title != "reply & <tag>" {
				t.Fatalf("payload=%s parsed=%+v", payload, parsed)
			}
			if strings.Count(payload, "<appmsg>") != 1 {
				t.Fatal("quoted XML escaped its text node")
			}
		})
	}
}

func TestIncomingAppQuotePreviewIsBoundedAndStripsPrivateFields(t *testing.T) {
	c := &Client{wxid: "wxid_self"}
	for _, raw := range []string{
		`<msg><appmsg><title>shared link</title><type>5</type><url>SECRET-URL</url><aeskey>SECRET-KEY</aeskey></appmsg></msg>`,
		`<appmsg><title>` + strings.Repeat("&amp;", 2000) + `</title><type>57</type><refermsg><content>SECRET-QUOTE</content></refermsg></appmsg>`,
	} {
		ev, ok := c.convertLocal(t.Context(), localMessage{MsgType: 49, Talker: "group@chatroom", SenderUserName: "wxid_friend", Content: "wxid_friend:\n" + raw})
		if !ok || ev.Message.Quote == nil {
			t.Fatal("missing app quote preview")
		}
		quote := ev.Message.Quote
		if quote.Type != remoteapi.MsgApp || len(quote.Text) > 4096 || strings.Contains(quote.Text, "SECRET") {
			t.Fatalf("unsafe quote preview: %+v", quote)
		}
		if _, err := appMessageTypeFromXML(quote.Text); err != nil {
			t.Fatalf("truncated XML preview: %v", err)
		}
	}
}

func TestNativeReplyXMLRejectsUnqualifiedOrInvalidIDs(t *testing.T) {
	for _, target := range []string{"", "0", "-1", "1e3", "9007199254740993x", "01", "<bad>"} {
		reply := &remoteapi.Reply{Target: remoteapi.MessageID(target), TargetKey: "new:" + target, Sender: "wxid_author"}
		if _, err := buildNativeReplyXML("wxid_friend", "reply", reply); err == nil {
			t.Fatalf("accepted %q", target)
		}
	}
	for _, reply := range []*remoteapi.Reply{nil, {Target: "123", TargetKey: "msg:123", Sender: "wxid_author"}, {Target: "123", TargetKey: "new:123"}, {Target: "123", TargetKey: "new:123", Sender: "wxid_author", Type: 99}} {
		if _, err := buildNativeReplyXML("wxid_friend", "reply", reply); err == nil {
			t.Fatalf("accepted %+v", reply)
		}
	}
}
