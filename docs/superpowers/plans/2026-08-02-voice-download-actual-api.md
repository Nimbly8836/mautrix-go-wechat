# Actual WeChat Voice Download Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make inbound voice downloads use one complete `/Tools/DownloadVoice` request per attempt with exactly the five fields defined by the actual API, eliminating unsupported offset chunking that can shorten or corrupt audio.

**Architecture:** Keep metadata extraction in `convert.go`, wire types in `models.go`, and the common envelope transport unchanged. Replace offset/chunk assembly with one bounded typed Base64 response decode, require `Bufid`, preserve strict decoded-length validation, and retain the existing one identical full-request retry only after SILK decoding reports `io.ErrUnexpectedEOF`.

**Tech Stack:** Go, `net/http`, `encoding/json`, `encoding/base64`, `httptest`, existing WeChat transport/envelope code, existing SILK decoder tests.

**Design:** `docs/superpowers/specs/2026-08-02-voice-download-actual-api-design.md`

## 中文审查摘要

**目标：** 将入站语音下载严格改为实际接口的五字段完整请求，移除无依据的 `Offset`/60 KiB 分片，解决长语音可能截断、重复或拼接损坏的问题。

**实施方式：** 先用大于 60 KiB 的真实 HTTP 回归测试确认现有实现失败，再在同一个可编译任务中同步替换请求模型、下载逻辑和旧分片测试；随后单独验证 XML `length` 与 `voicelength` 的语义、47 MiB 单响应上限、取消路径和来源字段回退。

**影响范围：** 新增一个聚焦测试文件，修改 `models.go`、`convert.go`、`voice_test.go`，并给四份旧文档增加 superseded 标记。不修改通用 transport、SILK 解码器、Matrix 上传和出站语音。

**主要风险：** Task 1 必须一次性清理所有旧分片符号，不能在包无法编译时提交；`voicelength` 不再用于下载字节数，但现有 Matrix duration 的 `length` 兼容回退会保留；47 MiB 上限是为适配现有 64 MiB Base64 JSON 响应限制。

**待确认事项：** 计划默认使用 subagent-driven development 连续执行。真正需要关注的是请求契约、重试边界、47 MiB 上限以及是否完整保留现有 SILK 解码行为。

---

## File Structure

- Create `internal/wechat/voice_download_api_test.go`: focused request-contract, response-validation, cancellation, and source-fallback tests.
- Modify `internal/wechat/models.go`: exact five-field request and minimal typed response.
- Modify `internal/wechat/convert.go`: one complete request per attempt, bounded Base64 decode, required metadata, 47 MiB raw limit, and correct byte-length extraction.
- Modify `internal/wechat/voice_test.go`: remove obsolete chunk tests/helpers and update existing webhook request validation.
- Modify four historical documents under `docs/superpowers/`: mark unsupported acquisition chunking as superseded while preserving SILK decoder decisions.

The implementation does not modify `internal/wechat/transport.go`, `internal/wechat/voice.go`, `internal/wechat/voice_decode.go`, Matrix upload code, or outbound voice sending.

---

### Task 1: Replace Chunk Downloading with the Actual API Contract

**Files:**
- Create: `internal/wechat/voice_download_api_test.go`
- Modify: `internal/wechat/models.go:190-213`
- Modify: `internal/wechat/convert.go:27-36,274-398`
- Modify: `internal/wechat/voice_test.go:1-815` (imports, obsolete download tests, cancellation helpers, request helper)

- [ ] **Step 1: Create RED request-contract tests without removing current tests**

Create `internal/wechat/voice_download_api_test.go` with the following initial tests and helpers. They compile against current production and fail for the intended reasons.

