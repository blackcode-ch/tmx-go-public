package term

import "sync"

// ring keeps the last len(buf) bytes written to it.
type ring struct {
	mu   sync.Mutex
	buf  []byte
	next int  // write position
	full bool // buf has wrapped at least once
}

func newRing(size int) *ring { return &ring{buf: make([]byte, size)} }

func (r *ring) Write(b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(b) >= len(r.buf) {
		copy(r.buf, b[len(b)-len(r.buf):])
		r.next, r.full = 0, true
		return
	}
	n := copy(r.buf[r.next:], b)
	if n < len(b) {
		copy(r.buf, b[n:])
		r.full = true
	}
	r.next = (r.next + len(b)) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// Bytes returns a copy of the buffered output, oldest first.
func (r *ring) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]byte(nil), r.buf[:r.next]...)
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	return append(out, r.buf[:r.next]...)
}
