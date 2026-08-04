package wechat

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/youthlin/silk"
)

const (
	voiceSampleRate            = 24000
	outboundVoiceSampleRate    = 16000
	outboundVoiceBitRate       = 12000
	outboundVoiceMaxInputBytes = 16 << 20
	outboundVoiceMaxPCMBytes   = 16 << 20
	outboundVoiceTimeout       = 2 * time.Minute
	maxSilkFramePayload        = 1024
)

type silkFrameInspection struct {
	PrefixEnd    int
	FrameCount   int
	PayloadBytes int
	DroppedBytes int
}

type silkPacket struct {
	PayloadStart int
	PayloadEnd   int
}

type silkRecoveryCandidate struct {
	Data        []byte
	SourceIndex int
	Inspection  silkFrameInspection
}

func isSilkData(data []byte) bool {
	if bytes.HasPrefix(data, []byte("#!SILK_V3")) {
		return true
	}
	return len(data) > 10 && data[0] == 0x02 && bytes.HasPrefix(data[1:], []byte("#!SILK_V3"))
}

func parseSilkPackets(data []byte) (silkFrameInspection, []silkPacket, error) {
	var inspection silkFrameInspection
	var packets []silkPacket
	position := 0
	if len(data) > 0 && data[0] == 0x02 {
		position++
	}

	const header = "#!SILK_V3"
	if len(data)-position < len(header) {
		return inspection, packets, fmt.Errorf("invalid SILK header: too short")
	}
	if string(data[position:position+len(header)]) != header {
		return inspection, packets, fmt.Errorf("invalid SILK header")
	}
	position += len(header)
	inspection.PrefixEnd = position

	for position < len(data) {
		frameStart := position
		if len(data)-position < 2 {
			inspection.DroppedBytes = len(data) - inspection.PrefixEnd
			return inspection, packets, fmt.Errorf("truncated SILK frame length: %w", io.ErrUnexpectedEOF)
		}

		payloadLength := int(int16(binary.LittleEndian.Uint16(data[position : position+2])))
		position += 2
		if payloadLength < 0 {
			if inspection.FrameCount == 0 {
				return inspection, packets, fmt.Errorf("SILK footer without a complete frame")
			}
			if position != len(data) {
				return inspection, packets, fmt.Errorf("SILK footer must be at end of data")
			}
			inspection.PrefixEnd = frameStart
			inspection.DroppedBytes = 0
			return inspection, packets, nil
		}
		if payloadLength == 0 {
			return inspection, packets, fmt.Errorf("zero-length SILK frame")
		}
		if payloadLength > maxSilkFramePayload {
			return inspection, packets, fmt.Errorf("SILK frame payload %d exceeds maximum %d", payloadLength, maxSilkFramePayload)
		}
		if len(data)-position < payloadLength {
			inspection.DroppedBytes = len(data) - inspection.PrefixEnd
			return inspection, packets, fmt.Errorf("truncated SILK frame payload: %w", io.ErrUnexpectedEOF)
		}

		payloadStart := position
		position += payloadLength
		packets = append(packets, silkPacket{PayloadStart: payloadStart, PayloadEnd: position})
		inspection.PrefixEnd = position
		inspection.FrameCount++
		inspection.PayloadBytes += payloadLength
	}

	if inspection.FrameCount == 0 {
		return inspection, packets, fmt.Errorf("no complete SILK frames")
	}
	return inspection, packets, nil
}

func inspectSilkFrames(data []byte) (silkFrameInspection, error) {
	inspection, _, err := parseSilkPackets(data)
	return inspection, err
}

func silkToWAV(data []byte) ([]byte, error) {
	pcm, err := decodeSilkPCM(data)
	if err != nil {
		return nil, err
	}
	return pcmToWAV(pcm, voiceSampleRate, 1, 16), nil
}

func sortSilkRecoveryCandidates(candidates []silkRecoveryCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.Inspection.FrameCount != right.Inspection.FrameCount {
			return left.Inspection.FrameCount > right.Inspection.FrameCount
		}
		if left.Inspection.PayloadBytes != right.Inspection.PayloadBytes {
			return left.Inspection.PayloadBytes > right.Inspection.PayloadBytes
		}
		return left.SourceIndex > right.SourceIndex
	})
}

func recoverTruncatedSilkToWAV(inputs ...[]byte) (wav []byte, sourceIndex, sourceBytes, droppedBytes int, err error) {
	candidates := make([]silkRecoveryCandidate, 0, len(inputs))
	for index, data := range inputs {
		inspection, inspectErr := inspectSilkFrames(data)
		if errors.Is(inspectErr, io.ErrUnexpectedEOF) && inspection.FrameCount > 0 {
			candidates = append(candidates, silkRecoveryCandidate{
				Data:        data,
				SourceIndex: index,
				Inspection:  inspection,
			})
		}
	}
	if len(candidates) == 0 {
		return nil, -1, 0, 0, fmt.Errorf("no recoverable SILK frames")
	}

	sortSilkRecoveryCandidates(candidates)
	decodeErrors := make([]error, 0, len(candidates))
	for _, candidate := range candidates {
		prefix := candidate.Data[:candidate.Inspection.PrefixEnd]
		recoveredWAV, decodeErr := silkToWAV(prefix)
		if decodeErr == nil {
			return recoveredWAV, candidate.SourceIndex, len(candidate.Data), candidate.Inspection.DroppedBytes, nil
		}
		decodeErrors = append(decodeErrors, fmt.Errorf("decode SILK recovery candidate %d: %w", candidate.SourceIndex, decodeErr))
	}
	return nil, -1, 0, 0, errors.Join(decodeErrors...)
}

