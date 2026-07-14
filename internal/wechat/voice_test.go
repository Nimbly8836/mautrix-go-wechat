package wechat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/youthlin/silk"
)

func TestHandleWebhookVoiceDownloadsSilkAndEmitsWAVAudio(t *testing.T) {
	silkData := testMultiFrameSilk(t)
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := validateVoiceDownloadRequest(r); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := writeVoiceResponse(w, silkData); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	ev := handleTestVoiceWebhook(t, server.URL)
	assertNoVoiceHandlerError(t, handlerErr)
	if got := requests.Load(); got != 1 {
		t.Fatalf("voice download requests = %d, want 1", got)
	}
	assertNonEmptyWAVAudio(t, ev)
}

func TestHandleWebhookVoiceRetriesTruncatedDownload(t *testing.T) {
	full := testMultiFrameSilk(t)
	expectedWAV, err := silkToWAV(full)
	if err != nil {
		t.Fatalf("decode complete test SILK: %v", err)
	}
	first := append([]byte(nil), full[:len(full)-1]...)
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if err := validateVoiceDownloadRequest(r); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data := full
		if attempt == 1 {
			data = first
		}
		if err := writeVoiceResponse(w, data); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	ev := handleTestVoiceWebhook(t, server.URL)
	assertNoVoiceHandlerError(t, handlerErr)
	if got := requests.Load(); got != 2 {
		t.Fatalf("voice download requests = %d, want 2", got)
	}
	if ev.Message == nil || ev.Message.Media == nil {
		t.Fatalf("expected audio media, got %#v", ev)
	}
	if !bytes.Equal(ev.Message.Media.Data, expectedWAV) {
		t.Fatalf("voice media does not match complete retry decode: got %d bytes, want %d", len(ev.Message.Media.Data), len(expectedWAV))
	}
}

func TestHandleWebhookVoiceRecoversWhenRetryRemainsTruncated(t *testing.T) {
	full := testMultiFrameSilk(t)
	truncated := append([]byte(nil), full[:len(full)-1]...)
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := validateVoiceDownloadRequest(r); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := writeVoiceResponse(w, truncated); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	ev := handleTestVoiceWebhook(t, server.URL)
	assertNoVoiceHandlerError(t, handlerErr)
	if got := requests.Load(); got != 2 {
		t.Fatalf("voice download requests = %d, want 2", got)
	}
	assertNonEmptyWAVAudio(t, ev)
}

func TestHandleWebhookVoiceDoesNotRetryNonEOFDecodeError(t *testing.T) {
	malformed := testSilkContainer(true, []byte{0x00, 0x00})
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := validateVoiceDownloadRequestLength(r, int64(len(malformed))); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := writeVoiceResponse(w, malformed); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	ev := handleTestVoiceWebhookLength(t, server.URL, int64(len(malformed)))
	assertNoVoiceHandlerError(t, handlerErr)
	if got := requests.Load(); got != 1 {
		t.Fatalf("voice download requests = %d, want 1", got)
	}
	if ev.Message == nil || ev.Message.Type != remoteapi.MsgAudio || ev.Message.Media == nil {
		t.Fatalf("expected audio fallback, got %#v", ev)
	}
	if len(ev.Message.Media.Data) != 0 {
		t.Fatalf("fallback media data = %d bytes, want empty", len(ev.Message.Media.Data))
	}
}

func TestHandleWebhookVoiceRecoversFirstPayloadWhenRetryDownloadFails(t *testing.T) {
	full := testMultiFrameSilk(t)
	truncated := append([]byte(nil), full[:len(full)-1]...)
	var logs bytes.Buffer
	originalLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalLogOutput) })
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if err := validateVoiceDownloadRequest(r); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if attempt == 2 {
			http.Error(w, "temporary voice download failure", http.StatusInternalServerError)
			return
		}
		if err := writeVoiceResponse(w, truncated); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	ev := handleTestVoiceWebhook(t, server.URL)
	assertNoVoiceHandlerError(t, handlerErr)
	if got := requests.Load(); got != 2 {
		t.Fatalf("voice download requests = %d, want 2", got)
	}
	assertNonEmptyWAVAudio(t, ev)
	for _, want := range []string{"failed to retry WeChat voice download 22", "msg_id=11", `from="wxid_voice_sender"`, "temporary voice download failure"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("retry failure log %q does not contain %q", logs.String(), want)
		}
	}
}

