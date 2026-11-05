package companion

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestListenerIdentityBeforeAnyKeyBytes(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unverified", true: "verified"}[trusted], func(t *testing.T) {
			local, peer := net.Pipe()
			defer peer.Close()
			checked := false
			dial := checkedDial(func(context.Context, string, string) (net.Conn, error) { return local, nil }, func(c net.Conn) error {
				checked = true
				if !trusted {
					return errors.New("wrong PID")
				}
				return nil
			})
			c, err := dial(context.Background(), "tcp", "test")
			if !checked {
				t.Fatal("identity check skipped")
			}
			if trusted {
				if err != nil || c != local {
					t.Fatal("verified connection refused")
				}
				c.Close()
			} else {
				if !errors.Is(err, ErrListenerIdentity) || c != nil {
					t.Fatal("unverified connection returned")
				}
				b := make([]byte, 1)
				if n, _ := peer.Read(b); n != 0 {
					t.Fatal("unverified listener received bytes")
				}
			}
		})
	}
}
