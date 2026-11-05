package transcribe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// WAV files: enough of RIFF to find the PCM data, measure it and cut a long file into parts
// (whisper-cli reads a whole file into memory, so a long recording runs part by part). The
// engine itself decodes WAV, FLAC, MP3 and Ogg and resamples to 16 kHz.

type wavInfo struct {
	Format     uint16 // 1 = PCM
	Channels   uint16
	Rate       uint32
	Bits       uint16
	DataOffset int64
	DataSize   int64
}

var errNotWAV = errors.New("not a WAV file")

// readWAV reads the header of the WAV file at path.
func readWAV(path string) (wavInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return wavInfo{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return wavInfo{}, err
	}
	return parseWAV(f, st.Size())
}

func parseWAV(r io.ReadSeeker, size int64) (wavInfo, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return wavInfo{}, errNotWAV
	}
	var w wavInfo
	pos := int64(12)
	haveFmt := false
	for i := 0; i < 64; i++ { // a WAV has a handful of chunks; stop on garbage
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			break
		}
		id, n := string(ch[0:4]), int64(binary.LittleEndian.Uint32(ch[4:8]))
		pos += 8
		switch id {
		case "fmt ":
			if n < 16 {
				return wavInfo{}, errors.New("a WAV file with a broken format header")
			}
			var fm [16]byte
			if _, err := io.ReadFull(r, fm[:]); err != nil {
				return wavInfo{}, err
			}
			w.Format = binary.LittleEndian.Uint16(fm[0:2])
			w.Channels = binary.LittleEndian.Uint16(fm[2:4])
			w.Rate = binary.LittleEndian.Uint32(fm[4:8])
			w.Bits = binary.LittleEndian.Uint16(fm[14:16])
			read := int64(16)
			if w.Format == 0xFFFE && n >= 40 { // WAVE_FORMAT_EXTENSIBLE: the sub-format GUID starts with the format
				var ext [24]byte
				if _, err := io.ReadFull(r, ext[:]); err != nil {
					return wavInfo{}, err
				}
				w.Format = binary.LittleEndian.Uint16(ext[8:10])
				read += 24
			}
			haveFmt = true
			if _, err := r.Seek(n-read+n%2, io.SeekCurrent); err != nil {
				return wavInfo{}, err
			}
			pos += n + n%2
		case "data":
			if !haveFmt {
				return wavInfo{}, errors.New("a WAV file without a format header")
			}
			w.DataOffset = pos
			// A writer that never finished leaves 0 or 0xFFFFFFFF: the data runs to the end.
			if n == 0 || n == 0xFFFFFFFF || pos+n > size {
				n = size - pos
			}
			w.DataSize = n
			if w.Channels == 0 || w.Rate == 0 || w.Bits == 0 {
				return wavInfo{}, errors.New("a WAV file with a broken format header")
			}
			return w, nil
		default:
			if _, err := r.Seek(n+n%2, io.SeekCurrent); err != nil {
				return wavInfo{}, err
			}
			pos += n + n%2
		}
	}
	return wavInfo{}, errors.New("a WAV file without audio data")
}

func (w wavInfo) frameBytes() int64 { return int64(w.Channels) * int64(w.Bits/8) }

// Duration of the audio.
func (w wavInfo) Duration() time.Duration {
	fb := w.frameBytes()
	if fb == 0 || w.Rate == 0 {
		return 0
	}
	return time.Duration(w.DataSize / fb * int64(time.Second) / int64(w.Rate))
}

// pcm16 reports whether the data is plain 16-bit PCM (what splitWAV can cut).
func (w wavInfo) pcm16() bool { return w.Format == 1 && w.Bits == 16 }

// wavHeader is a 44-byte header for PCM data of n bytes.
func wavHeader(channels uint16, rate uint32, bits uint16, n int64) []byte {
	b := make([]byte, 44)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+n))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], channels)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*uint32(channels)*uint32(bits/8))
	binary.LittleEndian.PutUint16(b[32:], channels*bits/8)
	binary.LittleEndian.PutUint16(b[34:], bits)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(n))
	return b
}

// part is a piece of a long file, starting at Offset into the original.
type part struct {
	Path   string
	Offset time.Duration
}

// splitWAV cuts a long 16-bit PCM WAV into files in dir, each about as long as every. A cut
// moves to the quietest moment in the 3 s before it, so it rarely falls in the middle of a word.
func splitWAV(path string, w wavInfo, every time.Duration, dir string) ([]part, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fb := w.frameBytes()
	perSec := int64(w.Rate) * fb
	step := int64(every/time.Second) * perSec
	total := w.DataSize / fb * fb
	var parts []part
	for start, i := int64(0), 0; start < total; i++ {
		end := start + step
		if end >= total || total-end < step/4 { // no tiny last part
			end = total
		} else {
			// The cut moves back by at most a quarter of a part, so every part has audio.
			end = max(quietCut(f, w, end, min(3*perSec, step/4)), start+fb)
		}
		p := filepath.Join(dir, fmt.Sprintf("part-%03d.wav", i))
		if err := writePart(f, w, start, end, p); err != nil {
			return nil, err
		}
		parts = append(parts, part{Path: p, Offset: time.Duration(start / fb * int64(time.Second) / int64(w.Rate))})
		start = end
	}
	return parts, nil
}

func writePart(f *os.File, w wavInfo, start, end int64, path string) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = out.Write(wavHeader(w.Channels, w.Rate, w.Bits, end-start))
	if err == nil {
		_, err = io.Copy(out, io.NewSectionReader(f, w.DataOffset+start, end-start))
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// quietCut returns the start of the quietest 100 ms window in [at-span, at] (frame aligned).
func quietCut(f *os.File, w wavInfo, at, span int64) int64 {
	fb := w.frameBytes()
	from := at - span
	if from < 0 {
		from = 0
	}
	buf := make([]byte, at-from)
	if _, err := f.ReadAt(buf, w.DataOffset+from); err != nil {
		return at
	}
	win := int64(w.Rate) / 10 * fb
	best, bestAt := int64(-1), at
	for off := int64(0); off+win <= int64(len(buf)); off += win / 2 {
		var e int64
		for i := off; i+1 < off+win; i += 2 {
			s := int64(int16(binary.LittleEndian.Uint16(buf[i:])))
			e += s * s
		}
		if best < 0 || e < best {
			best, bestAt = e, from+off
		}
	}
	return bestAt / fb * fb
}

// audioKind names the format of a file by its first bytes ("" = not one the engine reads).
func audioKind(head []byte) string {
	switch {
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WAVE":
		return "wav"
	case bytes.HasPrefix(head, []byte("fLaC")):
		return "flac"
	case bytes.HasPrefix(head, []byte("OggS")):
		return "ogg"
	case bytes.HasPrefix(head, []byte("ID3")), len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return "mp3"
	}
	return ""
}
