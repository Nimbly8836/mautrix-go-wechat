package matrixas

import (
	"fmt"
	"strings"
	"testing"
)

const quoteValidationSVRID = "9007199254740993"

func quoteValidationReference(kind int, svrid, content string) string {
	return fmt.Sprintf(`<refermsg><type>%d</type>%s<fromusr>group@chatroom</fromusr><chatusr>wxid_self</chatusr><displayname>小明🙂</displayname><content>%s</content></refermsg>`, kind, svrid, content)
}

func quoteValidationApp(title, reference string) string {
	return `<appmsg><title>` + title + `</title><type>57</type>` + reference + `</appmsg>`
}

func quoteValidationDeliver(t *testing.T, raw string, known bool) (relationMatrixMessage, []relationMatrixMessage) {
	t.Helper()
	b := newMessageRelationBridge(t)
	if known {
		b.callback(t, 301, 9007199254740993, 1, "已保存原消息", "已保存原消息")
	}
	b.callback(t, 302, 9007199254740995, 49, raw, "[引用消息]")
	messages, _ := b.snapshot()
	want := 1
	if known {
		want++
	}
	if len(messages) != want {
		t.Fatalf("expected %d Matrix messages, got %d", want, len(messages))
	}
	return messages[len(messages)-1], messages
}

func quoteValidationContains(t *testing.T, message relationMatrixMessage, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if !strings.Contains(message.Content.Body, field) {
			t.Errorf("readable body missing %q: %q", field, message.Content.Body)
		}
	}
}

func quoteValidationNoRelation(t *testing.T, message relationMatrixMessage) {
	t.Helper()
	if rel := message.Content.RelatesTo; rel != nil && rel.GetReplyTo() != "" {
		t.Errorf("unusable reference fabricated native reply: %+v", rel)
	}
}

func TestQuotedReplyValidationRootsAndLiteralText(t *testing.T) {
	for _, root := range []string{"appmsg", "msg/appmsg"} {
		for _, known := range []bool{false, true} {
			for _, fixture := range []struct{ name, xml, text string }{
				{"CDATA", "<![CDATA[中文第一行🙂 &amp;\n第二行 & 原文]]>", "中文第一行🙂 &amp;\n第二行 & 原文"},
				{"escaped", "中文第一行🙂 &amp;\n第二行 &amp;amp; 原文", "中文第一行🙂 &\n第二行 &amp; 原文"},
			} {
				t.Run(fmt.Sprintf("%s/known=%t/%s", root, known, fixture.name), func(t *testing.T) {
					raw := quoteValidationApp("新回复："+fixture.xml, quoteValidationReference(1, "<svrid>"+quoteValidationSVRID+"</svrid>", "引用："+fixture.xml))
					if root == "msg/appmsg" {
						raw = "<msg>" + raw + "</msg>"
					}
					reply, messages := quoteValidationDeliver(t, raw, known)
					quoteValidationContains(t, reply, "新回复："+fixture.text)
					if known {
						if rel := reply.Content.RelatesTo; rel == nil || rel.GetReplyTo() != messages[0].ID {
							t.Errorf("valid large SVRID did not attach original Matrix event: %+v", rel)
						}
					} else {
						quoteValidationNoRelation(t, reply)
						quoteValidationContains(t, reply, "小明🙂")
						// Quote renderers may prefix each line (for example with '> ').
						quoteValidationContains(t, reply, strings.Split("引用："+fixture.text, "\n")...)
					}
				})
			}
		}
	}
}