func TestDownloadVoiceChunksAssemblesRawBytes(t *testing.T) {
	raw := make([]byte, 70*1024)
	for i := range raw {
		raw[i] = byte(i)
	}

	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			handlerErr <- fmt.Errorf("decode chunk request %d: %w", requestNumber, err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		offset, offsetOK := body["Offset"].(float64)
		length, lengthOK := body["Length"].(float64)
		wantOffset := []int64{0, 60 * 1024}
		wantLength := []int64{60 * 1024, 10 * 1024}
		index := int(requestNumber - 1)
		if !offsetOK || !lengthOK || index >= len(wantOffset) || int64(offset) != wantOffset[index] || int64(length) != wantLength[index] {
			handlerErr <- fmt.Errorf("chunk request %d offset/length = %v/%v, want indexed values", requestNumber, body["Offset"], body["Length"])
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		start, size := int64(offset), int64(length)
		fragment := base64.StdEncoding.EncodeToString(raw[start : start+size])
		if err := json.NewEncoder(w).Encode(map[string]any{
			"Code": 0, "Success": true,
			"Data": map[string]any{
				"msgId": 11, "newMsgId": 22, "offset": start, "length": size,
				"voiceLength": 1000, "data": map[string]any{"buffer": fragment, "iLen": size},
				"endFlag": index, "cancelFlag": 0,
			},
		}); err != nil {
			handlerErr <- fmt.Errorf("encode chunk response %d: %w", requestNumber, err)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	got, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{
		FromUserName: "wxid_voice_sender", MsgID: 11, Length: int64(len(raw)), BufID: "buf-1",
	})
	if err != nil {
		t.Fatalf("downloadVoice: %v", err)
	}
	assertNoVoiceHandlerError(t, handlerErr)
	if requests.Load() != 2 {
		t.Fatalf("voice download requests = %d, want 2", requests.Load())
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("downloaded voice differs: got %d bytes, want %d", len(got), len(raw))
	}
}

func TestDownloadVoiceRejectsLengthAboveMaximumWithoutRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = writeVoiceResponse(w, nil)
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{
		FromUserName: "wxid_voice_sender", MsgID: 11, Length: 64<<20 + 1,
	})
	if err == nil {
		t.Fatal("downloadVoice returned nil error above maximum length")
	}
	if data != nil || requests.Load() != 0 {
		t.Fatalf("downloadVoice data=%d requests=%d, want nil data and no requests", len(data), requests.Load())
	}
}

func TestDownloadVoiceRejectsMaxInt64LengthWithoutRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = writeVoiceResponse(w, nil)
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{
		FromUserName: "wxid_voice_sender", MsgID: 11, Length: math.MaxInt64,
	})
	if err == nil {
		t.Fatal("downloadVoice returned nil error for MaxInt64 length")
	}
	if data != nil || requests.Load() != 0 {
		t.Fatalf("downloadVoice data=%d requests=%d, want nil data and no requests", len(data), requests.Load())
	}
}

func TestDownloadVoiceRejectsDecodedDataShorterThanDeclaredLength(t *testing.T) {
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if requestNumber > 2 {
			handlerErr <- fmt.Errorf("unexpected request %d", requestNumber)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"Code": 0, "Success": true,
			"Data": map[string]any{"data": map[string]any{"buffer": base64.StdEncoding.EncodeToString([]byte{byte(requestNumber)})}},
		}); err != nil {
			handlerErr <- err
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{
		FromUserName: "wxid_voice_sender", MsgID: 11, Length: voiceDownloadChunkSize + 10,
	})
	assertNoVoiceHandlerError(t, handlerErr)
	if err == nil || !strings.Contains(err.Error(), "assembled") {
		t.Fatalf("downloadVoice error = %v, want assembled length error", err)
	}
	if data != nil {
		t.Fatalf("downloadVoice returned %d partial bytes, want nil", len(data))
	}
	if requests.Load() != 2 {
		t.Fatalf("voice download requests = %d, want 2", requests.Load())
	}
}

