package turn

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/gortc/stun"
	"github.com/gortc/turn/internal/testutil"
)

type testSTUN struct {
	indicate func(m *stun.Message) error
	do       func(m *stun.Message, f func(e stun.Event)) error
}

func (t testSTUN) Indicate(m *stun.Message) error { return t.indicate(m) }

func (t testSTUN) Do(m *stun.Message, f func(e stun.Event)) error { return t.do(m, f) }

func TestNewClient(t *testing.T) {
	t.Run("NoConn", func(t *testing.T) {
		c, createErr := NewClient(ClientOptions{})
		if createErr == nil {
			t.Error("should error")
		}
		if c != nil {
			t.Error("client should be nil")
		}
	})
	t.Run("Simple", func(t *testing.T) {
		connL, connR := net.Pipe()
		c, createErr := NewClient(ClientOptions{
			Conn: connR,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if c == nil {
			t.Fatal("client should not be nil")
		}
		mustClose(t, connL)
		mustClose(t, connR)
	})
	t.Run("RefreshRate", func(t *testing.T) {
		t.Run("Default", func(t *testing.T) {
			connL, connR := net.Pipe()
			c, createErr := NewClient(ClientOptions{
				Conn: connR,
			})
			if createErr != nil {
				t.Fatal(createErr)
			}
			if c == nil {
				t.Fatal("client should not be nil")
			}
			if c.RefreshRate() != defaultRefreshRate {
				t.Error("refresh rate not equals to default")
			}
			mustClose(t, connL)
			mustClose(t, connR)
		})
		t.Run("Disabled", func(t *testing.T) {
			connL, connR := net.Pipe()
			c, createErr := NewClient(ClientOptions{
				RefreshDisabled: true,
				Conn:            connR,
			})
			if createErr != nil {
				t.Fatal(createErr)
			}
			if c == nil {
				t.Fatal("client should not be nil")
			}
			if c.RefreshRate() != 0 {
				t.Error("refresh rate not equals to zero")
			}
			mustClose(t, connL)
			mustClose(t, connR)
		})
		t.Run("Custom", func(t *testing.T) {
			connL, connR := net.Pipe()
			c, createErr := NewClient(ClientOptions{
				RefreshRate: time.Second,
				Conn:        connR,
			})
			if createErr != nil {
				t.Fatal(createErr)
			}
			if c == nil {
				t.Fatal("client should not be nil")
			}
			if c.RefreshRate() != time.Second {
				t.Error("refresh rate not equals to value")
			}
			mustClose(t, connL)
			mustClose(t, connR)
		})
	})
}

type verboseConn struct {
	name      string
	conn      net.Conn
	closed    bool
	closedMux sync.Mutex
	t         *testing.T
}

func verboseBytes(b []byte) string {
	switch {
	case stun.IsMessage(b):
		m := stun.New()
		if _, err := m.Write(b); err != nil {
			return "stun (invalid)"
		}
		return fmt.Sprintf("stun (%s)", m)
	case IsChannelData(b):
		d := ChannelData{
			Raw: b,
		}
		if err := d.Decode(); err != nil {
			return "chandata (invalid)"
		}
		return fmt.Sprintf("chandata (n: 0x%x, len: %d)", int(d.Number), d.Length)
	default:
		return "raw"
	}
}

func (c *verboseConn) isClosed() bool {
	c.closedMux.Lock()
	closed := c.closed
	c.closedMux.Unlock()
	return closed
}

func (c *verboseConn) Read(b []byte) (n int, err error) {
	if c.isClosed() {
		return 0, io.ErrClosedPipe
	}
	c.t.Helper()
	c.t.Logf("%s: read start", c.name)
	defer func() {
		if c.isClosed() {
			return
		}
		c.t.Helper()
		if err != nil {
			c.t.Logf("%s: read: %v", c.name, err)
		} else {
			c.t.Logf("%s: read(%d): %s", c.name, n, verboseBytes(b))
		}
	}()
	return c.conn.Read(b)
}

func (c *verboseConn) Write(b []byte) (n int, err error) {
	if c.isClosed() {
		return 0, io.ErrClosedPipe
	}
	c.t.Helper()
	c.t.Logf("%s: write start: %s", c.name, verboseBytes(b))
	defer func() {
		if c.isClosed() {
			return
		}
		c.t.Helper()
		if err != nil {
			c.t.Logf("%s: write: %s: %v", c.name, verboseBytes(b), err)
		} else {
			c.t.Logf("%s: write: %s", c.name, verboseBytes(b))
		}
	}()
	return c.conn.Write(b)
}

func (c *verboseConn) Close() error {
	c.t.Helper()
	c.t.Logf("%s: close", c.name)
	c.closedMux.Lock()
	c.closed = true
	c.closedMux.Unlock()
	return c.conn.Close()
}

func (c *verboseConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *verboseConn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *verboseConn) SetDeadline(t time.Time) error {
	if c.isClosed() {
		return io.ErrClosedPipe
	}
	c.t.Helper()
	c.t.Logf("%s: SetDeadline(%s)", c.name, t)
	return c.conn.SetDeadline(t)
}

func (c *verboseConn) SetReadDeadline(t time.Time) error {
	if c.isClosed() {
		return io.ErrClosedPipe
	}
	c.t.Helper()
	c.t.Logf("%s: SetReadDeadline(%s)", c.name, t)
	return c.conn.SetReadDeadline(t)
}

func (c *verboseConn) SetWriteDeadline(t time.Time) error {
	if c.isClosed() {
		return io.ErrClosedPipe
	}
	c.t.Helper()
	c.t.Logf("%s: SetWriteDeadline(%s)", c.name, t)
	return c.conn.SetWriteDeadline(t)
}

func testPipe(t *testing.T, lName, rName string) (net.Conn, net.Conn) {
	connL, connR := net.Pipe()
	return &verboseConn{
			name: lName,
			conn: connL,
			t:    t,
		}, &verboseConn{
			name: rName,
			conn: connR,
			t:    t,
		}
}

func TestClientMultiplexed(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	connL, connR := testPipe(t, "server", "client")
	timeout := time.Second * 10
	c, createErr := NewClient(ClientOptions{
		Log:          logger,
		Conn:         connR,
		RTO:          timeout,
		NoRetransmit: true,
	})
	if createErr != nil {
		t.Fatal(createErr)
	}
	if c == nil {
		t.Fatal("client should not be nil")
	}
	gotRequest := make(chan struct{})
	_ = connL.SetDeadline(time.Now().Add(timeout))
	_ = connR.SetDeadline(time.Now().Add(timeout))
	go func() {
		buf := make([]byte, 1500)
		readN, readErr := connL.Read(buf)
		t.Log("got write")
		if readErr != nil {
			t.Error("failed to read")
		}
		m := &stun.Message{
			Raw: buf[:readN],
		}
		if decodeErr := m.Decode(); decodeErr != nil {
			t.Error("failed to decode")
		}
		res := stun.MustBuild(m, stun.NewType(m.Type.Method, stun.ClassSuccessResponse),
			&RelayedAddress{
				IP:   net.IPv4(127, 0, 0, 1),
				Port: 1001,
			},
			&stun.XORMappedAddress{
				IP:   net.IPv4(127, 0, 0, 2),
				Port: 1002,
			},
			stun.Fingerprint,
		)
		res.Encode()
		if _, writeErr := connL.Write(res.Raw); writeErr != nil {
			t.Error("failed to write")
		}
		gotRequest <- struct{}{}
	}()
	a, allocErr := c.Allocate()
	if allocErr != nil {
		t.Fatal(allocErr)
	}
	select {
	case <-gotRequest:
		// success
	case <-time.After(timeout):
		t.Fatal("timed out")
	}
	peer := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 3),
		Port: 1003,
	}
	go func() {
		buf := make([]byte, 1500)
		readN, readErr := connL.Read(buf)
		if readErr != nil {
			t.Error("failed to read")
		}
		m := &stun.Message{
			Raw: buf[:readN],
		}
		if decodeErr := m.Decode(); decodeErr != nil {
			t.Error("failed to decode")
		}
		t.Logf("read request: %s", m)
		res := stun.MustBuild(m, stun.NewType(m.Type.Method, stun.ClassSuccessResponse),
			stun.Fingerprint,
		)
		t.Logf("writing response: %s", res)
		if _, writeErr := connL.Write(res.Raw); writeErr != nil {
			t.Error("failed to write")
		}
		t.Logf("wrote %s", res)
		gotRequest <- struct{}{}
	}()
	t.Log("creating udp permission")
	p, permErr := a.CreateUDP(peer)
	if permErr != nil {
		t.Fatal(permErr)
	}
	select {
	case <-gotRequest:
		// success
	case <-time.After(timeout):
		t.Fatal("timed out")
	}
	if p.Bound() {
		t.Error("should not be bound")
	}
	go func() {
		buf := make([]byte, 1500)
		readN, readErr := connL.Read(buf)
		t.Log("got write")
		if readErr != nil {
			t.Error("failed to read")
		}
		m := &stun.Message{
			Raw: buf[:readN],
		}
		if decodeErr := m.Decode(); decodeErr != nil {
			t.Error("failed to decode")
		}
		res := stun.MustBuild(m, stun.NewType(m.Type.Method, stun.ClassSuccessResponse),
			stun.Fingerprint,
		)
		res.Encode()
		_ = connL.SetWriteDeadline(time.Now().Add(timeout / 2))
		if _, writeErr := connL.Write(res.Raw); writeErr != nil {
			t.Error("failed to write")
		}
		gotRequest <- struct{}{}
	}()
	t.Log("starting binding")
	if bindErr := p.Bind(); bindErr != nil {
		t.Fatalf("failed to bind: %v", bindErr)
	}
	select {
	case <-gotRequest:
		// success
	case <-time.After(timeout):
		t.Fatal("timed out")
	}
	go func() {
		buf := make([]byte, 1500)
		readN, readErr := connL.Read(buf)
		t.Log("got write")
		if readErr != nil {
			t.Error("failed to read")
		}
		m := &ChannelData{
			Raw: buf[:readN],
		}
		if decodeErr := m.Decode(); decodeErr != nil {
			t.Error("failed to decode")
		}
		gotRequest <- struct{}{}
	}()
	sent := []byte{1, 2, 3, 4}
	if _, writeErr := p.Write(sent); writeErr != nil {
		t.Fatal(writeErr)
	}
	select {
	case <-gotRequest:
		// success
	case <-time.After(timeout):
		t.Fatal("timed out")
	}
	go func() {
		d := &ChannelData{
			Number: p.Binding(),
			Data:   sent,
			Raw:    make([]byte, 1500),
		}
		d.Encode()
		_, writeErr := connL.Write(d.Raw)
		if writeErr != nil {
			t.Error("failed to write")
		}
	}()
	buf := make([]byte, 1500)
	n, readErr := p.Read(buf)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(buf[:n], sent) {
		t.Error("data mismatch")
	}
	mustClose(t, connL)
	testutil.EnsureNoErrors(t, logs)
}

