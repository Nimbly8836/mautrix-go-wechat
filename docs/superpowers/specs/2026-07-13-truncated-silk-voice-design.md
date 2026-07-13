# Truncated SILK Voice Recovery

## 中文审查摘要

**目标：** 微信入站语音即使下载结果的最后一个 SILK 帧不完整，也应尽可能在 Matrix 中显示为可播放的 WAV 音频，而不是 `[voice]` 文本。

**方案：** 保留首次严格解码；仅当错误链包含 `io.ErrUnexpectedEOF` 时重试 `/Tools/DownloadVoice` 一次。重试仍截断或重试下载失败时，解析 SILK 容器边界，只裁掉最后一个不完整帧，保留至少一个完整帧后再次解码。所有其它解码错误继续按现有方式失败。

**影响范围：** `internal/wechat/convert.go` 的语音下载/附加流程、`internal/wechat/voice.go` 的 SILK 尾帧恢复，以及 `internal/wechat/voice_test.go` 的回归测试。

**主要风险：** 两次下载都截断时，恢复后的语音可能缺少末尾不足一个 SILK 帧的内容；这优于丢弃整条语音。只有遇到截断错误才增加一次 API 请求。

**待确认事项：** 无。用户已选择“重试一次，再容错不完整尾帧”。

## Problem Statement

Inbound WeChat voice messages are correctly classified as `remoteapi.MsgAudio`, but production logs show the downloaded SILK payload failing with:

```text
failed to read block: unexpected EOF
```

The current `attachVoiceMedia` flow treats any SILK decode error as fatal. The message retains its placeholder body (`[voice]`) but has no media bytes, so the Matrix conversion intentionally degrades it to a text message. A single incomplete trailing SILK frame therefore discards all otherwise complete audio frames.

## Goals

1. Retry one potentially incomplete voice download before repairing data.
2. Recover playable audio when a SILK payload has one incomplete trailing frame and at least one complete frame.
3. Reject empty PCM output, invalid headers, malformed interior frames, decoder failures unrelated to truncation, and payloads with no complete frame.
4. Preserve the existing Matrix upload path and emit `audio/wav` media containing a non-empty WAV data chunk.
5. Add deterministic regression tests for retry success and trailing-frame salvage.

## Non-Goals

- Concealing arbitrary SILK corruption or malformed frame lengths in the middle of a stream.
- Guaranteeing the missing final audio samples can be reconstructed.
- Adding FFmpeg or changing the container image.
- Uploading raw SILK as the normal Matrix representation.
- Changing outbound Matrix-to-WeChat audio behavior.

## Selected Design

### Validate, Then Strict Decode

Every downloaded SILK payload will first pass through the same lightweight structural inspector used for recovery. A healthy payload must have a recognized header, at least one complete positive-length frame no larger than 1024 bytes, and either end exactly after a complete frame or end at a negative footer with no trailing bytes. Zero-length frames, oversized frames, and footer trailers are rejected before invoking the dependency decoder.

After validation, `attachVoiceMedia` decodes normally. Healthy payloads retain the existing output behavior and incur no extra network request; the linear byte scan is bounded by the already downloaded payload size.

### Retry Only on Truncation

If strict decoding fails and `errors.Is(err, io.ErrUnexpectedEOF)` is true, the bridge will call `/Tools/DownloadVoice` once more with the identical request. Outcomes:

- If the retry downloads and decodes successfully, use the retry result.
- If the retry downloads but is still truncated, analyze both payloads as salvage candidates.
- Rank valid candidates by complete frame count, then total complete-frame payload bytes (excluding the header and frame-length fields); prefer the retry candidate only on an exact tie. Attempt candidates in rank order so a decoder failure in one candidate does not prevent trying the other.
- If the retry download fails, retain the first payload as the salvage candidate and log the retry failure.
- If retry decoding fails with a non-EOF error, do not salvage the retry payload, but still allow the originally truncated payload to be recovered.
- Do not retry an initially non-truncation decode error.

### Recover Only a Trailing Incomplete Frame

A pure structural inspector in `internal/wechat/voice.go` will validate normal payloads and identify recoverable trailing truncation:

1. Accept either `#!SILK_V3` or `0x02 + #!SILK_V3` header form.
2. Walk little-endian signed 16-bit frame lengths.
3. Accept positive frame lengths up to 1024 bytes, matching the decoder's normal payload bound. Treat zero-length or larger frame lengths as malformed rather than recoverable.
4. Stop normally at a negative footer only when no bytes follow it; bytes after a footer are malformed.
5. Treat either one dangling frame-length byte or a valid positive frame length whose payload extends past the available bytes as a trailing truncation. Return the prefix ending immediately before that incomplete length/frame.
6. Report recovery only if the prefix contains at least one complete positive-length frame.
7. Reject invalid headers, malformed interior structure, impossible frame lengths, footer trailers, or data with no complete frame.

