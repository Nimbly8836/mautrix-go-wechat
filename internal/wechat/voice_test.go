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
	"os/exec"
	"path/filepath"
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
		if err := validateVoiceDownloadRequest(r, int64(len(silkData))); err != nil {
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

	ev := handleTestVoiceWebhook(t, server.URL, int64(len(silkData)))
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
	first := testSameLengthTruncatedSilk(t, full)
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if err := validateVoiceDownloadRequest(r, int64(len(full))); err != nil {
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

	ev := handleTestVoiceWebhook(t, server.URL, int64(len(full)))
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
	truncated := testSameLengthTruncatedSilk(t, full)
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := validateVoiceDownloadRequest(r, int64(len(truncated))); err != nil {
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

	ev := handleTestVoiceWebhook(t, server.URL, int64(len(truncated)))
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
		if err := validateVoiceDownloadRequest(r, int64(len(malformed))); err != nil {
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
	truncated := testSameLengthTruncatedSilk(t, full)
	var logs bytes.Buffer
	originalLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalLogOutput) })
	var requests atomic.Int32
	handlerErr := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if err := validateVoiceDownloadRequest(r, int64(len(truncated))); err != nil {
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

	ev := handleTestVoiceWebhook(t, server.URL, int64(len(truncated)))
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

func TestDownloadVoiceRejectsLengthAboveMaximumWithoutRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = writeVoiceResponse(w, nil)
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	data, err := c.downloadVoice(context.Background(), voiceMessageDownloadReq{
		FromUserName: "wxid_voice_sender", MsgID: 11, Length: maxVoiceDownloadBytes + 1, BufID: "buf-1",
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
		FromUserName: "wxid_voice_sender", MsgID: 11, Length: math.MaxInt64, BufID: "buf-1",
	})
	if err == nil {
		t.Fatal("downloadVoice returned nil error for MaxInt64 length")
	}
	if data != nil || requests.Load() != 0 {
		t.Fatalf("downloadVoice data=%d requests=%d, want nil data and no requests", len(data), requests.Load())
	}
}

func TestConvertLocalVoiceUsesXMLFromUsername(t *testing.T) {
	voiceData := testSilkContainer(true, []byte{0x00, 0x00})
	var requests atomic.Int32
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := validateVoiceDownloadRequest(r, int64(len(voiceData))); err != nil {
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

func writeVoiceResponse(w http.ResponseWriter, silkData []byte) error {
	return json.NewEncoder(w).Encode(map[string]any{
		"Code":    0,
		"Success": true,
		"Data":    map[string]any{"data": map[string]any{"buffer": base64.StdEncoding.EncodeToString(silkData)}},
	})
}

func validateVoiceDownloadRequest(r *http.Request, wantLength int64) error {
	return validateActualVoiceRequest(r, voiceMessageDownloadReq{
		Wxid:         "wxid_self",
		FromUserName: "wxid_voice_sender",
		MsgID:        11,
		Length:       wantLength,
		BufID:        "buf-1",
	})
}

func testSameLengthTruncatedSilk(t *testing.T, full []byte) []byte {
	t.Helper()
	truncated := append([]byte(nil), full...)
	position := 0
	if len(truncated) > 0 && truncated[0] == 0x02 {
		position++
	}
	position += len("#!SILK_V3")
	lastLengthOffset := -1
	for position < len(truncated) {
		if len(truncated)-position < 2 {
			t.Fatalf("test SILK has truncated frame length at %d", position)
		}
		lastLengthOffset = position
		payloadLength := int(int16(binary.LittleEndian.Uint16(truncated[position : position+2])))
		if payloadLength <= 0 || payloadLength >= maxSilkFramePayload {
			t.Fatalf("test SILK frame length = %d at %d, cannot extend safely", payloadLength, position)
		}
		position += 2 + payloadLength
	}
	if position != len(truncated) || lastLengthOffset < 0 {
		t.Fatalf("test SILK framing ended at %d of %d bytes", position, len(truncated))
	}
	payloadLength := binary.LittleEndian.Uint16(truncated[lastLengthOffset : lastLengthOffset+2])
	binary.LittleEndian.PutUint16(truncated[lastLengthOffset:lastLengthOffset+2], payloadLength+1)
	inspection, err := inspectSilkFrames(truncated)
	if !errors.Is(err, io.ErrUnexpectedEOF) || inspection.FrameCount == 0 {
		t.Fatalf("same-length test SILK inspection = %#v, %v; want recoverable truncation", inspection, err)
	}
	return truncated
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

func handleTestVoiceWebhook(t *testing.T, serverURL string, length int64) remoteapi.Event {
	t.Helper()
	return handleTestVoiceWebhookLength(t, serverURL, length)
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
	pcm := make([]byte, outboundVoiceSampleRate/5*2)
	wav := pcmToWAV(pcm, outboundVoiceSampleRate, 1, 16)
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

type testAudioPCMTranscoder struct {
	pcm        []byte
	sampleRate int
	err        error
	calls      int
	data       []byte
	mimeType   string
}

func (transcoder *testAudioPCMTranscoder) TranscodeToPCM(_ context.Context, data []byte, mimeType string) ([]byte, int, error) {
	transcoder.calls++
	transcoder.data = append([]byte(nil), data...)
	transcoder.mimeType = mimeType
	return append([]byte(nil), transcoder.pcm...), transcoder.sampleRate, transcoder.err
}

func TestSendMessageAudioOGGTranscodesToSilk(t *testing.T) {
	ogg := []byte("OggS\x00\x02opus-audio")
	pcm := make([]byte, outboundVoiceSampleRate/5*2)
	transcoder := &testAudioPCMTranscoder{pcm: pcm, sampleRate: outboundVoiceSampleRate}
	var request sendVoiceReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Msg/SendVoice" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode SendVoice request: %v", err)
		}
		_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{"NewMsgId":456}}`))
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self", audioTranscoder: transcoder}
	msgID, err := c.SendMessage(context.Background(), "wxid_friend", remoteapi.OutgoingMessage{
		Type: remoteapi.MsgAudio,
		Media: &remoteapi.Media{
			Data:     ogg,
			MimeType: "audio/ogg; codecs=opus",
			FileName: "voice.ogg",
			Size:     int64(len(ogg)),
		},
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	if msgID != "456" {
		t.Fatalf("message ID = %q, want 456", msgID)
	}
	if transcoder.calls != 1 || !bytes.Equal(transcoder.data, ogg) || transcoder.mimeType != "audio/ogg; codecs=opus" {
		t.Fatalf("unexpected transcode call: %#v", transcoder)
	}
	if request.Type != 4 || request.VoiceTime != 200 {
		t.Fatalf("unexpected SendVoice request: %#v", request)
	}
	silkBytes, decodeErr := base64.StdEncoding.DecodeString(request.Base64)
	if decodeErr != nil || !isSilkData(silkBytes) {
		t.Fatalf("expected transcoded SILK, len=%d err=%v", len(silkBytes), decodeErr)
	}
}

func TestSendMessageAudioTranscodeFailureIsActionable(t *testing.T) {
	transcoder := &testAudioPCMTranscoder{err: errors.New("decoder unavailable")}
	c := &Client{wxid: "wxid_self", audioTranscoder: transcoder}
	_, err := c.SendMessage(context.Background(), "wxid_friend", remoteapi.OutgoingMessage{
		Type: remoteapi.MsgAudio,
		Media: &remoteapi.Media{
			Data:     []byte("OggS-invalid"),
			MimeType: "audio/ogg",
			FileName: "voice.ogg",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "transcode Matrix audio") || !strings.Contains(err.Error(), "decoder unavailable") {
		t.Fatalf("transcode error = %v", err)
	}
}

func TestSendMessageUnsupportedWAVProfilesUse16kHzTranscoder(t *testing.T) {
	tests := []struct {
		name       string
		sampleRate int
		channels   int
	}{
		{name: "stereo", sampleRate: outboundVoiceSampleRate, channels: 2},
		{name: "24 kHz mono", sampleRate: 24000, channels: 1},
		{name: "44.1 kHz mono", sampleRate: 44100, channels: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sourcePCM := make([]byte, test.sampleRate/10*test.channels*2)
			wav := pcmToWAV(sourcePCM, test.sampleRate, test.channels, 16)
			transcodedPCM := make([]byte, outboundVoiceSampleRate/10*2)
			transcoder := &testAudioPCMTranscoder{pcm: transcodedPCM, sampleRate: outboundVoiceSampleRate}
			var request sendVoiceReq
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatalf("decode SendVoice request: %v", err)
				}
				_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{"NewMsgId":788}}`))
			}))
			defer server.Close()
			c := &Client{t: newTransport(server.URL), wxid: "wxid_self", audioTranscoder: transcoder}
			if _, err := c.SendMessage(context.Background(), "wxid_friend", remoteapi.OutgoingMessage{
				Type:  remoteapi.MsgAudio,
				Media: &remoteapi.Media{Data: wav, MimeType: "audio/wav", FileName: "voice.wav"},
			}); err != nil {
				t.Fatalf("SendMessage returned error: %v", err)
			}
			if transcoder.calls != 1 || request.VoiceTime != 100 {
				t.Fatalf("transcode calls=%d request=%#v", transcoder.calls, request)
			}
		})
	}
}

