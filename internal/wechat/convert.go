package wechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// WeChat MsgType values (subset). See protocol docs.
const (
	wxText         = 1
	wxImage        = 3
	wxVoice        = 34
	wxVideo        = 43
	wxEmoji        = 47
	wxLocation     = 48
	wxApp          = 49 // links, files, mini-programs, quotes (parsed from XML)
	wxStatusNotify = 51
	wxRevoke       = 10002

	// The Base64-encoded voice payload plus its JSON envelope must fit the transport's 64 MiB response limit.
	maxVoiceDownloadBytes int64 = 47 << 20
)

func shouldDropLocal(m localMessage) bool {
	content := stripLastMessageOps(firstNonEmpty(m.ContentText, m.Content, m.PushContent))
	if m.MsgType == wxStatusNotify || content == "" {
		return true
	}
	if strings.HasPrefix(m.Talker, "@placeholder") || m.Talker == "" {
		return true
	}
	return false
}

func (c *Client) convertLocal(ctx context.Context, m localMessage) (remoteapi.Event, bool) {
	// QueryLocal can expose the rendered title in ContentText while keeping the
	// attachment XML (aeskey/attachid) in Content. Keep the XML so media can be
	// downloaded; applyMessageType will turn it into a human-readable body.
	content := stripLastMessageOps(firstNonEmpty(m.Content, m.ContentText, m.PushContent))
	if content == "" {
		return remoteapi.Event{}, false
	}
	chat := m.Talker
	// QueryLocal may report the local user's wxid as Talker/FromUserName for
	// outgoing echoes. For a direct chat the recipient is the conversation.
	if m.IsSendBySelf && !m.IsGroup && m.ToUserName != "" {
		chat = m.ToUserName
	}
	if chat == "" {
		if m.IsGroup {
			chat = firstNonEmpty(m.Talker, m.FromUserName)
		} else if m.IsSendBySelf {
			chat = m.ToUserName
		} else {
			chat = m.FromUserName
		}
	}
	sender := m.SenderUserName
	if sender == "" {
		if m.IsSendBySelf {
			sender = c.selfWxid()
		} else {
			sender = chat
		}
	}
	base := remoteapi.InboundMessage{
		ID:        remoteapi.MessageID(itoa(m.NewMsgID)),
		Chat:      remoteapi.ChatID(chat),
		Sender:    remoteapi.UserID(sender),
		Text:      content,
		Timestamp: time.Unix(m.CreateTime, 0),
		Extra: map[string]any{
			"msg_type":      m.MsgType,
			"msg_type_name": m.MsgTypeName,
			"msg_source":    m.MsgSource,
			"raw_msg_id":    m.MsgID,
			"msg_seq":       m.MsgSeq,
			"is_local":      true,
		},
	}
	applyMessageType(&base, m.MsgType, content, m.PushContent)
	if m.MsgType == wxApp {
		c.attachSolitaireSource(&base, firstNonEmpty(m.Content, m.ContentText), m.MsgID, m.NewMsgID)
	}
	if m.MsgType == wxImage {
		c.attachImageMedia(ctx, &base)
	}
	if m.MsgType == wxVideo {
		c.attachVideoMedia(ctx, &base, videoMessageDownloadReq{ToWxid: firstNonEmpty(m.ToUserName, chat), MsgID: firstNonZero(m.MsgID, m.NewMsgID), NewMsgID: m.NewMsgID, SenderUserName: sender, DataLen: int64Value(extraString(base.Extra, "video_data_len"))})
	}
	if m.MsgType == wxVoice {
		c.attachVoiceMedia(ctx, &base, voiceMessageDownloadReq{FromUserName: firstNonEmpty(extraString(base.Extra, "voice_fromusername"), sender, chat), MsgID: firstNonZero(m.MsgID, m.NewMsgID), Length: int64Value(extraString(base.Extra, "voice_length")), BufID: extraString(base.Extra, "voice_bufid")})
	}
	if base.Type == remoteapi.MsgFile {
		c.attachFileMedia(ctx, &base, fileMessageDownloadReq{Talker: chat, SenderUserName: sender, MsgID: m.MsgID, NewMsgID: m.NewMsgID, MsgSeq: m.MsgSeq, MsgType: m.MsgType, SourceXml: content, Xml: content})
	}
	if m.MsgType == wxEmoji {
		c.attachEmojiMedia(ctx, &base, content)
	}
	return remoteapi.Event{Kind: remoteapi.EventMessage, Message: &base}, true
}