func TestDownloadVoiceRejectsShortChunkMetadata(t *testing.T) {
	tests := []struct {
		name           string
		responseLength int64
		responseILen   int64
		wantError      string
	}{
		{name: "data iLen", responseLength: 80, responseILen: 79, wantError: "iLen"},
		{name: "response length", responseLength: 79, responseILen: 80, wantError: "length"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			handlerErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if err := writeVoiceChunkResponseMetadata(w, base64.StdEncoding.EncodeToString(make([]byte, 80)), 0, tt.responseLength, tt.responseILen); err != nil {
					handlerErr <- err
				}
			}))
			defer server.Close()

			c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			data, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{
				FromUserName: "wxid_voice_sender", MsgID: 11, Length: 80,
			})
			assertNoVoiceHandlerError(t, handlerErr)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("downloadVoice error = %v, want short %s metadata error", err, tt.wantError)
			}
			if data != nil || requests.Load() != 1 {
				t.Fatalf("downloadVoice data=%d requests=%d, want nil data and one request", len(data), requests.Load())
			}
		})
	}
}

func TestDownloadVoiceStopsBeforeNextChunkWhenContextCanceledAfterResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := writeVoiceChunkResponse(w, base64.StdEncoding.EncodeToString(make([]byte, voiceDownloadChunkSize)), 0, voiceDownloadChunkSize); err != nil {
			handlerErr <- err
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		cancel()
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(ctx, voiceMessageDownloadReq{
		FromUserName: "wxid_voice_sender", MsgID: 11, Length: voiceDownloadChunkSize + 10,
	})
	assertNoVoiceHandlerError(t, handlerErr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("downloadVoice error = %v, want context.Canceled", err)
	}
	if data != nil || requests.Load() != 1 {
		t.Fatalf("downloadVoice data=%d requests=%d, want nil data and one request", len(data), requests.Load())
	}
}

func TestDownloadVoiceChunksReturnsNoPartialDataOnSecondChunkAPIFailure(t *testing.T) {
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			handlerErr <- fmt.Errorf("decode request %d: %w", requestNumber, err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if requestNumber == 1 {
			if err := writeVoiceChunkResponse(w, base64.StdEncoding.EncodeToString(make([]byte, voiceDownloadChunkSize)), 0, voiceDownloadChunkSize); err != nil {
				handlerErr <- err
			}
			return
		}
		if _, err := w.Write([]byte(`{"Code":42,"Success":false,"Message":"chunk unavailable"}`)); err != nil {
			handlerErr <- fmt.Errorf("write API failure response: %w", err)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{FromUserName: "wxid_voice_sender", MsgID: 11, Length: 70 * 1024})
	assertNoVoiceHandlerError(t, handlerErr)
	if err == nil || !strings.Contains(err.Error(), "offset=61440") || !strings.Contains(err.Error(), "chunk unavailable") {
		t.Fatalf("downloadVoice error = %v, want second chunk offset and API message", err)
	}
	if data != nil {
		t.Fatalf("downloadVoice returned %d partial bytes, want nil", len(data))
	}
	if requests.Load() != 2 {
		t.Fatalf("voice download requests = %d, want 2", requests.Load())
	}
}

func TestDownloadVoiceChunksRejectsEmptyBuffer(t *testing.T) {
	var requests atomic.Int32
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"data": map[string]any{"buffer": ""}}}); err != nil {
			handlerErr <- err
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{FromUserName: "wxid_voice_sender", MsgID: 11, Length: 80})
	assertNoVoiceHandlerError(t, handlerErr)
	if err == nil || !strings.Contains(err.Error(), "empty data.buffer") {
		t.Fatalf("downloadVoice error = %v, want empty data.buffer error", err)
	}
	if data != nil || requests.Load() != 1 {
		t.Fatalf("downloadVoice data=%d requests=%d, want nil data and one request", len(data), requests.Load())
	}
}

func TestDownloadVoiceChunksRejectsMalformedBase64(t *testing.T) {
	var requests atomic.Int32
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := writeVoiceChunkResponse(w, "%%%", 0, 80); err != nil {
			handlerErr <- err
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{FromUserName: "wxid_voice_sender", MsgID: 11, Length: 80})
	assertNoVoiceHandlerError(t, handlerErr)
	if err == nil {
		t.Fatal("downloadVoice returned nil error for malformed Base64")
	}
	if data != nil || requests.Load() != 1 {
		t.Fatalf("downloadVoice data=%d requests=%d, want nil data and one request", len(data), requests.Load())
	}
}