func TestFFmpegAudioArgsUseLowBandwidthVoiceProfile(t *testing.T) {
	args := strings.Join(ffmpegAudioArgs(), " ")
	if !strings.Contains(args, "-ac 1") || !strings.Contains(args, "-ar 16000") || !strings.Contains(args, "-f s16le") {
		t.Fatalf("unexpected FFmpeg audio args: %s", args)
	}
	if strings.Contains(args, "24000") {
		t.Fatalf("FFmpeg audio args unexpectedly use 24kHz: %s", args)
	}
	if outboundVoiceBitRate != 12000 {
		t.Fatalf("outbound SILK bitrate = %d, want 12000", outboundVoiceBitRate)
	}
}

func TestAudioToSilkRejectsOversizedInputBeforeFastPaths(t *testing.T) {
	for _, prefix := range [][]byte{[]byte("#!SILK_V3"), []byte("RIFF")} {
		data := make([]byte, outboundVoiceMaxInputBytes+1)
		copy(data, prefix)
		transcoder := &testAudioPCMTranscoder{err: errors.New("must not be called")}
		_, _, err := audioToSilk(context.Background(), data, "audio/silk", transcoder)
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("oversized %q error = %v", prefix, err)
		}
		if transcoder.calls != 0 {
			t.Fatalf("oversized %q called transcoder %d times", prefix, transcoder.calls)
		}
	}
}

