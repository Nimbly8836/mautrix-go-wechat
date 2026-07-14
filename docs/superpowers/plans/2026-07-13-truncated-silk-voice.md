# Truncated SILK Voice Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## 中文审查摘要

**目标：** 修复微信入站语音因最后一个 SILK 帧截断而退化成 `[voice]` 的问题，使 Matrix 收到包含非空 PCM 的可播放 WAV。

**实施方式：** 先为 SILK 结构检查、空 PCM 拒绝和尾帧恢复写 RED 测试；再实现统一结构检查与有界恢复；最后为 `/Tools/DownloadVoice` 的单次重试和双截断恢复写 webhook 集成测试。

**影响范围：** 仅修改 `internal/wechat/voice.go`、`internal/wechat/voice_test.go`、`internal/wechat/convert.go`。不增加依赖，不修改 Matrix 上传、出站语音或容器镜像。

**风险：** 两次下载均截断时，恢复语音会缺少最后一个不完整帧；所有完整帧必须保留。健康语音不增加网络请求。

**待确认事项：** 无。规格已批准。

**Goal:** Retry truncated WeChat voice downloads once and salvage complete SILK frames into a non-empty playable WAV instead of emitting `[voice]` text.

**Architecture:** Add one shared SILK structural inspector that validates every payload before decoding and identifies a bounded trailing truncation. Keep healthy decoding unchanged, retry only errors matching `io.ErrUnexpectedEOF`, then rank recoverable prefixes by complete frame count and payload bytes.

**Tech Stack:** Go 1.25, `github.com/youthlin/silk`, standard `errors`, `io`, `encoding/binary`, `sort`, `sync/atomic`, `httptest`.

---

## File Structure

- Modify `internal/wechat/voice.go`: structural inspection, non-empty PCM validation, and ranked trailing-frame recovery.
- Modify `internal/wechat/voice_test.go`: deterministic SILK structure/recovery tests and webhook retry/recovery tests.
- Modify `internal/wechat/convert.go`: retry-on-truncation orchestration and diagnostic logging.

### Task 1: Validate SILK Structure and Recover Complete Frames

**Files:**
- Modify: `internal/wechat/voice.go:3-28`
- Modify: `internal/wechat/voice_test.go`
- Test: `internal/wechat/voice_test.go`

- [ ] **Step 1: Add failing structure and recovery tests**

Add `errors`, `encoding/binary`, and `io` to `internal/wechat/voice_test.go`. Add test helpers that construct containers independently from production parsing:

```go
func testSilkContainer(stx bool, frames ...[]byte) []byte {
	var out bytes.Buffer
	if stx {
		out.WriteByte(0x02)
	}
	out.WriteString("#!SILK_V3")
	for _, frame := range frames {
		_ = binary.Write(&out, binary.LittleEndian, int16(len(frame)))
		out.Write(frame)
	}
	return out.Bytes()
}

func wavDataLength(data []byte) int {
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[36:40]) != "data" {
		return 0
	}
	return int(binary.LittleEndian.Uint32(data[40:44]))
}
```

Add table-driven tests for `inspectSilkFrames`:

- valid `#!SILK_V3` and `0x02 + #!SILK_V3` inputs with one frame;
- one complete frame plus a valid negative footer: valid, `PrefixEnd` excludes the footer, and `DroppedBytes == 0`;
- footer-only input: invalid because it has no complete frame;
- one dangling frame-length byte after a complete frame → `io.ErrUnexpectedEOF` plus recoverable prefix;
- a positive frame length with partial payload → `io.ErrUnexpectedEOF` plus recoverable prefix;
- positive frame length with zero payload and no earlier frame → `io.ErrUnexpectedEOF` but zero recoverable frames;
- zero-length frame → non-EOF validation error;
- 1025-byte declared frame → non-EOF validation error;
- footer followed by a byte → non-EOF validation error;
- header only → non-EOF validation error.

Add a real codec regression test:

```go
func TestRecoverTruncatedSilkToWAVKeepsCompleteFrames(t *testing.T) {
	pcm := make([]byte, voiceSampleRate/5*2)
	full, err := silk.Encode(bytes.NewReader(pcm), silk.SampleRate(voiceSampleRate), silk.Stx(true))
	if err != nil {
		t.Fatalf("encode test silk: %v", err)
	}
	truncated := append([]byte(nil), full[:len(full)-1]...)
	if _, err := silkToWAV(truncated); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected strict truncated decode to match unexpected EOF, got %v", err)
	}

	wav, sourceIndex, sourceBytes, droppedBytes, err := recoverTruncatedSilkToWAV(truncated)
	if err != nil {
		t.Fatalf("recover truncated silk: %v", err)
	}
	if sourceIndex != 0 || sourceBytes != len(truncated) || droppedBytes <= 0 {
		t.Fatalf("unexpected recovery source=%d source_bytes=%d dropped=%d", sourceIndex, sourceBytes, droppedBytes)
	}
	if wavDataLength(wav) <= 0 {
		t.Fatalf("expected non-empty playable WAV, len=%d", len(wav))
	}
}
```

Add `TestSilkToWAVRejectsHeaderOnlyInput`, asserting a header-only payload returns an error and never produces a 44-byte empty WAV. Also call `silkToWAV` with zero-length-frame, oversized-frame, and footer-trailer fixtures and assert each is rejected even if the dependency decoder alone might return success.

Add `TestRecoverTruncatedSilkToWAVRejectsNoCompleteFrame`, passing a header plus a declared positive frame with no payload. Assert recovery returns a non-nil `no recoverable SILK frames` error, never a nil error with empty WAV.

Add candidate ordering tests. Introduce a pure `sortSilkRecoveryCandidates` helper and construct candidate metadata directly to assert order by frame count, then payload bytes, then later source index. Add one recovery integration assertion passing the same recoverable real fixture twice and require the returned `sourceIndex` to be `1`, proving exact ties prefer retry data.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
go test ./internal/wechat -run 'Test(InspectSilkFrames|RecoverTruncatedSilkToWAV|SilkToWAVRejectsHeaderOnlyInput)' -count=1 -v
```

Expected: build/test failure because `inspectSilkFrames` and `recoverTruncatedSilkToWAV` do not exist, proving the recovery behavior is absent.

- [ ] **Step 3: Implement the structural inspector**

In `internal/wechat/voice.go`, add `errors` and `sort` imports, then define:

```go
const maxSilkFramePayload = 1024

type silkFrameInspection struct {
	PrefixEnd    int
	FrameCount   int
	PayloadBytes int
	DroppedBytes int
}

func inspectSilkFrames(data []byte) (silkFrameInspection, error)
```

Implementation rules:

1. Accept optional `0x02`, then require exact `#!SILK_V3`.
2. Walk signed little-endian 16-bit frame lengths.
3. Reject zero or lengths above `maxSilkFramePayload`.
4. Accept a negative footer only at exact end of data.
5. On one dangling length byte or partial positive frame payload, return inspection metrics plus an error wrapping `io.ErrUnexpectedEOF`.
6. `PrefixEnd` points before the incomplete length/frame and excludes a valid footer. `DroppedBytes` is nonzero only for actual trailing truncation; a valid footer is not counted as dropped.
7. Require at least one complete frame for a structurally valid complete payload; footer-only and header-only inputs are invalid.

Use explicit errors such as:

```go
return info, fmt.Errorf("truncated SILK frame payload: %w", io.ErrUnexpectedEOF)
```

- [ ] **Step 4: Validate before decoding and reject empty PCM**

Update `silkToWAV`:

```go
func silkToWAV(data []byte) ([]byte, error) {
	if _, err := inspectSilkFrames(data); err != nil {
		return nil, err
	}
	pcm, err := silk.Decode(bytes.NewReader(data), silk.WithSampleRate(voiceSampleRate))
	if err != nil {
		return nil, err
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("decoded SILK audio contains no PCM samples")
	}
	return pcmToWAV(pcm, voiceSampleRate, 1, 16), nil
}
```

- [ ] **Step 5: Implement ranked recovery**

Add:

```go
type silkRecoveryCandidate struct {
	Data        []byte
	SourceIndex int
	Inspection  silkFrameInspection
}

func sortSilkRecoveryCandidates(candidates []silkRecoveryCandidate)
func recoverTruncatedSilkToWAV(inputs ...[]byte) (wav []byte, sourceIndex, sourceBytes, droppedBytes int, err error)
```

For each input, call `inspectSilkFrames`. Keep only results whose error matches `io.ErrUnexpectedEOF`, `FrameCount > 0`, and `PrefixEnd` is after the header. Rank candidates by:

1. larger `FrameCount`;
2. larger `PayloadBytes`;
3. later input index (retry candidate) on exact tie.