func mustClose(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("failed to close: %v", err)
	}
}

func TestClient_STUNHandler(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	connL, connR := testPipe(t, "server", "client")
	defer mustClose(t, connL)
	defer mustClose(t, connR)
	timeout := time.Second * 10
	c, createErr := NewClient(ClientOptions{
		Log:          logger,
		Conn:         connR,
		RTO:          timeout,
		NoRetransmit: true,
	})
	if createErr != nil {
		t.Fatal(createErr)
	}
	t.Run("IgnoreErr", func(t *testing.T) {
		c.stunHandler(stun.Event{
			Error: errors.New("error"),
		})
		testutil.EnsureNoErrors(t, logs)
	})
	t.Run("IgnoreNonData", func(t *testing.T) {
		c.stunHandler(stun.Event{
			Message: stun.MustBuild(stun.BindingRequest),
		})
		testutil.EnsureNoErrors(t, logs)
	})
	t.Run("ParseErr", func(t *testing.T) {
		c.stunHandler(stun.Event{
			Message: stun.MustBuild(dataIndication),
		})
		found := false
		for _, l := range logs.All() {
			if l.Level != zapcore.ErrorLevel {
				continue
			}
			if l.Message != "failed to parse while handling incoming STUN message" {
				t.Errorf("unexpected message: %s", l.Message)
			} else {
				found = true
			}
		}
		if !found {
			t.Error("expected error message not found")
		}
	})
	t.Run("WriteErr", func(t *testing.T) {
		stunClient := &testSTUN{}
		core, logs = observer.New(zapcore.DebugLevel)
		logger := zap.New(core)
		connL, connR = testPipe(t, "server", "client")
		timeout := time.Second * 10
		c, createErr = NewClient(ClientOptions{
			Log:          logger,
			Conn:         connR,
			RTO:          timeout,
			NoRetransmit: true,
			STUN:         stunClient,
		})
		if createErr != nil {
			t.Error(createErr)
		}
		stunClient.do = func(m *stun.Message, f func(e stun.Event)) error {
			switch m.Type {
			case AllocateRequest:
				f(stun.Event{
					Message: stun.MustBuild(m,
						stun.NewType(stun.MethodAllocate,
							stun.ClassSuccessResponse,
						),
						&RelayedAddress{
							Port: 1113,
							IP:   net.IPv4(127, 0, 0, 2),
						},
						stun.Fingerprint,
					),
				})
			case stun.NewType(stun.MethodCreatePermission, stun.ClassRequest):
				f(stun.Event{
					Message: stun.MustBuild(m,
						stun.NewType(m.Type.Method, stun.ClassSuccessResponse),
						stun.Fingerprint,
					),
				})
			case stun.NewType(stun.MethodChannelBind, stun.ClassRequest):
				f(stun.Event{
					Message: stun.MustBuild(m,
						stun.NewType(m.Type.Method, stun.ClassSuccessResponse),
					),
				})
			default:
				t.Fatalf("unexpected type: %s", m.Type)
			}
			return nil
		}
		a, allocErr := c.Allocate()
		if allocErr != nil {
			t.Fatal(allocErr)
		}
		perm, err := a.CreateUDP(&net.UDPAddr{
			IP: net.IPv4(127, 0, 0, 1),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer mustClose(t, perm)
		if err = perm.peerR.Close(); err != nil {
			t.Fatal(err)
		}
		if err = perm.peerL.Close(); err != nil {
			t.Fatal(err)
		}
		c.stunHandler(stun.Event{
			Message: stun.MustBuild(dataIndication,
				&PeerAddress{
					IP: net.IPv4(127, 0, 0, 1),
				},
				&Data{},
			),
		})
		found := false
		for _, l := range logs.All() {
			if l.Level != zapcore.ErrorLevel {
				continue
			}
			if l.Message != "failed to write" {
				t.Errorf("unexpected message: %s", l.Message)
			} else {
				found = true
			}
		}
		if !found {
			t.Error("expected error message not found")
		}

	})
}

func TestClient_sendChan(t *testing.T) {
	connL, connR := net.Pipe()
	c, createErr := NewClient(ClientOptions{
		Conn: connR,
	})
	if createErr != nil {
		t.Fatal(createErr)
	}
	if c == nil {
		t.Fatal("client should not be nil")
	}
	n, err := c.sendChan([]byte{0}, -1)
	if n > 0 {
		t.Error("sendChan should non return non-zero written bytes length")
	}
	if err != ErrInvalidChannelNumber {
		t.Errorf("unexpected err: %v", err)
	}
	mustClose(t, connL)
}

func TestClient_do(t *testing.T) {
	connL, connR := net.Pipe()
	stunClient := &testSTUN{}
	c, createErr := NewClient(ClientOptions{
		STUN: stunClient,
		Conn: connR,
	})
	if createErr != nil {
		t.Fatal(createErr)
	}
	if c == nil {
		t.Fatal("client should not be nil")
	}
	t.Run("BadMessage", func(t *testing.T) {
		stunClient.do = func(m *stun.Message, f func(e stun.Event)) error {
			f(stun.Event{
				Message: &stun.Message{
					Raw: []byte{0, 1, 1, 1, 2},
				},
			})
			return nil
		}
		req, res := stun.New(), stun.New()
		t.Run("Clone", func(t *testing.T) {
			if err := c.do(req, res); err == nil {
				t.Error("should error")
			}
		})
		t.Run("NoClone", func(t *testing.T) {
			if err := c.do(req, nil); err != nil {
				t.Error("should not error")
			}
		})
	})
	if err := connL.Close(); err != nil {
		t.Error(err)
	}
}
