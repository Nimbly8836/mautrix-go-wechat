# WeChat Voice Chunk Download Fix Implementation Plan

> **Superseded / 已被替代（2026-08-02）：** The `Offset`/60 KiB chunk protocol in this document does not match this repository's `swagger.json` or the user's actual TypeScript client. See the linked [2026-08-02 actual API design](../specs/2026-08-02-voice-download-actual-api-design.md) and [implementation plan](2026-08-02-voice-download-actual-api.md); this file is retained only as historical context. / 本文中的 `Offset`/60 KiB 分片协议与本仓库的 `swagger.json` 及用户实际使用的 TypeScript 客户端不一致。请以链接的 2026-08-02 实际接口设计和实施计划为准；本文仅保留作为历史背景。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## 中文审查摘要

**目标：** 精确匹配 `/Tools/DownloadVoice` 上游协议，完整获取并拼接语音数据。

**实施方式：** 先写 XML 来源用户、Offset/Length 分片、Base64 拼接与错误路径 RED 测试；再增加类型化请求/响应和 60 KiB 下载循环；最后跑全测和 race。

**影响范围：** 仅 `internal/wechat/models.go`、`convert.go`、`voice_test.go`。不改 decoder、FFmpeg、Docker 或出站。

**风险：** 上游版本可能省略响应元数据；数据 buffer 仍为必需字段，offset/length/endFlag 只用于诊断。

**待确认事项：** 无。规格已批准。

**Goal:** Download complete WeChat voice data using correct XML parameters, 60 KiB offset chunks, and ordered Base64 assembly.

**Architecture:** Replace generic one-shot media parsing with a typed voice response and deterministic chunk loop. Keep existing decode/retry/recovery unchanged after complete SILK bytes are assembled.

**Tech Stack:** Go standard JSON/Base64, existing HTTP transport, httptest.

---

### Task 1: Implement Typed Chunked Voice Download

**Files:**
- Modify: `internal/wechat/models.go`
- Modify: `internal/wechat/convert.go`
- Modify: `internal/wechat/voice_test.go`

- [ ] **Step 1: Add RED tests for request parameters**

Update the group voice webhook fixture to include:

```xml
<voicemsg length="80" bufid="buf-1" voicelength="1000" fromusername="wxid_voice_sender" />
```

In the test server first decode the request into `map[string]any` so the pre-change code still compiles. Assert the JSON contains an explicit `Offset` key whose numeric value is zero, plus:

```go
body["Wxid"] == "wxid_self"
body["FromUserName"] == "wxid_voice_sender" // not chatroom ID
body["MsgId"] == float64(11)
body["Offset"] == float64(0)
body["Length"] == float64(80)
body["Bufid"] == "buf-1"
```

After the production model exists, typed-request tests may additionally decode into `voiceMessageDownloadReq`.

Run:

```bash
go test ./internal/wechat -run TestHandleWebhookVoiceDownloadsSilkAndEmitsWAVAudio -count=1 -v
```

Expected behavioral RED: raw JSON has no `Offset` key and uses the chatroom ID instead of XML `fromusername`; the test compiles against the old model.

- [ ] **Step 2: Add RED tests for multi-chunk assembly**

Create a direct `downloadVoice` test with deterministic raw bytes larger than 60 KiB:

```go
raw := make([]byte, 70*1024)
for i := range raw {
	raw[i] = byte(i)
}
```

The test server must:

1. Record every request.
2. Assert request 1 is `Offset=0, Length=60*1024`.
3. Assert request 2 is `Offset=60*1024, Length=len(raw)-60*1024`.
4. Base64-encode `raw[offset:offset+length]` separately and return it in:

```json
{
  "Code": 0,
  "Success": true,
  "Data": {
    "msgId": 11,
    "offset": 0,
    "length": 61440,
    "voiceLength": 1000,
    "data": {"buffer": "...", "iLen": 61440},
    "endFlag": 0,
    "cancelFlag": 0,
    "newMsgId": 22
  }
}
```

Assert returned bytes equal `raw` byte-for-byte. Convert expected lengths to `int64` when comparing with typed request fields.

Add unit tests for fragment decoding:

- concatenated fragments where every non-final 60 KiB chunk has no Base64 padding;
- independently padded arbitrary fragments, requiring per-fragment fallback;
- malformed fragment returns an error;
- empty fragment list and empty chunk return errors.

Run:

```bash
go test ./internal/wechat -run 'TestDownloadVoiceChunks|TestDecodeVoiceBase64Fragments' -count=1 -v
```

Expected RED: the direct chunk-download test fails because current code makes one request and has no offset. Tests that directly call the not-yet-created `decodeVoiceBase64Fragments` are explicitly compile RED until Step 5; record both failure causes separately.

- [ ] **Step 3: Add typed models**

In `models.go` extend the request and add response types:

```go
type voiceMessageDownloadReq struct {
	Wxid         string `json:"Wxid"`
	FromUserName string `json:"FromUserName"`
	MsgID        int64  `json:"MsgId"`
	Offset       int64  `json:"Offset"`
	Length       int64  `json:"Length"`
	BufID        string `json:"Bufid,omitempty"`
}

type voiceDownloadData struct {
	Buffer string `json:"buffer"`
	ILen   int64  `json:"iLen"`
}

type voiceDownloadResp struct {
	MsgID       int64             `json:"msgId"`
	NewMsgID    int64             `json:"newMsgId"`
	Offset      int64             `json:"offset"`
	Length      int64             `json:"length"`
	VoiceLength int64             `json:"voiceLength"`
	ClientMsgID string            `json:"clientMsgId"`
	Data        voiceDownloadData `json:"data"`
	EndFlag     int               `json:"endFlag"`
	CancelFlag  int               `json:"cancelFlag"`
}
```

