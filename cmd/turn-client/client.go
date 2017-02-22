package main

import (
	"bytes"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/ernado/stun"
	"github.com/ernado/turn"
	"go.uber.org/zap"
)

var (
	server = flag.String("server",
		fmt.Sprintf("a1.cydev.ru:%d", turn.DefaultPort),
		"turn server address",
	)
	peer = flag.String("peer",
		"a1.cydev.ru:56780",
		"peer addres",
	)
	username = flag.String("username", "ernado", "username")
	password = flag.String("password", "", "password")

	allocReq = stun.MessageType{
		Class:  stun.ClassRequest,
		Method: stun.MethodAllocate,
	}
	reqTransport = turn.RequestedTransport{
		Protocol: turn.ProtoUDP,
	}
)

const (
	udp = "udp"
)

func isErr(m *stun.Message) bool {
	return m.Type.Class == stun.ClassErrorResponse
}

func main() {
	flag.Parse()
	var (
		req = new(stun.Message)
		res = new(stun.Message)
	)
	logger, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	if flag.Arg(0) == "peer" {
		_, port, err := net.SplitHostPort(*peer)
		logger.Info("running in peer mode")
		if err != nil {
			logger.Fatal("failed to find port in peer address", zap.Error(err))
		}
		laddr, err := net.ResolveUDPAddr(udp, ":"+port)
		if err != nil {
			logger.Fatal("failed to resolve UDP addr", zap.Error(err))
		}
		c, err := net.ListenUDP(udp, laddr)
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
	if len(*password) == 0 {
		fmt.Fprintln(os.Stderr, "No password set, auth is required.")
		flag.Usage()
		os.Exit(2)
	}

	// Resolving to TURN server.
	raddr, err := net.ResolveUDPAddr(udp, *server)
	if err != nil {
		logger.Fatal("failed to resolve TURN server",
			zap.Error(err),
		)
	}
	c, err := net.DialUDP(udp, nil, raddr)
	if err != nil {
		logger.Fatal("failed to dial to TURN server",
			zap.Error(err),
		)
	}
	logger.Info("dial",
		zap.Stringer("laddr", c.LocalAddr()),
		zap.Stringer("raddr", c.RemoteAddr()),
	)

	// Crafting allocation request.
	req.Build(stun.TransactionID, allocReq, reqTransport)
	if _, err := req.WriteTo(c); err != nil {
		logger.Fatal("failed to write allocation request",
			zap.Error(err),
		)
	}
	// Reading allocation response.
	res.Raw = make([]byte, 0, 1024)
	if _, err := res.ReadFrom(c); err != nil {
		logger.Fatal("failed to read allocation request",
			zap.Error(err),
		)
	}
	logger.Info("got", zap.Stringer("m", res))
	var (
		code  stun.ErrorCodeAttribute
		nonce stun.Nonce
		realm stun.Realm
	)
	if res.Type.Class != stun.ClassErrorResponse {
		logger.Fatal("expected error class, got " + res.Type.Class.String())
	}
	if err := code.GetFrom(res); err != nil {
		logger.Fatal("failed to get error code from message",
			zap.Error(err),
		)
	}
	if code.Code != stun.CodeUnauthorised {
		logger.Fatal("unexpected code of error",
			zap.Stringer("err", code),
		)
	}
	if err := nonce.GetFrom(res); err != nil {
		logger.Fatal("failed to nonce from message",
			zap.Error(err),
		)
	}
	if err := realm.GetFrom(res); err != nil {
		logger.Fatal("failed to get realm from message",
			zap.Error(err),
		)
	}
	realmStr := realm.String()
	nonceStr := nonce.String()
	logger.Info("got unauth error",
		zap.Stringer("nonce", nonce),
		zap.Stringer("realm", realm),
	)
	var (
		credentials = stun.NewLongTermIntegrity(*username, realm.String(), *password)
	)
	logger.Info("using integrity", zap.Stringer("i", credentials))

	// Constructing allocate request with integrity
	if err := req.Build(stun.TransactionID, reqTransport, realm,
		stun.NewUsername(*username), nonce, credentials,
	); err != nil {
		logger.Fatal("failed to build alloc request", zap.Error(err))
	}
	// Sending new request to server.
	if _, err := req.WriteTo(c); err != nil {
		logger.Fatal("failed to send alloc request", zap.Error(err))
	}

	// Reading response.
	if _, err := res.ReadFrom(c); err != nil {
		logger.Fatal("failed to read allocation request",
			zap.Error(err),
		)
	}
	logger.Info("got", zap.Stringer("m", res))
	if isErr(res) {
		code.GetFrom(res)
		logger.Fatal("got error response", zap.Stringer("err", code))
	}
	// Decoding relayed and mapped address.
	var (
		reladdr turn.RelayedAddress
		maddr   stun.XORMappedAddress
	)
	if err := reladdr.GetFrom(res); err != nil {
		logger.Fatal("failed to get relayed address", zap.Error(err))
	}
	logger.Info("relayed address", zap.Stringer("addr", reladdr))
	if err := maddr.GetFrom(res); err != nil && err != stun.ErrAttributeNotFound {
		logger.Fatal("failed to decode relayed address", zap.Error(err))
	} else {
		logger.Info("mapped address", zap.Stringer("addr", maddr))
	}

	// Creating permission request.
	echoAddr, err := net.ResolveUDPAddr(udp, *peer)
	if err != nil {
		logger.Fatal("failed to resonve addr", zap.Error(err))
	}
	peerAddr := turn.PeerAddress{
		IP:   echoAddr.IP,
		Port: echoAddr.Port,
	}
	logger.Info("peer address", zap.Stringer("addr", peerAddr))
	if err := req.Build(stun.TransactionID,
		turn.CreatePermissionRequest,
		peerAddr,
		stun.Realm(realmStr),
		stun.Nonce(nonceStr),
		stun.Username(*username),
		credentials,
	); err != nil {
		logger.Fatal("failed to build", zap.Error(err))
	}
	if _, err := req.WriteTo(c); err != nil {
		logger.Fatal("failed to write permission request", zap.Error(err))
	}

	// Reading response.
	if _, err := res.ReadFrom(c); err != nil {
		logger.Fatal("failed to read response", zap.Error(err))
	}
	logger.Info("got", zap.Stringer("m", res))
	if isErr(res) {
		code.GetFrom(res)
		logger.Fatal("failed to allocate", zap.Stringer("err", code))
	}
	if err := credentials.Check(res); err != nil {
		logger.Error("failed to check integrity", zap.Error(err))
	}

	var (
		sentData = turn.Data("Hello world!")
	)
	// Allocation succeed.
	// Sending data to echo server.
	// can be as resetTo(type, attrs)?
	if err := req.Build(stun.TransactionID,
		turn.SendIndication,
		sentData,
		peerAddr,
		stun.Fingerprint,
	); err != nil {
		logger.Fatal("failed to build", zap.Error(err))
	}
	if _, err := req.WriteTo(c); err != nil {
		panic(err)
	}
	logger.Info("sent", zap.Stringer("m", req))
	logger.Info("sent data", zap.String("v", string(sentData)))

	// Reading response.
	c.SetReadDeadline(time.Now().Add(time.Second * 2))
	if _, err := res.ReadFrom(c); err != nil {
		logger.Fatal("failed to read response", zap.Error(err))
	}
	logger.Info("got", zap.Stringer("m", res))
	if isErr(res) {
		code.GetFrom(res)
		logger.Fatal("got error response", zap.Stringer("err", code))
	}
	var data turn.Data
	if err := data.GetFrom(res); err != nil {
		logger.Fatal("failed to get DATA attribute", zap.Error(err))
	}
	logger.Info("got data", zap.String("v", string(data)))
	if bytes.Equal(data, sentData) {
		logger.Info("OK")
	} else {
		logger.Info("DATA missmatch")
	}

	// De-allocating.
	if err := req.Build(stun.TransactionID,
		turn.RefreshRequest,
		stun.Realm(realmStr),
		stun.Username(*username),
		stun.Nonce(nonceStr),
		turn.ZeroLifetime,
		credentials,
	); err != nil {
		logger.Fatal("failed to build", zap.Error(err))
	}
	if _, err := req.WriteTo(c); err != nil {
		panic(err)
	}
	logger.Info("sent", zap.Stringer("m", req))

	// Reading response.
	c.SetReadDeadline(time.Now().Add(time.Second * 2))
	if _, err := res.ReadFrom(c); err != nil {
		logger.Fatal("failed to read response", zap.Error(err))
	}
	logger.Info("got", zap.Stringer("m", res))
	if isErr(res) {
		code.GetFrom(res)
		logger.Fatal("got error response", zap.Stringer("err", code))
	}
	logger.Info("closing")
}
