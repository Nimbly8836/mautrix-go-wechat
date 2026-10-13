package wechat

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

const (
	maxRecordItems            = 100
	maxRecordDepth            = 4
	maxRecordMediaCount       = 8
	maxRecordMediaBytes int64 = 16 << 20
	maxRecordTotalBytes int64 = 32 << 20
)

type recordXMLApp struct {
	XMLName      xml.Name
	Titles       []replyXMLText `xml:"title"`
	Descriptions []replyXMLText `xml:"des"`
	Records      []replyXMLText `xml:"recorditem"`
}

type recordXMLInfo struct {
	XMLName     xml.Name        `xml:"recordinfo"`
	Title       string          `xml:"title"`
	Description string          `xml:"desc"`
	Items       []recordXMLItem `xml:"datalist>dataitem"`
}

type recordXMLItem struct {
	Type        string         `xml:"datatype,attr"`
	ID          string         `xml:"dataid,attr"`
	Title       string         `xml:"datatitle"`
	Description string         `xml:"datadesc"`
	Sender      string         `xml:"sourcename"`
	Time        string         `xml:"sourcetime"`
	Format      string         `xml:"datafmt"`
	Duration    string         `xml:"duration"`
	URL         string         `xml:"cdndataurl"`
	Key         string         `xml:"cdndatakey"`
	Size        string         `xml:"datasize"`
	MD5         string         `xml:"fullmd5"`
	Nested      *recordXMLInfo `xml:"recordinfo"`
	NestedXML   string         `xml:"recorditem"`
	Group       struct {
		URL  string `xml:"fileid"`
		Key  string `xml:"filekey"`
		Size string `xml:"filesize"`
	} `xml:"group"`
}

type recordBudget struct {
	items, downloads int
	bytes            int64
}

// Probe only: actual parsing strips only the authoritative envelope sender.
func isMergedRecordXML(raw string) bool {
	raw = strings.TrimSpace(stripLastMessageOps(raw))
	if !strings.HasPrefix(raw, "<") {
		if _, body, ok := strings.Cut(raw, ":"); ok {
			raw = strings.TrimSpace(body)
		}
	}
	kind, err := appMessageTypeFromXML(raw)
	return err == nil && kind == 19
}

// Limit size/depth and reject directives and namespaced lookalikes before
// unmarshalling. XML entities are decoded exactly once at each envelope layer.
func decodeRecordXML(raw string, out any) error {
	if len(raw) > maxAppXMLBytes {
		return fmt.Errorf("record XML too large")
	}
	d := xml.NewDecoder(strings.NewReader(raw))
	depth, roots := 0, 0
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch node := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				roots++
			}
			if depth > 32 || roots > 1 || node.Name.Space != "" {
				return fmt.Errorf("invalid record XML structure")
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return fmt.Errorf("record XML directives are unsupported")
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(node)) != "" {
				return fmt.Errorf("text outside record XML")
			}
		}
	}
	if roots != 1 || depth != 0 {
		return fmt.Errorf("missing record XML root")
	}
	return xml.Unmarshal([]byte(raw), out)
}

func (c *Client) attachMergedRecord(ctx context.Context, m *remoteapi.InboundMessage, raw string) {
	raw = strings.TrimSpace(stripLastMessageOps(raw))
	if strings.HasSuffix(string(m.Chat), "@chatroom") && m.Sender != "" {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, string(m.Sender)+":"))
	}
	kind, err := appMessageTypeFromXML(raw)
	if err != nil || kind != 19 {
		return
	}
	m.Type, m.Text = remoteapi.MsgApp, "[合并聊天记录]（记录格式无效，无法展开）"
	var doc struct {
		XMLName xml.Name
		Apps    []recordXMLApp `xml:"appmsg"`
	}
	if decodeRecordXML(raw, &doc) != nil {
		return
	}
	var app recordXMLApp
	if doc.XMLName.Local == "appmsg" {
		if decodeRecordXML(raw, &app) != nil {
			return
		}
	} else if len(doc.Apps) == 1 {
		app = doc.Apps[0]
	} else {
		return
	}
	title, _ := replyText(app.Titles)
	description, _ := replyText(app.Descriptions)
	m.Text = "[合并聊天记录] " + strings.TrimSpace(title)
	if description != "" {
		m.Text += "\n" + description
	}
	inner, ok := replyText(app.Records)
	var info recordXMLInfo
	if !ok || decodeRecordXML(inner, &info) != nil {
		m.Text += "\n（记录格式无效，无法展开）"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	budget := &recordBudget{}
	record := c.convertRecord(ctx, &info, 0, budget)
	if strings.TrimSpace(record.Title) == "" {
		record.Title = title
	}
	m.Record, m.Text = record, record.PlainText()
}