```go
package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDownloadVoiceUsesSingleActualAPIRequest(t *testing.T) {
	raw := make([]byte, 70*1024)
	for i := range raw {
		raw[i] = byte(i)
	}
	var requests atomic.Int32
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		want := voiceMessageDownloadReq{
			Wxid: "wxid_self", FromUserName: "wxid_voice_sender",
			MsgID: 11, Length: int64(len(raw)), BufID: "buf-1",
		}
		if err := validateActualVoiceRequest(r, want); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := writeVoiceResponse(w, raw); err != nil {
			handlerErr <- err
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	got, err := c.downloadVoice(context.Background(), testVoiceDownloadReq(int64(len(raw))))
	if err != nil {
		t.Fatalf("downloadVoice: %v", err)
	}
	assertNoVoiceHandlerError(t, handlerErr)
	if requests.Load() != 1 {
		t.Fatalf("voice download requests = %d, want 1", requests.Load())
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("downloaded voice differs: got %d bytes, want %d", len(got), len(raw))
	}
}

func TestDownloadVoiceRejectsMissingBufidWithoutRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = writeVoiceResponse(w, make([]byte, 80))
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{
		FromUserName: "wxid_voice_sender", MsgID: 11, Length: 80,
	})
	if err == nil || !strings.Contains(err.Error(), "Bufid") {
		t.Fatalf("downloadVoice error = %v, want missing Bufid", err)
	}
	if data != nil || requests.Load() != 0 {
		t.Fatalf("downloadVoice data=%d requests=%d, want nil and zero requests", len(data), requests.Load())
	}
}

func testVoiceDownloadReq(length int64) voiceMessageDownloadReq {
	return voiceMessageDownloadReq{
		FromUserName: "wxid_voice_sender",
		MsgID:        11,
		Length:       length,
		BufID:        "buf-1",
	}
}

func validateActualVoiceRequest(r *http.Request, want voiceMessageDownloadReq) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("method = %s, want POST", r.Method)
	}
	if r.URL.Path != "/Tools/DownloadVoice" {
		return fmt.Errorf("path = %s, want /Tools/DownloadVoice", r.URL.Path)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		return fmt.Errorf("Content-Type = %q, want application/json", got)
	}
	if r.URL.RawQuery != "" || r.Header.Get("Range") != "" {
		return fmt.Errorf("unexpected range query/header: query=%q range=%q", r.URL.RawQuery, r.Header.Get("Range"))
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	if len(body) != 5 ||
		body["Bufid"] != want.BufID ||
		body["FromUserName"] != want.FromUserName ||
		body["Length"] != float64(want.Length) ||
		body["MsgId"] != float64(want.MsgID) ||
		body["Wxid"] != want.Wxid {
		return fmt.Errorf("request body = %#v, want exactly %#v", body, want)
	}
	return nil
}
```

- [ ] **Step 2: Run both tests and verify RED**

Run:

```bash
go test ./internal/wechat -run '^(TestDownloadVoiceUsesSingleActualAPIRequest|TestDownloadVoiceRejectsMissingBufidWithoutRequest)$' -count=1 -v
```

Expected:

- `TestDownloadVoiceUsesSingleActualAPIRequest` fails on the first request because the body contains undocumented `Offset` and a chunk-sized `Length`.
- `TestDownloadVoiceRejectsMissingBufidWithoutRequest` fails because current production sends a network request instead of rejecting missing `Bufid`.

- [ ] **Step 3: Add the complete final single-response test suite**

Append the following tests to `internal/wechat/voice_download_api_test.go` before changing production. These tests replace the chunk-specific block later in this task. At the same time, add `encoding/base64`, `errors`, `io`, and `github.com/leat/mautrix-go-wechat/internal/remoteapi` to that file's import block; every new import is used by the appended code.