func TestQuotedReplyValidationUnusableSVRID(t *testing.T) {
	for _, tc := range []struct{ name, id string }{
		{"unavailable", "<svrid>9007199254740997</svrid>"},
		{"missing", ""},
		{"zero", "<svrid>0</svrid>"},
		{"nondecimal", "<svrid>not-an-id</svrid>"},
		{"overflow", "<svrid>18446744073709551616</svrid>"},
		{"duplicate", "<svrid>" + quoteValidationSVRID + "</svrid><svrid>" + quoteValidationSVRID + "</svrid>"},
		{"namespaced", `<x:svrid xmlns:x="urn:synthetic">` + quoteValidationSVRID + `</x:svrid>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := quoteValidationApp("新的答复✅", quoteValidationReference(1, tc.id, "引用正文：请明天到场"))
			reply, _ := quoteValidationDeliver(t, raw, true)
			quoteValidationNoRelation(t, reply)
			quoteValidationContains(t, reply, "小明🙂", "引用正文：请明天到场", "新的答复✅")
		})
	}
}

func TestQuotedReplyValidationAmbiguousReference(t *testing.T) {
	ref := quoteValidationReference(1, "<svrid>"+quoteValidationSVRID+"</svrid>", "引用正文")
	for _, tc := range []struct{ name, reference string }{
		{"duplicate", ref + ref},
		{"nested-only", "<wrapper>" + ref + "</wrapper>"},
		{"namespaced", strings.ReplaceAll(strings.ReplaceAll(ref, "<refermsg>", `<x:refermsg xmlns:x="urn:synthetic">`), "</refermsg>", "</x:refermsg>")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply, _ := quoteValidationDeliver(t, quoteValidationApp("保留我的新回复🙂", tc.reference), true)
			quoteValidationNoRelation(t, reply)
			quoteValidationContains(t, reply, "保留我的新回复🙂")
		})
	}
}

func TestQuotedReplyValidationMediaSafety(t *testing.T) {
	for _, tc := range []struct {
		kind       int
		label, xml string
	}{
		{3, "[图片]", `<msg><img aeskey="SYNTHETIC_AES_SECRET" cdnmidimgurl="SYNTHETIC_CDN_TOKEN"/></msg>`},
		{34, "[语音]", `<msg><voicemsg aeskey="SYNTHETIC_AES_SECRET"/></msg>`},
		{43, "[视频]", `<msg><videomsg aeskey="SYNTHETIC_AES_SECRET"/></msg>`},
		{47, "[表情]", `<msg><emoji aeskey="SYNTHETIC_AES_SECRET"/></msg>`},
		{48, "[位置]", `<msg><location x="1" y="2" label="SYNTHETIC_LOCATION_RAW"/></msg>`},
		{49, "", `<msg><appmsg><title>合成应用标题</title><attach><aeskey>SYNTHETIC_AES_SECRET</aeskey></attach></appmsg></msg>`},
		{9876, "[消息]", `<msg><unknown>SYNTHETIC_UNKNOWN_RAW</unknown></msg>`},
	} {
		t.Run(fmt.Sprint(tc.kind), func(t *testing.T) {
			raw := quoteValidationApp("新回复：好的", quoteValidationReference(tc.kind, "<svrid>9007199254740997</svrid>", "<![CDATA["+tc.xml+"]]>"))
			reply, _ := quoteValidationDeliver(t, raw, false)
			quoteValidationNoRelation(t, reply)
			quoteValidationContains(t, reply, "小明🙂", "新回复：好的", tc.label)
			combined := reply.Content.Body + reply.Content.FormattedBody
			for _, forbidden := range []string{"<msg>", "&lt;msg&gt;", "aeskey", "SYNTHETIC_AES_SECRET", "SYNTHETIC_CDN_TOKEN", "SYNTHETIC_LOCATION_RAW", "SYNTHETIC_UNKNOWN_RAW"} {
				if strings.Contains(combined, forbidden) {
					t.Errorf("quoted media leaked %q in Matrix content: %q", forbidden, combined)
				}
			}
		})
	}
}

func TestQuotedReplyValidationInvalidOuterTitleCannotExposeAttachment(t *testing.T) {
	const secret = "SYNTHETIC_ATTACHMENT_SECRET"
	content := `<![CDATA[<msg><appmsg><title>private attachment</title><url>https://cdn.example/file?token=` + secret + `</url></appmsg></msg>]]>`
	ref := quoteValidationReference(49, "<svrid>9007199254740997</svrid>", content)
	for _, tc := range []struct{ name, title, prefix string }{
		{"missing", "", ""},
		{"duplicate", "<title>first</title><title>second</title>", ""},
		{"markup", "<title><b>nested title</b></title>", ""},
		{"namespaced", `<x:title xmlns:x="urn:test">title</x:title>`, ""},
		{"mismatched sender", "<title>new reply</title>", "wxid_other:\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.prefix + "<appmsg><type>57</type>" + tc.title + ref + "</appmsg>"
			reply, _ := quoteValidationDeliver(t, raw, false)
			if strings.Contains(reply.Content.Body, secret) || strings.Contains(reply.Content.Body, "private attachment") || strings.Contains(reply.Content.Body, "<b>") {
				t.Fatalf("invalid outer title exposed quoted attachment fields: %q", reply.Content.Body)
			}
			if strings.TrimSpace(reply.Content.Body) == "" {
				t.Fatal("missing safe quote placeholder")
			}
		})
	}
}

func TestQuotedReplyValidationMalformedOuterXML(t *testing.T) {
	raw := quoteValidationApp("新的回复", quoteValidationReference(1, "<svrid>"+quoteValidationSVRID+"</svrid>", "SYNTHETIC_SOURCE_SECRET"))
	for _, malformed := range []string{"<msg>" + raw, "<msg>" + raw + "</wrong>"} {
		reply, _ := quoteValidationDeliver(t, malformed, true)
		quoteValidationNoRelation(t, reply)
		combined := reply.Content.Body + reply.Content.FormattedBody
		for _, forbidden := range []string{"<appmsg>", "&lt;appmsg&gt;", "<refermsg>", "SYNTHETIC_SOURCE_SECRET"} {
			if strings.Contains(combined, forbidden) {
				t.Errorf("malformed outer XML leaked %q: %q", forbidden, combined)
			}
		}
	}
}