func (c *Client) convertRecord(ctx context.Context, info *recordXMLInfo, depth int, budget *recordBudget) *remoteapi.ForwardedRecord {
	r := &remoteapi.ForwardedRecord{Title: info.Title}
	if len(info.Items) == 0 {
		r.Items = append(r.Items, remoteapi.RecordItem{Text: firstNonEmpty(info.Description, "（聊天记录为空）")})
	}
	for _, source := range info.Items {
		if budget.items >= maxRecordItems {
			r.Items = append(r.Items, remoteapi.RecordItem{Text: "（条目过多，后续记录已省略）"})
			break
		}
		budget.items++
		item := remoteapi.RecordItem{SenderName: firstNonEmpty(source.Sender, "未知发送者"), Time: source.Time, Text: source.Description}
		switch strings.TrimSpace(source.Type) {
		case "1":
			item.Text = firstNonEmpty(source.Description, source.Title, "[空文本]")
		case "2", "4", "8":
			label := map[string]string{"2": "[图片]", "4": "[视频]", "8": "[文件]"}[strings.TrimSpace(source.Type)]
			item.Text = label
			if source.Title != "" {
				item.Text += " " + source.Title
			}
			if source.Description != "" && source.Description != label {
				item.Text += "\n" + source.Description
			}
			if seconds, err := strconv.ParseUint(source.Duration, 10, 32); err == nil && seconds > 0 {
				item.Text += fmt.Sprintf("（%d秒）", seconds)
			}
			item.Media, item.MediaStatus = c.downloadRecordMedia(ctx, source, budget)
		case "17":
			item.Text = firstNonEmpty(source.Title, source.Description, "[聊天记录]")
			if depth >= maxRecordDepth-1 {
				item.MediaStatus = "嵌套层数过多，未展开"
			} else {
				nested := source.Nested
				if nested == nil && source.NestedXML != "" {
					var decoded recordXMLInfo
					if decodeRecordXML(source.NestedXML, &decoded) == nil {
						nested = &decoded
					}
				}
				if nested != nil {
					item.Record = c.convertRecord(ctx, nested, depth+1, budget)
				} else {
					item.MediaStatus = "无法展开嵌套记录"
				}
			}
		default:
			label := map[string]string{"3": "[语音]", "5": "[链接]", "6": "[位置]", "7": "[音乐]", "16": "[名片]", "19": "[小程序]"}[strings.TrimSpace(source.Type)]
			if label == "" {
				label = "[暂不支持的记录类型]"
			}
			item.Text = label
			if text := firstNonEmpty(source.Description, source.Title); text != "" && text != label {
				item.Text += " " + text
			}
		}
		r.Items = append(r.Items, item)
	}
	return r
}

type cdnDownloadRecordReq struct {
	Wxid       string
	CdnDataKey string
	CdnDataUrl string
	DataId     string
	DataSize   int64
	DataType   int // recorditem datatype; 4 selects video in fk-zhang v6.0.6+.
	FullMd5    string
	IsThumb    int64
}

