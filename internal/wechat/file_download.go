package wechat

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// downloadCDNFile is passive: it only downloads the original attachment. It
// never forwards to filehelper, retries, or falls back to the legacy CGI on a
// CDN failure. The caller owns the operation deadline, not the shared client's
// timeout for ordinary short requests and Sync long-polling.
func (c *Client) downloadCDNFile(ctx context.Context, payload fileAttachmentPayload, account string) ([]byte, string, error) {
	bulk := *c.t
	hc := *c.t.hc
	hc.Timeout = 0
	bulk.hc = &hc
	req := cdnDownloadRecordReq{
		Wxid: account, CdnDataUrl: payload.CDNURL, CdnDataKey: payload.AESKey,
		DataSize: payload.TotalLen, DataType: 8, IsThumb: 0, FullMd5: payload.FullMd5,
	}
	// Actual declared length, including Base64 expansion and a bounded JSON
	// envelope. A 64 MiB attachment must not hit the ordinary 64 MiB HTTP cap.
	limit := (payload.TotalLen+2)/3*4 + (1 << 20)
	raw, _, err := bulk.postRawLimited(ctx, "/Tools/CdnDownloadRecordItem", req, nil, limit)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		// Transport errors can contain a response snippet with CDN credentials.
		return nil, "", fmt.Errorf("file CDN download failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if c.selfWxid() != account {
		return nil, "", fmt.Errorf("file attachment account changed")
	}
	var env Envelope
	if json.Unmarshal(raw, &env) != nil || !env.Success || env.Code != 0 {
		return nil, "", fmt.Errorf("file CDN response rejected")
	}
	// The live producer uses Data.Image, not a guessed metadata string or URL.
	data, err := recordMediaBytes(raw, "application/json")
	if err != nil || int64(len(data)) != payload.TotalLen || int64(len(data)) > maxFileDownloadBytes {
		return nil, "", fmt.Errorf("file CDN bytes missing or length mismatch")
	}
	if payload.FullMd5 != "" {
		expected, err := hex.DecodeString(payload.FullMd5)
		// Protocol interoperability checksum only, not a security primitive.
		actual := md5.Sum(data)
		if err != nil || len(expected) != len(actual) || !strings.EqualFold(payload.FullMd5, hex.EncodeToString(actual[:])) {
			return nil, "", fmt.Errorf("file CDN checksum mismatch")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if c.selfWxid() != account {
		return nil, "", fmt.Errorf("file attachment account changed")
	}
	return data, mimeTypeForFile(payload.Title, data), nil
}

// fileChunkBytes verifies protocol acknowledgements before looking at bytes.
// The legacy endpoint can report outer Success=true while BaseResponse.ret=-2.
// Safe media decoding deliberately ignores attach IDs, hashes and error text.
func fileChunkBytes(raw []byte, contentType string, expected int64) ([]byte, error) {
	var fields map[string]json.RawMessage
	jsonResponse := strings.Contains(strings.ToLower(contentType), "json")
	if json.Unmarshal(raw, &fields) == nil {
		for key := range fields {
			jsonResponse = jsonResponse || strings.EqualFold(key, "Success")
		}
	}
	if jsonResponse {
		var env Envelope
		if json.Unmarshal(raw, &env) != nil || !env.Success || env.Code != 0 {
			return nil, fmt.Errorf("file chunk response rejected")
		}
		// Older versions return Data as base64 directly. Structured CGI
		// responses additionally carry BaseResponse and dataLen: validate both
		// whenever present, and require dataLen for a native acknowledgement.
		var dataFields map[string]json.RawMessage
		if json.Unmarshal(env.Data, &dataFields) == nil {
			var ack struct {
				BaseResponse *struct {
					Ret *int `json:"ret"`
				} `json:"BaseResponse"`
				DataLen *int64 `json:"dataLen"`
			}
			if json.Unmarshal(env.Data, &ack) != nil {
				return nil, fmt.Errorf("invalid file chunk acknowledgement")
			}
			for key := range dataFields {
				if strings.EqualFold(key, "BaseResponse") && (ack.BaseResponse == nil || ack.BaseResponse.Ret == nil || *ack.BaseResponse.Ret != 0 || ack.DataLen == nil) {
					return nil, fmt.Errorf("file chunk protocol rejected")
				}
				if strings.EqualFold(key, "dataLen") && (ack.DataLen == nil || *ack.DataLen != expected) {
					return nil, fmt.Errorf("file chunk declared length mismatch")
				}
			}
		}
	}
	data, err := recordMediaBytes(raw, contentType)
	if err != nil || int64(len(data)) != expected {
		return nil, fmt.Errorf("file chunk bytes missing or length mismatch")
	}
	return data, nil
}
