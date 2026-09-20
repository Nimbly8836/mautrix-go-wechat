package matrixas

import (
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestLocationUsesReadableRenderedFallback(t *testing.T) {
	for _, tc := range []struct{ name, xml, geo string }{
		{"invalid coordinates", `<msg><location x="NaN" y="2"/></msg>`, ""},
		{"malformed XML", `<msg><location`, ""},
		{"valid coordinates without XML labels", `<msg><location x="1" y="2"/></msg>`, "geo:1,2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const description = "地点\n完整街道地址"
			content := bridgeLocationCallback(t, tc.xml, description, "[位置]")
			wantType := event.MsgText
			if tc.geo != "" {
				wantType = event.MsgLocation
			}
			if content.MsgType != wantType || content.GeoURI != tc.geo || content.Body != description {
				t.Fatalf("readable fallback lost: type=%s geo=%q body=%q", content.MsgType, content.GeoURI, content.Body)
			}
		})
	}
}
