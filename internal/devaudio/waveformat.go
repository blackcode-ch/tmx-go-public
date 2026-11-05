package devaudio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// waveFormatExSize is the size of a WAVEFORMATEX without its extra bytes (cbSize).
const waveFormatExSize = 18

const (
	waveFormatPCM        = 0x0001
	waveFormatIEEEFloat  = 0x0003
	waveFormatExtensible = 0xFFFE
)

// ksSubtypeTail is the part after Data1 of every KSDATAFORMAT_SUBTYPE_* GUID made from a WAVE
// format tag ({tag-0000-0010-8000-00AA00389B71}), as stored.
var ksSubtypeTail = []byte{0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}

// parseWaveFormat reads a WAVEFORMATEX or WAVEFORMATEXTENSIBLE (what IAudioClient::GetMixFormat
// gives: little-endian, packed).
func parseWaveFormat(b []byte) (Format, error) {
	if len(b) < 16 {
		return Format{}, errors.New("the sound output's format is too short")
	}
	le16 := func(o int) int { return int(binary.LittleEndian.Uint16(b[o:])) }
	tag := le16(0)
	f := Format{Channels: le16(2), Rate: int(binary.LittleEndian.Uint32(b[4:])), Bits: le16(14)}
	align := le16(12)
	if tag == waveFormatExtensible {
		if len(b) < 40 {
			return Format{}, errors.New("the sound output's format is too short")
		}
		f.Mask = binary.LittleEndian.Uint32(b[20:])
		if !bytes.Equal(b[28:40], ksSubtypeTail) {
			return Format{}, errors.New("the sound output's format is not PCM")
		}
		tag = int(binary.LittleEndian.Uint32(b[24:]))
	}
	switch tag {
	case waveFormatPCM:
	case waveFormatIEEEFloat:
		f.Float = true
	default:
		return Format{}, fmt.Errorf("the sound output's format (0x%04X) is not supported", tag)
	}
	if err := f.check(); err != nil {
		return Format{}, err
	}
	if align != f.frameBytes() {
		return Format{}, fmt.Errorf("the sound output's frames are %d bytes, not %d", align, f.frameBytes())
	}
	return f, nil
}