// convertInbound maps a raw WeChat message into a neutral remoteapi.Event.
// Returns ok=false for messages the bridge should drop (system noise).
func (c *Client) convertInbound(ctx context.Context, m rawMessage) (remoteapi.Event, bool) {
	from := m.FromUserName.Str
	to := m.ToUserName.Str
	content := stripLastMessageOps(firstNonEmpty(m.ContentText, m.Content.Str, m.PushContent))
	if shouldDropWebhookNoise(m, content) {
		return remoteapi.Event{}, false
	}
	messageID := m.NewMsgId
	if messageID == 0 {
		messageID = m.NewMsgID
	}
	rawID := m.MsgId
	if rawID == 0 {
		rawID = m.MsgID
	}

	// Determine chat + real sender. Callback pushes may use the local-message
	// shape (Talker/SenderUserName/ContentText), while /Msg/Sync uses raw
	// FromUserName/ToUserName plus group sender prefixes in Content.
	chat := firstNonEmpty(m.Talker, from)
	sender := firstNonEmpty(m.SenderUserName, from)
	if m.IsSendBySelf && !m.IsGroup && to != "" {
		// Outgoing direct-chat echoes can carry the local user as Talker/From.
		chat = to
		sender = c.selfWxid()
	}
	if chat == "" {
		if m.IsGroup {
			chat = from
		} else if m.IsSendBySelf {
			chat = to
		} else {
			chat = from
		}
	}
	if sender == "" {
		if m.IsSendBySelf {
			sender = c.selfWxid()
		} else {
			sender = chat
		}
	}
	if strings.HasSuffix(chat, "@chatroom") && m.SenderUserName == "" {
		if idx := strings.Index(content, ":\n"); idx > 0 {
			sender = content[:idx]
			content = content[idx+2:]
		} else if idx := strings.Index(content, ":"); idx > 0 && strings.HasPrefix(content[:idx], "wxid_") {
			sender = content[:idx]
			content = strings.TrimSpace(content[idx+1:])
		}
	} else if from != "" && from == c.selfWxid() {
		// Outgoing echo: the conversation is the recipient, even when the
		// callback includes Talker equal to the local user's wxid.
		if !m.IsGroup && to != "" {
			chat = to
		}
		sender = from
	}

	base := remoteapi.InboundMessage{
		ID:        remoteapi.MessageID(itoa(messageID)),
		Chat:      remoteapi.ChatID(chat),
		Sender:    remoteapi.UserID(sender),
		Text:      content,
		Timestamp: time.Unix(m.CreateTime, 0),
		Extra: map[string]any{
			"msg_type":   m.MsgType,
			"msg_source": m.MsgSource,
			"raw_msg_id": rawID,
		},
	}

	if m.MsgType == wxRevoke {
		rev := parseRevokeMessage(content)
		if rev.Chat != "" {
			chat = rev.Chat
		}
		if rev.By != "" {
			sender = rev.By
		}
		return remoteapi.Event{
			Kind: remoteapi.EventRedaction,
			Redaction: &remoteapi.Redaction{
				Chat:      remoteapi.ChatID(chat),
				By:        remoteapi.UserID(sender),
				Target:    remoteapi.MessageID(firstNonEmpty(rev.OldMsgID, rev.MsgID)),
				Text:      rev.Text,
				Timestamp: time.Unix(m.CreateTime, 0),
			},
		}, true
	}
	applyMessageType(&base, m.MsgType, content, m.PushContent)
	if m.MsgType == wxApp {
		c.attachSolitaireSource(&base, firstNonEmpty(m.Content.Str, m.ContentText), rawID, messageID)
	}
	if m.MsgType == wxImage {
		c.attachImageMedia(ctx, &base)
	}
	if m.MsgType == wxVideo {
		c.attachVideoMedia(ctx, &base, videoMessageDownloadReq{ToWxid: firstNonEmpty(to, chat), MsgID: firstNonZero(rawID, messageID), NewMsgID: messageID, SenderUserName: sender, DataLen: int64Value(extraString(base.Extra, "video_data_len"))})
	}
	if m.MsgType == wxVoice {
		c.attachVoiceMedia(ctx, &base, voiceMessageDownloadReq{FromUserName: firstNonEmpty(extraString(base.Extra, "voice_fromusername"), sender, chat), MsgID: firstNonZero(rawID, messageID), Length: int64Value(extraString(base.Extra, "voice_length")), BufID: extraString(base.Extra, "voice_bufid")})
	}
	if base.Type == remoteapi.MsgFile {
		c.attachFileMedia(ctx, &base, fileMessageDownloadReq{Talker: chat, SenderUserName: sender, MsgID: rawID, NewMsgID: messageID, MsgSeq: m.MsgSeq, MsgType: m.MsgType, SourceXml: content, Xml: content})
	}
	if m.MsgType == wxEmoji {
		c.attachEmojiMedia(ctx, &base, content)
	}

	return remoteapi.Event{Kind: remoteapi.EventMessage, Message: &base}, true
}