func TestDownloadVoiceChunksHonorsPreCanceledContext(t *testing.T) {
	var requests atomic.Int32
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := writeVoiceChunkResponse(w, base64.StdEncoding.EncodeToString([]byte("unexpected")), 0, 80); err != nil {
			handlerErr <- err
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(ctx, voiceMessageDownloadReq{FromUserName: "wxid_voice_sender", MsgID: 11, Length: 70 * 1024})
	assertNoVoiceHandlerError(t, handlerErr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("downloadVoice error = %v, want context.Canceled", err)
	}
	if data != nil || requests.Load() != 0 {
		t.Fatalf("downloadVoice data=%d requests=%d, want nil data and no completed request", len(data), requests.Load())
	}
}

func TestDownloadVoiceChunksAllowsNonShortMetadataMismatchWithoutReordering(t *testing.T) {
	raw := make([]byte, 70*1024)
	for i := range raw {
		raw[i] = byte(i * 31)
	}
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			handlerErr <- err
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		offset, offsetOK := body["Offset"].(float64)
		length, lengthOK := body["Length"].(float64)
		if !offsetOK || !lengthOK {
			handlerErr <- fmt.Errorf("request %d missing numeric offset/length: %#v", requestNumber, body)
			http.Error(w, "invalid range", http.StatusBadRequest)
			return
		}
		start, size := int64(offset), int64(length)
		metadataOffset := int64(999)
		metadataLength := size + 1
		if err := writeVoiceChunkResponse(w, base64.StdEncoding.EncodeToString(raw[start:start+size]), metadataOffset, metadataLength); err != nil {
			handlerErr <- err
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	got, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{FromUserName: "wxid_voice_sender", MsgID: 11, Length: int64(len(raw))})
	if err != nil {
		t.Fatalf("downloadVoice: %v", err)
	}
	assertNoVoiceHandlerError(t, handlerErr)
	if !bytes.Equal(got, raw) {
		t.Fatalf("metadata mismatch reordered data: got %d bytes, want %d", len(got), len(raw))
	}
	if requests.Load() != 2 {
		t.Fatalf("voice download requests = %d, want 2", requests.Load())
	}
}

func TestConvertLocalVoiceUsesXMLFromUsername(t *testing.T) {
	voiceData := testSilkContainer(true, []byte{0x00, 0x00})
	var requests atomic.Int32
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := validateVoiceDownloadRequestLength(r, int64(len(voiceData))); err != nil {
			handlerErr <- err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := writeVoiceResponse(w, voiceData); err != nil {
			handlerErr <- err
		}
	}))
	defer server.Close()

	voiceXML := fmt.Sprintf(`<msg><voicemsg length="%d" bufid="buf-1" voicelength="1000" fromusername="wxid_voice_sender" /></msg>`, len(voiceData))
	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	ev, ok := c.convertLocal(context.Background(), localMessage{
		MsgID: 11, NewMsgID: 22, MsgType: wxVoice, ContentText: voiceXML,
		Talker: "45170834716@chatroom", SenderUserName: "wxid_local_sender", CreateTime: 1710000000,
	})
	assertNoVoiceHandlerError(t, handlerErr)
	if !ok || ev.Message == nil || ev.Message.Type != remoteapi.MsgAudio {
		t.Fatalf("convertLocal returned ok=%v event=%#v", ok, ev)
	}
	if requests.Load() != 1 {
		t.Fatalf("voice download requests = %d, want 1", requests.Load())
	}
}

