# WeChat Voice Download Against the Actual API

## 中文审查摘要

**目标：** 修正当前 Go 桥接器与实际 `/Tools/DownloadVoice` 接口之间的请求协议偏差，避免长语音因错误分片而被截断、重复或拼接损坏。

**方案：** 正常下载路径只发送一次 JSON POST；请求体严格包含 `Bufid`、`FromUserName`、`Length`、`MsgId`、`Wxid`，其中 `Length` 始终是语音 XML 声明的 SILK 总字节数。继续从现有 `Data.data.buffer` 响应结构读取 Base64，并在解码后严格校验字节数。仅当完整下载后的 SILK 严格解码报告 `io.ErrUnexpectedEOF` 时，保留现有的一次相同请求重试；这不是分片，重试请求仍携带完整总长度。

**影响范围：** `internal/wechat/models.go`、`internal/wechat/convert.go`、`internal/wechat/voice_test.go`，以及先前基于错误分片假设的设计与计划文档说明。不修改通用 HTTP transport、SILK 解码器、Matrix 上传、出站音频或认证逻辑。

**主要风险：** Swagger 未定义响应体；本设计保留已经由现有代码和测试覆盖的 `Data.data.buffer` Base64 响应结构。若生产接口返回不同结构，需要以真实响应样本另行修正，不能通过恢复无依据的请求分片来规避。

**待确认事项：** 用户已确认采用严格单次下载方案，不保留未在实际接口中出现的 `Offset` 分片兼容路径。这里的“单次”指每次下载尝试只发送一个完整请求；仅在完整 SILK 响应发生现有的可恢复截断错误时，允许一次完全相同的重试。

## Problem Statement

The current Go implementation does not match either the repository's actual Swagger definition or the supplied TypeScript client behavior.

The authoritative local schema at `swagger.json` defines `Tools.DownloadVoiceParam` with exactly these properties:

```text
Bufid, FromUserName, Length, MsgId, Wxid
```

The supplied TypeScript client likewise sends one JSON request with those five fields. It does not use `Offset`, `Section`, `StartPos`, a Range header, multipart data, a CDN URL, or a chunk loop.

By contrast, the current Go implementation:

1. adds an undocumented `Offset` field;
2. treats the XML `length` as a total to split into 60 KiB ranges;
3. replaces each request's `Length` with the current range size;
4. issues multiple requests for voices larger than 60 KiB;
5. concatenates the resulting Base64 fragments.

This behavior came from a previous design based on a different public client rather than this project's Swagger and the user's actual interface. If the backend interprets `Length` as the total SILK byte length and does not implement offsets, the bridge can repeatedly retrieve a prefix, request an incomplete payload, or assemble invalid SILK. Any of these can manifest as shortened audio.

## Considered Approaches

### 1. Strict Single-Request Download — Selected

Send exactly one request matching `Tools.DownloadVoiceParam`, retain typed parsing for the already-supported `Data.data.buffer` response, and require decoded bytes to match the declared total length.

This is the smallest change that aligns request behavior with both available authoritative references. It also makes malformed or incomplete responses fail explicitly rather than reaching the SILK recovery path as apparently valid media.

### 2. Single Request with Generic Response Parsing

Send the correct request but restore the older recursive media-response parser.

This could accept more undocumented response variants, but it weakens type safety and can accidentally select unrelated nested strings. There is no current evidence that the actual response differs from `Data.data.buffer`, so this additional flexibility is not justified.

### 3. Runtime Protocol Auto-Detection

Attempt the strict request first and fall back to the old offset-based chunk protocol.

This is rejected because the fallback protocol is absent from the actual interface. Retrying with invented fields would obscure backend failures, duplicate requests, and preserve the same corruption risk.

## Selected Design

### Request Model

`voiceMessageDownloadReq` will contain only wire fields accepted by the actual API:

```go
type voiceMessageDownloadReq struct {
    Wxid         string `json:"Wxid"`
    FromUserName string `json:"FromUserName"`
    MsgID        int64  `json:"MsgId"`
    Length       int64  `json:"Length"`
    BufID        string `json:"Bufid"`
}
```

No `Offset` field will be marshalled. The existing metadata source rules remain:

```text
Bufid        <- voice XML bufid
FromUserName <- voice XML fromusername, then sender/chat fallback
Length       <- voice XML length only (total SILK bytes; no voicelength fallback)
MsgId        <- raw message ID, then new message ID fallback
Wxid         <- active account
```

**中文提示：** `Length` 是完整 SILK 数据的总字节数，不是毫秒时长，也不是单次分片大小。`voicelength` 表示播放时长，不能在缺少 `length` 时充当下载字节数；此时应在发请求前报错。`Bufid` 必须存在并始终序列化，确保请求体恰好包含五个接口字段。

### Download Flow

`downloadVoice` will:

1. validate account ID, sender, message ID, non-empty buffer ID, positive total length, and the voice download size limit;
2. copy the active account ID into the request;
3. issue one `POST /Tools/DownloadVoice` call with the full declared `Length`; the request has no range query parameters and no `Range` header;
4. normalize ASCII whitespace in `data.buffer`;
5. reject an empty buffer or an encoded payload that exceeds the bounded Base64 size for the declared length;
6. decode the Base64 through the existing media-string decoder;
7. require a non-empty decoded result whose exact byte length equals the XML declaration;
8. re-check context cancellation after the response and before returning assembled media;
9. return the complete SILK payload to the unchanged SILK decode/retry/recovery layer.

A pre-canceled context sends no request. In-flight cancellation returns the context/transport error and no bytes. Cancellation observed after a response but before return also returns `context.Canceled` and no bytes. The request will not inspect or depend on response `offset`, `length`, `voiceLength`, `endFlag`, or `cancelFlag`, because those fields are not part of the documented request contract and are unnecessary for a single response.

### Response Model

The implementation will keep only the response fields it consumes:

```go
type voiceDownloadData struct {
    Buffer string `json:"buffer"`
}

type voiceDownloadResp struct {
    Data voiceDownloadData `json:"data"`
}
```

The common transport continues to unwrap the top-level API envelope before decoding this structure. This matches the existing tested response shape:

```json
{
  "Code": 0,
  "Success": true,
  "Data": {
    "data": {
      "buffer": "<base64 SILK>"
    }
  }
}
```

### Error Handling and Observability

The function returns no partial bytes on any failure. Errors distinguish:

- missing required request metadata;
- an excessive declared length;
- transport or API-envelope failure;
- an empty `data.buffer`;
- Base64 data exceeding its declared bound;
- malformed Base64;
- decoded byte length differing from the XML declaration.

Successful diagnostics will log message ID, source user, declared bytes, and actual bytes. No audio content, Base64 payload, credentials, or authentication data will be logged.

The existing retry in `attachVoiceMedia` remains limited to strict SILK decoding failures classified as `io.ErrUnexpectedEOF`. It performs at most one second **identical full request** and is explicitly not a chunk continuation. A download length mismatch fails before decoding and is not disguised as a recoverable SILK tail.

The generic transport limits an entire response envelope to 64 MiB. Because Base64 expands raw bytes by roughly 4/3, the voice-specific raw-byte limit will be reduced to **47 MiB**, leaving at least 1 MiB inside that envelope for Base64 rounding, JSON keys, and API metadata. This keeps the single-request response representable without broadening the generic transport limit.

## Testing Strategy

The regression test will use a payload larger than 60 KiB so that the current implementation demonstrably fails before the fix. It will assert:

