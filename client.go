package turn

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gortc/stun"
	"go.uber.org/zap"
)

// Allocation reflects TURN Allocation.
type Allocation struct {
	log       *zap.Logger
	c         *Client
	relayed   RelayedAddress
	reflexive stun.XORMappedAddress
	p         []*Permission
	minBound  ChannelNumber
	integrity stun.MessageIntegrity
	nonce     stun.Nonce
}

// Client for TURN server.
//
// Provides transparent net.Conn interfaces to remote peers.
type Client struct {
	log       *zap.Logger
	con       net.Conn
	stun      STUNClient
	mux       sync.Mutex
	username  stun.Username
	password  string
	realm     stun.Realm
	integrity stun.MessageIntegrity
	a         *Allocation // the only allocation
}

// ClientOptions contains available config for TURN  client.
type ClientOptions struct {
	Conn net.Conn
	STUN STUNClient  // optional STUN client
	Log  *zap.Logger // defaults to Nop

	// Long-term integrity.
	Username string
	Password string
	Realm    string
}

// NewClient creates and initializes new TURN client.
func NewClient(o ClientOptions) (*Client, error) {
	if o.Conn == nil {
		return nil, errors.New("connection not provided")
	}
	if o.Log == nil {
		o.Log = zap.NewNop()
	}
	c := &Client{
		password: o.Password,
		log:      o.Log,
	}
	if o.STUN == nil {
		// Setting up de-multiplexing.
		m := newMultiplexer(o.Conn, c.log.Named("multiplexer"))
		go m.discardData() // discarding any non-stun/turn data
		o.Conn = bypassWriter{
			reader: m.turnL,
			writer: m.conn,
		}
		// Starting STUN client on multiplexed connection.
		var err error
		o.STUN, err = stun.NewClient(stun.ClientOptions{
			Handler: c.stunHandler,
			Connection: bypassWriter{
				reader: m.stunL,
				writer: m.conn,
			},
		})
		if err != nil {
			return nil, err
		}
	}
	c.stun = o.STUN
	c.con = o.Conn

	if o.Username != "" {
		c.username = stun.NewUsername(o.Username)
	}
	if o.Realm != "" {
		c.realm = stun.NewRealm(o.Realm)
	}
	go c.readUntilClosed()
	return c, nil
}

// STUNClient abstracts STUN protocol interaction.
type STUNClient interface {
	Indicate(m *stun.Message) error
	Do(m *stun.Message, f func(e stun.Event)) error
}

func (c *Client) stunHandler(e stun.Event) {
	if e.Error != nil {
		// Just ignoring.
		return
	}
	if e.Message.Type != stun.NewType(stun.MethodData, stun.ClassIndication) {
		return
	}
	var (
		data Data
		addr PeerAddress
	)
	if err := e.Message.Parse(&data, &addr); err != nil {
		c.log.Error("failed to parse while handling incoming STUN message", zap.Error(err))
		return
	}
	c.mux.Lock()
	for i := range c.a.p {
		if !Addr(c.a.p[i].peerAddr).Equal(Addr(addr)) {
			continue
		}
		if _, err := c.a.p[i].peerL.Write(data); err != nil {
			c.log.Error("failed to write", zap.Error(err))
		}
	}
	c.mux.Unlock()
}

func (c *Client) handleChannelData(data *ChannelData) {
	c.log.Debug("handleChannelData", zap.Uint32("n", uint32(data.Number)))
	c.mux.Lock()
	for i := range c.a.p {
		if data.Number != c.a.p[i].Binding() {
			continue
		}
		if _, err := c.a.p[i].peerL.Write(data.Data); err != nil {
			c.log.Error("failed to write", zap.Error(err))
		}
	}
	c.mux.Unlock()
}