func (c *Client) attachVoiceMedia(ctx context.Context, base *remoteapi.InboundMessage, req voiceMessageDownloadReq) {
	if c == nil || c.t == nil || base == nil || base.Media == nil {
		return
	}
	data, err := c.downloadVoice(ctx, req)
	if err != nil {
		log.Printf("failed to download WeChat voice %s msg_id=%d from=%q length=%d: %v", base.ID, req.MsgID, req.FromUserName, req.Length, err)
		return
	}
	wav, decodeErr := silkToWAV(data)
	if decodeErr != nil && errors.Is(decodeErr, io.ErrUnexpectedEOF) {
		log.Printf("first WeChat voice decode was truncated %s msg_id=%d from=%q first_bytes=%d: %v", base.ID, req.MsgID, req.FromUserName, len(data), decodeErr)
		candidates := [][]byte{data}
		var retryErr error

		retryData, retryDownloadErr := c.downloadVoice(ctx, req)
		if retryDownloadErr != nil {
			log.Printf("failed to retry WeChat voice download %s msg_id=%d from=%q: %v", base.ID, req.MsgID, req.FromUserName, retryDownloadErr)
			retryErr = fmt.Errorf("retry WeChat voice download: %w", retryDownloadErr)
		} else {
			retryWAV, retryDecodeErr := silkToWAV(retryData)
			if retryDecodeErr == nil {
				wav = retryWAV
				decodeErr = nil
				log.Printf("decoded retried WeChat voice %s msg_id=%d from=%q retry_bytes=%d", base.ID, req.MsgID, req.FromUserName, len(retryData))
			} else {
				log.Printf("failed to decode retried WeChat voice %s msg_id=%d from=%q retry_bytes=%d: %v", base.ID, req.MsgID, req.FromUserName, len(retryData), retryDecodeErr)
				retryErr = fmt.Errorf("decode retried WeChat voice retry_bytes=%d: %w", len(retryData), retryDecodeErr)
				if errors.Is(retryDecodeErr, io.ErrUnexpectedEOF) {
					candidates = append(candidates, retryData)
				}
			}
		}

		if decodeErr != nil {
			var recoveryErr error
			var sourceIndex, sourceBytes, droppedBytes int
			wav, sourceIndex, sourceBytes, droppedBytes, recoveryErr = recoverTruncatedSilkToWAV(candidates...)
			if recoveryErr == nil {
				decodeErr = nil
				log.Printf("recovered truncated WeChat voice %s msg_id=%d from=%q source_attempt=%d source_bytes=%d dropped_bytes=%d", base.ID, req.MsgID, req.FromUserName, sourceIndex+1, sourceBytes, droppedBytes)
			} else {
				decodeErr = errors.Join(decodeErr, retryErr, recoveryErr)
			}
		}
	}
	if decodeErr != nil {
		log.Printf("failed to decode WeChat voice %s msg_id=%d from=%q bytes=%d: %v", base.ID, req.MsgID, req.FromUserName, len(data), decodeErr)
		return
	}
	pcm, _, _, _, wavErr := wavToPCM(wav)
	if wavErr != nil || len(pcm) == 0 {
		log.Printf("failed to attach decoded WeChat voice %s msg_id=%d from=%q wav_bytes=%d: WAV data chunk is empty or invalid: %v", base.ID, req.MsgID, req.FromUserName, len(wav), wavErr)
		return
	}
	base.Media.Data = wav
	base.Media.Size = int64(len(wav))
	base.Media.MimeType = "audio/wav"
	if strings.TrimSpace(base.Media.FileName) == "" {
		base.Media.FileName = string(base.ID) + ".wav"
	}
}

func (c *Client) downloadVoice(ctx context.Context, req voiceMessageDownloadReq) ([]byte, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	if wxid == "" {
		return nil, fmt.Errorf("missing wxid")
	}
	if req.FromUserName == "" {
		return nil, fmt.Errorf("missing voice FromUserName")
	}
	if req.MsgID == 0 {
		return nil, fmt.Errorf("missing voice MsgId")
	}
	if req.BufID == "" {
		return nil, fmt.Errorf("missing voice Bufid")
	}
	if req.Length <= 0 {
		return nil, fmt.Errorf("missing voice length")
	}
	if req.Length > maxVoiceDownloadBytes {
		return nil, fmt.Errorf("voice length %d exceeds maximum %d", req.Length, maxVoiceDownloadBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	req.Wxid = wxid
	var resp voiceDownloadResp
	if err := c.t.post(ctx, "/Tools/DownloadVoice", req, nil, &resp); err != nil {
		return nil, fmt.Errorf("download voice: %w", err)
	}
	fragment := normalizeVoiceBase64Fragment(resp.Data.Buffer)
	if fragment == "" {
		return nil, fmt.Errorf("download voice returned empty data.buffer")
	}
	maxEncodedChars := int64(base64.StdEncoding.EncodedLen(int(req.Length)))
	if int64(len(fragment)) > maxEncodedChars {
		return nil, fmt.Errorf("voice Base64 data exceeds maximum encoded size: actual=%d maximum=%d", len(fragment), maxEncodedChars)
	}
	data, _, err := decodeMediaString(fragment)
	if err != nil {
		return nil, fmt.Errorf("decode WeChat voice: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(data)) != req.Length {
		log.Printf("WeChat voice length mismatch msg_id=%d from=%q declared=%d actual=%d", req.MsgID, req.FromUserName, req.Length, len(data))
		return nil, fmt.Errorf("downloaded voice data does not match declared length: actual=%d declared=%d", len(data), req.Length)
	}
	log.Printf("downloaded WeChat voice msg_id=%d from=%q declared=%d actual=%d", req.MsgID, req.FromUserName, req.Length, len(data))
	return data, nil
}

func normalizeVoiceBase64Fragment(fragment string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			return -1
		}
		return r
	}, fragment)
}

