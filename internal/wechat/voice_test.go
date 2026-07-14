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
		if err := validateVoiceDownloadRequest(r); err != nil {
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

	ev := handleTestVoiceWebhook(t, server.URL)
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
	for _, want := range []string{"failed to retry WeChat voice download 22", "msg_id=11", `from="45170834716@chatroom"`, "temporary voice download failure"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("retry failure log %q does not contain %q", logs.String(), want)
		}
	}
}

func writeVoiceResponse(w http.ResponseWriter, silkData []byte) error {
	return json.NewEncoder(w).Encode(map[string]any{
		"Code":    0,
		"Success": true,
		"Data":    map[string]any{"data": map[string]any{"buffer": base64.StdEncoding.EncodeToString(silkData)}},
	})
}

func validateVoiceDownloadRequest(r *http.Request) error {
	if r.URL.Path != "/Tools/DownloadVoice" {
		return fmt.Errorf("unexpected path: %s", r.URL.Path)
	}
	var req voiceMessageDownloadReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return fmt.Errorf("decode voice download request: %w", err)
	}
	if req.Wxid != "wxid_self" || req.FromUserName != "45170834716@chatroom" || req.MsgID != 11 || req.Length != 80 || req.BufID != "buf-1" {
		return fmt.Errorf("unexpected voice download request: %#v", req)
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
	c := &Client{t: newTransport(serverURL), wxid: "wxid_self", events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	voiceXML := `<msg><voicemsg length="80" bufid="buf-1" voicelength="1000" /></msg>`
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
