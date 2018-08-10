package turn

import (
	"bytes"
	"net"
	"testing"

	"github.com/gortc/stun"
)

type testSTUN struct {
	indicate func(m *stun.Message) error
	do       func(m *stun.Message, f func(e stun.Event)) error
}

func (t testSTUN) Indicate(m *stun.Message) error { return t.indicate(m) }

func (t testSTUN) Do(m *stun.Message, f func(e stun.Event)) error { return t.do(m, f) }

func TestClient_Allocate(t *testing.T) {
	connL, connR := net.Pipe()
	connL.Close()
	stunClient := &testSTUN{}
	c, createErr := NewClient(ClientOptions{
		Conn: connR, // should not be used
		STUN: stunClient,
	})
	if createErr != nil {
		t.Fatal(createErr)
	}
	stunClient.indicate = func(m *stun.Message) error {
		t.Fatal("should not be called")
		return nil
	}
	stunClient.do = func(m *stun.Message, f func(e stun.Event)) error {
		f(stun.Event{
			Message: stun.MustBuild(m, stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse),
				&RelayedAddress{
					Port: 1113,
					IP:   net.IPv4(127, 0, 0, 2),
				},
				stun.Fingerprint,
			),
		})
		return nil
	}
	a, allocErr := c.Allocate()
	if allocErr != nil {
		t.Fatal(allocErr)
	}
	peer := PeerAddress{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 1001,
	}
	p, permErr := a.CreateUDP(peer)
	if permErr != nil {
		t.Fatal(allocErr)
	}
	stunClient.indicate = func(m *stun.Message) error {
		var (
			data     Data
			peerAddr PeerAddress
		)
		if err := m.Parse(&data, &peerAddr); err != nil {
			return err
		}
		go c.stunHandler(stun.Event{
			Message: stun.MustBuild(stun.TransactionID,
				stun.NewType(stun.MethodData, stun.ClassIndication),
				data, peerAddr,
				stun.Fingerprint,
			),
		})
		return nil
	}
	if _, writeErr := p.Write([]byte{1, 2, 3, 4}); writeErr != nil {
		t.Fatal(writeErr)
	}
	buf := make([]byte, 1500)
	n, readErr := p.Read(buf)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(buf[:n], []byte{1, 2, 3, 4}) {
		t.Error("data mismatch")
	}
}
