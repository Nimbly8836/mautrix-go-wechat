package matrixas

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestLocationMessagesKeepReadableTextAndValidCoordinates(t *testing.T) {
	for _, tc := range []struct {
		name, xml, body, geo string
	}{
		{"zero is a real coordinate", `<msg><location x="0" y="0" poiname="原点" /></msg>`, "原点", "geo:0,0"},
		{"negative coordinates", `<msg><location x="-33.123456" y="-70.123456" label="南半球地址" /></msg>`, "南半球地址", "geo:-33.123456,-70.123456"},
		{"boundary coordinates", `<msg><location x="90" y="-180" label="边界" /></msg>`, "边界", "geo:90,-180"},
		{"entities decoded once", `<msg><location x="1" y="2" poiname="A &amp; B &amp;lt;C&amp;gt;" label="地址 &quot;一&quot; &lt;二&gt;" /></msg>`, "A & B &lt;C&gt;\n地址 \"一\" <二>", "geo:1,2"},
		{"identical name and address", `<msg><location x="1" y="2" poiname="同一地点" label="同一地点" /></msg>`, "同一地点", "geo:1,2"},
		{"single quotes and XML declaration", `<?xml version="1.0"?><msg><location x='1.2500' y=' 2.50 ' poiname='地点📍'/></msg>`, "地点📍", "geo:1.25,2.5"},
		{"missing labels", `<msg><location x="1" y="2" /></msg>`, "位置暂不可用", "geo:1,2"},
		{"missing latitude", `<msg><location y="2" poiname="保留名称" label="保留地址" /></msg>`, "保留名称\n保留地址", ""},
		{"missing longitude", `<msg><location x="1" label="保留地址" /></msg>`, "保留地址", ""},
		{"empty latitude", `<msg><location x="" y="2" label="保留地址" /></msg>`, "保留地址", ""},
		{"latitude out of range", `<msg><location x="90.01" y="2" label="保留地址" /></msg>`, "保留地址", ""},
		{"longitude out of range", `<msg><location x="1" y="-180.01" label="保留地址" /></msg>`, "保留地址", ""},
		{"NaN", `<msg><location x="NaN" y="2" label="保留地址" /></msg>`, "保留地址", ""},
		{"infinity", `<msg><location x="1" y="+Inf" label="保留地址" /></msg>`, "保留地址", ""},
		{"non numeric", `<msg><location x="north" y="2" label="保留地址" /></msg>`, "保留地址", ""},
		{"plain text fallback", "原有位置描述", "原有位置描述", ""},
		{"unrelated namespace cannot override", `<msg xmlns:p="urn:other"><p:location x="70" y="80"/><location x="1" y="2" label="正确地址" p:x="70" p:label="错误地址" /></msg>`, "正确地址", "geo:1,2"},
		{"nested unrelated location ignored", `<msg><other><location x="70" y="80"/></other><location x="1" y="2" label="正确地址"/></msg>`, "正确地址", "geo:1,2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := bridgeLocationCallback(t, tc.xml, "[位置]", "位置暂不可用")
			assertLocationText(t, content, tc.body, tc.geo)
		})
	}
	longAddress := strings.Repeat("详细地址段落📍", 150) + "第一层104-02室"
	content := bridgeLocationCallback(t, `<msg><location x="1" y="2" poiname="长地址地点" label="`+longAddress+`" /></msg>`, "[位置]", "[位置]")
	assertLocationText(t, content, "长地址地点\n"+longAddress, "geo:1,2")
}

func TestInvalidLocationXMLDoesNotCreateMisleadingMap(t *testing.T) {
	const valid = `<location x="1" y="2" poiname="不应选中"/>`
	for name, xml := range map[string]string{
		"malformed":             `<msg>` + valid,
		"duplicate locations":   `<msg>` + valid + valid + `</msg>`,
		"nested location":       `<msg><other>` + valid + `</other></msg>`,
		"wrong root":            `<other>` + valid + `</other>`,
		"namespaced root":       `<msg xmlns="urn:other">` + valid + `</msg>`,
		"namespaced location":   `<msg xmlns:p="urn:other"><p:location x="1" y="2" /></msg>`,
		"namespaced coordinate": `<msg xmlns:p="urn:other"><location p:x="1" y="2" /></msg>`,
		"duplicate latitude":    `<msg><location x="1" x="2" y="3" /></msg>`,
		"DTD":                   `<!DOCTYPE msg><msg>` + valid + `</msg>`,
		"unknown entity":        `<msg><location x="1" y="2" label="&unknown;"/></msg>`,
		"multiple roots":        `<msg>` + valid + `</msg><msg/>`,
		"trailing garbage":      `<msg>` + valid + `</msg>garbage`,
		"location child markup": `<msg><location x="1" y="2"><other/></location></msg>`,
		"location child text":   `<msg><location x="1" y="2">unexpected</location></msg>`,
		"duplicate address":     `<msg><location x="1" y="2" label="first" label="second"/></msg>`,
		"oversized XML":         `<msg>` + strings.Repeat(" ", 1<<20) + valid + `</msg>`,
	} {
		t.Run(name, func(t *testing.T) {
			content := bridgeLocationCallback(t, xml, "[位置]", "位置暂不可用")
			if content.MsgType != event.MsgText || content.GeoURI != "" || content.Body != "位置暂不可用" {
				t.Fatalf("invalid XML created a map or leaked XML: type=%s geo=%q body=%q", content.MsgType, content.GeoURI, content.Body)
			}
		})
	}
}