1. exactly one `/Tools/DownloadVoice` request on the normal success path;
2. the request uses POST with `Content-Type: application/json`;
3. the request body contains exactly five keys, including a non-empty `Bufid`;
4. no `Offset`, `Section`, or `StartPos` is present, and there is no query range or `Range` header;
5. `Length` equals the complete raw SILK byte count;
6. `Bufid`, `FromUserName`, `MsgId`, and `Wxid` retain their expected sources;
7. the returned bytes match the complete payload byte for byte.

Additional focused tests will retain coverage for:

- lengths above the 47 MiB raw limit and `math.MaxInt64`, without sending a request;
- missing XML `length` even when `voicelength` exists, without sending a request;
- missing/empty `bufid`, without sending a request;
- an empty response buffer;
- malformed Base64;
- encoded payloads that exceed the declared size bound;
- decoded data shorter or longer than the declared total;
- pre-request, in-flight, and post-response context cancellation, always returning no partial bytes;
- API failures;
- webhook metadata extraction and SILK decoding/recovery behavior;
- the existing `io.ErrUnexpectedEOF` path issuing at most one identical full-request retry.

Tests that assert multiple offset requests, per-chunk metadata, fragment fallback, or second-chunk failures will be removed or rewritten because they encode the rejected protocol rather than valid behavior. The 60 KiB chunk constant, fragment-assembly helpers, chunk response metadata fields, and other dead protocol artifacts will be removed.

## Non-Goals

- Changing the SILK decoder or its sample rate.
- Changing WAV construction or Matrix duration metadata.
- Adding FFmpeg or another transcoder.
- Modifying outbound voice sending.
- Adding undocumented authentication headers.
- Supporting a second offset-based protocol without evidence from the actual API.

## Documentation Consistency

The previous files `docs/superpowers/specs/2026-07-14-voice-download-chunks-design.md` and `docs/superpowers/plans/2026-07-14-voice-download-chunks.md` remain historical records of the earlier decision. They must be clearly marked as superseded by this design so future work does not reintroduce unsupported chunking.

The acquisition-specific statements in `docs/superpowers/specs/2026-07-14-voice-multiframe-decode-design.md` and `docs/superpowers/plans/2026-07-14-voice-multiframe-decode.md` that say to preserve chunk download/Base64 assembly or keep chunk tests green must also be marked as superseded. Their SILK packet and internal-frame decoder decisions remain authoritative and unchanged.

## Technical Vocabulary / 技术词汇

- **authoritative schema** — 权威接口模式
- **wire field** — 实际传输字段
- **declared length** — 声明长度
- **total byte length** — 总字节长度
- **single-request download** — 单次请求下载
- **typed response parsing** — 类型化响应解析
- **response envelope** — 响应包装结构
- **byte-for-byte reconstruction** — 逐字节完整还原
- **protocol divergence** — 协议偏差
- **runtime auto-detection** — 运行时自动探测
- **superseded design** — 已被替代的设计

## 中文审查清单

- [ ] 是否同意 Swagger 与实际 TypeScript 客户端是本次请求协议的依据？
- [ ] 是否确认请求体只能包含 5 个接口字段，不能发送 `Offset`？
- [ ] 是否确认大于 60 KiB 的语音在正常路径只请求一次，并传完整 `Length`？
- [ ] 是否接受仅在严格 SILK 解码报告截断时执行一次相同完整请求重试？
- [ ] 是否确认 `Bufid` 必填，且缺少 XML `length` 时不能回退到 `voicelength`？
- [ ] 是否接受将原始语音上限调整为 47 MiB，以适配现有 64 MiB JSON/Base64 响应上限？
- [ ] 是否同意继续使用现有 `Data.data.buffer` Base64 响应结构？
- [ ] 是否同意对解码后字节数执行严格相等校验，不接受部分音频？
- [ ] 是否确认本次不修改 SILK 解码器和 Matrix 上传链路？
- [ ] 是否同意将旧分片设计、计划及多帧文档中的分片验收表述标记为已被本设计替代，同时保留多帧 SILK 解码设计？
