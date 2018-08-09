package turn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gortc/stun"
)

// Client for TURN server.
//
// Provides transparent net.Conn interfaces to remote peers.
type Client struct {
	con       net.Conn
	stun      STUNClient
	p         []Permission
	mux       sync.Mutex
	nonce     stun.Nonce
	username  stun.Username
	password  string
	realm     stun.Realm
	reladdr   RelayedAddress
	reflexive stun.XORMappedAddress
	integrity stun.MessageIntegrity
}

type ClientOptions struct {
	Conn net.Conn
	STUN STUNClient // optional STUN client

	// Long-term integrity.
	Username string
	Password string
	Realm    string
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
		stun:     o.STUN,
		con:      o.Conn,
		password: o.Password,
	}
	if o.Username != "" {
		c.username = stun.NewUsername(o.Username)
	}
	if o.Realm != "" {
		c.realm = stun.NewRealm(o.Realm)
	}
	return c, nil
}

// STUNClient abstracts STUN protocol interaction.
type STUNClient interface {
	Indicate(m *stun.Message) error
	Do(m *stun.Message, f func(e stun.Event)) error
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

func (c *Client) bind(p *Permission, n ChannelNumber, f stun.Handler) error {
	s := make([]stun.Setter, 0, 10)
	s = append(s,
		stun.TransactionID,
	)

	return c.stun.Do(stun.MustBuild(stun.TransactionID,
		stun.NewType(stun.MethodSend, stun.ClassIndication),
	), f)
}

var ErrNotImplemented = errors.New("functionality not implemented")

// Connect creates permission on TURN server.
func (c *Client) Connect() error {
	var (
		stunErr error
		m       = stun.New()
		success = stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse)
	)
	if err := c.stun.Do(stun.MustBuild(stun.TransactionID,
		AllocateRequest, RequestedTransportUDP,
		stun.Fingerprint,
	), func(e stun.Event) {
		if e.Error != nil {
			stunErr = e.Error
			return
		}
		if err := e.Message.CloneTo(m); err != nil {
			stunErr = err
		}
	}); err != nil {
		return err
	}
	if stunErr != nil {
		return stunErr
	}
	if m.Type == success {
		// Allocated.
		if err := c.reladdr.GetFrom(m); err != nil {
			return err
		}
		if err := c.reflexive.GetFrom(m); err != nil && err != stun.ErrAttributeNotFound {
			return err
		}
		return nil
	}

	// Anonymous allocate failed, trying to authenticate.
	if m.Type.Method != stun.MethodAllocate {
		return errors.New("unexpected response type")
	}
	var (
		code stun.ErrorCode
	)
	if code != stun.CodeUnauthorised {
		return errors.New("unexpected error code")
	}
	if err := c.nonce.GetFrom(m); err != nil {
		return err
	}
	if len(c.realm) == 0 {
		if err := c.realm.GetFrom(m); err != nil {
			return err
		}
	}
	c.integrity = stun.NewLongTermIntegrity(
		c.username.String(), c.realm.String(), c.password,
	)

	// Trying to authorise.
	if err := c.stun.Do(stun.MustBuild(stun.TransactionID,
		AllocateRequest, RequestedTransportUDP,
		&c.username, &c.realm,
		&c.nonce,
		&c.integrity, stun.Fingerprint,
	), func(e stun.Event) {
		if e.Error != nil {
			stunErr = e.Error
			return
		}
		if err := e.Message.CloneTo(m); err != nil {
			stunErr = err
		}
	}); err != nil {
		return err
	}
	if stunErr != nil {
		return stunErr
	}
	if m.Type == success {
		// Allocated.
		if err := c.reladdr.GetFrom(m); err != nil {
			return err
		}
		if err := c.reflexive.GetFrom(m); err != nil && err != stun.ErrAttributeNotFound {
			return err
		}
		return nil
	}
	if m.Type.Method != stun.MethodAllocate {
		return errors.New("unexpected response type")
	}
	return fmt.Errorf("got messate: %s", m)
}

// CreateUDP creates new UDP Permission to peer.
func (c *Client) CreateUDP(peer *PeerAddress) (*Permission, error) {
	var pErr error
	if err := c.stun.Do(stun.MustBuild(
		stun.TransactionID, stun.NewType(stun.MethodCreatePermission, stun.ClassRequest), peer,
	), func(e stun.Event) {
		e.Error = pErr
	}); err != nil {
		return nil, err
	}
	if pErr != nil {
		return nil, pErr
	}
	p := &Permission{
		peerData: make(chan []byte, 10),
		c:        c,
	}
	return p, nil
}

// Permission. Implements net.PacketConn.
type Permission struct {
	mux          sync.RWMutex
	binding      bool
	bindErr      error
	number       uint32
	c            *Client
	readDeadline time.Time
	peerData     chan []byte
	p            Protocol
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
	if err := p.c.bind(p, n, func(e stun.Event) {
		p.bindErr = e.Error
		p.binding = false
		if e.Error == nil {
			// Binding succeed.
			atomic.StoreUint32(&p.number, uint32(n))
		}
	}); err != nil {
		return err
	}
	return p.bindErr
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
