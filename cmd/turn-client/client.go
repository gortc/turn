package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/gortc/turn"
	"github.com/pion/turnc"
)

var (
	server = flag.String("server",
		fmt.Sprintf("localhost:3478"),
		"turn server address",
	)
	peer = flag.String("peer",
		"localhost:56780",
		"peer addres",
	)
	username = flag.String("u", "user", "username")
	password = flag.String("p", "secret", "password")
)

func main() {
	flag.Parse()
	logCfg := zap.NewDevelopmentConfig()
	logCfg.DisableCaller = true
	logCfg.DisableStacktrace = true
	start := time.Now()
	logCfg.EncoderConfig.EncodeTime = func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
		d := int64(time.Since(start).Nanoseconds() / 1e6)
		enc.AppendString(fmt.Sprintf("%04d", d))
	}
	logger, err := logCfg.Build()
	if err != nil {
		panic(err)
	}
	if flag.Arg(0) == "peer" {
		_, port, err := net.SplitHostPort(*peer)
		logger.Info("running in peer mode")
		if err != nil {
			logger.Fatal("failed to find port in peer address", zap.Error(err))
		}
		laddr, err := net.ResolveUDPAddr("udp", ":"+port)
		if err != nil {
			logger.Fatal("failed to resolve UDP addr", zap.Error(err))
		}
		c, err := net.ListenUDP("udp", laddr)
		if err != nil {
			logger.Fatal("failed to listen", zap.Error(err))
		}
		logger.Info("listening as echo server", zap.Stringer("laddr", c.LocalAddr()))
		for {
			// Starting echo server.
			buf := make([]byte, 1024)
			n, addr, err := c.ReadFromUDP(buf)
			if err != nil {
				logger.Fatal("failed to read", zap.Error(err))
			}
			logger.Info("got message",
				zap.String("body", string(buf[:n])),
				zap.Stringer("raddr", addr),
			)
			// Echoing back.
			if _, err := c.WriteToUDP(buf[:n], addr); err != nil {
				logger.Fatal("failed to write back", zap.Error(err))
			}
			logger.Info("echoed back",
				zap.Stringer("raddr", addr),
			)
		}
	}
	if *password == "" {
		fmt.Fprintln(os.Stderr, "No password set, auth is required.")
		flag.Usage()
		os.Exit(2)
	}
	// Resolving to TURN server.
	raddr, err := net.ResolveUDPAddr("udp", *server)
	if err != nil {
		logger.Fatal("failed to resolve TURN server",
			zap.Error(err),
		)
	}
	c, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		logger.Fatal("failed to dial to TURN server",
			zap.Error(err),
		)
	}
	logger.Info("dial server",
		zap.Stringer("laddr", c.LocalAddr()),
		zap.Stringer("raddr", c.RemoteAddr()),
	)
	client, clientErr := turn.NewClient(turn.ClientOptions{
		Log:      logger,
		Conn:     c,
		Username: *username,
		Password: *password,
	})
	if clientErr != nil {
		logger.Fatal("failed to init client", zap.Error(clientErr))
	}
	a, allocErr := client.Allocate()
	if allocErr != nil {
		logger.Fatal("failed to allocate", zap.Error(allocErr))
	}
	logger.Info("allocated")
	peerAddr, resolveErr := net.ResolveUDPAddr("udp", *peer)
	if resolveErr != nil {
		logger.Fatal("failed to resolve:", zap.Error(resolveErr))
	}
	permission, createErr := a.Create(peerAddr)
	if createErr != nil {
		logger.Fatal("failed to create permission:", zap.Error(resolveErr))
	}
	if _, writeRrr := fmt.Fprint(permission, "hello world!"); writeRrr != nil {
		logger.Fatal("failed to write", zap.Error(writeRrr))
	}
	buf := make([]byte, 1500)
	n, readErr := permission.Read(buf)
	if readErr != nil {
		logger.Fatal("failed to read:", zap.Error(readErr))
	}
	logger.Info("got message", zap.String("body", string(buf[:n])))
}
