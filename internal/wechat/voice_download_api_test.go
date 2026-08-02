package wechat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

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
		name      string
		xmlFrom   string
		msgID     int64
		newMsgID  int64
		sender    string
		wantFrom  string
		wantMsgID int64
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