func (c *Client) readUntilClosed() {
	buf := make([]byte, 1500)
	for {
		n, err := c.con.Read(buf)
		if err != nil {
			if err == io.EOF {
				continue
			}
			c.log.Error("read failed", zap.Error(err))
			break
		}
		data := buf[:n]
		if !IsChannelData(data) {
			continue
		}
		cData := &ChannelData{
			Raw: make([]byte, n),
		}
		copy(cData.Raw, data)
		if err := cData.Decode(); err != nil {
			panic(err)
		}
		go c.handleChannelData(cData)
	}
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
	return c.stun.Do(stun.MustBuild(stun.TransactionID,
		stun.NewType(stun.MethodChannelBind, stun.ClassRequest),
		n, &p.peerAddr,
		stun.Fingerprint,
	), f)
}

// ErrNotImplemented means that functionality is not currently implemented,
// but it will be (eventually).
var ErrNotImplemented = errors.New("functionality not implemented")

var errUnauthorised = errors.New("unauthorised")

func (c *Client) allocate(req, res *stun.Message) (*Allocation, error) {
	var stunErr error
	if doErr := c.stun.Do(req, func(e stun.Event) {
		if e.Error != nil {
			stunErr = e.Error
			return
		}
		if err := e.Message.CloneTo(res); err != nil {
			stunErr = err
		}
	}); doErr != nil {
		return nil, doErr
	}
	if stunErr != nil {
		return nil, stunErr
	}
	if res.Type == stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse) {
		var (
			relayed   RelayedAddress
			reflexive stun.XORMappedAddress
			nonce     stun.Nonce
		)
		if err := relayed.GetFrom(res); err != nil {
			return nil, err
		}
		if err := reflexive.GetFrom(res); err != nil && err != stun.ErrAttributeNotFound {
			return nil, err
		}
		if err := nonce.GetFrom(req); err != nil && err != stun.ErrAttributeNotFound {
			return nil, err
		}
		a := &Allocation{
			c:         c,
			log:       c.log.Named("allocation"),
			reflexive: reflexive,
			relayed:   relayed,
			minBound:  minChannelNumber,
			integrity: c.integrity,
			nonce:     nonce,
		}
		c.a = a
		return a, nil
	}
	// Anonymous allocate failed, trying to authenticate.
	if res.Type.Method != stun.MethodAllocate {
		return nil, fmt.Errorf("unexpected response type %s", res.Type)
	}
	var (
		code stun.ErrorCodeAttribute
	)
	if err := code.GetFrom(res); err != nil {
		return nil, err
	}
	if code.Code != stun.CodeUnauthorised {
		return nil, fmt.Errorf("unexpected error code %d", code)
	}
	return nil, errUnauthorised
}

// Allocate creates an allocation for current 5-tuple. Currently there can be
// only one allocation per client, because client wraps one net.Conn.
func (c *Client) Allocate() (*Allocation, error) {
	var (
		nonce stun.Nonce
		res   = stun.New()
	)
	req, reqErr := stun.Build(stun.TransactionID,
		AllocateRequest, RequestedTransportUDP,
		stun.Fingerprint,
	)
	if reqErr != nil {
		return nil, reqErr
	}
	a, allocErr := c.allocate(req, res)
	if allocErr == nil {
		return a, nil
	}
	if allocErr != errUnauthorised {
		return nil, allocErr
	}
	// Anonymous allocate failed, trying to authenticate.
	if err := nonce.GetFrom(res); err != nil {
		return nil, err
	}
	if err := c.realm.GetFrom(res); err != nil {
		return nil, err
	}
	c.integrity = stun.NewLongTermIntegrity(
		c.username.String(), c.realm.String(), c.password,
	)
	// Trying to authorise.
	if reqErr = req.Build(stun.TransactionID,
		AllocateRequest, RequestedTransportUDP,
		&c.username, &c.realm,
		&nonce,
		&c.integrity, stun.Fingerprint,
	); reqErr != nil {
		return nil, reqErr
	}
	return c.allocate(req, res)
}

