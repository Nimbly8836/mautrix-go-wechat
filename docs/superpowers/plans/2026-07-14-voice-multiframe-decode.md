# Complete WeChat SILK Internal-Frame Decode Implementation Plan

> **Partial supersession / 部分内容已被替代（2026-08-02）：** Statements about `/Tools/DownloadVoice` offset chunking, Base64 fragment assembly, and chunk-specific tests are superseded by the linked [2026-08-02 actual API design](../specs/2026-08-02-voice-download-actual-api-design.md) and [implementation plan](2026-08-02-voice-download-actual-api.md), because the old `/Tools/DownloadVoice` offset chunking contract does not match either this repository's `swagger.json` or the user's actual TypeScript client. The SILK packet parsing and multi-internal-frame decoder design remains authoritative. / 本文中关于 `/Tools/DownloadVoice` 偏移分片、Base64 分片拼接和分片专用测试的内容已被链接的 2026-08-02 实际接口设计和实施计划替代，因为旧的 `/Tools/DownloadVoice` 偏移分片契约既不符合本仓库的 `swagger.json`，也不符合用户实际使用的 TypeScript 客户端；SILK 数据包解析与多内部帧解码器设计仍然有效并保持权威。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox syntax.

## 中文审查摘要

目标是只修复入站 SILK 40ms packet 被解成 20ms 的问题。先用 1 秒 fixture 看到 500ms RED，再实现共享 parser 和 SDK 循环得到约 1000ms GREEN。范围不含下载、FFmpeg、出站、Docker 或 Matrix。

**Goal:** Decode every internal SILK frame while preserving the completed voice acquisition fix.

**Architecture:** Refactor the validated frame scanner into a shared packet parser and add one focused SDK adapter using a single decoder state and the official `moreInternalDecoderFrames` loop.

**Files:**
- Modify: `internal/wechat/voice.go`
- Create: `internal/wechat/voice_decode.go`
- Create: `internal/wechat/voice_decode_test.go`
- Modify: `internal/wechat/voice_test.go` only if shared test helpers are needed
- Modify: `go.mod`, `go.sum`

### Task 1: Multi-Internal-Frame Decode

- [ ] **Step 1: Write and run the RED duration test**

Encode one second of 24kHz PCM with youthlin `PacketSizeMs(40)` and `Stx(true)`. Independently remove zero-length buffering records. Assert 25 zero records were removed, 25 positive packets remain, and existing inspection accepts the fixture.

Call `silkToWAV`, parse with `wavToPCM`, and calculate duration. Require `980..1020ms`.

```bash
go test ./internal/wechat -run TestSilkToWAVDecodesAllInternalFrames -count=1 -v
```

Expected RED: approximately `500ms`.

- [ ] **Step 2: Refactor shared packet parsing**

Add:

```go
type silkPacket struct { PayloadStart, PayloadEnd int }
func parseSilkPackets([]byte) (silkFrameInspection, []silkPacket, error)
```

Move current inspection logic unchanged into the parser; `inspectSilkFrames` becomes a wrapper. Add STX/non-STX offset tests. Run all inspection/recovery tests immediately and preserve `errors.Is(io.ErrUnexpectedEOF)`.

- [ ] **Step 3: Pin decoder dependencies**

```bash
go get github.com/wdvxdr1123/go-silk@v0.0.0-20220304095002-f67345df09ea
go get modernc.org/libc@v1.8.1
go mod tidy
```

Keep youthlin/silk v0.0.4 as direct encoder dependency.

- [ ] **Step 4: Implement `decodeSilkPCM`**

Create `voice_decode.go` using `sdk.SDK_Get_Decoder_Size`, `SDK_InitDecoder`, and `SDK_Decode`.

Requirements:

- one TLS and decoder state per stream;
- reject TLS/allocation/init failures;
- defer Xfree after defer TLS close so LIFO frees first;
- per packet, reset `nSamplesOut=480` before every call;
- call with the same payload until `FmoreInternalDecoderFrames==0`;
- check SDK return and sample count `1..480`;
- safety cap five calls;
- `runtime.KeepAlive` for unsafe-referenced values;
- append signed samples as little-endian PCM;
- reject empty PCM.

Update `silkToWAV` to validate via shared parser, call `decodeSilkPCM`, and use existing WAV writer.

Add `pcmDurationMS` using int64 arithmetic for the regression test only; do not modify message metadata.

- [ ] **Step 5: Verify GREEN and regressions**

```bash
gofmt -w internal/wechat/voice.go internal/wechat/voice_decode.go internal/wechat/voice_decode_test.go internal/wechat/voice_test.go
go test ./internal/wechat -run 'TestSilkToWAVDecodesAllInternalFrames|TestInspectSilkFrames|TestRecoverTruncatedSilkToWAV|TestSilkToWAVRejects' -count=1 -v
go test ./... -count=1
go test -race -gcflags=github.com/wdvxdr1123/go-silk/sdk=-d=checkptr=0 ./internal/wechat -run 'TestSilkToWAVDecodesAllInternalFrames|TestInspectSilkFrames|TestRecoverTruncatedSilkToWAV' -count=10
go vet ./...
git diff --check
```

Expected: fixture `1000ms`; all acquisition, retry, and recovery tests pass.

- [ ] **Step 6: Scope review and commit**

Confirm no changes to `convert.go`, `models.go`, `client.go`, Docker, Matrix, or outbound code.

```bash
git add internal/wechat/voice.go internal/wechat/voice_decode.go internal/wechat/voice_decode_test.go internal/wechat/voice_test.go go.mod go.sum
git commit -m "fix: decode all internal SILK frames"
```

## Acceptance Criteria

- 1-second 40ms fixture decodes to `980..1020ms`.
- Existing DownloadVoice chunk and recovery tests remain green.
- No FFmpeg/outbound/Docker/Matrix behavior changes.
- Full tests, focused race, vet, formatting, and reviews pass.

## Technical Vocabulary / 技术词汇

- **internal frame** — 内部帧
- **shared packet parser** — 共享包解析器
- **decoder state** — 解码器状态
- **sample buffer** — 样本缓冲区
- **LIFO cleanup** — 后进先出清理

## 中文审查清单

- [ ] RED 是否约 500ms？
- [ ] GREEN 是否约 1000ms？
- [ ] 是否复用同一 packet 边界？
- [ ] 是否只改解码相关文件？
- [ ] 获取层测试是否保持通过？