func TestAudioToSilkHonorsCanceledContextBeforeEncoding(t *testing.T) {
	pcm := make([]byte, outboundVoiceSampleRate/10*2)
	wav := pcmToWAV(pcm, outboundVoiceSampleRate, 1, 16)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := audioToSilk(ctx, wav, "audio/wav", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled audioToSilk error = %v", err)
	}
}

func TestFFmpegAudioTranscoderMissingExecutableIsActionable(t *testing.T) {
	_, _, err := (ffmpegAudioPCMTranscoder{binary: filepath.Join(t.TempDir(), "missing-ffmpeg")}).TranscodeToPCM(
		context.Background(), []byte("OggS"), "audio/ogg",
	)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg is not installed") {
		t.Fatalf("missing executable error = %v", err)
	}
}

func TestBoundedCommandOutputRejectsOverflow(t *testing.T) {
	output := &boundedCommandOutput{maxBytes: 4}
	written, err := output.Write([]byte("12345"))
	if err == nil || written != 4 || !output.overflow || output.buffer.String() != "1234" {
		t.Fatalf("overflow write = (%d, %v), overflow=%t data=%q", written, err, output.overflow, output.buffer.String())
	}
}

func TestFFmpegAudioTranscoderConvertsOGGTo16kHzMonoPCM(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	generate := exec.Command(
		ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "0.1", "-c:a", "libopus", "-f", "ogg", "pipe:1",
	)
	ogg, err := generate.Output()
	if err != nil {
		t.Skipf("ffmpeg cannot generate Opus fixture: %v", err)
	}
	pcm, sampleRate, err := (ffmpegAudioPCMTranscoder{binary: ffmpeg}).TranscodeToPCM(context.Background(), ogg, "audio/ogg; codecs=opus")
	if err != nil {
		t.Fatalf("TranscodeToPCM returned error: %v", err)
	}
	if sampleRate != outboundVoiceSampleRate || len(pcm) < 2400 || len(pcm) > 4800 || len(pcm)%2 != 0 {
		t.Fatalf("transcoded sampleRate=%d bytes=%d, want 16kHz mono 16-bit PCM near 0.1s", sampleRate, len(pcm))
	}
}