```go
func TestDownloadVoiceResponseValidation(t *testing.T) {
	for _, tt := range []struct {
		name       string
		length     int64
		buffer     string
		want       []byte
		wantErrors []string
	}{
		{name: "whitespace normalized", length: 1, buffer: " \n\t" + base64.StdEncoding.EncodeToString([]byte{1}), want: []byte{1}},
		{name: "empty buffer", length: 1, buffer: "", wantErrors: []string{"empty data.buffer"}},
		{name: "malformed Base64", length: 1, buffer: "%%%", wantErrors: []string{"decode WeChat voice"}},
		{name: "encoded size exceeds bound", length: 1, buffer: strings.Repeat("A", base64.StdEncoding.EncodedLen(1)+3), wantErrors: []string{"Base64", "maximum"}},
		{name: "decoded shorter", length: 2, buffer: base64.StdEncoding.EncodeToString([]byte{1}), wantErrors: []string{"declared length"}},
		{name: "decoded longer", length: 1, buffer: base64.StdEncoding.EncodeToString([]byte{1, 2}), wantErrors: []string{"declared length"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_ = writeVoiceBufferResponse(w, tt.buffer)
			}))
			defer server.Close()

			c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			data, err := c.downloadVoice(context.Background(), testVoiceDownloadReq(tt.length))
			if len(tt.wantErrors) == 0 {
				if err != nil || !bytes.Equal(data, tt.want) {
					t.Fatalf("downloadVoice data=%v error=%v, want data=%v", data, err, tt.want)
				}
			} else {
				if err == nil {
					t.Fatal("downloadVoice returned nil error")
				}
				for _, wantError := range tt.wantErrors {
					if !strings.Contains(err.Error(), wantError) {
						t.Fatalf("downloadVoice error = %v, want %q", err, wantError)
					}
				}
				if data != nil {
					t.Fatalf("downloadVoice returned %d bytes on error", len(data))
				}
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want 1", requests.Load())
			}
		})
	}
}

func TestDownloadVoiceHonorsPreCanceledContext(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = writeVoiceResponse(w, []byte{1})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(ctx, testVoiceDownloadReq(1))
	if !errors.Is(err, context.Canceled) || data != nil || requests.Load() != 0 {
		t.Fatalf("data=%v error=%v requests=%d, want nil/context.Canceled/0", data, err, requests.Load())
	}
}

func TestDownloadVoiceReturnsCanceledInFlight(t *testing.T) {
	started := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	type result struct {
		data []byte
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		data, err := c.downloadVoice(ctx, testVoiceDownloadReq(1))
		resultCh <- result{data: data, err: err}
	}()
	<-started
	cancel()
	got := <-resultCh
	if !errors.Is(got.err, context.Canceled) || got.data != nil || requests.Load() != 1 {
		t.Fatalf("data=%v error=%v requests=%d, want nil/context.Canceled/1", got.data, got.err, requests.Load())
	}
}

func TestDownloadVoiceReturnsCanceledAfterResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = writeVoiceResponse(w, []byte{1})
	}))
	defer server.Close()

	transport := newTransport(server.URL)
	transport.hc.Transport = cancelVoiceAtEOFTransport{base: http.DefaultTransport, cancel: cancel}
	c := &Client{t: transport, wxid: "wxid_self"}
	data, err := c.downloadVoice(ctx, testVoiceDownloadReq(1))
	if !errors.Is(err, context.Canceled) || data != nil {
		t.Fatalf("data=%v error=%v, want nil/context.Canceled", data, err)
	}
}

func TestDownloadVoiceReturnsAPIFailureWithoutData(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"Code":42,"Success":false,"Message":"voice unavailable"}`))
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(context.Background(), testVoiceDownloadReq(1))
	if err == nil || !strings.Contains(err.Error(), "voice unavailable") || data != nil || requests.Load() != 1 {
		t.Fatalf("data=%v error=%v requests=%d, want API error/nil/1", data, err, requests.Load())
	}
}

