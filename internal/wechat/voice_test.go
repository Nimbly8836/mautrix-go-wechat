package wechat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/youthlin/silk"
)

func TestHandleWebhookVoiceDownloadsSilkAndEmitsWAVAudio(t *testing.T) {
	pcm := make([]byte, voiceSampleRate/10*2)
	silkData, err := silk.Encode(bytes.NewReader(pcm), silk.SampleRate(voiceSampleRate), silk.Stx(true))
	if err != nil {
		t.Fatalf("encode test silk: %v", err)
	}
	var requested bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Tools/DownloadVoice" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		requested = true
		var req voiceMessageDownloadReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode voice download request: %v", err)
		}
		if req.Wxid != "wxid_self" || req.FromUserName != "wxid_friend" || req.MsgID != 11 || req.Length != 80 || req.BufID != "buf-1" {
			t.Fatalf("unexpected voice download request: %#v", req)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Code":    0,
			"Success": true,
			"Data":    map[string]any{"data": map[string]any{"buffer": base64.StdEncoding.EncodeToString(silkData)}},
		})
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self", events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	voiceXML := `<msg><voicemsg length="80" bufid="buf-1" voicelength="1000" /></msg>`
	payload := []byte(`{"MsgID":11,"NewMsgID":22,"MsgType":34,"ContentText":` + strconvQuote(voiceXML) + `,"PushContent":"反贼王 sent you a voice message in a group chat.","Talker":"45170834716@chatroom","SenderUserName":"wxid_friend","CreateTime":1710000000}`)
	if err := c.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	select {
	case ev := <-c.Events():
		if !requested {
			t.Fatalf("expected voice download request")
		}
		if ev.Message == nil || ev.Message.Type != remoteapi.MsgAudio || ev.Message.Media == nil {
			t.Fatalf("expected audio media, got %#v", ev)
		}
		if ev.Message.Media.MimeType != "audio/wav" || !bytes.HasPrefix(ev.Message.Media.Data, []byte("RIFF")) {
			t.Fatalf("expected WAV media, got mime=%q prefix=%q", ev.Message.Media.MimeType, ev.Message.Media.Data[:4])
		}
	default:
		t.Fatalf("expected webhook to emit voice message")
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

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
