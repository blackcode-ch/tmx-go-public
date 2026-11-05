package nativerec

import (
	"encoding/binary"
	"os"

	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

// wavFile is a segment's 16 kHz mono PCM16 WAV as it is written. Its header is rewritten
// about every second, so the file is valid if the window process dies (the daemon also fixes
// the header from the file's size when it finishes the recording).
type wavFile struct {
	f        *os.File
	n        int64 // data bytes
	sinceHdr int64
	buf      []byte
}

const headerEvery = recorder.PCMRate * 2 // bytes: a second

func createWAV(path string) (*wavFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(recorder.WAVHeader(0)); err != nil {
		f.Close()
		return nil, err
	}
	return &wavFile{f: f}, nil
}

func (w *wavFile) write(samples []int16) error {
	if len(samples) == 0 {
		return nil
	}
	w.buf = w.buf[:0]
	for _, v := range samples {
		w.buf = binary.LittleEndian.AppendUint16(w.buf, uint16(v))
	}
	if _, err := w.f.WriteAt(w.buf, 44+w.n); err != nil {
		return err
	}
	w.n += int64(len(w.buf))
	w.sinceHdr += int64(len(w.buf))
	if w.sinceHdr >= headerEvery {
		w.sinceHdr = 0
		return w.header()
	}
	return nil
}

func (w *wavFile) header() error {
	_, err := w.f.WriteAt(recorder.WAVHeader(w.n), 0)
	return err
}

func (w *wavFile) close() error {
	err := w.header()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	return err
}