func (c *Client) attachFileMedia(ctx context.Context, base *remoteapi.InboundMessage, req fileMessageDownloadReq) {
	if c == nil || c.t == nil || base == nil {
		return
	}
	payload := parseFileAttachmentPayload(base.Text, req.SourceXml, req.SenderUserName, req.Talker)
	data, mimeType, err := c.downloadFile(ctx, payload)
	if err != nil {
		log.Printf("failed to download WeChat file %s title=%q app_id=%q attach_id_set=%t total_len=%d user=%q: %v", base.ID, payload.Title, payload.AppID, payload.AttachID != "", payload.TotalLen, payload.UserName, err)
		return
	}
	// For messages sent by this account, WeChat often puts the sender (our
	// own wxid) in <fromusername>. DownloadFile needs the conversation peer.
	// Prefer Talker in that case, otherwise self-sent files fail to download.
	if payload.UserName == c.selfWxid() && req.Talker != "" {
		payload.UserName = req.Talker
	}
	fileName := payload.Title
	if fileName == "" {
		fileName = fileNameFromFileMessage(base.Text, req.SourceXml)
	}
	if mimeType == "" {
		mimeType = mimeTypeForFile(fileName, data)
	}
	base.Media = &remoteapi.Media{Data: data, MimeType: mimeType, FileName: fileName, Size: int64(len(data))}
}

type fileAttachmentPayload struct {
	Title    string
	AppID    string
	AttachID string
	TotalLen int64
	FileExt  string
	UserName string
}

func parseFileAttachmentPayload(text, sourceXML, sender, talker string) fileAttachmentPayload {
	payload := fileAttachmentPayload{
		Title:    fileNameFromFileMessage(text, sourceXML),
		AppID:    firstNonEmpty(xmlAttr(sourceXML, "appid"), xmlTag(sourceXML, "appid")),
		AttachID: firstNonEmpty(xmlTag(sourceXML, "attachid"), xmlTag(sourceXML, "cdnattachid")),
		TotalLen: int64Value(xmlTag(sourceXML, "totallen")),
		FileExt:  xmlTag(sourceXML, "fileext"),
		UserName: firstNonEmpty(xmlTag(sourceXML, "fromusername"), xmlTag(sourceXML, "fromuser"), sender, talker),
	}
	return payload
}

func (c *Client) downloadFile(ctx context.Context, payload fileAttachmentPayload) ([]byte, string, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	if wxid == "" {
		return nil, "", fmt.Errorf("missing wxid")
	}
	if payload.AttachID == "" || payload.TotalLen <= 0 {
		return nil, "", fmt.Errorf("missing file attach_id or total length")
	}
	const chunkSize int64 = 65536
	var out []byte
	for start := int64(0); start < payload.TotalLen; start += chunkSize {
		n := chunkSize
		if remaining := payload.TotalLen - start; remaining < n {
			n = remaining
		}
		req := downloadFileReq{
			Wxid:     wxid,
			AppID:    payload.AppID,
			AttachId: payload.AttachID,
			DataLen:  payload.TotalLen,
			UserName: payload.UserName,
			Section:  downloadSectionReq{StartPos: start, DataLen: n},
		}
		chunk, _, err := c.downloadFileChunk(ctx, req)
		if err != nil {
			return nil, "", err
		}
		out = append(out, chunk...)
	}
	return out, mimeTypeForFile(payload.Title, out), nil
}

func (c *Client) downloadFileChunk(ctx context.Context, req downloadFileReq) ([]byte, string, error) {
	raw, contentType, err := c.t.postRaw(ctx, "/Tools/DownloadFile", req, nil)
	if err != nil {
		return nil, "", err
	}
	return mediaBytesFromRawResponse(raw, contentType, "/Tools/DownloadFile")
}

func fileNameFromFileMessage(text, sourceXML string) string {
	if title := strings.TrimSpace(xmlTag(sourceXML, "title")); title != "" {
		return title
	}
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if line != "" {
		return line
	}
	return "wechat-file"
}