func TestConvertLocalVoiceUsesSourceAndMessageIDFallbacks(t *testing.T) {
	voiceData := testSilkContainer(true, []byte{0x00, 0x00})
	for _, tt := range []struct {
		name       string
		xmlFrom    string
		msgID      int64
		newMsgID   int64
		sender     string
		wantFrom   string
		wantMsgID  int64
	}{
		{name: "sender fallback", msgID: 11, newMsgID: 22, sender: "wxid_local_sender", wantFrom: "wxid_local_sender", wantMsgID: 11},
		{name: "new message ID fallback", xmlFrom: ` fromusername="wxid_xml_sender"`, newMsgID: 22, sender: "wxid_local_sender", wantFrom: "wxid_xml_sender", wantMsgID: 22},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			handlerErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				want := voiceMessageDownloadReq{Wxid: "wxid_self", FromUserName: tt.wantFrom, MsgID: tt.wantMsgID, Length: int64(len(voiceData)), BufID: "buf-1"}
				if err := validateActualVoiceRequest(r, want); err != nil {
					handlerErr <- err
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				_ = writeVoiceResponse(w, voiceData)
			}))
			defer server.Close()

			xml := fmt.Sprintf(`<msg><voicemsg length="%d" bufid="buf-1" voicelength="1000"%s /></msg>`, len(voiceData), tt.xmlFrom)
			c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			_, ok := c.convertLocal(context.Background(), localMessage{
				MsgID: tt.msgID, NewMsgID: tt.newMsgID, MsgType: wxVoice, ContentText: xml,
				Talker: "45170834716@chatroom", SenderUserName: tt.sender, CreateTime: 1710000000,
			})
			assertNoVoiceHandlerError(t, handlerErr)
			if !ok || requests.Load() != 1 {
				t.Fatalf("ok=%v requests=%d, want true/1", ok, requests.Load())
			}
		})
	}
}

func writeVoiceBufferResponse(w http.ResponseWriter, buffer string) error {
	return json.NewEncoder(w).Encode(map[string]any{
		"Code": 0, "Success": true,
		"Data": map[string]any{"data": map[string]any{"buffer": buffer}},
	})
}

type cancelVoiceAtEOFTransport struct {
	base   http.RoundTripper
	cancel context.CancelFunc
}

func (t cancelVoiceAtEOFTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		resp.Body = &cancelVoiceAtEOFBody{ReadCloser: resp.Body, cancel: t.cancel}
	}
	return resp, err
}

type cancelVoiceAtEOFBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelVoiceAtEOFBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		b.cancel()
	}
	return n, err
}

var _ = remoteapi.MsgAudio
```

The final `var _` keeps the imported package available until Task 2 adds its metadata test; Task 2 removes this line when it uses `remoteapi` directly.

- [ ] **Step 4: Replace production wire models and downloader**

In `internal/wechat/models.go`, replace the voice request/response types with:

```go
type voiceMessageDownloadReq struct {
	Wxid         string `json:"Wxid"`
	FromUserName string `json:"FromUserName"`
	MsgID        int64  `json:"MsgId"`
	Length       int64  `json:"Length"`
	BufID        string `json:"Bufid"`
}

type voiceDownloadData struct {
	Buffer string `json:"buffer"`
}

