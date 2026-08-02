# WeChat Voice Chunk Download Fix

> **Superseded / 已被替代（2026-08-02）：** The `Offset`/60 KiB chunk protocol in this document does not match this repository's `swagger.json` or the user's actual TypeScript client. See the linked [2026-08-02 actual API design](../specs/2026-08-02-voice-download-actual-api-design.md) and [implementation plan](../plans/2026-08-02-voice-download-actual-api.md); this file is retained only as historical context. / 本文中的 `Offset`/60 KiB 分片协议与本仓库的 `swagger.json` 及用户实际使用的 TypeScript 客户端不一致。请以链接的 2026-08-02 实际接口设计和实施计划为准；本文仅保留作为历史背景。

## 中文审查摘要

**目标：** 按上游 `/Tools/DownloadVoice` 的真实协议完整获取微信语音，避免错误参数、遗漏分片或错误拼接导致语音不完整。

**方案：** 从语音 XML 读取 `fromusername`、`length`、`bufid`；使用原始 `MsgId`；按 60 KiB 设置 `Offset`/`Length` 分片请求；提取每次响应的 `Data.data.buffer` Base64 字符串，按顺序拼接后只解码一次。

**影响范围：** `internal/wechat/models.go`、`internal/wechat/convert.go` 及语音下载测试。不改 SILK 解码器、不增加 FFmpeg、不改出站音频。

**主要风险：** 不同上游版本可能省略响应元数据；因此元数据用于诊断而不作为强制失败条件，核心顺序由请求 offset 保证。

**待确认事项：** 无。用户已要求先只修获取并推送。

## Problem Statement

The current implementation diverges from a public client using the same API:

1. `voiceMessageDownloadReq` has no `Offset` field.
2. It sends the XML total `length` in one request instead of chunks up to 60 KiB.
3. Group messages prefer the chatroom ID for `FromUserName`, while the API reference uses `<voicemsg fromusername="...">`.
4. It decodes each response through a generic media parser immediately, while the reference concatenates ordered Base64 fragments and decodes once.
5. It discards response metadata (`offset`, `length`, `voiceLength`, `endFlag`, `cancelFlag`).

The reference request is:

```go
DownloadVoiceRequest{
    Wxid:         wxid,
    MsgId:        message.MsgId,
    Bufid:        voiceXML.Voicemsg.BufID,
    FromUserName: voiceXML.Voicemsg.FromUsername,
    Offset:       offset,
    Length:       currentChunkSize,
}
```

## Goals

1. Match the upstream request fields and chunking behavior.
2. Prefer XML `fromusername`, with safe fallback for payload variants.
3. Download every chunk in offset order.
4. Concatenate Base64 fragments before decoding.
5. Preserve context cancellation and actionable diagnostics.
6. Keep all decoding/transcoding behavior unchanged.

## Non-Goals

- Changing SILK frame decoding.
- Adding FFmpeg or outbound format support.
- Changing Matrix media upload or duration metadata.
- Repairing an upstream response whose fragments are missing.

## Selected Design

### XML Metadata

For `wxVoice`, extract:

- `length` → total encoded voice byte length;
- `bufid` / `bufID` → buffer ID;
- `fromusername` / `fromUserName` → download source user.

Store source user as `voice_fromusername` in `InboundMessage.Extra`.

Request source priority:

```text
voice XML fromusername → parsed sender → chat/talker
```

Use the raw `MsgId` when present, falling back to `NewMsgId` only when raw ID is zero, matching existing compatibility behavior and the reference client.

### Chunk Requests

Extend `voiceMessageDownloadReq` with:

```go
Offset int64 `json:"Offset"`
```

Treat the incoming `Length` as `totalLen`. For offsets `0, 60*1024, ...`, send:

```text
Offset = current offset
Length = min(60 KiB, totalLen - offset)
```

The 60 KiB size is divisible by three, allowing independently encoded Base64 fragments to concatenate without intermediate padding for every non-final chunk.

### Typed Response Parsing

Decode the envelope `Data` into a voice-specific response containing:

```text
msgId, newMsgId, offset, length, voiceLength,
endFlag, cancelFlag, clientMsgId, data.buffer, data.iLen
```

Require non-empty `data.buffer` for every requested chunk. Response metadata may be absent on older versions; when present and inconsistent, log requested and returned offsets/lengths but do not silently reorder fragments.

### Assembly

Append each Base64 fragment to a `strings.Builder` in request-offset order. After all requests succeed, normalize whitespace and decode the combined Base64 exactly once using the existing supported Base64 variants.

If combined decoding fails because an API variant pads each chunk independently, fall back to decoding every fragment separately and append decoded bytes in the same order. This fallback is diagnostic and does not change request ordering.

Return an error if no bytes are assembled.

## Data Flow

1. Parse webhook message and voice XML.
2. Construct total length, source username, buffer ID, and raw message ID.
3. Request each 60 KiB range with `Offset` and per-chunk `Length`.
4. Extract typed `data.buffer` and record response metadata.
5. Concatenate fragments in ascending request offset.
6. Decode Base64 once; use per-fragment fallback only for padded variants.
7. Pass complete SILK bytes to the existing strict decode/retry/recovery path.

## Error Handling and Observability

Log non-sensitive fields only:

- message ID, source user, total length;
- chunk index, requested offset/length;
- returned offset/length/end flag when present;
- fragment and final assembled byte counts;
- whether per-fragment Base64 fallback was required.

Any failed or empty chunk aborts the download; partial audio is not passed to the decoder as if complete.

## Testing Strategy

1. Group webhook test with XML `fromusername` different from chatroom ID; assert request uses XML value.
2. Single-chunk test asserts `Offset=0`, `Length=totalLen`, raw `MsgId`, `Bufid`, and account `Wxid`.
3. Multi-chunk test with more than 60 KiB raw data asserts exact offset/length sequence and complete byte-for-byte reconstruction.
4. Test independently padded fragment fallback.
5. Test empty chunk, malformed Base64, API failure, and context cancellation.
6. Preserve existing voice decode, truncation recovery, and webhook tests unchanged.
7. Run focused tests, full tests, race tests, vet, formatting, and diff checks.

## Trade-offs

This fix follows a known client implementation of the same API rather than inferring behavior from the incomplete Swagger schema. It adds multiple requests only when XML `length` exceeds 60 KiB. Typed response parsing is narrower and safer than recursive generic media extraction.

Per-fragment decode fallback supports upstream variants while keeping the documented concatenate-then-decode behavior primary.

## Technical Vocabulary / 技术词汇

- **chunk offset** — 分片偏移
- **total encoded length** — 编码总长度
- **Base64 fragment** — Base64 分片
- **typed response** — 类型化响应
- **request ordering** — 请求顺序
- **assembly** — 拼接
- **padding** — 填充字符
- **fallback decoding** — 回退解码
- **source username** — 来源用户名
- **response metadata** — 响应元数据

## 中文审查清单

- [ ] 是否使用 XML `fromusername`，而不是优先 chatroom ID？
- [ ] 是否加入 `Offset` 并按 60 KiB 分片？
- [ ] 是否每个请求只传当前分片 `Length`？
- [ ] 是否先拼接 Base64，再统一解码？
- [ ] 是否对独立 padding 分片提供安全回退？
- [ ] 是否拒绝空分片和不完整结果？
- [ ] 是否确认不修改转码器和出站音频？