func (c *Client) downloadRecordMedia(ctx context.Context, source recordXMLItem, budget *recordBudget) (*remoteapi.Media, string) {
	size, err := strconv.ParseInt(strings.TrimSpace(firstNonEmpty(source.Size, source.Group.Size)), 10, 64)
	if err != nil || size <= 0 {
		return nil, "附件大小未知，未下载"
	}
	if size > maxRecordMediaBytes || size > maxRecordTotalBytes-budget.bytes {
		return nil, "附件超过下载大小限制"
	}
	if budget.downloads >= maxRecordMediaCount {
		return nil, "附件数量超过下载限制"
	}
	key, url := firstNonEmpty(source.Key, source.Group.Key), firstNonEmpty(source.URL, source.Group.URL)
	if key == "" || url == "" {
		return nil, "缺少附件下载信息"
	}
	if c == nil || c.t == nil || c.selfWxid() == "" {
		return nil, "附件下载不可用"
	}
	if ctx.Err() != nil {
		return nil, "附件下载超时或已取消"
	}
	kind, _ := strconv.Atoi(strings.TrimSpace(source.Type))
	available := min(maxRecordMediaBytes, maxRecordTotalBytes-budget.bytes)
	responseLimit := size
	if kind == 4 {
		// CDN video renditions need not match the source record's original size.
		responseLimit = available
	}
	budget.downloads++
	budget.bytes += size // Failed attempts also consume the bundle's budget.
	req := cdnDownloadRecordReq{Wxid: c.selfWxid(), CdnDataKey: key, CdnDataUrl: url, DataId: source.ID, DataSize: size, DataType: kind, FullMd5: source.MD5}
	const endpoint = "/Tools/CdnDownloadRecordItem"
	// Allow base64 expansion plus a bounded JSON envelope, never an unbounded response.
	raw, contentType, err := c.t.postRawLimited(ctx, endpoint, req, nil, (responseLimit+2)/3*4+(1<<20))
	if err != nil {
		return nil, "附件下载失败或已过期"
	}
	data, err := recordMediaBytes(raw, contentType)
	if err != nil || len(data) == 0 {
		return nil, "上游附件下载失败或返回格式不支持"
	}
	actualSize := int64(len(data))
	if actualSize > available {
		return nil, "附件超过下载大小限制"
	}
	if kind == 4 {
		if !completeRecordMP4(data) {
			return nil, "视频内容不完整或不是有效 MP4"
		}
	} else if actualSize != size {
		return nil, "附件大小校验失败"
	}
	rendition := kind == 4 && actualSize != size
	// FullMd5 refers to the original, not a differently sized CDN rendition.
	if source.MD5 != "" && !rendition {
		expected, err := hex.DecodeString(source.MD5)
		// Protocol checksum only, not a security hash or authentication primitive.
		actual := md5.Sum(data)
		if err != nil || len(expected) != len(actual) || !strings.EqualFold(source.MD5, hex.EncodeToString(actual[:])) {
			return nil, "附件完整性校验失败"
		}
	}
	if actualSize > size {
		budget.bytes += actualSize - size
	}
	name := strings.TrimSpace(path.Base(strings.ReplaceAll(source.Title, "\\", "/")))
	if name == "" || name == "." || name == "/" {
		name = fmt.Sprintf("record-%d", budget.items)
	}
	ext := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(source.Format)), ".")
	if len(ext) > 12 || strings.IndexFunc(ext, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) >= 0 {
		ext = ""
	}
	if ext == "" && source.Type == "4" {
		ext = "mp4"
	}
	if ext != "" && !strings.HasSuffix(strings.ToLower(name), "."+ext) {
		name += "." + ext
	}
	return &remoteapi.Media{Data: data, Size: actualSize, FileName: name, MimeType: mimeTypeForFile(name, data)}, ""
}

// Unlike the older generic decoder, do not guess that arbitrary metadata
// strings (MD5, CDN keys, paths) are base64 attachments. Never fetch returned URLs.
func recordMediaBytes(raw []byte, contentType string) ([]byte, error) {
	jsonResponse := strings.Contains(strings.ToLower(contentType), "json")
	// Raw file downloads can themselves be JSON or plain text. Treat a body
	// as an API envelope only when advertised as JSON or carrying Success.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		for key := range fields {
			jsonResponse = jsonResponse || strings.EqualFold(key, "Success")
		}
	}
	if jsonResponse {
		var env Envelope
		if json.Unmarshal(raw, &env) != nil || !env.Success {
			return nil, fmt.Errorf("record download failed")
		}
		if env.Data62 != "" {
			data, _, err := decodeMediaString(env.Data62)
			return data, err
		}
		var value any
		if json.Unmarshal(env.Data, &value) != nil {
			return nil, fmt.Errorf("missing record data")
		}
		return recordMediaValue(value, 0)
	}
	if len(raw) == 0 || int64(len(raw)) > maxRecordMediaBytes {
		return nil, fmt.Errorf("invalid record response size")
	}
	return raw, nil
}

func recordMediaValue(value any, depth int) ([]byte, error) {
	if depth > 8 {
		return nil, fmt.Errorf("record response too deep")
	}
	switch v := value.(type) {
	case string:
		data, _, err := decodeMediaString(v)
		return data, err
	case []any:
		data := make([]byte, len(v))
		for i, value := range v {
			number, ok := value.(float64)
			if !ok || number < 0 || number > 255 || number != float64(byte(number)) {
				return nil, fmt.Errorf("invalid record byte array")
			}
			data[i] = byte(number)
		}
		if len(data) > 0 {
			return data, nil
		}
	case map[string]any:
		for _, key := range []string{"Image", "image", "Base64", "base64", "Data", "data", "Data62", "data62", "FileData", "fileData", "Buffer", "buffer", "Bytes", "bytes"} {
			if child, ok := v[key]; ok {
				if data, err := recordMediaValue(child, depth+1); err == nil && len(data) > 0 {
					return data, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("missing record attachment bytes")
}
