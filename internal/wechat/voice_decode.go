package wechat

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/wdvxdr1123/go-silk/sdk"
	"modernc.org/libc"
	"modernc.org/libc/sys/types"
)

const (
	maxSilkInternalFrames = 5
	maxSilkFrameSamples   = 480
)

// The translated SDK exposes C-style uintptr handles that checkptr cannot validate.
//
//go:nocheckptr
func decodeSilkPCM(data []byte) ([]byte, error) {
	_, packets, err := parseSilkPackets(data)
	if err != nil {
		return nil, fmt.Errorf("invalid SILK structure: %w", err)
	}

	tls := libc.NewTLS()
	if tls == nil {
		return nil, fmt.Errorf("create SILK decoder TLS")
	}
	defer tls.Close()

	var decoderBytes int32
	if ret := sdk.SDK_Get_Decoder_Size(tls, uintptr(unsafe.Pointer(&decoderBytes))); ret != 0 {
		runtime.KeepAlive(&decoderBytes)
		return nil, fmt.Errorf("get SILK decoder size returned %d", ret)
	}
	runtime.KeepAlive(&decoderBytes)
	if decoderBytes <= 0 {
		return nil, fmt.Errorf("invalid SILK decoder size %d", decoderBytes)
	}

	decoder := libc.Xmalloc(tls, types.Size_t(decoderBytes))
	if decoder == 0 {
		return nil, fmt.Errorf("allocate %d-byte SILK decoder", decoderBytes)
	}
	defer libc.Xfree(tls, decoder)

	if ret := sdk.SDK_InitDecoder(tls, decoder); ret != 0 {
		return nil, fmt.Errorf("initialize SILK decoder returned %d", ret)
	}

	control := sdk.SDK_DecControlStruct{
		FAPI_sampleRate:  voiceSampleRate,
		FframesPerPacket: 1,
	}
	pcm := make([]byte, 0, len(packets)*maxSilkFrameSamples*2)
	output := make([]int16, maxSilkFrameSamples)
	for packetIndex, packet := range packets {
		payload := data[packet.PayloadStart:packet.PayloadEnd]
		for internalFrame := 0; ; internalFrame++ {
			if internalFrame >= maxSilkInternalFrames {
				return nil, fmt.Errorf("SILK packet %d exceeds %d internal frames", packetIndex, maxSilkInternalFrames)
			}

			nSamplesOut := int16(maxSilkFrameSamples)
			ret := sdk.SDK_Decode(
				tls,
				decoder,
				uintptr(unsafe.Pointer(&control)),
				0,
				uintptr(unsafe.Pointer(&payload[0])),
				int32(len(payload)),
				uintptr(unsafe.Pointer(&output[0])),
				uintptr(unsafe.Pointer(&nSamplesOut)),
			)
			runtime.KeepAlive(data)
			runtime.KeepAlive(payload)
			runtime.KeepAlive(&control)
			runtime.KeepAlive(output)
			runtime.KeepAlive(&nSamplesOut)
			if ret != 0 {
				return nil, fmt.Errorf("decode SILK packet %d internal frame %d returned %d", packetIndex, internalFrame, ret)
			}
			if nSamplesOut < 1 || nSamplesOut > maxSilkFrameSamples {
				return nil, fmt.Errorf("decode SILK packet %d internal frame %d produced %d samples", packetIndex, internalFrame, nSamplesOut)
			}

			pcmStart := len(pcm)
			pcm = append(pcm, make([]byte, int(nSamplesOut)*2)...)
			for sampleIndex, sample := range output[:int(nSamplesOut)] {
				binary.LittleEndian.PutUint16(pcm[pcmStart+sampleIndex*2:], uint16(sample))
			}
			if control.FmoreInternalDecoderFrames == 0 {
				break
			}
		}
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("decoded SILK contains empty PCM")
	}
	return pcm, nil
}