type voiceDownloadResp struct {
	Data voiceDownloadData `json:"data"`
}
```

In `internal/wechat/convert.go`, delete `voiceDownloadChunkSize`, keep `maxVoiceDownloadBytes` at 64 MiB until Task 2's boundary test is RED, retain `normalizeVoiceBase64Fragment`, delete `decodeVoiceBase64Fragments`, and replace `downloadVoice` with:

```go
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
	maxEncodedChars := int64(base64.StdEncoding.EncodedLen(int(req.Length))) + 2
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
```

Do not modify `attachVoiceMedia`; its existing `io.ErrUnexpectedEOF` branch remains the sole source of the one identical retry.

- [ ] **Step 5: Remove obsolete chunk tests and make existing tests use the exact request helper**

Delete these complete functions/types from `internal/wechat/voice_test.go` because the new file replaces their valid behaviors:

```text
TestDownloadVoiceChunksAssemblesRawBytes
TestDownloadVoiceRejectsDecodedDataLongerThanDeclaredLength
TestDownloadVoiceRejectsBase64OverDeclaredSizeDuringCollection
TestDownloadVoiceRejectsDecodedDataShorterThanDeclaredLength
TestDownloadVoiceRejectsShortChunkMetadata
TestDownloadVoiceStopsBeforeNextChunkWhenContextCanceledAfterResponse
TestDownloadVoiceReturnsCanceledAfterFinalChunkBeforeAssembly
cancelAtEOFTransport
cancelAtEOFBody
TestDownloadVoiceChunksReturnsNoPartialDataOnSecondChunkAPIFailure
TestDownloadVoiceChunksRejectsEmptyBuffer
TestDownloadVoiceChunksRejectsMalformedBase64
TestDownloadVoiceChunksHonorsPreCanceledContext
TestDownloadVoiceChunksAllowsNonShortMetadataMismatchWithoutReordering
TestDecodeVoiceBase64Fragments
writeVoiceChunkResponse
writeVoiceChunkResponseMetadata
```

Update both retained maximum-length tests to include `BufID: "buf-1"`; do not change their current 64 MiB boundary until Task 2.

Replace the old helper pair `validateVoiceDownloadRequest` / `validateVoiceDownloadRequestLength` with:

```go
func validateVoiceDownloadRequest(r *http.Request, wantLength int64) error {
	return validateActualVoiceRequest(r, voiceMessageDownloadReq{
		Wxid:         "wxid_self",
		FromUserName: "wxid_voice_sender",
		MsgID:        11,
		Length:       wantLength,
		BufID:        "buf-1",
	})
}
```

Replace every call to `validateVoiceDownloadRequestLength` with `validateVoiceDownloadRequest`. Remove now-unused imports from `voice_test.go` with `goimports` if available or `gofmt` plus compiler feedback.

- [ ] **Step 6: Run focused tests, then the entire package, and verify GREEN**

Run:

```bash
gofmt -w internal/wechat/models.go internal/wechat/convert.go internal/wechat/voice_test.go internal/wechat/voice_download_api_test.go
go test ./internal/wechat -run '^(TestDownloadVoice|TestConvertLocalVoice|TestHandleWebhookVoice)' -count=1 -v
go test ./internal/wechat -count=1
```

Expected: both commands PASS. The package must compile as a whole; no intermediate commit is allowed while stale chunk symbols remain.

- [ ] **Step 7: Verify obsolete protocol symbols are gone**

Run:

```bash
rg -n 'voiceDownloadChunkSize|decodeVoiceBase64Fragments|writeVoiceChunkResponse|Offset.*voice|ShortChunk|SecondChunk|data\.iLen' internal/wechat
```

Expected: no voice-download matches. Video `Section`/`StartPos` behavior remains untouched.

- [ ] **Step 8: Commit the compilable protocol correction**

```bash
git add internal/wechat/models.go internal/wechat/convert.go internal/wechat/voice_test.go internal/wechat/voice_download_api_test.go
git commit -m "fix: use actual voice download protocol"
```

---

### Task 2: Enforce XML Byte Length and the Single-Response Size Boundary

**Files:**
- Modify: `internal/wechat/convert.go:27-36,918-931`
- Modify: `internal/wechat/voice_test.go` (retained maximum tests)
- Modify: `internal/wechat/voice_download_api_test.go`

- [ ] **Step 1: Add RED XML and 47 MiB boundary tests**

Append to `internal/wechat/voice_download_api_test.go` and remove the temporary `var _ = remoteapi.MsgAudio` line:

```go
func TestApplyMessageTypeVoiceKeepsDownloadLengthDistinct(t *testing.T) {
	for _, tt := range []struct {
		name         string
		xml          string
		wantLength   string
		wantDuration string
	}{
		{name: "both", xml: `<msg><voicemsg length="123" voicelength="456" bufid="buf-1" /></msg>`, wantLength: "123", wantDuration: "456"},
		{name: "duration is not byte length", xml: `<msg><voicemsg voicelength="456" bufid="buf-1" /></msg>`, wantDuration: "456"},
		{name: "legacy duration fallback remains", xml: `<msg><voicemsg length="123" bufid="buf-1" /></msg>`, wantLength: "123", wantDuration: "123"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := remoteapi.InboundMessage{ID: "voice-1", Text: tt.xml, Extra: map[string]any{}}
			applyMessageType(&base, wxVoice, tt.xml, "")
			if got := extraString(base.Extra, "voice_length"); got != tt.wantLength {
				t.Fatalf("voice_length = %q, want %q", got, tt.wantLength)
			}
			if got := extraString(base.Extra, "duration"); got != tt.wantDuration {
				t.Fatalf("duration = %q, want %q", got, tt.wantDuration)
			}
		})
	}
}