A damaged final length field can be indistinguishable from a genuinely truncated final payload. Recovery is therefore bounded by the 1024-byte frame limit and the requirement that every preceding frame is structurally complete; this ambiguity will be documented in recovery logs rather than generalized to interior corruption.

The inspector reports complete frame count and total complete frame payload bytes. Candidate ranking compares frame count first and payload bytes second, explicitly excluding optional STX/header bytes; retry data wins only an exact tie.

The repaired prefix is decoded with the existing `silk.Decode` path. `silkToWAV` will additionally reject empty PCM output, preventing header-only or zero-frame input from producing a 44-byte but unplayable WAV. The helper is invoked only after the strict decoder has reported `io.ErrUnexpectedEOF`, so it cannot silently reinterpret unrelated errors.

> 中文提示：修复只删除“已经声明长度但数据不够”的最后一帧；不会跳过中间损坏，也不会把任意二进制误当语音。

## Data Flow

1. Webhook payload is converted to `remoteapi.MsgAudio` with a media placeholder.
2. `/Tools/DownloadVoice` returns SILK bytes.
3. The SILK inspector validates structure; trailing truncation is surfaced through an error wrapping `io.ErrUnexpectedEOF`.
4. Valid data is strictly decoded and required to produce non-empty PCM.
5. On `io.ErrUnexpectedEOF` from inspection or decoding, download is retried once.
6. If retry validation/decode also fails with truncation, rank valid complete-frame prefixes by frame count and then complete-frame payload bytes; try them in order.
7. Decode the repaired prefix to non-empty PCM and wrap it as WAV with a non-empty data chunk.
8. Attach WAV bytes and `audio/wav` metadata to the inbound message.
9. Existing Matrix code uploads and sends an `m.audio` event instead of `[voice]` text.

## Error Handling and Observability

Logs will include only non-sensitive metadata:

- message ID and chat identifier already present in existing logs;
- first and retry payload byte lengths;
- retry download failure, if any;
- number of trailing bytes removed during recovery.

If retry and recovery cannot produce valid WAV audio, retain the current failure log and placeholder behavior. The webhook itself must continue processing rather than returning an error for one bad media attachment.

## Testing Strategy

1. Build SILK fixtures using the existing `github.com/youthlin/silk` encoder.
2. Deterministically truncate a later frame payload while preserving earlier complete frames.
3. Confirm the current strict decoder returns an error matching `io.ErrUnexpectedEOF` (RED evidence).
4. Test the structural inspector across both SILK header forms, a partial trailing payload, one dangling length byte, a valid footer, zero-length frames, oversized frames, footer trailers, positive length with zero payload, and zero-complete-frame input.
5. Add integration assertions that zero/oversized frames and footer trailers are rejected even where the dependency decoder would otherwise return success.
6. Assert recovered WAV contains a `RIFF` header and a non-empty data chunk, not merely a 44-byte wrapper.
7. Test webhook retry success: first `/Tools/DownloadVoice` response is truncated, second is complete, exactly two requests occur, and the event contains playable WAV audio.
8. Test double truncation: both responses are truncated, exactly two requests occur, candidate ranking is deterministic using frame count then frame payload bytes, and complete frames are salvaged into playable WAV rather than leaving empty media.
9. Use atomic request counters and return HTTP test errors instead of calling `t.Fatalf` inside handlers, keeping race tests stable.
10. Run focused voice tests, full tests, race tests, formatting, and vet.

## Trade-offs

Retrying only after `io.ErrUnexpectedEOF` avoids extra load for healthy voice messages. Salvaging complete frames prioritizes successful message delivery over perfect preservation of an already unavailable tail. The approach cannot reconstruct missing bytes, and a corrupted final length field may be indistinguishable from truncation; the bounded frame size and complete-prefix validation limit that risk. Payloads without any complete audio frame are intentionally refused.

A raw SILK fallback would preserve bytes but would not provide normal playback in common Matrix clients. A general-purpose tolerant decoder would risk hiding arbitrary corruption. The selected bounded recovery is narrower and testable.

## Technical Vocabulary / 技术词汇

- **trailing frame** — 尾帧
- **recoverable prefix** — 可恢复前缀
- **strict decode** — 严格解码
- **truncation** — 截断
- **frame boundary** — 帧边界
- **little-endian** — 小端序
- **error chain** — 错误链
- **salvage candidate** — 抢救候选数据
- **deterministic fixture** — 确定性测试夹具
- **playable WAV** — 可播放 WAV

## 中文审查清单

- [ ] 是否确认只在 `io.ErrUnexpectedEOF` 时重试和恢复？
- [ ] 是否确认健康语音不增加额外请求？
- [ ] 是否接受两次下载均截断时最多丢失最后一个不完整帧？
- [ ] 是否确认没有完整帧时仍按失败处理，不伪造空音频？
- [ ] 是否同意本次不引入 FFmpeg、不改变出站语音？
- [ ] 是否以 Matrix 中出现可播放 `m.audio`/WAV 而不是 `[voice]` 为主要验收标准？