Use `sort.SliceStable`. For each ranked candidate, call `silkToWAV(data[:PrefixEnd])`; return the first WAV whose data chunk is non-empty. Return the selected input index, `sourceBytes=len(original)`, and `droppedBytes=len(original)-PrefixEnd`.

If filtering produces zero candidates, return an explicit non-nil `no recoverable SILK frames` error before calling `errors.Join`. If candidates exist but none decode, join their non-nil decode errors; never allow an empty `errors.Join()` result to represent success.

- [ ] **Step 6: Run Task 1 tests and commit**

Run:

```bash
gofmt -w internal/wechat/voice.go internal/wechat/voice_test.go
go test ./internal/wechat -run 'Test(InspectSilkFrames|RecoverTruncatedSilkToWAV|SilkToWAVRejectsHeaderOnlyInput)' -count=1 -v
go test -race ./internal/wechat -run 'Test(InspectSilkFrames|RecoverTruncatedSilkToWAV|SilkToWAVRejectsHeaderOnlyInput)' -count=10
git diff --check
```

Expected: all focused tests pass with no race or formatting errors.

Commit:

```bash
git add internal/wechat/voice.go internal/wechat/voice_test.go
git commit -m "fix: recover complete frames from truncated SILK voice"
```

### Task 2: Retry Truncated Voice Downloads and Attach Playable WAV

**Files:**
- Modify: `internal/wechat/convert.go:202-229`
- Modify: `internal/wechat/voice_test.go`
- Test: `internal/wechat/voice_test.go`

- [ ] **Step 1: Add failing webhook retry tests**

Add helpers in `internal/wechat/voice_test.go` that return successful `/Tools/DownloadVoice` envelopes without calling `t.Fatalf` inside the HTTP handler:

```go
func writeVoiceResponse(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"Code": 0, "Success": true,
		"Data": map[string]any{"data": map[string]any{"buffer": base64.StdEncoding.EncodeToString(data)}},
	})
}
```

Use `atomic.Int32` for request counts and a buffered `handlerErr` channel sized for at least two requests. Refactor the existing healthy voice test to use the same pattern: do not call `t.Fatalf` or mutate an ordinary `bool` inside any HTTP handler; send an error to `handlerErr`, write an HTTP error response, and return immediately.

Add `TestHandleWebhookVoiceRetriesTruncatedDownload`:

1. Encode a real multi-frame SILK fixture.
2. Return `full[:len(full)-1]` on request 1 and full data on request 2.
3. Call `HandleWebhook` with the existing group voice payload shape.
4. Assert exactly two requests, no handler error, `MsgAudio`, MIME `audio/wav`, and `wavDataLength(...) > 0`.

Add `TestHandleWebhookVoiceRecoversWhenRetryRemainsTruncated`:

1. Return truncated data on both requests.
2. Assert exactly two requests.
3. Assert the emitted event has non-empty playable WAV media rather than empty media/`[voice]` fallback.

Add `TestHandleWebhookVoiceDoesNotRetryNonEOFDecodeError`, returning structurally invalid non-EOF data and asserting exactly one request plus empty media fallback.

Add `TestHandleWebhookVoiceRecoversFirstPayloadWhenRetryDownloadFails`, returning recoverably truncated SILK first and an HTTP/API failure second; assert two requests and playable recovered WAV from the first payload.

Update the existing healthy voice test to assert exactly one download request, proving no retry on valid audio.

- [ ] **Step 2: Run webhook tests and verify RED**

Run:

```bash
go test ./internal/wechat -run 'TestHandleWebhookVoice(RetriesTruncatedDownload|RecoversWhenRetryRemainsTruncated)' -count=1 -v
```

Expected: both tests fail because `attachVoiceMedia` performs no retry or recovery and leaves media data empty.

- [ ] **Step 3: Implement retry and recovery orchestration**

Add `errors` and `io` imports to `internal/wechat/convert.go`.

Update `attachVoiceMedia` while retaining its existing nil guards and final media assignment:

