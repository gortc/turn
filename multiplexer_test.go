package turn

import (
	"io"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type closeFunc func() error

func (f closeFunc) Close() error {
	return f()
}

type readFunc func(buf []byte) (int, error)

func (f readFunc) Read(buf []byte) (int, error) {
	return f(buf)
}

func TestMultiplexer(t *testing.T) {
	t.Run("closeLogged", func(t *testing.T) {
		core, logs := observer.New(zapcore.ErrorLevel)
		logger := zap.New(core)
		closeLogged(logger, "message", closeFunc(func() error {
			return io.ErrUnexpectedEOF
		}))
		if logs.Len() < 1 {
			t.Error("no errors logged")
		}
	})
	t.Run("discardLogged", func(t *testing.T) {
		core, logs := observer.New(zapcore.ErrorLevel)
		logger := zap.New(core)
		discardLogged(logger, "message", readFunc(func(buf []byte) (int, error) {
			return 0, io.ErrUnexpectedEOF
		}))
		if logs.Len() < 1 {
			t.Error("no errors logged")
		}
	})
}
