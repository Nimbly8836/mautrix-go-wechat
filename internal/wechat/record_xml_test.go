package wechat

import (
	"html"
	"strings"
	"testing"
)

func recordApp(inner string) string {
	return `<msg><appmsg><title>聊天记录</title><type>19</type><recorditem>` + html.EscapeString(inner) + `</recorditem></appmsg></msg>`
}

func TestRecordXMLLiteralTextAndNestedRecords(t *testing.T) {
	inner := `<recordinfo><title>记录 &amp; 标题</title><datalist count="999"><dataitem datatype="1"><datadesc><![CDATA[&amp; <script>text</script>]]></datadesc><sourcename>&lt;Alice&gt;</sourcename></dataitem><dataitem datatype="17"><recordinfo><title>内层</title><datalist><dataitem datatype="1"><datadesc>真正的正文</datadesc></dataitem></datalist></recordinfo></dataitem></datalist></recordinfo>`
	c := &Client{wxid: "self"}
	ev, _ := c.convertLocal(t.Context(), localMessage{MsgType: 49, Talker: "friend", Content: recordApp(inner)})
	r := ev.Message.Record
	if r == nil || r.Title != "记录 & 标题" || len(r.Items) != 2 || r.Items[0].Text != "&amp; <script>text</script>" || r.Items[0].SenderName != "<Alice>" || r.Items[1].Record == nil || r.Items[1].Record.Items[0].Text != "真正的正文" {
		t.Fatalf("bad decoding: %+v", r)
	}
}

func TestRecordXMLInvalidDocumentsNeverLeakAttachmentFields(t *testing.T) {
	for _, inner := range []string{
		`<recordinfo><datalist>`,
		`<wrong><datalist/></wrong>`,
		`<!DOCTYPE recordinfo [<!ENTITY x SYSTEM "file:///tmp/secret">]><recordinfo/>`,
		`<recordinfo/><recordinfo/>`,
		`<recordinfo xmlns="urn:spoof"/>`,
		`<recordinfo>` + strings.Repeat("<x>", 33) + strings.Repeat("</x>", 33) + `</recordinfo>`,
		`<recordinfo><desc>` + strings.Repeat("a", maxAppXMLBytes) + `</desc></recordinfo>`,
	} {
		c := &Client{wxid: "self"}
		raw := recordApp(inner)
		ev, _ := c.convertLocal(t.Context(), localMessage{MsgType: 49, Talker: "friend", Content: raw})
		if ev.Message.Record != nil || !strings.Contains(ev.Message.Text, "合并聊天记录") || strings.Contains(ev.Message.Text, "recordinfo") {
			t.Errorf("bad invalid XML fallback: %.100s", ev.Message.Text)
		}
	}
}

func TestRecordXMLMismatchedSenderAndDuplicatePayload(t *testing.T) {
	c := &Client{wxid: "self"}
	for _, raw := range []string{
		"different_sender:\n" + mergedRecordXML,
		strings.Replace(mergedRecordXML, "</recorditem>", "</recorditem><recorditem>SECRET</recorditem>", 1),
	} {
		ev, _ := c.convertLocal(t.Context(), localMessage{MsgType: 49, Talker: "group@chatroom", SenderUserName: "wxid_friend", Content: raw})
		if ev.Message.Record != nil || strings.Contains(ev.Message.Text, "SECRET") || strings.Contains(ev.Message.Text, "video-key") {
			t.Fatalf("unsafe record: %s", ev.Message.Text)
		}
	}
}

func TestRecordXMLGlobalItemAndDepthBounds(t *testing.T) {
	c := &Client{wxid: "self"}
	item := `<dataitem datatype="1"><datadesc>one</datadesc></dataitem>`
	inner := `<recordinfo><datalist>` + strings.Repeat(item, maxRecordItems+10) + `</datalist></recordinfo>`
	ev, _ := c.convertLocal(t.Context(), localMessage{MsgType: 49, Talker: "friend", Content: recordApp(inner)})
	if len(ev.Message.Record.Items) != maxRecordItems+1 || !strings.Contains(ev.Message.Text, "后续记录已省略") {
		t.Fatal("item limit missing")
	}
	inner = `<recordinfo><datalist>` + item + `</datalist></recordinfo>`
	for i := 0; i < 6; i++ {
		inner = `<recordinfo><datalist><dataitem datatype="17">` + inner + `</dataitem></datalist></recordinfo>`
	}
	ev, _ = c.convertLocal(t.Context(), localMessage{MsgType: 49, Talker: "friend", Content: recordApp(inner)})
	if ev.Message.Record == nil || !strings.Contains(ev.Message.Text, "嵌套层数过多") {
		t.Fatal("nested limit missing")
	}
}

func TestRecordMediaDecoderRawTextAndJSONFiles(t *testing.T) {
	for _, tc := range []struct{ body, contentType string }{
		{"plain text attachment", "text/plain"},
		{`{"configuration":true}`, "application/octet-stream"},
	} {
		data, err := recordMediaBytes([]byte(tc.body), tc.contentType)
		if err != nil || string(data) != tc.body {
			t.Fatalf("raw file lost: %q %v", data, err)
		}
	}
}

func TestRecordMediaDecoderNeverUsesMetadataAsBytes(t *testing.T) {
	for _, raw := range []string{
		`{"Success":true,"Data":{"md5":"YWJjZGVm"}}`,
		`{"Success":false,"Data62":"YWJjZGVm"}`,
		`{"Success":true,"Data":{"url":"https://example.invalid/download"}}`,
		`{"Success":true,"Data":{}}`,
		`{"Success":true,"Data":[1.5,2,3]}`,
		`{"Success":true,"Data":[1,256,3]}`,
	} {
		if _, err := recordMediaBytes([]byte(raw), "application/json"); err == nil {
			t.Errorf("metadata accepted: %s", raw)
		}
	}
}