```go
data, err := c.downloadVoice(ctx, req)
if err != nil {
	// existing download failure log and return
}

wav, decodeErr := silkToWAV(data)
if decodeErr != nil && errors.Is(decodeErr, io.ErrUnexpectedEOF) {
	log.Printf("retrying truncated WeChat voice download id=%s msg_id=%d from=%q bytes=%d: %v", base.ID, req.MsgID, req.FromUserName, len(data), decodeErr)
	candidates := [][]byte{data}
	errs := []error{fmt.Errorf("initial decode: %w", decodeErr)}

	retryData, retryErr := c.downloadVoice(ctx, req)
	if retryErr != nil {
		errs = append(errs, fmt.Errorf("retry download: %w", retryErr))
	} else {
		retryWAV, retryDecodeErr := silkToWAV(retryData)
		if retryDecodeErr == nil {
			wav = retryWAV
			decodeErr = nil
		} else {
			errs = append(errs, fmt.Errorf("retry decode: %w", retryDecodeErr))
			if errors.Is(retryDecodeErr, io.ErrUnexpectedEOF) {
				candidates = append(candidates, retryData)
			}
		}
	}

	if decodeErr != nil {
		recoveredWAV, sourceIndex, sourceBytes, droppedBytes, recoverErr := recoverTruncatedSilkToWAV(candidates...)
		if recoverErr == nil {
			wav = recoveredWAV
			decodeErr = nil
			log.Printf("recovered truncated WeChat voice id=%s msg_id=%d from=%q source_attempt=%d source_bytes=%d dropped_bytes=%d", base.ID, req.MsgID, req.FromUserName, sourceIndex+1, sourceBytes, droppedBytes)
		} else {
			errs = append(errs, fmt.Errorf("trailing frame recovery: %w", recoverErr))
			decodeErr = errors.Join(errs...)
		}
	}
}
if decodeErr != nil {
	log.Printf("failed to decode WeChat voice %s msg_id=%d from=%q bytes=%d: %v", base.ID, req.MsgID, req.FromUserName, len(data), decodeErr)
	return
}
```

When retry data is received, include `retry_bytes=len(retryData)` in the retry decode success/failure diagnostic so both first and retry payload lengths are observable. Then preserve the current WAV metadata assignment. Do not retry download errors that occur before any decode, and do not retry non-EOF decode errors.

- [ ] **Step 4: Run focused GREEN tests**

Run:

```bash
gofmt -w internal/wechat/convert.go internal/wechat/voice_test.go
go test ./internal/wechat -run 'TestHandleWebhookVoice' -count=1 -v
go test -race ./internal/wechat -run 'TestHandleWebhookVoice' -count=20
```

Expected: healthy, retry-success, double-truncation recovery, non-EOF no-retry, and retry-download-failure recovery tests all pass; healthy and non-EOF tests report one request, while truncation paths report two.

- [ ] **Step 5: Run full verification**

Run:

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
git diff --check
```

Expected: every package passes, race detector reports no races, vet and diff checks are clean.

- [ ] **Step 6: Review scope and commit**

Run:

```bash
git diff HEAD -- internal/wechat/convert.go internal/wechat/voice.go internal/wechat/voice_test.go
```

Confirm no changes to outbound audio, Matrix upload, dependencies, or Docker files.

Commit:

```bash
git add internal/wechat/convert.go internal/wechat/voice_test.go
git commit -m "fix: retry truncated WeChat voice downloads"
```

## Acceptance Criteria

- A healthy inbound voice uses one download request and produces non-empty WAV audio.
- A first truncated download triggers exactly one retry.
- A successful retry produces complete WAV audio.
- Two truncated downloads salvage the best complete-frame prefix and produce a non-empty WAV data chunk.
- Invalid headers, zero/oversized frames, footer trailers, and header-only payloads are rejected.
- Non-EOF decode failures are not retried or silently trimmed.
- Matrix receives media bytes and therefore emits `m.audio`, not `[voice]` text.
- Full tests, race tests, vet, formatting, and diff checks pass.

## Technical Vocabulary / 技术词汇

- **structural inspector** — 结构检查器
- **complete-frame prefix** — 完整帧前缀
- **payload bytes** — 负载字节数
- **bounded recovery** — 有界恢复
- **retry orchestration** — 重试编排
- **non-empty PCM** — 非空 PCM
- **data chunk** — 数据块
- **trailing truncation** — 尾部截断
- **candidate ranking** — 候选排序
- **race detector** — 竞态检测器

## 中文审查清单

- [ ] 是否先验证 SILK 结构，再调用依赖解码器？
- [ ] 是否拒绝空 PCM、零/超长帧和 footer 后残留？
- [ ] 是否只在 `io.ErrUnexpectedEOF` 时重试一次？
- [ ] 是否按完整帧数、帧 payload 字节数、retry 平局优先排序？
- [ ] 是否确认健康语音只有一次下载？
- [ ] 是否确认恢复 WAV 的 data chunk 非空？
- [ ] 是否运行 focused RED/GREEN、全测、race、vet 和格式检查？
