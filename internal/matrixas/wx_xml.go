package matrixas

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"maunium.net/go/mautrix/event"
)

// portalXML extracts an explicit, standalone wx_xml block. XML without this
// marker remains ordinary text. HTML is necessary because clients may consume
// the Markdown fences and put the language only in formatted_body.
func portalXML(msg *event.MessageEventContent) (string, bool, error) {
	if payload, found, err := fencedPortalXML(msg.Body); found {
		return payload, true, err
	}
	if msg.Format != event.FormatHTML || msg.FormattedBody == "" {
		return "", false, nil
	}
	nodes, err := html.ParseFragment(strings.NewReader(msg.FormattedBody), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return "", false, nil
	}
	marked := false
	var findMarker func(*html.Node)
	findMarker = func(n *html.Node) {
		if isWXXMLCode(n) {
			marked = true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findMarker(c)
		}
	}
	for _, n := range nodes {
		findMarker(n)
	}
	if !marked {
		return "", false, nil
	}
	var pre *html.Node
	for _, n := range nodes {
		if n.Type == html.TextNode && strings.TrimSpace(n.Data) == "" {
			continue
		}
		if pre != nil || n.Type != html.ElementNode || n.Data != "pre" {
			return "", true, standaloneXMLError()
		}
		pre = n
	}
	if pre == nil || pre.FirstChild == nil || pre.FirstChild != pre.LastChild || !isWXXMLCode(pre.FirstChild) {
		return "", true, standaloneXMLError()
	}
	var payload strings.Builder
	// Text and syntax-highlighting spans are lossless. Do not serialize HTML
	// tags back into the XML or unescape the text a second time.
	var appendText func(*html.Node) error
	appendText = func(n *html.Node) error {
		if n.Type == html.TextNode {
			payload.WriteString(n.Data)
			return nil
		}
		if n.Type != html.ElementNode || n.Data != "span" {
			return fmt.Errorf("wx_xml 代码块内只能包含文本或语法高亮 span")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := appendText(c); err != nil {
				return err
			}
		}
		return nil
	}
	for c := pre.FirstChild.FirstChild; c != nil; c = c.NextSibling {
		if err := appendText(c); err != nil {
			return "", true, err
		}
	}
	if strings.TrimSpace(payload.String()) == "" {
		return "", true, fmt.Errorf("wx_xml 代码块不能为空")
	}
	return payload.String(), true, nil
}

func isWXXMLCode(n *html.Node) bool {
	if n.Type != html.ElementNode || n.Data != "code" {
		return false
	}
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, class := range strings.Fields(a.Val) {
				if class == "language-wx_xml" {
					return true
				}
			}
		}
	}
	return false
}

func fencedPortalXML(body string) (string, bool, error) {
	var openMarker byte
	openLength, position, payloadStart := 0, 0, 0
	marked := false
	for _, line := range strings.SplitAfter(body, "\n") {
		marker, length, info := parseCodeFence(line)
		if openLength > 0 {
			if marker == openMarker && length >= openLength && info == "" {
				if marked {
					if strings.TrimSpace(body[position+len(line):]) != "" {
						return "", true, standaloneXMLError()
					}
					// Strip only the separator before the closing fence. Do not
					// reformat XML whitespace, CDATA, entities or emoji tokens.
					payload := strings.TrimSuffix(strings.TrimSuffix(body[payloadStart:position], "\n"), "\r")
					if strings.TrimSpace(payload) == "" {
						return "", true, fmt.Errorf("wx_xml 代码块不能为空")
					}
					return payload, true, nil
				}
				openLength = 0
			}
		} else if length > 0 {
			openMarker, openLength = marker, length
			language := strings.Fields(info)
			if len(language) > 0 && language[0] == "wx_xml" {
				if strings.TrimSpace(body[:position]) != "" {
					return "", true, standaloneXMLError()
				}
				marked = true
				payloadStart = position + len(line)
			}
		}
		position += len(line)
	}
	if marked {
		return "", true, standaloneXMLError()
	}
	return "", false, nil
}

// Track all top-level fences, not just wx_xml: a marker inside an ordinary
// fenced example is literal text, not another executable block. Backtick and
// tilde fences require at least three characters and at most three spaces of
// indentation. Closing fences must match the opener and be at least as long.
func parseCodeFence(line string) (byte, int, string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, ""
	}
	marker, length := trimmed[0], 0
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	info := strings.TrimSpace(trimmed[length:])
	if length < 3 || (marker == '`' && strings.Contains(info, "`")) {
		return 0, 0, ""
	}
	return marker, length, info
}

func standaloneXMLError() error {
	return fmt.Errorf("wx_xml 消息必须是一个完整代码块，使用匹配的围栏结束，不要混合其他文字或多个代码块")
}