func TestDecodeVoiceBase64Fragments(t *testing.T) {
	t.Run("combined unpadded boundary", func(t *testing.T) {
		first := make([]byte, voiceDownloadChunkSize)
		for i := range first {
			first[i] = byte(i)
		}
		second := []byte("final tail")
		fragments := []string{
			base64.StdEncoding.EncodeToString(first),
			" \n" + base64.StdEncoding.EncodeToString(second) + "\t",
		}
		got, fallback, err := decodeVoiceBase64Fragments(fragments)
		if err != nil {
			t.Fatalf("decode fragments: %v", err)
		}
		want := append(append([]byte(nil), first...), second...)
		if fallback || !bytes.Equal(got, want) {
			t.Fatalf("decode fragments fallback=%v bytes=%d, want fallback=false bytes=%d", fallback, len(got), len(want))
		}
	})

	t.Run("independent padding fallback", func(t *testing.T) {
		fragments := []string{
			base64.StdEncoding.EncodeToString([]byte("ab")),
			base64.StdEncoding.EncodeToString([]byte("cde")),
		}
		got, fallback, err := decodeVoiceBase64Fragments(fragments)
		if err != nil {
			t.Fatalf("decode fragments: %v", err)
		}
		if !fallback || string(got) != "abcde" {
			t.Fatalf("decode fragments fallback=%v data=%q, want fallback=true data=abcde", fallback, got)
		}
	})

	for _, tt := range []struct {
		name      string
		fragments []string
	}{
		{name: "malformed", fragments: []string{"YWJj", "%%%"}},
		{name: "empty list"},
		{name: "empty chunk", fragments: []string{"YWJj", " \n\t"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, _, err := decodeVoiceBase64Fragments(tt.fragments)
			if err == nil {
				t.Fatalf("decode fragments returned %d bytes, want error", len(data))
			}
			if len(data) != 0 {
				t.Fatalf("decode fragments returned partial data: %d bytes", len(data))
			}
		})
	}
}

func writeVoiceChunkResponse(w http.ResponseWriter, buffer string, offset, length int64) error {
	return writeVoiceChunkResponseMetadata(w, buffer, offset, length, length)
}

func writeVoiceChunkResponseMetadata(w http.ResponseWriter, buffer string, offset, length, iLen int64) error {
	return json.NewEncoder(w).Encode(map[string]any{
		"Code": 0, "Success": true,
		"Data": map[string]any{
			"msgId": 11, "newMsgId": 22, "offset": offset, "length": length,
			"voiceLength": 1000, "data": map[string]any{"buffer": buffer, "iLen": iLen},
			"endFlag": 0, "cancelFlag": 0,
		},
	})
}

func writeVoiceResponse(w http.ResponseWriter, silkData []byte) error {
	return json.NewEncoder(w).Encode(map[string]any{
		"Code":    0,
		"Success": true,
		"Data":    map[string]any{"data": map[string]any{"buffer": base64.StdEncoding.EncodeToString(silkData)}},
	})
}

func validateVoiceDownloadRequest(r *http.Request) error {
	return validateVoiceDownloadRequestLength(r, 80)
}

func validateVoiceDownloadRequestLength(r *http.Request, wantLength int64) error {
	if r.URL.Path != "/Tools/DownloadVoice" {
		return fmt.Errorf("unexpected path: %s", r.URL.Path)
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode voice download request: %w", err)
	}
	offset, hasOffset := body["Offset"]
	if !hasOffset {
		return fmt.Errorf("voice download request has no explicit Offset: %#v", body)
	}
	if body["Wxid"] != "wxid_self" || body["FromUserName"] != "wxid_voice_sender" || body["MsgId"] != float64(11) || offset != float64(0) || body["Length"] != float64(wantLength) || body["Bufid"] != "buf-1" {
		return fmt.Errorf("unexpected voice download request: %#v", body)
	}
	return nil
}

func testMultiFrameSilk(t *testing.T) []byte {
	t.Helper()
	pcm := make([]byte, voiceSampleRate/5*2)
	for i := range pcm {
		pcm[i] = byte(i)
	}
	silkData, err := silk.Encode(bytes.NewReader(pcm), silk.SampleRate(voiceSampleRate), silk.Stx(true))
	if err != nil {
		t.Fatalf("encode test SILK: %v", err)
	}
	inspection, err := inspectSilkFrames(silkData)
	if err != nil {
		t.Fatalf("inspect test SILK: %v", err)
	}
	if inspection.FrameCount < 2 {
		t.Fatalf("test SILK frame count = %d, want at least 2", inspection.FrameCount)
	}
	return silkData
}

func handleTestVoiceWebhook(t *testing.T, serverURL string) remoteapi.Event {
	t.Helper()
	return handleTestVoiceWebhookLength(t, serverURL, 80)
}