func TestConvertLocalVoiceWithoutByteLengthDoesNotDownload(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = writeVoiceResponse(w, make([]byte, 1000))
	}))
	defer server.Close()

	xml := `<msg><voicemsg bufid="buf-1" voicelength="1000" fromusername="wxid_voice_sender" /></msg>`
	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	ev, ok := c.convertLocal(context.Background(), localMessage{
		MsgID: 11, NewMsgID: 22, MsgType: wxVoice, ContentText: xml,
		Talker: "45170834716@chatroom", SenderUserName: "wxid_local_sender", CreateTime: 1710000000,
	})
	if !ok || ev.Message == nil || ev.Message.Media == nil {
		t.Fatalf("convertLocal returned ok=%v event=%#v", ok, ev)
	}
	if requests.Load() != 0 || len(ev.Message.Media.Data) != 0 {
		t.Fatalf("requests=%d media_bytes=%d, want 0/0", requests.Load(), len(ev.Message.Media.Data))
	}
}
```

Before production changes, update `TestDownloadVoiceRejectsLengthAboveMaximumWithoutRequest` in `voice_test.go` to use `Length: 47<<20 + 1` and `BufID: "buf-1"`. Keep its zero-request assertion. The current 64 MiB maximum allows that request to reach the server, which makes the test RED.

- [ ] **Step 2: Run all three behaviors and verify RED**

Run:

```bash
go test ./internal/wechat -run '^(TestApplyMessageTypeVoiceKeepsDownloadLengthDistinct|TestConvertLocalVoiceWithoutByteLengthDoesNotDownload|TestDownloadVoiceRejectsLengthAboveMaximumWithoutRequest)$' -count=1 -v
```

Expected:

- duration-only XML incorrectly populates `voice_length`, so both XML tests fail;
- `47 MiB + 1 byte` reaches the server under the old 64 MiB maximum, so the boundary test fails its zero-request assertion.

- [ ] **Step 3: Remove only the invalid byte-length fallback**

In the `wxVoice` branch of `applyMessageType`, change only the download byte-length extraction:

```go
if length := xmlAttr(base.Text, "length"); length != "" {
	base.Extra["voice_length"] = length
}
```

Keep the existing Matrix duration compatibility expression unchanged:

```go
if voiceTime := firstNonEmpty(xmlAttr(base.Text, "voicelength"), xmlAttr(base.Text, "length")); voiceTime != "" {
	base.Extra["duration"] = voiceTime
}
```

This prevents `voicelength` from becoming request bytes without changing Matrix-visible legacy duration behavior.

- [ ] **Step 4: Set the raw-byte maximum to 47 MiB**

Replace the voice limit with:

```go
const (
	// Base64 expansion plus JSON/API metadata must fit transport's 64 MiB response limit.
	maxVoiceDownloadBytes int64 = 47 << 20
)
```

After observing RED, change the fixed test input from `47<<20 + 1` to `maxVoiceDownloadBytes + 1`. Keep `BufID: "buf-1"` in both maximum and `math.MaxInt64` tests.

- [ ] **Step 5: Run focused and full package tests and verify GREEN**

Run:

```bash
gofmt -w internal/wechat/convert.go internal/wechat/voice_test.go internal/wechat/voice_download_api_test.go
go test ./internal/wechat -run '^(TestApplyMessageTypeVoiceKeepsDownloadLengthDistinct|TestConvertLocalVoiceWithoutByteLengthDoesNotDownload|TestDownloadVoiceRejectsLengthAboveMaximumWithoutRequest|TestDownloadVoiceRejectsMaxInt64LengthWithoutRequest)$' -count=1 -v
go test ./internal/wechat -count=1
```

Expected: all PASS; duration-only XML sends no request, the legacy duration fallback remains, and oversized requests are rejected before HTTP.

- [ ] **Step 6: Commit metadata and boundary validation**

```bash
git add internal/wechat/convert.go internal/wechat/voice_test.go internal/wechat/voice_download_api_test.go
git commit -m "fix: validate voice download metadata"
```

---

### Task 3: Mark Unsupported Chunk Documentation as Superseded

**Files:**
- Modify: `docs/superpowers/specs/2026-07-14-voice-download-chunks-design.md:1-3`
- Modify: `docs/superpowers/plans/2026-07-14-voice-download-chunks.md:1-3`
- Modify: `docs/superpowers/specs/2026-07-14-voice-multiframe-decode-design.md:1-12`
- Modify: `docs/superpowers/plans/2026-07-14-voice-multiframe-decode.md:1-15`

- [ ] **Step 1: Add full supersession banners to both chunk documents**

Immediately below the top-level heading in both `voice-download-chunks` documents, add:

```markdown
> **Superseded / 已被替代（2026-08-02）：** The `Offset`/60 KiB chunk protocol in this document does not match this repository's `swagger.json` or the user's actual TypeScript client. Use `docs/superpowers/specs/2026-08-02-voice-download-actual-api-design.md` and `docs/superpowers/plans/2026-08-02-voice-download-actual-api.md`. This file is retained only as historical context.
```

- [ ] **Step 2: Add scoped supersession banners to both multiframe documents**

Immediately below the top-level heading in both `voice-multiframe-decode` documents, add:

```markdown
> **Partial supersession / 部分内容已被替代（2026-08-02）：** Statements that preserve `/Tools/DownloadVoice` offset chunking, Base64 fragment assembly, or chunk-specific tests are superseded by `docs/superpowers/specs/2026-08-02-voice-download-actual-api-design.md`. The SILK packet parsing and multi-internal-frame decoder design in this document remains authoritative.
```

Do not rewrite historical task text; the banner must preserve the decoder rationale while overriding acquisition assumptions.

- [ ] **Step 3: Verify all four documents point to the new authority**

Run:

```bash
rg -n 'Superseded|Partial supersession|2026-08-02-voice-download-actual-api' \
  docs/superpowers/specs/2026-07-14-voice-download-chunks-design.md \
  docs/superpowers/plans/2026-07-14-voice-download-chunks.md \
  docs/superpowers/specs/2026-07-14-voice-multiframe-decode-design.md \
  docs/superpowers/plans/2026-07-14-voice-multiframe-decode.md
