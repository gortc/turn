package turn

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gortc/stun"
)

type Allocation struct {
	c         *Client
	reladdr   RelayedAddress
	reflexive stun.XORMappedAddress
	nonce     stun.Nonce
	p         []*Permission
	minBound  ChannelNumber
}

// Client for TURN server.
//
// Provides transparent net.Conn interfaces to remote peers.
type Client struct {
	con       net.Conn
	stun      STUNClient
	mux       sync.Mutex
	nonce     stun.Nonce
	username  stun.Username
	password  string
	realm     stun.Realm
	integrity stun.MessageIntegrity
	a         *Allocation // the only allocation
}

type ClientOptions struct {
	Conn net.Conn
	STUN STUNClient // optional STUN client

	// Long-term integrity.
	Username string
	Password string
	Realm    string
}

func stunLog(data []byte) {
	m := &stun.Message{
		Raw: data,
	}
	if err := m.Decode(); err == nil {
		log.Println("stun:", m)
	}
}

// multiplexer de-multiplexes STUN, TURN and application data
// from one connection into separate ones.
type multiplexer struct {
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

func newMultiplexer(conn net.Conn) *multiplexer {
	m := &multiplexer{conn: conn, capacity: 1500}
	m.stunL, m.stunR = net.Pipe()
	m.turnL, m.turnR = net.Pipe()
	m.dataL, m.dataR = net.Pipe()
	go m.readUntilClosed()
	return m
}

func (m *multiplexer) discardData() {
	io.Copy(ioutil.Discard, m.dataL)
}

func (m *multiplexer) readUntilClosed() {
	buf := make([]byte, m.capacity)
	for {
		n, err := m.conn.Read(buf)
		log.Println("multiplexer: read", n, err)
		if err != nil {
			// End of cycle.
			// TODO: Handle timeouts and temporary errors.
			m.turnR.Close()
			m.stunR.Close()
			m.dataR.Close()
			break
		}
		data := buf[:n]
		conn := m.dataR
		switch {
		case stun.IsMessage(data):
			log.Println("multiplexer: got STUN data")
			stunLog(data)
			conn = m.stunR
		case IsChannelData(data):
			log.Println("multiplexer: got TURN data")
			conn = m.turnR
		default:
			log.Println("multiplexer: got APP data")
		}
		conn.Write(data)
	}
}

func NewClient(o ClientOptions) (*Client, error) {
	if o.Conn == nil {
		return nil, errors.New("connection not provided")
	}
	c := &Client{
		password: o.Password,
	}
	if o.STUN == nil {
		// Setting up de-multiplexing.
		m := newMultiplexer(o.Conn)
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
	fmt.Println("turn client: got", e.Message)
	if e.Message.Type != stun.NewType(stun.MethodData, stun.ClassIndication) {
		return
	}
	var (
		data Data
		addr PeerAddress
	)
	if err := e.Message.Parse(&data, &addr); err != nil {
		log.Println("failed to parse:", err)
		return
	}
	c.mux.Lock()
	for i := range c.a.p {
		if !Addr(c.a.p[i].peerAddr).Equal(Addr(addr)) {
			continue
		}
		c.a.p[i].peerData <- data
	}
	c.mux.Unlock()
}

func (c *Client) handleChannelData(data *ChannelData) {
	log.Println("handleChannelData:", data.Number)
	c.mux.Lock()
	for i := range c.a.p {
		if data.Number != ChannelNumber(atomic.LoadUint32(&c.a.p[i].number)) {
			continue
		}
		c.a.p[i].peerData <- data.Data
	}
	c.mux.Unlock()
}

func (c *Client) readUntilClosed() {
	buf := make([]byte, 1500)
	for {
		n, err := c.con.Read(buf)
		if err != nil {
			log.Println("client.readUntilClosed:", err)
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

var ErrNotImplemented = errors.New("functionality not implemented")

// Connect creates permission on TURN server.
func (c *Client) Allocate() (*Allocation, error) {
	var (
		stunErr error
		nonce   stun.Nonce
		m       = stun.New()
		success = stun.NewType(stun.MethodAllocate, stun.ClassSuccessResponse)
	)
	req, err := stun.Build(stun.TransactionID,
		AllocateRequest, RequestedTransportUDP,
		stun.Fingerprint,
	)
	if err != nil {
		return nil, err
	}
	if err := c.stun.Do(req, func(e stun.Event) {
		if e.Error != nil {
			stunErr = e.Error
			return
		}
		if err := e.Message.CloneTo(m); err != nil {
			stunErr = err
		}
	}); err != nil {
		return nil, err
	}
	if stunErr != nil {
		return nil, stunErr
	}
	if m.Type == success {
		// Allocated.
		var (
			reladdr   RelayedAddress
			reflexive stun.XORMappedAddress
		)
		if err := reladdr.GetFrom(m); err != nil {
			return nil, err
		}
		if err := reflexive.GetFrom(m); err != nil && err != stun.ErrAttributeNotFound {
			return nil, err
		}
		a := &Allocation{
			c:         c,
			reflexive: reflexive,
			reladdr:   reladdr,
			minBound:  minChannelNumber,
		}
		c.a = a
		return a, nil
	}

	// Anonymous allocate failed, trying to authenticate.
	if m.Type.Method != stun.MethodAllocate {
		return nil, errors.New("unexpected response type")
	}
	var (
		code stun.ErrorCode
	)
	if code != stun.CodeUnauthorised {
		return nil, errors.New("unexpected error code")
	}
	if err := nonce.GetFrom(m); err != nil {
		return nil, err
	}
	if err := c.realm.GetFrom(m); err != nil {
		return nil, err
	}
	c.integrity = stun.NewLongTermIntegrity(
		c.username.String(), c.realm.String(), c.password,
	)

	// Trying to authorise.
	if err = req.Build(stun.TransactionID,
		AllocateRequest, RequestedTransportUDP,
		&c.username, &c.realm,
		&nonce,
		&c.integrity, stun.Fingerprint,
	); err != nil {
		return nil, err
	}
	if err := c.stun.Do(req, func(e stun.Event) {
		if e.Error != nil {
			stunErr = e.Error
			return
		}
		if err := e.Message.CloneTo(m); err != nil {
			stunErr = err
		}
	}); err != nil {
		return nil, err
	}
	if stunErr != nil {
		return nil, stunErr
	}
	if m.Type == success {
		// Allocated.
		var (
			reladdr   RelayedAddress
			reflexive stun.XORMappedAddress
		)
		if err := reladdr.GetFrom(m); err != nil {
			return nil, err
		}
		if err := reflexive.GetFrom(m); err != nil && err != stun.ErrAttributeNotFound {
			return nil, err
		}
		a := &Allocation{
			c:         c,
			reflexive: reflexive,
			reladdr:   reladdr,
			nonce:     nonce,
			minBound:  minChannelNumber,
		}
		c.a = a
		return a, nil
	}
	if m.Type.Method != stun.MethodAllocate {
		return nil, errors.New("unexpected response type")
	}
	return nil, fmt.Errorf("got message: %s", m)
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
		peerAddr: peer,
		peerData: make(chan []byte, 10),
		c:        a.c,
	}
	a.p = append(a.p, p)
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
	peerAddr     PeerAddress
	p            Protocol
}

// Read data from peer.
func (p *Permission) Read(b []byte) (n int, err error) {
	select {
	case <-time.After(time.Second * 1):
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
		log.Println("using channel data to write")
		return p.c.sendChan(b, ChannelNumber(n))
	}
	log.Println("using STUN to write")
	return p.c.sendData(b, &p.peerAddr)
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
