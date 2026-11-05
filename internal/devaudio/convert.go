package devaudio

import (
	"encoding/binary"
	"math"
)

// convert appends frames of data (in format f) to dst as interleaved Int16: mono stays mono,
// stereo stays stereo, and more channels are folded into stereo (see weights).
func convert(dst []int16, data []byte, frames int, f Format) []int16 {
	ch := f.Channels
	bps := f.Bits / 8
	at := func(frame, c int) float64 { return sample(data[(frame*ch+c)*bps:], f) }
	switch ch {
	case 1:
		for i := 0; i < frames; i++ {
			dst = append(dst, toInt16(at(i, 0)))
		}
	case 2:
		for i := 0; i < frames; i++ {
			dst = append(dst, toInt16(at(i, 0)), toInt16(at(i, 1)))
		}
	default:
		wl, wr := weights(f)
		for i := 0; i < frames; i++ {
			var l, r float64
			for c := 0; c < ch; c++ {
				v := at(i, c)
				l += v * wl[c]
				r += v * wr[c]
			}
			dst = append(dst, toInt16(l), toInt16(r))
		}
	}
	return dst
}

// The speaker positions of a WAVE channel mask (ksmedia.h SPEAKER_*), lowest bit first: the
// channels of a frame come in this order.
const (
	spFL  = 1 << iota // front left
	spFR              // front right
	spFC              // front centre
	spLFE             // subwoofer
	spBL              // back left
	spBR              // back right
	spFLC             // front left of centre
	spFRC             // front right of centre
	spBC              // back centre
	spSL              // side left
	spSR              // side right
	spTC              // top centre
	spTFL             // top front left
	spTFC             // top front centre
	spTFR             // top front right
	spTBL             // top back left
	spTBC             // top back centre
	spTBR             // top back right
)

// weights folds more than two channels into stereo, the usual way: the front left and right
// as they are, every other left or right speaker at -3 dB into its side, a centre one at -3 dB
// into both, the subwoofer left out. The channels' speakers come from f.Mask; without one (or
// one that names fewer speakers than there are channels) they are taken in the mask's order.
func weights(f Format) (wl, wr []float64) {
	const g = math.Sqrt2 / 2
	wl, wr = make([]float64, f.Channels), make([]float64, f.Channels)
	mask := f.Mask
	if bitsSet(mask) < f.Channels {
		mask = 1<<f.Channels - 1
	}
	c := 0
	for bit := uint32(1); bit != 0 && c < f.Channels; bit <<= 1 {
		if mask&bit == 0 {
			continue
		}
		switch bit {
		case spFL:
			wl[c] = 1
		case spFR:
			wr[c] = 1
		case spLFE:
		case spFC, spBC, spTC, spTFC, spTBC:
			wl[c], wr[c] = g, g
		case spBL, spFLC, spSL, spTFL, spTBL:
			wl[c] = g
		case spBR, spFRC, spSR, spTFR, spTBR:
			wr[c] = g
		default: // a speaker without a side (reserved bits)
			wl[c], wr[c] = g/2, g/2
		}
		c++
	}
	return wl, wr
}

func bitsSet(m uint32) int {
	n := 0
	for ; m != 0; m &= m - 1 {
		n++
	}
	return n
}

// sample reads one sample at the start of b as a value in [-1, 1].
func sample(b []byte, f Format) float64 {
	switch {
	case f.Float:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
	case f.Bits == 16:
		return float64(int16(binary.LittleEndian.Uint16(b))) / 32768
	case f.Bits == 24:
		v := int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8
		return float64(v) / (1 << 23)
	default: // 32-bit integers (also 24 valid bits in a 32-bit container: the low byte is 0)
		return float64(int32(binary.LittleEndian.Uint32(b))) / (1 << 31)
	}
}

// toInt16 scales v (nominally [-1, 1]) by 32768 and clips it to Int16, so 16-bit input comes
// back exactly as it was.
func toInt16(v float64) int16 {
	if v != v { // NaN
		return 0
	}
	v = math.Round(v * 32768)
	if v <= math.MinInt16 {
		return math.MinInt16
	}
	if v >= math.MaxInt16 {
		return math.MaxInt16
	}
	return int16(v)
}

func appendSilence(dst []int16, n int) []int16 {
	for range n {
		dst = append(dst, 0)
	}
	return dst
}