type audioPCMTranscoder interface {
	TranscodeToPCM(context.Context, []byte, string) ([]byte, int, error)
}

type ffmpegAudioPCMTranscoder struct {
	binary string
}

func ffmpegAudioArgs() []string {
	return []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", "pipe:0", "-map_metadata", "-1", "-vn", "-sn", "-dn",
		"-ac", "1", "-ar", fmt.Sprint(outboundVoiceSampleRate),
		"-acodec", "pcm_s16le", "-f", "s16le", "pipe:1",
	}
}

type boundedCommandOutput struct {
	buffer   bytes.Buffer
	maxBytes int
	overflow bool
}

func (output *boundedCommandOutput) Write(data []byte) (int, error) {
	remaining := output.maxBytes - output.buffer.Len()
	if remaining <= 0 {
		output.overflow = true
		return 0, fmt.Errorf("audio output exceeds %d bytes", output.maxBytes)
	}
	if len(data) > remaining {
		_, _ = output.buffer.Write(data[:remaining])
		output.overflow = true
		return remaining, fmt.Errorf("audio output exceeds %d bytes", output.maxBytes)
	}
	return output.buffer.Write(data)
}

func (transcoder ffmpegAudioPCMTranscoder) TranscodeToPCM(ctx context.Context, data []byte, _ string) ([]byte, int, error) {
	binary := strings.TrimSpace(transcoder.binary)
	if binary == "" {
		binary = "ffmpeg"
	}
	command := exec.CommandContext(ctx, binary, ffmpegAudioArgs()...)
	command.Stdin = bytes.NewReader(data)
	stdout := &boundedCommandOutput{maxBytes: outboundVoiceMaxPCMBytes}
	stderr := &boundedCommandOutput{maxBytes: 4 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, ctxErr
		}
		if stdout.overflow {
			return nil, 0, fmt.Errorf("decoded audio is too large")
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, 0, fmt.Errorf("ffmpeg is not installed or not in PATH")
		}
		detail := boundedFFmpegErrorDetail(stderr.buffer.String())
		if detail != "" {
			return nil, 0, fmt.Errorf("ffmpeg audio conversion failed: %w (%s)", err, detail)
		}
		return nil, 0, fmt.Errorf("ffmpeg audio conversion failed: %w", err)
	}
	pcm := stdout.buffer.Bytes()
	if stdout.overflow {
		return nil, 0, fmt.Errorf("decoded audio is too large")
	}
	if len(pcm) == 0 {
		return nil, 0, fmt.Errorf("ffmpeg produced empty PCM audio")
	}
	if len(pcm)%2 != 0 {
		return nil, 0, fmt.Errorf("ffmpeg produced an incomplete PCM sample")
	}
	return append([]byte(nil), pcm...), outboundVoiceSampleRate, nil
}

func boundedFFmpegErrorDetail(raw string) string {
	detail := strings.Join(strings.Fields(strings.ToValidUTF8(raw, "�")), " ")
	const maxRunes = 512
	if runes := []rune(detail); len(runes) > maxRunes {
		detail = string(runes[:maxRunes])
	}
	return detail
}

func audioToSilk(ctx context.Context, data []byte, mimeType string, transcoder audioPCMTranscoder) ([]byte, int, error) {
	if len(data) == 0 {
		return nil, 0, fmt.Errorf("empty audio data")
	}
	if len(data) > outboundVoiceMaxInputBytes {
		return nil, 0, fmt.Errorf("audio input is too large: %d bytes exceeds %d", len(data), outboundVoiceMaxInputBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if isSilkData(data) {
		return data, 0, nil
	}

	pcm, sampleRate, channels, bitsPerSample, wavErr := wavToPCM(data)
	if wavErr != nil || sampleRate != outboundVoiceSampleRate || channels != 1 || bitsPerSample != 16 {
		if transcoder == nil {
			transcoder = ffmpegAudioPCMTranscoder{}
		}
		transcodeCtx, cancel := context.WithTimeout(ctx, outboundVoiceTimeout)
		defer cancel()
		var err error
		pcm, sampleRate, err = transcoder.TranscodeToPCM(transcodeCtx, data, mimeType)
		if err != nil {
			return nil, 0, fmt.Errorf("transcode Matrix audio to 16 kHz mono PCM: %w", err)
		}
	}
	if len(pcm) == 0 || len(pcm) > outboundVoiceMaxPCMBytes || sampleRate != outboundVoiceSampleRate || len(pcm)%2 != 0 {
		return nil, 0, fmt.Errorf("invalid 16 kHz 16-bit mono PCM audio")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	silkData, err := silk.Encode(
		bytes.NewReader(pcm),
		silk.SampleRate(sampleRate),
		silk.MaxInternal(outboundVoiceSampleRate),
		silk.BitRate(outboundVoiceBitRate),
		silk.Stx(true),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("encode WeChat SILK voice: %w", err)
	}
	if err := ctx.Err(); err != nil {
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