- [ ] **Step 4: Extract XML source username**

In the `wxVoice` branch of `applyMessageType`, add:

```go
if from := firstNonEmpty(xmlAttr(base.Text, "fromusername"), xmlAttr(base.Text, "fromUserName")); from != "" {
	base.Extra["voice_fromusername"] = from
}
```

Update both `convertLocal` and `convertInbound` voice request construction points with priority:

```go
FromUserName: firstNonEmpty(
	extraString(base.Extra, "voice_fromusername"),
	sender,
	chat,
)
```

Keep raw `MsgID` preference and existing fallbacks.

- [ ] **Step 5: Implement Base64 fragment assembly**

Add:

```go
func decodeVoiceBase64Fragments(fragments []string) ([]byte, bool, error)
```

Behavior:

1. Remove whitespace from every fragment and reject empty fragments.
2. Join all normalized fragments and try existing `decodeMediaString` once.
3. If combined decoding succeeds, return `(data, false, nil)`.
4. If combined decoding fails, decode each normalized fragment separately and append bytes in order; return `(data, true, nil)`.
5. Reject malformed fragments and zero assembled bytes.

The bool reports whether per-fragment fallback was needed.

- [ ] **Step 6: Implement the 60 KiB download loop**

Use:

```go
const voiceDownloadChunkSize int64 = 60 * 1024
```

In `downloadVoice` preserve account/source/msg ID/total length validation, then:

```go
totalLen := req.Length
fragments := make([]string, 0) // avoid int64 capacity overflow; append per successful chunk
for offset := int64(0); offset < totalLen; offset += voiceDownloadChunkSize {
	chunkLen := voiceDownloadChunkSize
	if remaining := totalLen - offset; remaining < chunkLen {
		chunkLen = remaining
	}
	chunkReq := req
	chunkReq.Wxid = wxid
	chunkReq.Offset = offset
	chunkReq.Length = chunkLen

	var resp voiceDownloadResp
	if err := c.t.post(ctx, "/Tools/DownloadVoice", chunkReq, nil, &resp); err != nil {
		return nil, fmt.Errorf("download voice chunk offset=%d length=%d: %w", offset, chunkLen, err)
	}
	fragment := strings.TrimSpace(resp.Data.Buffer)
	if fragment == "" {
		return nil, fmt.Errorf("download voice chunk offset=%d returned empty data.buffer", offset)
	}
	fragments = append(fragments, fragment)
	// Log request/response metadata when returned values differ; do not reorder.
}
```

Check `ctx.Err()` before each request. After the loop call `decodeVoiceBase64Fragments`. Log total length, chunk count, assembled byte count, and fallback mode. Do not call `mediaBytesFromRawResponse` for voice chunks.

- [ ] **Step 7: Add error and cancellation tests**

Cover:

- second chunk logical API-envelope failure aborts with the requested offset in the error and returns no partial bytes;
- empty `data.buffer` aborts;
- malformed Base64 aborts;
- canceled context returns an error matching `context.Canceled` and stops before completing all chunks; the loop checks `ctx.Err()` before each request;
- response metadata mismatch does not reorder or duplicate data;
- both webhook (`convertInbound`) and local polling (`convertLocal`) use XML `fromusername` priority.

Update existing log assertions, including `TestHandleWebhookVoiceRecoversFirstPayloadWhenRetryDownloadFails`, to expect `from="wxid_voice_sender"` rather than the chatroom ID.

Use atomic request counters and buffered handler error channels; do not call `t.Fatalf` inside HTTP handlers.

- [ ] **Step 8: Verify GREEN and full suite**

Run:

```bash
gofmt -w internal/wechat/models.go internal/wechat/convert.go internal/wechat/voice_test.go
go test ./internal/wechat -run 'TestHandleWebhookVoice|TestDownloadVoiceChunks|TestDecodeVoiceBase64Fragments' -count=1 -v
go test -race ./internal/wechat -run 'TestHandleWebhookVoice|TestDownloadVoiceChunks|TestDecodeVoiceBase64Fragments' -count=20
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
git diff --check
```

Expected: all tests pass; existing SILK decode/recovery behavior remains unchanged.

- [ ] **Step 9: Review scope and commit**

Confirm implementation changes are limited to the three target Go files and no decoder/FFmpeg/Docker/outbound code changed. The separately committed specification and plan documents are expected branch changes.

```bash
git add internal/wechat/models.go internal/wechat/convert.go internal/wechat/voice_test.go
git commit -m "fix: download complete WeChat voice chunks"
```

## Acceptance Criteria

- Group voice requests use XML `fromusername`.
- Request includes `Offset` and per-chunk `Length`.
- Data larger than 60 KiB generates exact ordered requests.
- Base64 fragments reconstruct original bytes exactly.
- Empty/failed/malformed chunks never reach SILK decoding as partial media.
- Existing voice decode, retry, recovery, and Matrix delivery tests remain green.
- No decoder, FFmpeg, Docker, or outbound changes are included.

## Technical Vocabulary / 技术词汇

- **per-chunk length** — 单分片长度
- **ordered assembly** — 有序拼接
- **typed envelope** — 类型化信封响应
- **fragment fallback** — 分片回退
- **metadata mismatch** — 元数据不一致
- **raw message ID** — 原始消息 ID
- **source priority** — 来源优先级
- **partial media** — 不完整媒体

## 中文审查清单

- [ ] XML `fromusername` 是否优先？
- [ ] Offset/Length 是否按 60 KiB 正确递增？
- [ ] MsgId 是否仍优先 raw ID？
- [ ] 是否先拼 Base64 再统一解码？
- [ ] 独立 padding 是否有回退？
- [ ] 空/失败分片是否立即终止？
- [ ] 是否完全不触碰 decoder 和出站？
