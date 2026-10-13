package wechat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const mergedRecordXML = `<?xml version="1.0"?><msg><appmsg><title>Hayek与undefined的聊天记录</title><des>Hayek: 摘要</des><type>19</type><url>https://support.weixin.qq.com/unsupported</url><recorditem><![CDATA[<recordinfo><title>Hayek与undefined的聊天记录</title><datalist count="2"><dataitem datatype="1"><datadesc>没有视频（完整正文）</datadesc><sourcename>Hayek</sourcename><sourcetime>2026-10-13&#x20;10:16:24</sourcetime></dataitem><dataitem datatype="4" dataid="video-id"><datadesc>[视频]</datadesc><cdndataurl>video-cdn</cdndataurl><cdndatakey>video-key</cdndatakey><datasize>12</datasize><datafmt>mp4</datafmt><duration>2</duration><sourcename>Hayek</sourcename><sourcetime>2026-10-13&#x20;10:16:38</sourcetime></dataitem></datalist></recordinfo>]]></recorditem></appmsg></msg>`

func TestMergedRecordTranscriptFromLocalAndCallback(t *testing.T) {
	for _, callback := range []bool{false, true} {
		name := "local"
		if callback {
			name = "callback"
		}
		t.Run(name, func(t *testing.T) {
			c := &Client{wxid: "wxid_self"}
			local := localMessage{MsgID: 1, NewMsgID: 2, MsgType: wxApp, Talker: "group@chatroom", SenderUserName: "wxid_friend", IsGroup: true, Content: "wxid_friend:\n" + mergedRecordXML, ContentText: "[聊天记录]"}
			ev, ok := c.convertLocal(context.Background(), local)
			if callback {
				data, _ := json.Marshal(local)
				var raw rawMessage
				if err := json.Unmarshal(data, &raw); err != nil {
					t.Fatal(err)
				}
				ev, ok = c.convertInbound(context.Background(), raw)
			}
			if !ok || ev.Message == nil {
				t.Fatal("no message")
			}
			for _, want := range []string{"没有视频（完整正文）", "Hayek", "2026-10-13 10:16:24", "[视频]"} {
				if !strings.Contains(ev.Message.Text, want) {
					t.Errorf("missing %q in %q", want, ev.Message.Text)
				}
			}
			if strings.Contains(ev.Message.Text, "support.weixin.qq.com") {
				t.Errorf("unsupported browser URL leaked into transcript: %q", ev.Message.Text)
			}
		})
	}
}
