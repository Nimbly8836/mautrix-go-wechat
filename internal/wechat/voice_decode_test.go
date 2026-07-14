package wechat

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/youthlin/silk"
)

func TestSilkToWAVDecodesAllInternalFrames(t *testing.T) {
	pcm := make([]byte, voiceSampleRate*2)
	for i := range pcm {
		pcm[i] = byte(i)
	}
	encoded, err := silk.Encode(
		bytes.NewReader(pcm),
		silk.SampleRate(voiceSampleRate),
		silk.PacketSizeMs(40),
		silk.Stx(true),
	)
	if err != nil {
		t.Fatalf("encode 40 ms SILK fixture: %v", err)
	}

	fixture, zeroRecords, positivePackets := removeZeroLengthSilkRecords(t, encoded)
	if zeroRecords != 25 {
		t.Fatalf("removed zero-length SILK records = %d, want 25", zeroRecords)
	}
	if positivePackets != 25 {
		t.Fatalf("positive SILK packets = %d, want 25", positivePackets)
	}
	inspection, err := inspectSilkFrames(fixture)
	if err != nil {
		t.Fatalf("inspect 40 ms SILK fixture: %v", err)
	}
	if inspection.FrameCount != 25 {
		t.Fatalf("inspected SILK packets = %d, want 25", inspection.FrameCount)
	}

	wav, err := silkToWAV(fixture)
	if err != nil {
		t.Fatalf("decode 40 ms SILK fixture: %v", err)
	}
	decodedPCM, sampleRate, channels, bitsPerSample, err := wavToPCM(wav)
	if err != nil {
		t.Fatalf("parse decoded WAV: %v", err)
	}
	gotDuration := pcmDurationMS(decodedPCM, sampleRate, channels, bitsPerSample)
	t.Logf("decoded duration = %d ms", gotDuration)
	if gotDuration < 980 || gotDuration > 1020 {
		t.Fatalf("decoded duration = %d ms, want 980..1020 ms", gotDuration)
	}
}

func removeZeroLengthSilkRecords(t *testing.T, encoded []byte) (fixture []byte, zeroRecords, positivePackets int) {
	t.Helper()
	const header = "\x02#!SILK_V3"
	if !bytes.HasPrefix(encoded, []byte(header)) {
		t.Fatalf("encoded SILK fixture has invalid header")
	}
	fixture = append([]byte(nil), encoded[:len(header)]...)
	for offset := len(header); offset < len(encoded); {
		if len(encoded)-offset < 2 {
			t.Fatalf("encoded SILK fixture has truncated record length at offset %d", offset)
		}
		length := int(int16(binary.LittleEndian.Uint16(encoded[offset : offset+2])))
		offset += 2
		if length < 0 {
			t.Fatalf("encoded STX SILK fixture has footer at offset %d", offset-2)
		}
		if len(encoded)-offset < length {
			t.Fatalf("encoded SILK fixture has truncated record payload at offset %d", offset)
		}
		if length == 0 {
			zeroRecords++
		} else {
			positivePackets++
			fixture = binary.LittleEndian.AppendUint16(fixture, uint16(length))
			fixture = append(fixture, encoded[offset:offset+length]...)
		}
		offset += length
	}
	return fixture, zeroRecords, positivePackets
}

func pcmDurationMS(pcm []byte, sampleRate, channels, bitsPerSample int) int64 {
	if len(pcm) == 0 || sampleRate <= 0 || channels <= 0 || bitsPerSample <= 0 {
		return 0
	}
	bytesPerSampleFrame := int64(channels) * int64(bitsPerSample) / 8
	if bytesPerSampleFrame <= 0 {
		return 0
	}
	return int64(len(pcm)) * 1000 / (int64(sampleRate) * bytesPerSampleFrame)
}