func mimeTypeForFile(fileName string, data []byte) string {
	if idx := strings.LastIndex(fileName, "."); idx >= 0 {
		if mt := mime.TypeByExtension(fileName[idx:]); mt != "" {
			return mt
		}
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return "application/octet-stream"
}

func (c *Client) attachImageMedia(ctx context.Context, base *remoteapi.InboundMessage) {
	if c == nil || c.t == nil || base == nil || base.Media == nil {
		return
	}
	aesKey := extraString(base.Extra, "image_aeskey")
	fileNo := extraString(base.Extra, "image_file_no")
	if aesKey == "" || fileNo == "" {
		log.Printf("skip downloading WeChat image %s: missing aeskey/file_no aeskey_set=%t file_no_set=%t text=%q", base.ID, aesKey != "", fileNo != "", trimLog(base.Text, 160))
		return
	}
	data, mimeType, err := c.downloadCDNImage(ctx, aesKey, fileNo)
	if err != nil {
		log.Printf("failed to download WeChat image %s file_no=%s: %v", base.ID, fileNo, err)
		return
	}
	base.Media.Data = data
	base.Media.Size = int64(len(data))
	if mimeType != "" {
		base.Media.MimeType = mimeType
	}
}

const videoDownloadTimeout = 30 * time.Second

var videoDownloadChunkSize int64 = 1024

func (c *Client) attachVideoMedia(ctx context.Context, base *remoteapi.InboundMessage, req videoMessageDownloadReq) {
	if c == nil || c.t == nil || base == nil || base.Media == nil {
		return
	}
	downloadCtx, cancel := context.WithTimeout(ctx, videoDownloadTimeout)
	defer cancel()
	data, mimeType, err := c.downloadVideo(downloadCtx, req)
	if err != nil {
		log.Printf("failed to download WeChat video %s data_len=%d user=%q: %v", base.ID, req.DataLen, req.SenderUserName, err)
		return
	}
	base.Media.Data = data
	base.Media.Size = int64(len(data))
	if mimeType != "" {
		base.Media.MimeType = mimeType
	}
	if base.Media.MimeType == "" || base.Media.MimeType == "application/octet-stream" {
		base.Media.MimeType = "video/mp4"
	}
	if strings.TrimSpace(base.Media.FileName) == "" {
		base.Media.FileName = string(base.ID) + ".mp4"
	}
}

func (c *Client) downloadVideo(ctx context.Context, req videoMessageDownloadReq) ([]byte, string, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	if wxid == "" {
		return nil, "", fmt.Errorf("missing wxid")
	}
	if req.ToWxid == "" {
		return nil, "", fmt.Errorf("missing video ToWxid")
	}
	if req.MsgID == 0 {
		return nil, "", fmt.Errorf("missing video MsgId")
	}
	if req.DataLen <= 0 {
		return nil, "", fmt.Errorf("missing video data length")
	}
	chunkSize := videoDownloadChunkSize
	if chunkSize <= 0 {
		chunkSize = 1024
	}
	var out []byte
	mimeType := "video/mp4"
	for start := int64(0); start < req.DataLen; {
		n := chunkSize
		if remaining := req.DataLen - start; remaining < n {
			n = remaining
		}
		chunkReq := videoMessageDownloadReq{
			Wxid:         wxid,
			ToWxid:       req.ToWxid,
			MsgID:        req.MsgID,
			DataLen:      req.DataLen,
			Section:      downloadSectionReq{StartPos: start, DataLen: n},
			CompressType: 0,
		}
		chunk, gotLen, gotMime, err := c.downloadVideoChunk(ctx, chunkReq)
		if err != nil {
			return nil, "", err
		}
		if len(chunk) == 0 {
			return nil, "", fmt.Errorf("empty video chunk at offset %d", start)
		}
		out = append(out, chunk...)
		if strings.HasPrefix(gotMime, "video/") {
			mimeType = gotMime
		}
		if gotLen <= 0 {
			gotLen = int64(len(chunk))
		}
		start += gotLen
	}
	if int64(len(out)) > req.DataLen {
		out = out[:req.DataLen]
	}
	return out, mimeType, nil
}

func (c *Client) downloadVideoChunk(ctx context.Context, req videoMessageDownloadReq) ([]byte, int64, string, error) {
	raw, contentType, err := c.t.postRaw(ctx, "/Tools/DownloadVideo", req, nil)
	if err != nil {
		return nil, 0, "", err
	}
	return videoChunkFromRawResponse(raw, contentType, "/Tools/DownloadVideo")
}

func (c *Client) downloadCDNImage(ctx context.Context, aesKey, fileNo string) ([]byte, string, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	if wxid == "" {
		return nil, "", fmt.Errorf("missing wxid")
	}
	raw, contentType, err := c.t.postRaw(ctx, "/Tools/CdnDownloadImage", cdnDownloadImageReq{Wxid: wxid, FileAesKey: aesKey, FileNo: fileNo}, nil)
	if err != nil {
		return nil, "", err
	}
	return mediaBytesFromRawResponse(raw, contentType, "/Tools/CdnDownloadImage")
}

func videoChunkFromRawResponse(raw []byte, contentType, path string) ([]byte, int64, string, error) {
	var env Envelope
	if json.Unmarshal(raw, &env) == nil && (env.Success || env.Code != 0 || env.Message != "" || len(env.Data) > 0 || env.Data62 != "") {
		if !env.Success {
			return nil, 0, "", &APIError{Code: env.Code, Message: env.Message, Path: path}
		}
		data, mimeType, iLen, err := videoChunkFromJSON(env.Data)
		if err != nil {
			return nil, 0, "", err
		}
		return data, iLen, mimeType, nil
	}
	return raw, int64(len(raw)), strings.Split(contentType, ";")[0], nil
}

func videoChunkFromJSON(raw json.RawMessage) ([]byte, string, int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, "", 0, fmt.Errorf("response has no video chunk data")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, "", 0, fmt.Errorf("decode video chunk json: %w", err)
	}
	data, mimeType, ok := mediaBytesFromValue(v)
	if !ok || len(data) == 0 {
		return nil, "", 0, fmt.Errorf("unsupported video chunk response shape: %s", mediaResponseShape(v, 0))
	}
	iLen := int64Value(findNestedValue(v, "iLen", "ILen", "ilen", "len", "Len"))
	if iLen <= 0 {
		iLen = int64(len(data))
	}
	return data, mimeType, iLen, nil
}

