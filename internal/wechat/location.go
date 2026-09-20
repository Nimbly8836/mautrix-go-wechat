package wechat

import (
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

const maxLocationXMLBytes = 1 << 20

func attachLocation(base *remoteapi.InboundMessage, content, rendered, push string) {
	if strings.HasSuffix(string(base.Chat), "@chatroom") && base.Sender != "" {
		// Sender metadata remains authoritative. Remove only its matching wire
		// envelope, including when the callback already supplied SenderUserName.
		content = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), string(base.Sender)+":"))
	}
	base.Location, base.Text = parseLocationMessage(content, locationFallbackText(rendered, push))
}

func parseLocationMessage(content, push string) (*remoteapi.Location, string) {
	attrs, ok := locationAttributes(content)
	if !ok {
		return nil, locationFallbackText(content, push)
	}
	name, address := strings.TrimSpace(attrs["poiname"]), strings.TrimSpace(attrs["label"])
	parts := make([]string, 0, 2)
	if name != "" {
		parts = append(parts, name)
	}
	if address != "" && address != name {
		parts = append(parts, address)
	}
	body := strings.Join(parts, "\n")
	if body == "" {
		body = locationFallbackText("", push)
	}
	latitude, latErr := strconv.ParseFloat(strings.TrimSpace(attrs["x"]), 64)
	longitude, lonErr := strconv.ParseFloat(strings.TrimSpace(attrs["y"]), 64)
	point := &remoteapi.Location{Latitude: latitude, Longitude: longitude}
	if latErr != nil || lonErr != nil || point.GeoURI() == "" {
		return nil, body
	}
	return point, body
}

// Read the whole document, not just its first element: Unmarshal accepts a
// second root/trailing text and matches attributes by local name alone.
func locationAttributes(content string) (map[string]string, bool) {
	if len(content) > maxLocationXMLBytes {
		return nil, false
	}
	decoder := xml.NewDecoder(strings.NewReader(content))
	depth, roots := 0, 0
	insideLocation := false
	var attrs map[string]string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return attrs, roots == 1 && depth == 0 && attrs != nil
		}
		if err != nil {
			return nil, false
		}
		switch node := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				roots++
				if roots != 1 || node.Name != (xml.Name{Local: "msg"}) {
					return nil, false
				}
			}
			if insideLocation {
				return nil, false
			}
			if depth == 2 && node.Name == (xml.Name{Local: "location"}) {
				if attrs != nil {
					return nil, false
				}
				attrs = unqualifiedLocationAttributes(node.Attr)
				if attrs == nil {
					return nil, false
				}
				insideLocation = true
			}
		case xml.EndElement:
			if depth == 2 {
				insideLocation = false
			}
			depth--
		case xml.CharData:
			if (depth == 0 || insideLocation) && strings.TrimSpace(string(node)) != "" {
				return nil, false
			}
		case xml.Directive:
			return nil, false
		}
	}
}

func unqualifiedLocationAttributes(raw []xml.Attr) map[string]string {
	attrs := make(map[string]string)
	for _, attr := range raw {
		if attr.Name.Space != "" {
			continue
		}
		switch attr.Name.Local {
		case "x", "y", "poiname", "label":
			if _, duplicate := attrs[attr.Name.Local]; duplicate {
				return nil
			}
			attrs[attr.Name.Local] = attr.Value
		}
	}
	return attrs
}

func locationFallbackText(content, push string) string {
	fallback := "[位置]"
	for _, text := range []string{content, push} {
		text = strings.TrimSpace(text)
		// Do not leak location XML even when an unrecognized sender envelope
		// precedes it. Generic summaries must not hide a readable alternative.
		if text == "" || strings.HasPrefix(text, "<") || strings.Contains(text, "<msg") || strings.Contains(text, "<location") {
			continue
		}
		if text == "[位置]" || strings.EqualFold(text, "[location]") {
			fallback = text
			continue
		}
		return text
	}
	return fallback
}
