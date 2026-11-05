package devaudio

import (
	"encoding/binary"
	"math"
	"testing"
)

func f32(vs ...float32) []byte {
	b := make([]byte, 4*len(vs))
	for i, v := range vs {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	return b
}

func i16(vs ...int16) []byte {
	b := make([]byte, 2*len(vs))
	for i, v := range vs {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func eq(t *testing.T, got, want []int16) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d samples %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("sample %d: got %d, want %d (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestToInt16(t *testing.T) {
	for _, c := range []struct {
		v    float64
		want int16
	}{
		{0, 0}, {1, 32767}, {-1, -32768}, {2, 32767}, {-3, -32768}, {0.5, 16384}, {-0.5, -16384},
		{1.0 / 32767, 1}, {math.NaN(), 0}, {math.Inf(1), 32767}, {math.Inf(-1), -32768},
	} {
		if got := toInt16(c.v); got != c.want {
			t.Errorf("toInt16(%v) = %d, want %d", c.v, got, c.want)
		}
	}
}

func TestConvertFloatStereo(t *testing.T) {
	f := Format{Rate: 48000, Channels: 2, Bits: 32, Float: true}
	got := convert(nil, f32(0, 1, -1, 0.5, 1.5, -0.25), 3, f)
	eq(t, got, []int16{0, 32767, -32768, 16384, 32767, -8192})
}

func TestConvertInt16MonoAndAppend(t *testing.T) {
	f := Format{Rate: 44100, Channels: 1, Bits: 16}
	got := convert([]int16{7}, i16(-32768, 0, 32767, 123), 4, f)
	eq(t, got, []int16{7, -32768, 0, 32767, 123})
}

func TestConvertInt24AndInt32(t *testing.T) {
	// 24-bit packed: 0x7FFFFF (max), 0x800000 (min), 0x000100 (256 → 1 in 16 bits)
	b24 := []byte{0xFF, 0xFF, 0x7F, 0x00, 0x00, 0x80, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00}
	got := convert(nil, b24, 2, Format{Rate: 48000, Channels: 2, Bits: 24})
	eq(t, got, []int16{32767, -32768, 1, 0})

	b32 := make([]byte, 8)
	binary.LittleEndian.PutUint32(b32, 0x40000000)     // 0.5
	binary.LittleEndian.PutUint32(b32[4:], 0x80000000) // -1
	got = convert(nil, b32, 2, Format{Rate: 48000, Channels: 1, Bits: 32})
	eq(t, got, []int16{16384, -32768})
}

func TestConvertFiveOneFoldsToStereo(t *testing.T) {
	// FL FR FC LFE BL BR (mask 0x3F): FL and FR as they are, FC into both, BL/BR into their side.
	f := Format{Rate: 48000, Channels: 6, Bits: 32, Float: true, Mask: 0x3F}
	g := float32(math.Sqrt2 / 2)
	cases := []struct {
		frame []float32
		l, r  int16
	}{
		{[]float32{0.5, 0, 0, 0, 0, 0}, 16384, 0},
		{[]float32{0, 0.5, 0, 0, 0, 0}, 0, 16384},
		{[]float32{0, 0, 0.5, 0, 0, 0}, toInt16(float64(0.5 * g)), toInt16(float64(0.5 * g))},
		{[]float32{0, 0, 0, 0.9, 0, 0}, 0, 0}, // the subwoofer is left out
		{[]float32{0, 0, 0, 0, 0.5, 0}, toInt16(float64(0.5 * g)), 0},
		{[]float32{0, 0, 0, 0, 0, 0.5}, 0, toInt16(float64(0.5 * g))},
	}
	for i, c := range cases {
		got := convert(nil, f32(c.frame...), 1, f)
		if len(got) != 2 || absDiff(got[0], c.l) > 1 || absDiff(got[1], c.r) > 1 {
			t.Errorf("case %d: got %v, want [%d %d]", i, got, c.l, c.r)
		}
	}
}

func TestConvertQuadUsesTheMask(t *testing.T) {
	// FL FR BL BR (mask 0x33): the third channel is back left, not the centre.
	f := Format{Rate: 48000, Channels: 4, Bits: 16, Mask: 0x33}
	got := convert(nil, i16(0, 0, 16384, 0), 1, f)
	if got[0] == 0 || got[1] != 0 {
		t.Fatalf("back left went to %v, want left only", got)
	}
	// Without a mask the standard order applies (FL FR FC LFE): the third is the centre.
	f.Mask = 0
	got = convert(nil, i16(0, 0, 16384, 0), 1, f)
	if got[0] == 0 || got[0] != got[1] {
		t.Fatalf("centre went to %v, want both sides alike", got)
	}
}

func absDiff(a, b int16) int {
	d := int(a) - int(b)
	if d < 0 {
		return -d
	}
	return d
}

// waveFormatBytes builds a WAVEFORMATEX (or, for waveFormatExtensible, a WAVEFORMATEXTENSIBLE
// with subformat sub) as GetMixFormat gives it.
func waveFormatBytes(tag, ch, rate, bits, align int, sub uint32, mask uint32) []byte {
	b := make([]byte, 18)
	binary.LittleEndian.PutUint16(b[0:], uint16(tag))
	binary.LittleEndian.PutUint16(b[2:], uint16(ch))
	binary.LittleEndian.PutUint32(b[4:], uint32(rate))
	binary.LittleEndian.PutUint32(b[8:], uint32(rate*align))
	binary.LittleEndian.PutUint16(b[12:], uint16(align))
	binary.LittleEndian.PutUint16(b[14:], uint16(bits))
	if tag != waveFormatExtensible {
		return b
	}
	binary.LittleEndian.PutUint16(b[16:], 22)
	ext := make([]byte, 22)
	binary.LittleEndian.PutUint16(ext[0:], uint16(bits)) // valid bits
	binary.LittleEndian.PutUint32(ext[2:], mask)
	binary.LittleEndian.PutUint32(ext[6:], sub)
	copy(ext[10:], ksSubtypeTail)
	return append(b, ext...)
}

func TestParseWaveFormat(t *testing.T) {
	f, err := parseWaveFormat(waveFormatBytes(waveFormatExtensible, 2, 48000, 32, 8, waveFormatIEEEFloat, 3))
	if err != nil || f != (Format{Rate: 48000, Channels: 2, Bits: 32, Float: true, Mask: 3}) {
		t.Fatalf("extensible float: %+v, %v", f, err)
	}
	f, err = parseWaveFormat(waveFormatBytes(waveFormatPCM, 2, 44100, 16, 4, 0, 0))
	if err != nil || f != (Format{Rate: 44100, Channels: 2, Bits: 16}) {
		t.Fatalf("plain PCM: %+v, %v", f, err)
	}
	f, err = parseWaveFormat(waveFormatBytes(waveFormatExtensible, 6, 48000, 24, 18, waveFormatPCM, 0x3F))
	if err != nil || f != (Format{Rate: 48000, Channels: 6, Bits: 24, Mask: 0x3F}) {
		t.Fatalf("extensible 24-bit 5.1: %+v, %v", f, err)
	}
	bad := waveFormatBytes(waveFormatExtensible, 2, 48000, 32, 8, waveFormatIEEEFloat, 3)
	bad[39] ^= 0xFF // not a KSDATAFORMAT subtype from a WAVE tag
	for name, b := range map[string][]byte{
		"short":       {1, 0, 2, 0},
		"short ext":   waveFormatBytes(waveFormatExtensible, 2, 48000, 32, 8, waveFormatIEEEFloat, 3)[:30],
		"other guid":  bad,
		"mp3":         waveFormatBytes(0x55, 2, 48000, 16, 4, 0, 0),
		"8-bit":       waveFormatBytes(waveFormatPCM, 1, 8000, 8, 1, 0, 0),
		"64-bit":      waveFormatBytes(waveFormatIEEEFloat, 2, 48000, 64, 16, 0, 0),
		"block align": waveFormatBytes(waveFormatPCM, 2, 48000, 16, 6, 0, 0),
	} {
		if _, err := parseWaveFormat(b); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
