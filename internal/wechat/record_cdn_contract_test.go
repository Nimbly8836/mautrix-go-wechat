package wechat

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestRecordVideoActualCDNContract(t *testing.T) {
	// Synthetic one-frame H.264 MP4, generated with ffmpeg (16x16 black).
	// No private message media is stored in this fixture.
	fixture := "testdata/record-video.mp4"
	if privateFixture := os.Getenv("WECHAT_RECORD_VIDEO_FIXTURE"); privateFixture != "" {
		fixture = privateFixture
	}
	video, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		data     []byte
		declared int
		want     bool
	}{
		{"same_size", video, len(video), true},
		{"source_metadata_size", video, 74548, true},
		{"larger_rendition", video, 12, true},
		{"truncated_rendition", video[:len(video)-4], 74548, false},
		{"not_a_video", []byte("not a valid video"), 74548, false},
		{"same_size_not_a_video", []byte("not a valid video"), len("not a valid video"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Tools/CdnDownloadRecordItem" {
					t.Errorf("unexpected endpoint %s", r.URL.Path)
				}
				var req struct {
					DataType int
					IsThumb  int
					DataSize int
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.DataType != 4 || req.IsThumb != 0 || req.DataSize != tc.declared {
					t.Errorf("video classification lost: %+v", req)
				}
				// Actual fk-zhang CdnImageBase64 DTO, also used for video.
				_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]string{"Image": base64.StdEncoding.EncodeToString(tc.data)}})
			}))
			defer srv.Close()
			c := &Client{wxid: "wxid_self", t: newTransport(srv.URL)}
			raw := strings.Replace(mergedRecordXML, "<datasize>12</datasize>", "<datasize>"+strconv.Itoa(tc.declared)+"</datasize>", 1)
			ev, ok := c.convertLocal(t.Context(), localMessage{MsgType: 49, Talker: "friend", Content: raw})
			if !ok || ev.Message.Record == nil {
				t.Fatal("record disappeared")
			}
			item := ev.Message.Record.Items[1]
			if (item.Media != nil) != tc.want {
				t.Fatalf("media_present=%t status=%q", item.Media != nil, item.MediaStatus)
			}
			if tc.want && (item.Media.Size != int64(len(tc.data)) || string(item.Media.Data) != string(tc.data)) {
				t.Fatal("wrong downloaded bytes or size")
			}
			if !tc.want && item.MediaStatus == "" {
				t.Fatal("failure hidden")
			}
		})
	}
}

func TestRecordCDNImageField(t *testing.T) {
	data, err := recordMediaBytes([]byte(`{"Code":0,"Success":true,"Data":{"Image":"/9j/2w=="}}`), "application/json")
	if err != nil || string(data) != string([]byte{255, 216, 255, 219}) {
		t.Fatalf("actual Image field ignored: %x %v", data, err)
	}
}
