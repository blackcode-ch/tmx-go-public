package recorder

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
	"mime"
	"os"
	"strings"
)

// The 16 kHz track: mono PCM16 little-endian, 32 000 bytes a second, kept as a plain WAV
// whose header is rewritten after every chunk, so the file is valid at every moment (a
// crash or a closed window leaves a playable WAV of what arrived).
const (
	PCMRate       = 16000
	pcmBytesPerMS = PCMRate * 2 / 1000
	wavHeaderSize = 44
)

func createEmpty(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func appendFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func createWAV(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(wavHeader(0)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// appendPCM writes data after the first have PCM bytes of the WAV and fixes its header.
func appendPCM(path string, have int64, data []byte) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(data, wavHeaderSize+have); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteAt(wavHeader(have+int64(len(data))), 0); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// wavHeader is the 44-byte RIFF header of a 16 kHz mono PCM16 WAV with n data bytes.
func wavHeader(n int64) []byte {
	h := make([]byte, wavHeaderSize)
	le := binary.LittleEndian
	copy(h[0:], "RIFF")
	le.PutUint32(h[4:], uint32(36+n))
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	le.PutUint32(h[16:], 16)        // fmt chunk size
	le.PutUint16(h[20:], 1)         // PCM
	le.PutUint16(h[22:], 1)         // mono
	le.PutUint32(h[24:], PCMRate)   // sample rate
	le.PutUint32(h[28:], PCMRate*2) // byte rate
	le.PutUint16(h[32:], 2)         // block align
	le.PutUint16(h[34:], 16)        // bits per sample
	copy(h[36:], "data")
	le.PutUint32(h[40:], uint32(n))
	return h
}

// mediaExt is the file extension for the recorder's MIME type.
func mediaExt(mt string) string {
	base := baseType(mt)
	switch base {
	case "video/mp4":
		return ".mp4"
	case "audio/mp4":
		return ".m4a"
	case "video/webm", "audio/webm":
		return ".webm"
	case "audio/ogg", "video/ogg":
		return ".ogg"
	case "video/x-matroska":
		return ".mkv"
	}
	return ".bin"
}

// baseType is the MIME type without parameters (codecs).
func baseType(mt string) string {
	if t, _, err := mime.ParseMediaType(mt); err == nil {
		return t
	}
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		return strings.TrimSpace(mt[:i])
	}
	return mt
}

func newSum() hash.Hash { return sha256.New() }

func hexSum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

// hashFile feeds the file into h and returns its size.
func hashFile(path string, h hash.Hash) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(h, f)
}
