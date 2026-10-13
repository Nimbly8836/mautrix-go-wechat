package wechat

import "encoding/binary"

// The source datasize can describe a different CDN rendition. In that case it
// cannot prove download completeness. Require a bounded ISO-BMFF container with
// a movie, a video track and media data; every parsed box must fit in its parent.
// This checks structural completeness, not full codec/bitstream correctness.
func completeRecordMP4(data []byte) bool {
	var fileType, movie, videoTrack, mediaData bool
	valid := walkRecordMP4Boxes(data, func(kind string, payload []byte) bool {
		switch kind {
		case "ftyp":
			fileType = len(payload) >= 8 && len(payload)%4 == 0
			return fileType
		case "moov":
			var header bool
			if !walkRecordMP4Boxes(payload, func(kind string, body []byte) bool {
				switch kind {
				case "mvhd":
					header = len(body) >= 20
				case "trak":
					if !walkRecordMP4Boxes(body, func(kind string, track []byte) bool {
						if kind != "mdia" {
							return true
						}
						return walkRecordMP4Boxes(track, func(kind string, media []byte) bool {
							if kind == "hdlr" && len(media) >= 12 && string(media[8:12]) == "vide" {
								videoTrack = true
							}
							return true
						})
					}) {
						return false
					}
				}
				return true
			}) {
				return false
			}
			movie = header
		case "mdat":
			mediaData = mediaData || len(payload) > 0
		}
		return true
	})
	return valid && fileType && movie && videoTrack && mediaData
}

func walkRecordMP4Boxes(data []byte, visit func(string, []byte) bool) bool {
	for len(data) > 0 {
		if len(data) < 8 {
			return false
		}
		size := uint64(binary.BigEndian.Uint32(data[:4]))
		header := uint64(8)
		kind := string(data[4:8])
		if size == 1 {
			if len(data) < 16 {
				return false
			}
			size, header = binary.BigEndian.Uint64(data[8:16]), 16
		} else if size == 0 {
			size = uint64(len(data))
		}
		if size < header || size > uint64(len(data)) {
			return false
		}
		if !visit(kind, data[header:size]) {
			return false
		}
		data = data[size:]
	}
	return true
}