func findNestedValue(v any, keys ...string) any {
	switch val := v.(type) {
	case map[string]any:
		for _, key := range keys {
			if item, ok := val[key]; ok {
				return item
			}
		}
		for _, item := range val {
			if found := findNestedValue(item, keys...); found != nil {
				return found
			}
		}
	case []any:
		for _, item := range val {
			if found := findNestedValue(item, keys...); found != nil {
				return found
			}
		}
	}
	return nil
}

func mediaBytesFromRawResponse(raw []byte, contentType, path string) ([]byte, string, error) {
	var env Envelope
	if json.Unmarshal(raw, &env) == nil && (env.Success || env.Code != 0 || env.Message != "" || len(env.Data) > 0 || env.Data62 != "") {
		if !env.Success {
			return nil, "", &APIError{Code: env.Code, Message: env.Message, Path: path}
		}
		return mediaBytesFromEnvelope(&env)
	}
	return raw, strings.Split(contentType, ";")[0], nil
}

func mediaBytesFromEnvelope(env *Envelope) ([]byte, string, error) {
	if env == nil {
		return nil, "", fmt.Errorf("empty response")
	}
	if strings.TrimSpace(env.Data62) != "" {
		return decodeMediaString(env.Data62)
	}
	return mediaBytesFromJSON(env.Data)
}

func mediaBytesFromJSON(raw json.RawMessage) ([]byte, string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, "", fmt.Errorf("response has no media data")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, "", fmt.Errorf("decode media json: %w", err)
	}
	if data, mimeType, ok := mediaBytesFromValue(v); ok {
		return data, mimeType, nil
	}
	return nil, "", fmt.Errorf("unsupported media response shape: %s", mediaResponseShape(v, 0))
}

func mediaBytesFromValue(v any) ([]byte, string, bool) {
	switch val := v.(type) {
	case string:
		if strings.TrimSpace(val) == "" {
			return nil, "", false
		}
		data, mimeType, err := decodeMediaString(val)
		return data, mimeType, err == nil && len(data) > 0
	case []any:
		if data, ok := numericByteArray(val); ok {
			return data, http.DetectContentType(data), true
		}
		for _, item := range val {
			if data, mimeType, ok := mediaBytesFromValue(item); ok {
				return data, mimeType, true
			}
		}
	case map[string]any:
		for _, key := range []string{"Base64", "base64", "Data", "data", "Data62", "data62", "ImageBase64", "imageBase64", "FileData", "fileData", "file_data", "Buffer", "buffer", "Content", "content", "Bytes", "bytes", "ImgBuf", "imgBuf"} {
			if item, ok := val[key]; ok {
				if data, mimeType, ok := mediaBytesFromValue(item); ok {
					return data, mimeType, true
				}
			}
		}
		for _, item := range val {
			if data, mimeType, ok := mediaBytesFromValue(item); ok {
				return data, mimeType, true
			}
		}
	}
	return nil, "", false
}

func numericByteArray(items []any) ([]byte, bool) {
	if len(items) == 0 {
		return nil, false
	}
	out := make([]byte, len(items))
	for i, item := range items {
		var n int64
		switch v := item.(type) {
		case float64:
			n = int64(v)
		case int64:
			n = v
		case int:
			n = int64(v)
		default:
			return nil, false
		}
		if n < 0 || n > 255 {
			return nil, false
		}
		out[i] = byte(n)
	}
	return out, true
}

