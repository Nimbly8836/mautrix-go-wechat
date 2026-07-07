package wechat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/youthlin/silk"
)

const voiceSampleRate = 24000

func isSilkData(data []byte) bool {
	if bytes.HasPrefix(data, []byte("#!SILK_V3")) {
		return true
	}
	return len(data) > 10 && data[0] == 0x02 && bytes.HasPrefix(data[1:], []byte("#!SILK_V3"))
}

func silkToWAV(data []byte) ([]byte, error) {
	pcm, err := silk.Decode(bytes.NewReader(data), silk.WithSampleRate(voiceSampleRate))
	if err != nil {
		return nil, err
	}
	return pcmToWAV(pcm, voiceSampleRate, 1, 16), nil
}

func audioToSilk(data []byte, mimeType string) ([]byte, int, error) {
	if len(data) == 0 {
		return nil, 0, fmt.Errorf("empty audio data")
	}
	if isSilkData(data) || strings.EqualFold(strings.Split(mimeType, ";")[0], "audio/silk") {
		return data, 0, nil
	}
	pcm, sampleRate, channels, bitsPerSample, err := wavToPCM(data)
	if err != nil {
		return nil, 0, fmt.Errorf("wechat voice sending currently supports SILK or PCM WAV audio only: %w", err)
	}
	if channels != 1 || bitsPerSample != 16 {
		return nil, 0, fmt.Errorf("wechat voice sending requires 16-bit mono WAV, got channels=%d bits=%d", channels, bitsPerSample)
	}
	silkData, err := silk.Encode(bytes.NewReader(pcm), silk.SampleRate(sampleRate), silk.MaxInternal(sampleRate), silk.Stx(true))
	if err != nil {
		return nil, 0, err
	}
	voiceTime := int((int64(len(pcm)) / 2) * 1000 / int64(sampleRate))
	return silkData, voiceTime, nil
}

func pcmToWAV(pcm []byte, sampleRate, channels, bitsPerSample int) []byte {
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8
	out := bytes.NewBuffer(make([]byte, 0, 44+len(pcm)))
	out.WriteString("RIFF")
	_ = binary.Write(out, binary.LittleEndian, uint32(36+len(pcm)))
	out.WriteString("WAVE")
	out.WriteString("fmt ")
	_ = binary.Write(out, binary.LittleEndian, uint32(16))
	_ = binary.Write(out, binary.LittleEndian, uint16(1))
	_ = binary.Write(out, binary.LittleEndian, uint16(channels))
	_ = binary.Write(out, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(out, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(out, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(out, binary.LittleEndian, uint16(bitsPerSample))
	out.WriteString("data")
	_ = binary.Write(out, binary.LittleEndian, uint32(len(pcm)))
	out.Write(pcm)
	return out.Bytes()
}

func wavToPCM(data []byte) ([]byte, int, int, int, error) {
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, 0, 0, fmt.Errorf("not a RIFF/WAVE file")
	}
	r := bytes.NewReader(data[12:])
	var sampleRate, channels, bitsPerSample int
	var pcm []byte
	for r.Len() >= 8 {
		var chunkID [4]byte
		if _, err := io.ReadFull(r, chunkID[:]); err != nil {
			return nil, 0, 0, 0, err
		}
		var size uint32
		if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
			return nil, 0, 0, 0, err
		}
		if int(size) > r.Len() {
			return nil, 0, 0, 0, fmt.Errorf("invalid WAV chunk size")
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return nil, 0, 0, 0, err
		}
		if size%2 == 1 && r.Len() > 0 {
			_, _ = r.ReadByte()
		}
		switch string(chunkID[:]) {
		case "fmt ":
			if len(chunk) < 16 {
				return nil, 0, 0, 0, fmt.Errorf("short WAV fmt chunk")
			}
			format := binary.LittleEndian.Uint16(chunk[0:2])
			if format != 1 {
				return nil, 0, 0, 0, fmt.Errorf("unsupported WAV format %d", format)
			}
			channels = int(binary.LittleEndian.Uint16(chunk[2:4]))
			sampleRate = int(binary.LittleEndian.Uint32(chunk[4:8]))
			bitsPerSample = int(binary.LittleEndian.Uint16(chunk[14:16]))
		case "data":
			pcm = chunk
		}
	}
	if sampleRate == 0 || channels == 0 || bitsPerSample == 0 || len(pcm) == 0 {
		return nil, 0, 0, 0, fmt.Errorf("missing WAV fmt or data chunk")
	}
	return pcm, sampleRate, channels, bitsPerSample, nil
}