```

Expected: all four files contain the appropriate banner and the SILK decoder documents remain explicitly authoritative for decoding.

- [ ] **Step 4: Commit documentation consistency changes**

```bash
git add docs/superpowers/specs/2026-07-14-voice-download-chunks-design.md \
  docs/superpowers/plans/2026-07-14-voice-download-chunks.md \
  docs/superpowers/specs/2026-07-14-voice-multiframe-decode-design.md \
  docs/superpowers/plans/2026-07-14-voice-multiframe-decode.md
git commit -m "docs: supersede unsupported voice chunking"
```

---

### Task 4: Full Verification and Code Review

**Files:**
- Verify: `internal/wechat/models.go`
- Verify: `internal/wechat/convert.go`
- Verify: `internal/wechat/voice_test.go`
- Verify: `internal/wechat/voice_download_api_test.go`
- Verify: the four superseded historical documents

- [ ] **Step 1: Run formatting, whitespace, and static checks**

Run:

```bash
gofmt -w internal/wechat/models.go internal/wechat/convert.go internal/wechat/voice_test.go internal/wechat/voice_download_api_test.go
git diff --check
go vet ./...
```

Expected: all commands exit 0.

- [ ] **Step 2: Run the complete test suite**

Run:

```bash
go test ./... -count=1
```

Expected: every package PASS with zero failures.

- [ ] **Step 3: Run focused race coverage**

Run:

```bash
go test -race ./internal/wechat -run '^(TestDownloadVoice|TestConvertLocalVoice|TestHandleWebhookVoice|TestApplyMessageTypeVoice)' -count=3
```

Expected: PASS with no race reports.

- [ ] **Step 4: Audit the request model and obsolete symbols**

Run:

```bash
rg -n 'type voiceMessageDownloadReq|json:"(Wxid|FromUserName|MsgId|Length|Bufid)"|voiceDownloadChunkSize|decodeVoiceBase64Fragments|voiceDownloadResp' internal/wechat/models.go internal/wechat/convert.go
```

Expected:

- `voiceMessageDownloadReq` has exactly five actual API fields;
- no voice `Offset`, 60 KiB constant, or fragment decoder remains;
- response model contains only the nested `data.buffer` fields production consumes.

- [ ] **Step 5: Inspect the final diff against the approved design**

Run:

```bash
BASE_SHA=$(git log --format=%H --grep='docs: plan actual voice download protocol' -1)
git diff "$BASE_SHA"...HEAD -- internal/wechat docs/superpowers
git status --short
```

Expected: only planned voice protocol, tests, and supersession banners; clean status after commits. Confirm:

```text
[ ] normal success path sends one full request
[ ] request body has exactly five documented fields
[ ] Length comes only from XML length and is sent unchanged
[ ] Bufid is required and always serialized
[ ] decoded byte length must exactly equal declared length
[ ] one identical retry remains limited to io.ErrUnexpectedEOF
[ ] no SILK decoder, transport, Matrix upload, or outbound voice changes
[ ] all four historical documents are marked superseded in scope
```

- [ ] **Step 6: Request read-only code review**

Give the reviewer the approved design, this plan, the plan commit as base, final HEAD, and fresh verification output. Require Critical/Important/Minor classification. Fix all Critical and Important findings with a new failing regression test before production changes.

- [ ] **Step 7: Rerun complete verification after review corrections**

If review requires changes, commit only the files from this plan with:

```bash
git add internal/wechat/models.go internal/wechat/convert.go internal/wechat/voice_test.go internal/wechat/voice_download_api_test.go \
  docs/superpowers/specs/2026-07-14-voice-download-chunks-design.md \
  docs/superpowers/plans/2026-07-14-voice-download-chunks.md \
  docs/superpowers/specs/2026-07-14-voice-multiframe-decode-design.md \
  docs/superpowers/plans/2026-07-14-voice-multiframe-decode.md
