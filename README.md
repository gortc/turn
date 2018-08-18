[![Build Status](https://travis-ci.com/gortc/turn.svg?branch=master)](https://travis-ci.com/gortc/turn)
[![Master status](https://tc.gortc.io/app/rest/builds/buildType:(id:stun_MasterStatus)/statusIcon.svg)](https://tc.gortc.io/project.html?projectId=turn&tab=projectOverview)
[![Build status](https://ci.appveyor.com/api/projects/status/bodd3l5hgu1agxpf/branch/master?svg=true)](https://ci.appveyor.com/project/ernado/turn-gvuk2/branch/master)
[![GoDoc](https://godoc.org/github.com/gortc/turn?status.svg)](http://godoc.org/github.com/gortc/turn)
[![codecov](https://codecov.io/gh/gortc/turn/branch/master/graph/badge.svg)](https://codecov.io/gh/gortc/turn)
[![Go Report](https://goreportcard.com/badge/github.com/gortc/turn)](http://goreportcard.com/report/gortc/turn)
[![stability-beta](https://img.shields.io/badge/stability-beta-33bbff.svg)](https://github.com/mkenney/software-guides/blob/master/STABILITY-BADGES.md#beta)

# TURN

Package turn implements TURN [[RFC 5766](https://tools.ietf.org/html/rfc5766)] Traversal Using Relays around NAT.
Complies to [gortc principles](https://gortc.io/#principles) as core package.
Based on [gortc/stun](https://github.com/gortc/stun) package.
See [gortcd](https://github.com/gortc/gortcd) for TURN server.

## Example
If we have a TURN Server listening on example.com port 3478 (UDP) and
know correct credentials, we can use it to relay data to peer which
is listens on 10.0.0.1:34587 (UDP) and writing back any data it receives:
```go
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/gortc/turn"
)

func main() {
	// Resolving to TURN server.
	raddr, err := net.ResolveUDPAddr("udp", "example.com:3478")
	if err != nil {
		panic(err)
	}
	c, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		panic(err)
	}
	client, clientErr := turn.NewClient(turn.ClientOptions{
		Conn:     c,
		// Credentials:
		Username: "username",
		Password: "password",
	})
	if clientErr != nil {
		panic(clientErr)
	}
	a, allocErr := client.Allocate()
	if allocErr != nil {
		panic(allocErr)
	}
	peerAddr, resolveErr := net.ResolveUDPAddr("udp", "10.0.0.1:34587")
	if resolveErr != nil {
		panic(resolveErr)
	}
	permission, createErr := a.Create(peerAddr)
	if createErr != nil {
		panic(createErr)
	}
	// Permission implements net.Conn.
	if _, writeRrr := fmt.Fprint(permission, "hello world!"); writeRrr != nil {
		panic(peerAddr)
	}
	buf := make([]byte, 1500)
	n, readErr := permission.Read(buf)
	if readErr != nil {
		panic(readErr)
	}
	fmt.Println("got message:", string(buf[:n]))
	// Also you can use ChannelData messages to reduce overhead:
	if err := permission.Bind(); err != nil {
		panic(err)
	}
}
```

## Supported RFCs

- [x] [RFC 5766](https://tools.ietf.org/html/rfc5766) — Traversal Using Relays around NAT
- [x] [RFC 7065](https://tools.ietf.org/html/rfc7065) — TURN URI
- [x] [RFC 6156](https://tools.ietf.org/html/rfc6156) — TURN Extension for IPv6
- [ ] TCP or TLS transport for client

## Benchmarks


```
goos: linux
goarch: amd64
pkg: github.com/gortc/turn
PASS
benchmark                                 iter     time/iter     throughput   bytes alloc        allocs
---------                                 ----     ---------     ----------   -----------        ------
BenchmarkIsChannelData-12           2000000000    1.64 ns/op   6694.29 MB/s        0 B/op   0 allocs/op
BenchmarkChannelData_Encode-12       200000000    9.11 ns/op   1317.35 MB/s        0 B/op   0 allocs/op
BenchmarkChannelData_Decode-12       500000000    3.92 ns/op   3061.45 MB/s        0 B/op   0 allocs/op
BenchmarkChannelNumber/AddTo-12      100000000   12.60 ns/op                       0 B/op   0 allocs/op
BenchmarkChannelNumber/GetFrom-12    200000000    7.23 ns/op                       0 B/op   0 allocs/op
BenchmarkData/AddTo-12               100000000   18.80 ns/op                       0 B/op   0 allocs/op
BenchmarkData/AddToRaw-12            100000000   16.80 ns/op                       0 B/op   0 allocs/op
BenchmarkLifetime/AddTo-12           100000000   13.70 ns/op                       0 B/op   0 allocs/op
BenchmarkLifetime/GetFrom-12         200000000    7.10 ns/op                       0 B/op   0 allocs/op
ok  	github.com/gortc/turn	19.110s
```