func mediaResponseShape(v any, depth int) string {
	if depth > 2 {
		return "…"
	}
	switch val := v.(type) {
	case map[string]any:
		parts := make([]string, 0, len(val))
		for k, child := range val {
			parts = append(parts, k+":"+mediaResponseShape(child, depth+1))
		}
		sort.Strings(parts)
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		if len(val) == 0 {
			return "[]"
		}
		return fmt.Sprintf("[%d]%s", len(val), mediaResponseShape(val[0], depth+1))
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func decodeMediaString(s string) ([]byte, string, error) {
	s = strings.TrimSpace(s)
	mimeType := ""
	if idx := strings.Index(s, ","); strings.HasPrefix(s, "data:") && idx > 0 {
		meta := s[len("data:"):idx]
		if semi := strings.Index(meta, ";"); semi >= 0 {
			mimeType = meta[:semi]
		} else {
			mimeType = meta
		}
		s = s[idx+1:]
	}
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			return -1
		}
		return r
	}, s)
	var data []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		data, err = enc.DecodeString(s)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, "", err
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	return data, mimeType, nil
}

func extraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v := extra[key]
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func trimLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

func shouldDropWebhookNoise(m rawMessage, content string) bool {
	return m.MsgType == wxStatusNotify || content == ""
}

func stripLastMessageOps(content string) string {
	for strings.Contains(content, "<name>lastMessage</name>") {
		start := strings.LastIndex(content[:strings.Index(content, "<name>lastMessage</name>")], "<msg>")
		if start < 0 {
			break
		}
		endRel := strings.Index(content[start:], "</msg>")
		if endRel < 0 {
			break
		}
		end := start + endRel + len("</msg>")
		content = content[:start] + content[end:]
	}
	return strings.TrimSpace(content)
}

func applyMessageType(base *remoteapi.InboundMessage, msgType int, content, push string) {
	if strings.Contains(content, "<voipmsg") {
		base.Type = remoteapi.MsgApp
		base.Text = parseVoIPMessageText(content)
		return
	}
	switch msgType {
	case wxText:
		base.Type = remoteapi.MsgText
	case wxImage:
		base.Type = remoteapi.MsgImage
		base.Media = &remoteapi.Media{MimeType: "image/jpeg", FileName: string(base.ID) + ".jpg"}
		if isXMLMessage(base.Text) {
			if aesKey := xmlAttr(base.Text, "aeskey"); aesKey != "" {
				base.Extra["image_aeskey"] = aesKey
			}
			if fileNo := firstNonEmpty(xmlAttr(base.Text, "cdnmidimgurl"), xmlAttr(base.Text, "cdnthumburl")); fileNo != "" {
				base.Extra["image_file_no"] = fileNo
			}
		}
		if base.Text == "" || isXMLMessage(base.Text) {
			base.Text = firstNonEmpty(push, "[image]")
		}
	case wxVoice:
		base.Type = remoteapi.MsgAudio
		base.Media = &remoteapi.Media{MimeType: "audio/wav", FileName: string(base.ID) + ".wav"}
		if isXMLMessage(base.Text) {
			if length := xmlAttr(base.Text, "length"); length != "" {
				base.Extra["voice_length"] = length
			}
			if bufID := firstNonEmpty(xmlAttr(base.Text, "bufid"), xmlAttr(base.Text, "bufID")); bufID != "" {
				base.Extra["voice_bufid"] = bufID
			}
			if from := firstNonEmpty(xmlAttr(base.Text, "fromusername"), xmlAttr(base.Text, "fromUserName")); from != "" {
				base.Extra["voice_fromusername"] = from
			}
			if voiceTime := firstNonEmpty(xmlAttr(base.Text, "voicelength"), xmlAttr(base.Text, "length")); voiceTime != "" {
				base.Extra["duration"] = voiceTime
			}
		}
		base.Text = firstNonEmpty(push, "[voice]")
	case wxVideo:
		base.Type = remoteapi.MsgVideo
		base.Media = &remoteapi.Media{MimeType: "video/mp4", FileName: string(base.ID) + ".mp4"}
		if isXMLMessage(base.Text) {
			if aesKey := firstNonEmpty(xmlAttr(base.Text, "aeskey"), xmlAttr(base.Text, "cdnthumbaeskey")); aesKey != "" {
				base.Extra["video_aeskey"] = aesKey
			}
			if fileNo := firstNonEmpty(xmlAttr(base.Text, "cdnvideourl"), xmlAttr(base.Text, "cdnthumburl")); fileNo != "" {
				base.Extra["video_file_no"] = fileNo
			}
			if length := xmlAttr(base.Text, "length"); length != "" {
				base.Extra["video_data_len"] = length
			}
			if playLength := xmlAttr(base.Text, "playlength"); playLength != "" {
				base.Extra["duration"] = playLength
			}
		}
		base.Text = firstNonEmpty(push, "[video]")
	case wxEmoji:
		base.Type = remoteapi.MsgEmoji
		base.Text = firstNonEmpty(base.Text, push, "[emoji]")
	case wxLocation:
		base.Type = remoteapi.MsgLocation
	case wxApp:
		base.Type, base.Text = parseAppMessage(content, push)
		if base.Text == "" {
			base.Text = "[app]"
		}
	default:
		base.Type = remoteapi.MsgApp
		base.Text = firstNonEmpty(base.Text, push, "[message]")
	}
}