func handleTestVoiceWebhookLength(t *testing.T, serverURL string, length int64) remoteapi.Event {
	t.Helper()
	c := &Client{t: newTransport(serverURL), wxid: "wxid_self", events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	voiceXML := fmt.Sprintf(`<msg><voicemsg length="%d" bufid="buf-1" voicelength="1000" fromusername="wxid_voice_sender" /></msg>`, length)
	payload := []byte(`{"MsgID":11,"NewMsgID":22,"MsgType":34,"ContentText":` + strconvQuote(voiceXML) + `,"PushContent":"反贼王 sent you a voice message in a group chat.","Talker":"45170834716@chatroom","SenderUserName":"wxid_friend","CreateTime":1710000000}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		return ev
	default:
		t.Fatalf("expected webhook to emit voice message")
		return remoteapi.Event{}
	}
}

func assertNoVoiceHandlerError(t *testing.T, handlerErr <-chan error) {
	t.Helper()
	select {
	case err := <-handlerErr:
		t.Fatalf("voice download handler: %v", err)
	default:
	}
}

func assertNonEmptyWAVAudio(t *testing.T, ev remoteapi.Event) {
	t.Helper()
	if ev.Message == nil || ev.Message.Type != remoteapi.MsgAudio || ev.Message.Media == nil {
		t.Fatalf("expected audio media, got %#v", ev)
	}
	if ev.Message.Media.MimeType != "audio/wav" || !bytes.HasPrefix(ev.Message.Media.Data, []byte("RIFF")) {
		t.Fatalf("expected WAV media, got mime=%q data_bytes=%d", ev.Message.Media.MimeType, len(ev.Message.Media.Data))
	}
	if got := testWAVDataChunkLength(t, ev.Message.Media.Data); got == 0 {
		t.Fatalf("WAV data chunk length = 0")
	}
}

func TestSendMessageAudioWAVCallsSendVoiceAsSilk(t *testing.T) {
	pcm := make([]byte, voiceSampleRate/5*2)
	wav := pcmToWAV(pcm, voiceSampleRate, 1, 16)
	var sawSendVoice bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Msg/SendVoice" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		sawSendVoice = true
		var req sendVoiceReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode SendVoice request: %v", err)
		}
		if req.Wxid != "wxid_self" || req.ToWxid != "wxid_friend" || req.Type != 4 || req.VoiceTime != 200 {
			t.Fatalf("unexpected SendVoice request: %#v", req)
		}
		silkBytes, err := base64.StdEncoding.DecodeString(req.Base64)
		if err != nil || !isSilkData(silkBytes) {
			t.Fatalf("expected silk base64, len=%d err=%v", len(silkBytes), err)
		}
		_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{"NewMsgId":123}}`))
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	msgID, err := c.SendMessage(context.Background(), "wxid_friend", remoteapi.OutgoingMessage{
		Type:  remoteapi.MsgAudio,
		Text:  "voice.wav",
		Media: &remoteapi.Media{Data: wav, MimeType: "audio/wav", FileName: "voice.wav", Size: int64(len(wav))},
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	if msgID != "123" || !sawSendVoice {
		t.Fatalf("unexpected send result id=%q saw=%v", msgID, sawSendVoice)
	}
}

func TestInspectSilkFrames(t *testing.T) {
	frame := testSilkFrame([]byte{0x11, 0x22, 0x33})
	frameEndWithoutSTX := len("#!SILK_V3") + len(frame)
	frameEndWithSTX := 1 + frameEndWithoutSTX

	tests := []struct {
		name           string
		data           []byte
		want           silkFrameInspection
		wantErr        bool
		wantUnexpected bool
	}{
		{
			name: "valid single frame without STX",
			data: testSilkContainer(false, frame),
			want: silkFrameInspection{PrefixEnd: frameEndWithoutSTX, FrameCount: 1, PayloadBytes: 3},
		},
		{
			name: "valid single frame with STX",
			data: testSilkContainer(true, frame),
			want: silkFrameInspection{PrefixEnd: frameEndWithSTX, FrameCount: 1, PayloadBytes: 3},
		},
		{
			name: "complete frame and negative footer",
			data: testSilkContainer(true, append(append([]byte{}, frame...), 0xff, 0xff)),
			want: silkFrameInspection{PrefixEnd: frameEndWithSTX, FrameCount: 1, PayloadBytes: 3, DroppedBytes: 0},
		},
		{
			name:    "footer only",
			data:    testSilkContainer(false, []byte{0xff, 0xff}),
			want:    silkFrameInspection{PrefixEnd: len("#!SILK_V3")},
			wantErr: true,
		},
		{
			name:    "dangling one byte length",
			data:    testSilkContainer(false, append(append([]byte{}, frame...), 0x03)),
			want:    silkFrameInspection{PrefixEnd: frameEndWithoutSTX, FrameCount: 1, PayloadBytes: 3, DroppedBytes: 1},
			wantErr: true, wantUnexpected: true,
		},
		{
			name:    "partial frame payload",
			data:    testSilkContainer(false, append(append(append([]byte{}, frame...), 0x03, 0x00), 0x44)),
			want:    silkFrameInspection{PrefixEnd: frameEndWithoutSTX, FrameCount: 1, PayloadBytes: 3, DroppedBytes: 3},
			wantErr: true, wantUnexpected: true,
		},
		{
			name:    "positive length with zero payload and no complete frame",
			data:    testSilkContainer(false, []byte{0x03, 0x00}),
			want:    silkFrameInspection{PrefixEnd: len("#!SILK_V3"), DroppedBytes: 2},
			wantErr: true, wantUnexpected: true,
		},
		{
			name:    "zero length",
			data:    testSilkContainer(false, []byte{0x00, 0x00}),
			want:    silkFrameInspection{PrefixEnd: len("#!SILK_V3")},
			wantErr: true,
		},
		{
			name:    "oversized length",
			data:    testSilkContainer(false, []byte{0x01, 0x04}),
			want:    silkFrameInspection{PrefixEnd: len("#!SILK_V3")},
			wantErr: true,
		},
		{
			name:    "footer trailer",
			data:    testSilkContainer(false, append(append(append([]byte{}, frame...), 0xff, 0xff), 0x00)),
			want:    silkFrameInspection{PrefixEnd: frameEndWithoutSTX, FrameCount: 1, PayloadBytes: 3},
			wantErr: true,
		},
		{
			name:    "header only",
			data:    testSilkContainer(false, nil),
			want:    silkFrameInspection{PrefixEnd: len("#!SILK_V3")},
			wantErr: true,
		},
		{
			name:    "short garbage header",
			data:    []byte("x"),
			wantErr: true,
		},
		{
			name:    "incomplete header",
			data:    []byte("#!SILK"),
			wantErr: true,
		},
		{
			name:    "STX and incomplete header",
			data:    []byte("\x02#!SILK"),
			wantErr: true,
		},
		{
			name:    "inexact header",
			data:    []byte("#!SILK_V2\x01\x00\x44"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := inspectSilkFrames(tt.data)
			if got != tt.want {
				t.Fatalf("inspection = %#v, want %#v", got, tt.want)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if errors.Is(err, io.ErrUnexpectedEOF) != tt.wantUnexpected {
				t.Fatalf("errors.Is(error, io.ErrUnexpectedEOF) = %v, want %v (error: %v)", errors.Is(err, io.ErrUnexpectedEOF), tt.wantUnexpected, err)
			}
		})
	}
}

func TestRecoverTruncatedSilkToWAV(t *testing.T) {
	pcm := make([]byte, voiceSampleRate/5*2) // 200 ms, ten SILK frames.
	for i := range pcm {
		pcm[i] = byte(i)
	}
	silkData, err := silk.Encode(bytes.NewReader(pcm), silk.SampleRate(voiceSampleRate), silk.Stx(true))
	if err != nil {
		t.Fatalf("encode test SILK: %v", err)
	}
	truncated := append([]byte(nil), silkData[:len(silkData)-1]...)

	if _, err = silkToWAV(truncated); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("silkToWAV error = %v, want io.ErrUnexpectedEOF", err)
	}

	wav, sourceIndex, sourceBytes, droppedBytes, err := recoverTruncatedSilkToWAV(truncated, truncated)
	if err != nil {
		t.Fatalf("recoverTruncatedSilkToWAV: %v", err)
	}
	if got := testWAVDataChunkLength(t, wav); got == 0 {
		t.Fatalf("recovered WAV data chunk length = 0")
	}
	if sourceIndex != 1 {
		t.Fatalf("sourceIndex = %d, want later tied source 1", sourceIndex)
	}
	if sourceBytes != len(truncated) {
		t.Fatalf("sourceBytes = %d, want %d", sourceBytes, len(truncated))
	}
	if droppedBytes <= 0 {
		t.Fatalf("droppedBytes = %d, want > 0", droppedBytes)
	}
}

func TestRecoverTruncatedSilkToWAVNoRecoverableFrames(t *testing.T) {
	partialFirstFrame := testSilkContainer(true, []byte{0x03, 0x00})
	wav, _, _, _, err := recoverTruncatedSilkToWAV(partialFirstFrame)
	if err == nil || !strings.Contains(err.Error(), "no recoverable SILK frames") {
		t.Fatalf("error = %v, want explicit no recoverable SILK frames error", err)
	}
	if wav != nil {
		t.Fatalf("WAV = %d bytes, want nil", len(wav))
	}
}

func TestRecoverTruncatedSilkToWAVCandidateSorting(t *testing.T) {
	candidates := []silkRecoveryCandidate{
		{Data: []byte("frames-first"), SourceIndex: 9, Inspection: silkFrameInspection{FrameCount: 1, PayloadBytes: 100}},
		{Data: []byte("tie-earlier"), SourceIndex: 0, Inspection: silkFrameInspection{FrameCount: 2, PayloadBytes: 5}},
		{Data: []byte("payload-third"), SourceIndex: 8, Inspection: silkFrameInspection{FrameCount: 2, PayloadBytes: 4}},
		{Data: []byte("tie-later"), SourceIndex: 1, Inspection: silkFrameInspection{FrameCount: 2, PayloadBytes: 5}},
	}

	sortSilkRecoveryCandidates(candidates)
	want := []string{"tie-later", "tie-earlier", "payload-third", "frames-first"}
	for i, candidate := range candidates {
		if string(candidate.Data) != want[i] {
			t.Fatalf("candidate %d = %q, want %q", i, candidate.Data, want[i])
		}
	}
}

func TestSilkToWAVRejectsMalformedStructure(t *testing.T) {
	frame := testSilkFrame([]byte{0x11})
	tests := []struct {
		name string
		data []byte
	}{
		{name: "header only", data: testSilkContainer(false, nil)},
		{name: "zero frame", data: testSilkContainer(false, []byte{0x00, 0x00})},
		{name: "oversized frame", data: testSilkContainer(false, []byte{0x01, 0x04})},
		{name: "footer trailer", data: testSilkContainer(false, append(append(append([]byte{}, frame...), 0xff, 0xff), 0x00))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if wav, err := silkToWAV(tt.data); err == nil {
				t.Fatalf("silkToWAV returned %d-byte WAV, want error", len(wav))
			}
		})
	}
}

func TestSilkToWAVRejectsEmptyPCM(t *testing.T) {
	emptySilk, err := silk.Encode(bytes.NewReader(nil), silk.SampleRate(voiceSampleRate), silk.Stx(true))
	if err != nil {
		t.Fatalf("encode empty SILK: %v", err)
	}
	pcm, err := silk.Decode(bytes.NewReader(emptySilk), silk.WithSampleRate(voiceSampleRate))
	if err != nil {
		t.Fatalf("confirm fixture decodes: %v", err)
	}
	if len(pcm) != 0 {
		t.Fatalf("fixture PCM length = %d, want 0", len(pcm))
	}
	if wav, err := silkToWAV(emptySilk); err == nil {
		t.Fatalf("silkToWAV returned %d-byte WAV for empty PCM, want error", len(wav))
	}
}

func testSilkContainer(withSTX bool, body []byte) []byte {
	data := make([]byte, 0, 1+len("#!SILK_V3")+len(body))
	if withSTX {
		data = append(data, 0x02)
	}
	data = append(data, "#!SILK_V3"...)
	return append(data, body...)
}

func testSilkFrame(payload []byte) []byte {
	frame := make([]byte, 2, 2+len(payload))
	binary.LittleEndian.PutUint16(frame, uint16(len(payload)))
	return append(frame, payload...)
}

func testWAVDataChunkLength(t *testing.T, wav []byte) uint32 {
	t.Helper()
	if len(wav) < 12 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("not a WAV file: %d bytes", len(wav))
	}
	for offset := 12; offset+8 <= len(wav); {
		size := binary.LittleEndian.Uint32(wav[offset+4 : offset+8])
		if string(wav[offset:offset+4]) == "data" {
			return size
		}
		offset += 8 + int(size) + int(size%2)
	}
	t.Fatalf("WAV has no data chunk")
	return 0
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
