package turn

import (
	"io"
	"net"
	"testing"
	"time"
)

type testConn struct {
	read             func(b []byte) (n int, err error)
	write            func(b []byte) (n int, err error)
	close            func() error
	localAddr        func() net.Addr
	remoteAddr       func() net.Addr
	setDeadline      func(d time.Time) error
	setReadDeadline  func(d time.Time) error
	setWriteDeadline func(d time.Time) error
}

func (t *testConn) Read(b []byte) (n int, err error) {
	return t.read(b)
}

func (t *testConn) Write(b []byte) (n int, err error) {
	return t.write(b)
}

func (t *testConn) Close() error {
	return t.close()
}

func (t *testConn) LocalAddr() net.Addr {
	return t.localAddr()
}

func (t *testConn) RemoteAddr() net.Addr {
	return t.remoteAddr()
}

func (t *testConn) SetDeadline(d time.Time) error {
	return t.setDeadline(d)
}

func (t *testConn) SetReadDeadline(d time.Time) error {
	return t.setReadDeadline(d)
}

func (t *testConn) SetWriteDeadline(d time.Time) error {
	return t.setWriteDeadline(d)
}

func TestBypassWriter(t *testing.T) {
	reader := &testConn{}
	writer := &testConn{}
	b := &bypassWriter{
		writer: writer,
		reader: reader,
	}
	t.Run("Read", func(t *testing.T) {
		b.reader = &testConn{
			read: func(b []byte) (n int, err error) {
				return 0, nil
			},
		}
		b.Read(nil)
	})
	t.Run("Write", func(t *testing.T) {
		b.writer = &testConn{
			write: func(b []byte) (n int, err error) {
				return 0, nil
			},
		}
		b.Write(nil)
	})
	t.Run("Close", func(t *testing.T) {
		b.writer = &testConn{
			close: func() error {
				return nil
			},
		}
		b.reader = &testConn{
			close: func() error {
				return nil
			},
		}
		if err := b.Close(); err != nil {
			t.Error(err)
		}
		b.reader = &testConn{
			close: func() error {
				return io.ErrUnexpectedEOF
			},
		}
		if err := b.Close(); err == nil {
			t.Error("should be not nil")
		}
	})
	t.Run("LocalAddr", func(t *testing.T) {
		b.writer = &testConn{
			localAddr: func() net.Addr {
				return &net.UDPAddr{
					IP:   net.IPv4(127, 0, 0, 1),
					Port: 127,
				}
			},
		}
		if b.LocalAddr().(*net.UDPAddr).Port != 127 {
			t.Error("invalid port")
		}
	})
	t.Run("RemoteAddr", func(t *testing.T) {
		b.writer = &testConn{
			remoteAddr: func() net.Addr {
				return &net.UDPAddr{
					IP:   net.IPv4(127, 0, 0, 1),
					Port: 128,
				}
			},
		}
		if b.RemoteAddr().(*net.UDPAddr).Port != 128 {
			t.Error("invalid port")
		}
	})
	t.Run("SetDeadline", func(t *testing.T) {
		var (
			readerDeadline time.Time
			writeDeadline  time.Time
		)
		b.writer = &testConn{
			setDeadline: func(d time.Time) error {
				writeDeadline = d
				return nil
			},
		}
		b.reader = &testConn{
			setDeadline: func(d time.Time) error {
				readerDeadline = d
				return nil
			},
		}
		deadline := time.Now()
		if b.SetDeadline(deadline) != nil {
			t.Error("got err")
		}
		if !deadline.Equal(readerDeadline) || !deadline.Equal(writeDeadline) {
			t.Error("not set")
		}
		deadline = time.Time{}
		b.writer = &testConn{
			setDeadline: func(d time.Time) error {
				return io.ErrUnexpectedEOF
			},
		}
		if b.SetDeadline(deadline) == nil {
			t.Error("should return error")
		}
		if deadline.Equal(readerDeadline) || deadline.Equal(writeDeadline) {
			t.Error("should not set")
		}
	})
	t.Run("SetWriteDeadline", func(t *testing.T) {
		var (
			writeDeadline time.Time
		)
		b.writer = &testConn{
			setWriteDeadline: func(d time.Time) error {
				writeDeadline = d
				return nil
			},
		}
		deadline := time.Now()
		if b.SetWriteDeadline(deadline) != nil {
			t.Error("got err")
		}
		if !deadline.Equal(writeDeadline) {
			t.Error("not set")
		}
	})
	t.Run("SetReadDeadline", func(t *testing.T) {
		var (
			readDeadline time.Time
		)
		b.reader = &testConn{
			setReadDeadline: func(d time.Time) error {
				readDeadline = d
				return nil
			},
		}
		deadline := time.Now()
		if b.SetReadDeadline(deadline) != nil {
			t.Error("got err")
		}
		if !deadline.Equal(readDeadline) {
			t.Error("not set")
		}
	})
}
