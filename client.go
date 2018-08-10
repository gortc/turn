package turn

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/gortc/stun"
)

// Allocation reflects TURN Allocation.
type Allocation struct {
	log       *zap.Logger
	c         *Client
	relayed   RelayedAddress
	reflexive stun.XORMappedAddress
	p         []*Permission
	minBound  ChannelNumber
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

func stunLog(ce *zapcore.CheckedEntry, data []byte) {
	m := &stun.Message{
		Raw: data,
	}
	if err := m.Decode(); err == nil {
		ce.Write(zap.Stringer("msg", m))
	}
}

// multiplexer de-multiplexes STUN, TURN and application data
// from one connection into separate ones.
type multiplexer struct {
	log      *zap.Logger
	capacity int
	conn     net.Conn

	stunL, stunR net.Conn
	turnL, turnR net.Conn
	dataL, dataR net.Conn
}

type bypassWriter struct {
	reader net.Conn
	writer net.Conn
}

func (w bypassWriter) Close() error {
	rErr := w.reader.Close()
	wErr := w.writer.Close()
	if rErr == nil && wErr == nil {
		return nil
	}
	return fmt.Errorf("reader: %v, writer: %v", rErr, wErr)
}

func (w bypassWriter) LocalAddr() net.Addr {
	return w.writer.LocalAddr()
}

func (w bypassWriter) Read(b []byte) (n int, err error) {
	return w.reader.Read(b)
}

func (w bypassWriter) RemoteAddr() net.Addr {
	return w.writer.RemoteAddr()
}

func (w bypassWriter) SetDeadline(t time.Time) error {
	if err := w.writer.SetDeadline(t); err != nil {
		return err
	}
	return w.reader.SetDeadline(t)
}

func (w bypassWriter) SetReadDeadline(t time.Time) error {
	return w.reader.SetReadDeadline(t)
}

func (w bypassWriter) SetWriteDeadline(t time.Time) error {
	return w.writer.SetWriteDeadline(t)
}

func (w bypassWriter) Write(b []byte) (n int, err error) {
	return w.writer.Write(b)
}

func newMultiplexer(conn net.Conn, log *zap.Logger) *multiplexer {
	m := &multiplexer{conn: conn, capacity: 1500, log: log}
	m.stunL, m.stunR = net.Pipe()
	m.turnL, m.turnR = net.Pipe()
	m.dataL, m.dataR = net.Pipe()
	go m.readUntilClosed()
	return m
}

func (m *multiplexer) discardData() {
	_, err := io.Copy(ioutil.Discard, m.dataL)
	if err != nil {
		m.log.Error("discard error", zap.Error(err))
	}
}

func (m *multiplexer) close() {
	if closeErr := m.turnR.Close(); closeErr != nil {
		m.log.Error("failed to close turnR", zap.Error(closeErr))
	}
	if closeErr := m.stunR.Close(); closeErr != nil {
		m.log.Error("failed to close stunR", zap.Error(closeErr))
	}
	if closeErr := m.dataR.Close(); closeErr != nil {
		m.log.Error("failed to close dataR", zap.Error(closeErr))
	}
}

func (m *multiplexer) readUntilClosed() {
	buf := make([]byte, m.capacity)
	for {
		n, err := m.conn.Read(buf)
		if ce := m.log.Check(zap.DebugLevel, "read"); ce != nil {
			ce.Write(zap.Error(err), zap.Int("n", n))
		}
		if err != nil {
			// End of cycle.
			// TODO: Handle timeouts and temporary errors.
			m.log.Error("failed to read", zap.Error(err))
			m.close()
			break
		}
		data := buf[:n]
		conn := m.dataR
		switch {
		case stun.IsMessage(data):
			m.log.Debug("got STUN data")
			if ce := m.log.Check(zap.DebugLevel, "stun message"); ce != nil {
				stunLog(ce, data)
			}
			conn = m.stunR
		case IsChannelData(data):
			m.log.Debug("got TURN data")
			conn = m.turnR
		default:
			m.log.Debug("got APP data")
		}
		_, err = conn.Write(data)
		if err != nil {
			m.log.Warn("failed to write", zap.Error(err))
		}
	}
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
		if data.Number != ChannelNumber(atomic.LoadUint32(&c.a.p[i].number)) {
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
		)
		if err := relayed.GetFrom(res); err != nil {
			return nil, err
		}
		if err := reflexive.GetFrom(res); err != nil && err != stun.ErrAttributeNotFound {
			return nil, err
		}
		a := &Allocation{
			c:         c,
			log:       c.log.Named("allocation"),
			reflexive: reflexive,
			relayed:   relayed,
			minBound:  minChannelNumber,
		}
		c.a = a
		return a, nil
	}
	// Anonymous allocate failed, trying to authenticate.
	if res.Type.Method != stun.MethodAllocate {
		return nil, fmt.Errorf("unexpected response type %s", res.Type)
	}
	var (
		code stun.ErrorCode
	)
	if code != stun.CodeUnauthorised {
		return nil, fmt.Errorf("unexpected error code %d", code)
	}
	return nil, errUnauthorised
}

// Allocate creates an allocation for current 5-tuple. Currently there can be
// only one allocation per client, because client wraps one net.Conn.
//
// TODO: simplify
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
	if err := a.c.stun.Do(stun.MustBuild(
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
	number       uint32
	bindErr      error
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
// TODO: Start binding refresh cycle
func (p *Permission) Bind() error {
	p.mux.Lock()
	defer p.mux.Unlock()

	if p.binding {
		return ErrBindingInProgress
	}
	p.binding = true
	if p.number != 0 {
		return ErrAlreadyBound
	}
	p.c.a.minBound++
	n := p.c.a.minBound
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
		if ce := p.log.Check(zap.DebugLevel, "using channel data to write"); ce != nil {
			ce.Write()
		}
		return p.c.sendChan(b, ChannelNumber(n))
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
