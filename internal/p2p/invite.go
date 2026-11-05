package p2p

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// LinkPrefix starts every invite link; the rest is the base64url (no padding) payload.
const LinkPrefix = "tmx://join/"

const (
	payloadVersion = 1
	secretLen      = 16 // 128 bits
	maxNameBytes   = 40
	maxGroupID     = 40
	maxInviteAddrs = 6
	maxLinkLen     = 2048
)

const flagGroup = 1 << 0

// payload is what an invite link carries (DESIGN.md "Peer-to-peer network"). Binary,
// length-prefixed, so a DM link with a few addresses stays around 200 characters: a QR code
// of version ~10, easy to scan from a phone screen.
type payload struct {
	Kind      InviteKind
	PeerID    peer.ID
	Addrs     []ma.Multiaddr
	Secret    []byte
	ExpiresAt int64 // unix ms (encoded in seconds)
	Name      string
	GroupID   string
	GroupName string
}

var errBadLink = errors.New("this is not a valid tmx invite link")

func putBytes(b []byte, v []byte) []byte {
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

func truncUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func (p payload) encode() (string, error) {
	if len(p.Secret) != secretLen {
		return "", errors.New("invite secret must be 16 bytes")
	}
	pid, err := p.PeerID.MarshalBinary()
	if err != nil {
		return "", err
	}
	var flags byte
	if p.Kind == InviteGroup {
		flags |= flagGroup
	}
	b := []byte{payloadVersion, flags}
	b = putBytes(b, pid)
	addrs := p.Addrs
	if len(addrs) > maxInviteAddrs {
		addrs = addrs[:maxInviteAddrs]
	}
	b = binary.AppendUvarint(b, uint64(len(addrs)))
	for _, a := range addrs {
		b = putBytes(b, a.Bytes())
	}
	b = append(b, p.Secret...)
	b = binary.AppendUvarint(b, uint64(max(p.ExpiresAt, 0)/1000))
	b = putBytes(b, []byte(truncUTF8(p.Name, maxNameBytes)))
	if p.Kind == InviteGroup {
		b = putBytes(b, []byte(truncUTF8(p.GroupID, maxGroupID)))
		b = putBytes(b, []byte(truncUTF8(p.GroupName, maxNameBytes)))
	}
	return LinkPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = errBadLink
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) bytes(n uint64) []byte {
	if r.err != nil {
		return nil
	}
	if n > uint64(len(r.b)) {
		r.err = errBadLink
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *reader) field(limit uint64) []byte {
	n := r.uvarint()
	if n > limit {
		r.err = errBadLink
		return nil
	}
	return r.bytes(n)
}

// parseLink decodes an invite link. It accepts the link with surrounding whitespace or
// pasted inside other text (the first tmx://join/ token wins).
func parseLink(link string) (payload, error) {
	var p payload
	s := strings.TrimSpace(link)
	if i := strings.Index(s, LinkPrefix); i >= 0 {
		s = s[i+len(LinkPrefix):]
	} else {
		return p, errBadLink
	}
	if j := strings.IndexAny(s, " \t\r\n\"'<>)"); j >= 0 {
		s = s[:j]
	}
	if len(s) == 0 || len(s) > maxLinkLen {
		return p, errBadLink
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return p, errBadLink
	}
	if len(raw) < 2 {
		return p, errBadLink
	}
	if raw[0] != payloadVersion {
		return p, fmt.Errorf("this invite was made by a newer tmx (format %d); update tmx to join", raw[0])
	}
	flags := raw[1]
	r := &reader{b: raw[2:]}
	pid, err := peer.IDFromBytes(r.field(128))
	if r.err != nil || err != nil {
		return p, errBadLink
	}
	p.PeerID = pid
	n := r.uvarint()
	if n > maxInviteAddrs*2 {
		return p, errBadLink
	}
	for i := uint64(0); i < n && r.err == nil; i++ {
		ab := r.field(512)
		if r.err != nil {
			break
		}
		a, err := ma.NewMultiaddrBytes(ab)
		if err != nil {
			continue // an address type this build does not know: skip it, keep the rest
		}
		p.Addrs = append(p.Addrs, a)
	}
	p.Secret = append([]byte(nil), r.bytes(secretLen)...)
	p.ExpiresAt = int64(r.uvarint()) * 1000
	p.Name = string(r.field(maxNameBytes * 4))
	p.Kind = InviteDM
	if flags&flagGroup != 0 {
		p.Kind = InviteGroup
		p.GroupID = string(r.field(maxGroupID * 4))
		p.GroupName = string(r.field(maxNameBytes * 4))
	}
	if r.err != nil {
		return payload{}, errBadLink
	}
	if !utf8.ValidString(p.Name) || !utf8.ValidString(p.GroupID) || !utf8.ValidString(p.GroupName) {
		return payload{}, errBadLink
	}
	return p, nil
}

// pairProof proves knowledge of an invite's secret, bound to both peer IDs so it cannot be
// replayed to another node or by another joiner. The secret itself never crosses the wire.
func pairProof(secret []byte, inviter, joiner peer.ID) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("tmx-pair-v1\x00"))
	m.Write([]byte(inviter))
	m.Write([]byte{0})
	m.Write([]byte(joiner))
	return m.Sum(nil)
}

// SafetyCode is the short code both sides of a pairing can compare (in person or on a call)
// to rule out a swapped link: 6 digits from SHA-256 of the two peer IDs (order-independent).
// It depends only on the two identities, so it stays the same for the life of the contact.
func SafetyCode(a, b string) string {
	if a > b {
		a, b = b, a
	}
	h := sha256.Sum256([]byte("tmx-safety-v1\x00" + a + "\x00" + b))
	n := binary.BigEndian.Uint32(h[:4]) % 1_000_000
	return fmt.Sprintf("%03d %03d", n/1000, n%1000)
}
