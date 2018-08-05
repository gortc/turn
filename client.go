package turn

import (
	"errors"
	"net"
	"time"

	"github.com/gortc/stun"
)

// Client for TURN server.
//
// Provides transparent net.Conn interface to remote peer.
type Client struct {
	con  net.Conn
	stun STUNClient
}

// STUNClient abstracts STUN protocol interaction.
type STUNClient interface {
	Indicate(m *stun.Message) error
	Start(m *stun.Message, d time.Time, h stun.Handler)
}

// HandleEvent implements stun.Handler.
func (c *Client) HandleEvent(e stun.Event) {
	panic("not implemented")
}

func (c *Client) sendData(buf []byte, peerAddr *PeerAddress) error {
	return c.stun.Indicate(stun.MustBuild(stun.TransactionID,
		stun.NewType(stun.MethodSend, stun.ClassIndication),
		Data(buf), peerAddr,
	))
}

func (c *Client) sendChan(buf []byte, n ChannelNumber) error {
	if !n.Valid() {
		return ErrInvalidChannelNumber
	}
	d := &ChannelData{
		Data:   buf,
		Number: n,
	}
	d.Encode()
	_, err := c.con.Write(d.Raw)
	return err
}

var ErrNotImplemented = errors.New("functionality not implemented")

// Permission. Implements net.PacketConn.
type Permission struct {
	useChanData bool // use ChannelData Messages
	c           *Client
}

func (Permission) Read(b []byte) (n int, err error) {
	panic("implement me")
	// Block until client receives data from peer.
}

func (p *Permission) Write(b []byte) (n int, err error) {
	panic("implement me")
	if p.useChanData {
		return len(b), p.c.sendChan(b, ChannelNumber(1))
	}
	return len(b), p.c.sendData(b, &PeerAddress{})
}

func (Permission) Close() error {
	return ErrNotImplemented
}

// LocalAddr is relayed address from TURN server.
func (Permission) LocalAddr() net.Addr {
	panic("implement me")
}

// RemoteAddr is peer address.
func (Permission) RemoteAddr() net.Addr {
	panic("implement me")
}

func (Permission) SetDeadline(t time.Time) error {
	return ErrNotImplemented
}

func (Permission) SetReadDeadline(t time.Time) error {
	return ErrNotImplemented
}

func (Permission) SetWriteDeadline(t time.Time) error {
	return ErrNotImplemented
}