func TestSendMessageAudioWAVFastPathSkipsTranscoder(t *testing.T) {
	pcm := make([]byte, outboundVoiceSampleRate/10*2)
	wav := pcmToWAV(pcm, outboundVoiceSampleRate, 1, 16)
	transcoder := &testAudioPCMTranscoder{err: errors.New("must not be called")}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{"NewMsgId":789}}`))
	}))
	defer server.Close()
	c := &Client{t: newTransport(server.URL), wxid: "wxid_self", audioTranscoder: transcoder}
	if _, err := c.SendMessage(context.Background(), "wxid_friend", remoteapi.OutgoingMessage{
		Type:  remoteapi.MsgAudio,
		Media: &remoteapi.Media{Data: wav, MimeType: "audio/wav", FileName: "voice.wav"},
	}); err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	if transcoder.calls != 0 {
		t.Fatalf("WAV fast path called transcoder %d times", transcoder.calls)
	}
}

func TestParseSilkPacketsPayloadOffsets(t *testing.T) {
	firstPayload := []byte{0x11, 0x22, 0x33}
	secondPayload := []byte{0x44, 0x55}
	body := append(testSilkFrame(firstPayload), testSilkFrame(secondPayload)...)

	for _, tt := range []struct {
		name    string
		withSTX bool
	}{
		{name: "without STX", withSTX: false},
		{name: "with STX", withSTX: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := testSilkContainer(tt.withSTX, body)
			inspection, packets, err := parseSilkPackets(data)
			if err != nil {
				t.Fatalf("parseSilkPackets: %v", err)
			}
			if inspection.FrameCount != 2 || len(packets) != 2 {
				t.Fatalf("inspection=%#v packets=%#v, want two packets", inspection, packets)
			}
			headerEnd := len("#!SILK_V3")
			if tt.withSTX {
				headerEnd++
			}
			want := []silkPacket{
				{PayloadStart: headerEnd + 2, PayloadEnd: headerEnd + 2 + len(firstPayload)},
				{PayloadStart: headerEnd + 2 + len(firstPayload) + 2, PayloadEnd: len(data)},
			}
			for index := range want {
				if packets[index] != want[index] {
					t.Fatalf("packet %d = %#v, want %#v", index, packets[index], want[index])
				}
				if got := data[packets[index].PayloadStart:packets[index].PayloadEnd]; !bytes.Equal(got, [][]byte{firstPayload, secondPayload}[index]) {
					t.Fatalf("packet %d payload = %x", index, got)
				}
			}
		})
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
