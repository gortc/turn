package turn

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
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

type ClientOptions struct {
	Conn net.Conn
	STUN STUNClient // optional STUN client
}

func NewClient(o ClientOptions) (*Client, error) {
	if o.Conn == nil {
		return nil, errors.New("connection not provided")
	}
	if o.STUN == nil {
		var err error
		o.STUN, err = stun.NewClient(stun.ClientOptions{
			Connection: o.Conn,
		})
		if err != nil {
			return nil, err
		}
	}
	c := &Client{
		stun: o.STUN,
		con:  o.Conn,
	}
	return c, nil
}

// STUNClient abstracts STUN protocol interaction.
type STUNClient interface {
	Indicate(m *stun.Message) error
	Start(m *stun.Message, h stun.Handler) error
}

// HandleEvent implements stun.Handler.
func (c *Client) HandleEvent(e stun.Event) {
	panic("not implemented")
}

func (c *Client) sendData(buf []byte, peerAddr *PeerAddress) (int, error) {
	err := c.stun.Indicate(stun.MustBuild(stun.TransactionID,
		stun.NewType(stun.MethodSend, stun.ClassIndication),
		Data(buf), peerAddr,
	))
	if err == nil {
		return len(buf), nil
	}
	return 0, err
}

func (c *Client) sendChan(buf []byte, n ChannelNumber) (int, error) {
	if !n.Valid() {
		return 0, ErrInvalidChannelNumber
	}
	d := &ChannelData{
		Data:   buf,
		Number: n,
	}
	d.Encode()
	return c.con.Write(d.Raw)
}

func (c *Client) handleBinding(p *Permission, n ChannelNumber, f stun.Handler) error {
	return c.stun.Start(stun.MustBuild(stun.TransactionID,
		stun.NewType(stun.MethodSend, stun.ClassIndication),
	), f)
}

var ErrNotImplemented = errors.New("functionality not implemented")

// Permission. Implements net.PacketConn.
type Permission struct {
	mux          *sync.RWMutex
	binding      bool
	bindErr      error
	number       uint32
	c            *Client
	readDeadline time.Time
	peerData     chan []byte
}

// Read data from peer.
func (p *Permission) Read(b []byte) (n int, err error) {
	p.mux.Lock()
	deadline := p.readDeadline
	p.mux.Unlock()
	select {
	case <-time.After(time.Until(deadline)):
		return 0, errors.New("deadline reached")
	case d := <-p.peerData:
		if len(b) < len(d) {
			go func() {
				p.peerData <- d
			}()
			return 0, io.ErrShortBuffer
		}
		return copy(b, d), nil
	}
}

// Bound returns true if channel number is bound for current permission.
func (p *Permission) Bound() bool {
	return atomic.LoadUint32(&p.number) == 0
}

// Binding returns current channel number or 0 if not bound.
func (p *Permission) Binding() ChannelNumber {
	return ChannelNumber(atomic.LoadUint32(&p.number))
}

// ErrBindingInProgress means that previous binding transaction for selected permission
// is still in progress.
var ErrBindingInProgress = errors.New("binding in progress")

// ErrAlreadyBound means that selected permission already has bound channel number.
var ErrAlreadyBound = errors.New("channel already bound")

// Bind performs binding transaction, allocating channel binding for
// the permission.
//
// TODO: Handle ctx cancellation
// TODO: Start binding refresh cycle
func (p *Permission) Bind(ctx context.Context, n ChannelNumber) error {
	p.mux.Lock()
	defer p.mux.Unlock()

	if p.binding {
		return ErrBindingInProgress
	}
	p.binding = true
	if p.number != 0 {
		return ErrAlreadyBound
	}

	// Starting transaction.
	done := make(chan struct{})
	if err := p.c.handleBinding(p, n, func(e stun.Event) {
		p.bindErr = e.Error
		p.binding = false
		if e.Error == nil {
			atomic.StoreUint32(&p.number, uint32(n))
		}
		done <- struct{}{}
	}); err != nil {
		return err
	}

	// Waiting until transaction is done.
	select {
	case <-done:
		return p.bindErr
	case <-ctx.Done():
		go func() {
			<-done
		}()
		return ctx.Err()
	}
}

// Write sends buffer to peer.
//
// If permission is bound, the ChannelData message will be used.
func (p *Permission) Write(b []byte) (n int, err error) {
	if n := atomic.LoadUint32(&p.number); n != 0 {
		return p.c.sendChan(b, ChannelNumber(n))
	}
	return p.c.sendData(b, &PeerAddress{})
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