func parseVoIPMessageText(content string) string {
	msg := xmlTag(content, "msg")
	if msg == "" {
		return "[voip]"
	}
	return "[voip] " + msg
}

func parseAppMessage(content, push string) (remoteapi.MessageType, string) {
	// WeChat app messages are usually XML: <msg><appmsg><title>...</title>...
	// If the API only gives PushContent, fall back to that instead of showing
	// a useless [app] placeholder.
	if !strings.Contains(content, "<appmsg") && !strings.Contains(content, "<msg") {
		return remoteapi.MsgApp, strings.TrimSpace(firstNonEmpty(content, push))
	}
	title := xmlTag(content, "title")
	des := xmlTag(content, "des")
	url := xmlTag(content, "url")
	fileExt := xmlTag(content, "fileext")
	appType := xmlTag(content, "type")

	var typ remoteapi.MessageType
	switch appType {
	case "5", "33", "36": // link / mini program-ish
		typ = remoteapi.MsgLink
	case "6":
		typ = remoteapi.MsgFile
	case "19":
		typ = remoteapi.MsgApp // merged forward / chat history
	default:
		typ = remoteapi.MsgApp
	}

	parts := make([]string, 0, 4)
	if title != "" {
		parts = append(parts, title)
	}
	if des != "" && des != title {
		parts = append(parts, des)
	}
	if fileExt != "" && typ == remoteapi.MsgFile {
		parts = append(parts, "file type: "+fileExt)
	}
	if url != "" {
		parts = append(parts, url)
	}
	if len(parts) == 0 {
		fallback := strings.TrimSpace(push)
		if fallback == "" || strings.Contains(fallback, "<appmsg") {
			fallback = "[app]"
		}
		return typ, fallback
	}
	return typ, strings.Join(parts, "\n")
}

func isXMLMessage(content string) bool {
	content = strings.TrimSpace(content)
	return strings.HasPrefix(content, "<msg") || strings.HasPrefix(content, "<?xml")
}

func xmlAttr(raw, attr string) string {
	for _, quote := range []string{"\"", "'"} {
		needle := attr + "=" + quote
		for searchFrom := 0; searchFrom < len(raw); {
			index := strings.Index(raw[searchFrom:], needle)
			if index < 0 {
				break
			}
			start := searchFrom + index
			if start > 0 && !strings.ContainsRune(" \t\r\n", rune(raw[start-1])) {
				searchFrom = start + len(attr)
				continue
			}
			start += len(needle)
			end := strings.Index(raw[start:], quote)
			if end < 0 {
				return ""
			}
			return strings.TrimSpace(html.UnescapeString(raw[start : start+end]))
		}
	}
	return ""
}

type revokeInfo struct {
	Chat     string
	By       string
	MsgID    string
	OldMsgID string
	Text     string
}

func parseRevokeMessage(raw string) revokeInfo {
	dec := xml.NewDecoder(strings.NewReader(raw))
	var info revokeInfo
	var current string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			current = strings.ToLower(t.Name.Local)
		case xml.CharData:
			value := strings.TrimSpace(html.UnescapeString(string(t)))
			if value == "" {
				continue
			}
			switch current {
			case "session":
				info.Chat = value
			case "fromusername":
				info.By = value
			case "msgid":
				info.MsgID = value
			case "oldmsgid":
				info.OldMsgID = value
			case "replacemsg":
				info.Text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "<![CDATA["), "]]>"))
			}
		case xml.EndElement:
			current = ""
		}
	}
	return info
}

func xmlTag(raw, tag string) string {
	start := strings.Index(raw, "<"+tag+">")
	endTag := "</" + tag + ">"
	if start < 0 {
		return ""
	}
	start += len(tag) + 2
	end := strings.Index(raw[start:], endTag)
	if end < 0 {
		return ""
	}
	value := strings.TrimSpace(html.UnescapeString(raw[start : start+end]))
	value = strings.TrimPrefix(value, "<![CDATA[")
	value = strings.TrimSuffix(value, "]]>")
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func (c *Client) selfWxid() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.wxid
}

func itoa(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = digits[v%10]
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