// CreateUDP creates new UDP Permission to peer.
func (a *Allocation) CreateUDP(peer PeerAddress) (*Permission, error) {
	var pErr error
	req := stun.New()
	req.TransactionID = stun.NewTransactionID()
	req.Type = stun.NewType(stun.MethodCreatePermission, stun.ClassRequest)
	req.WriteHeader()
	setters := make([]stun.Setter, 0, 10)
	setters = append(setters, peer)
	if len(a.integrity) > 0 {
		// Applying auth.
		setters = append(setters,
			a.nonce, a.c.username, a.integrity,
		)
	}
	setters = append(setters, stun.Fingerprint)
	for _, s := range setters {
		if setErr := s.AddTo(req); setErr != nil {
			return nil, setErr
		}
	}
	if err := a.c.stun.Do(req, func(e stun.Event) {
		e.Error = pErr
	}); err != nil {
		return nil, err
	}
	if pErr != nil {
		return nil, pErr
	}
	p := &Permission{
		log:      a.log.Named("permission"),
		peerAddr: peer,
		c:        a.c,
	}
	p.peerL, p.peerR = net.Pipe()
	a.p = append(a.p, p)
	return p, nil
}

// Permission implements net.PacketConn.
type Permission struct {
	log          *zap.Logger
	mux          sync.RWMutex
	binding      bool
	number       ChannelNumber
	peerAddr     PeerAddress
	peerL, peerR net.Conn
	c            *Client
}

// Read data from peer.
func (p *Permission) Read(b []byte) (n int, err error) {
	return p.peerR.Read(b)
}

// Bound returns true if channel number is bound for current permission.
func (p *Permission) Bound() bool {
	p.mux.RLock()
	defer p.mux.RUnlock()
	return p.number.Valid()
}

// Binding returns current channel number or 0 if not bound.
func (p *Permission) Binding() ChannelNumber {
	p.mux.RLock()
	defer p.mux.RUnlock()
	return p.number
}

// ErrAlreadyBound means that selected permission already has bound channel number.
var ErrAlreadyBound = errors.New("channel already bound")

// Bind performs binding transaction, allocating channel binding for
// the permission.
//
// TODO: Start binding refresh cycle
func (p *Permission) Bind() error {
	p.mux.Lock()
	defer p.mux.Unlock()
	if p.number != 0 {
		return ErrAlreadyBound
	}
	p.c.a.minBound++
	n := p.c.a.minBound

	// Starting transaction.
	var transactionErr error
	res := stun.New()
	if err := p.c.bind(p, n, func(e stun.Event) {
		if e.Error != nil {
			transactionErr = e.Error
			return
		}
		transactionErr = e.Message.CloneTo(res)
	}); err != nil {
		return err
	}
	if transactionErr != nil {
		return transactionErr
	}
	if res.Type != stun.NewType(stun.MethodChannelBind, stun.ClassSuccessResponse) {
		return fmt.Errorf("unexpected response type %s", res.Type)
	}
	return nil
}

// Write sends buffer to peer.
//
// If permission is bound, the ChannelData message will be used.
func (p *Permission) Write(b []byte) (n int, err error) {
	if n := p.Binding(); n.Valid() {
		if ce := p.log.Check(zap.DebugLevel, "using channel data to write"); ce != nil {
			ce.Write()
		}
		return p.c.sendChan(b, n)
	}
	if ce := p.log.Check(zap.DebugLevel, "using STUN to write"); ce != nil {
		ce.Write()
	}
	return p.c.sendData(b, &p.peerAddr)
}

// Close implements net.Conn.
func (p *Permission) Close() error {
	return p.peerR.Close()
}

// LocalAddr is relayed address from TURN server.
func (p *Permission) LocalAddr() net.Addr {
	return Addr(p.c.a.relayed)
}

// RemoteAddr is peer address.
func (p *Permission) RemoteAddr() net.Addr {
	return Addr(p.peerAddr)
}

// SetDeadline implements net.Conn.
func (p *Permission) SetDeadline(t time.Time) error {
	return p.peerR.SetDeadline(t)
}

// SetReadDeadline implements net.Conn.
func (p *Permission) SetReadDeadline(t time.Time) error {
	return p.peerR.SetReadDeadline(t)
}

// SetWriteDeadline implements net.Conn.
func (p *Permission) SetWriteDeadline(t time.Time) error {
	return ErrNotImplemented
}
