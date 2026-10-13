package wechat

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

const maxAppXMLBytes = 1 << 20

func (c *Client) sendAppXML(ctx context.Context, wxid, to, payload string) (remoteapi.MessageID, error) {
	return c.sendAppXMLWithWireType(ctx, wxid, to, payload, 0)
}

// Native quotes use outer wire Type 49 with inner XML subtype 57. Raw wx_xml
// keeps its existing subtype-derived behavior unless the native quote path
// supplies an explicit wire type.
func (c *Client) sendAppXMLWithWireType(ctx context.Context, wxid, to, payload string, wireType int) (remoteapi.MessageID, error) {
	appType, err := appMessageTypeFromXML(payload)
	if err != nil {
		return "", err
	}
	if wireType == 0 {
		wireType = appType
	}
	req := sendAppReq{Wxid: wxid, ToWxid: to, Type: wireType, Xml: payload}
	// sendResp validates BaseResponse and exact returned IDs itself. Do not
	// embed it beside sibling fields: UnmarshalJSON promotion swallows them.
	var response sendResp
	if err := c.t.post(ctx, "/Msg/SendApp", req, nil, &response); err != nil {
		return "", err
	}
	return qualifiedSendID(ctx, response), nil
}

// appMessageTypeFromXML validates the document and reads only the direct
// appmsg/type child (not a quoted/nested message's type). It never reserializes
// the XML: SendApp receives the user's payload unchanged. Go's XML decoder
// does not resolve external entities; directives/DTDs are explicitly rejected.
func appMessageTypeFromXML(payload string) (int, error) {
	if len(payload) > maxAppXMLBytes {
		return 0, fmt.Errorf("wx_xml 超过 1 MiB 大小限制")
	}
	decoder := xml.NewDecoder(strings.NewReader(payload))
	depth, appDepth, typeDepth := 0, 0, 0
	roots, apps, types := 0, 0, 0
	root := ""
	var typeText strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("wx_xml 格式无效: %w", err)
		}
		switch node := token.(type) {
		case xml.StartElement:
			depth++
			if typeDepth != 0 {
				return 0, fmt.Errorf("wx_xml appmsg/type 必须是整数文本")
			}
			if depth == 1 {
				roots++
				root = node.Name.Local
				if roots != 1 || node.Name.Space != "" || (root != "msg" && root != "appmsg") {
					return 0, fmt.Errorf("wx_xml 需要单个 <appmsg> 或 <msg><appmsg> 文档")
				}
			}
			if node.Name.Space == "" && node.Name.Local == "appmsg" && (depth == 1 || (depth == 2 && root == "msg")) {
				apps++
				appDepth = depth
			}
			if appDepth > 0 && depth == appDepth+1 && node.Name.Space == "" && node.Name.Local == "type" {
				types++
				typeDepth = depth
			}
		case xml.EndElement:
			if depth == typeDepth {
				typeDepth = 0
			}
			if depth == appDepth {
				appDepth = 0
			}
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(node)) != "" {
				return 0, fmt.Errorf("wx_xml 文档外不能包含其他文本")
			}
			if typeDepth != 0 {
				typeText.Write(node)
			}
		case xml.Directive:
			return 0, fmt.Errorf("wx_xml 不支持 DTD 或 XML 指令声明")
		}
	}
	if roots != 1 || apps != 1 || types != 1 {
		return 0, fmt.Errorf("wx_xml 必须包含一个 appmsg 及其唯一的 <type>正整数</type>")
	}
	appType, err := strconv.ParseInt(strings.TrimSpace(typeText.String()), 10, 32)
	if err != nil || appType <= 0 {
		return 0, fmt.Errorf("wx_xml appmsg/type 必须是正的 32 位整数")
	}
	return int(appType), nil
}