git commit -m "fix: address voice download review"
```

Then run:

```bash
go test ./... -count=1
go vet ./...
```

Expected: both commands exit 0. If review requires no changes, do not create an empty commit.

---

## Technical Vocabulary / 技术词汇

- **wire contract** — 传输协议契约
- **single-response boundary** — 单响应大小边界
- **decoded-length validation** — 解码后长度校验
- **in-flight cancellation** — 请求进行中的取消
- **source fallback** — 来源字段回退
- **compilable commit** — 可编译提交
- **regression test** — 回归测试
- **response envelope** — 响应包装结构
- **partial supersession** — 部分内容被替代
- **obsolete protocol artifact** — 过时协议残留

## 中文审查清单

- [ ] Task 1 是否确保测试、模型和下载实现同步迁移，提交时整个 `internal/wechat` 包可编译并通过？
- [ ] 是否用大于 60 KiB 的测试断言正常路径只有一个请求且请求体恰好五个字段？
- [ ] 是否确认 `Bufid` 必填，`Length` 只来自 XML `length`？
- [ ] 是否保留 Matrix duration 的现有兼容回退，不扩大行为变更？
- [ ] 是否覆盖请求前、请求中、响应后三种取消时机？
- [ ] 是否覆盖 XML 发送者和原始消息 ID 缺失时的来源回退？
- [ ] 是否接受 47 MiB 原始语音上限以适配 64 MiB transport 响应限制？
- [ ] 是否确认严格 SILK 解码出现 `io.ErrUnexpectedEOF` 时仍只允许一次相同完整请求重试？
- [ ] 是否确认不修改 SILK 解码器、通用 transport、Matrix 上传和出站语音？
- [ ] 是否确认四份旧文档仅在下载分片部分标记为已被替代？
