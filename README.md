[![Build Status](https://travis-ci.org/gortc/turn.svg)](https://travis-ci.org/gortc/turn)
[![Build status](https://ci.appveyor.com/api/projects/status/bodd3l5hgu1agxpf/branch/master?svg=true)](https://ci.appveyor.com/project/ernado/turn-gvuk2/branch/master)
[![GoDoc](https://godoc.org/github.com/gortc/turn?status.svg)](http://godoc.org/github.com/gortc/turn)
[![Coverage Status](https://coveralls.io/repos/github/gortc/turn/badge.svg?branch=master)](https://coveralls.io/github/gortc/turn?branch=master)
[![Go Report](https://goreportcard.com/badge/github.com/gortc/turn)](http://goreportcard.com/report/gortc/turn)


# TURN

Package turn implements Traversal Using Relays around NAT.

Work in progress.

## RFCs

rfc | status | description
---|--------|----
[![RFC5766](https://img.shields.io/badge/RFC-5766-blue.svg)](https://tools.ietf.org/html/rfc5766) | ![status](https://img.shields.io/badge/status-beta-green.svg)  | Traversal Using Relays around NAT
[![RFC6156](https://img.shields.io/badge/RFC-6156-blue.svg)](https://tools.ietf.org/html/rfc6156) | ![status](https://img.shields.io/badge/status-research-orange.svg)  | TURN Extension for IPv6

## Benchmarks


```
goos: linux
goarch: amd64
pkg: github.com/gortc/turn
PASS
benchmark                                iter     time/iter   bytes alloc        allocs
---------                                ----     ---------   -----------        ------
BenchmarkChannelNumber/AddTo-12     100000000   12.50 ns/op        0 B/op   0 allocs/op
BenchmarkChannelNumber/GetFrom-12   200000000    7.30 ns/op        0 B/op   0 allocs/op
BenchmarkData/AddTo-12              100000000   18.40 ns/op        0 B/op   0 allocs/op
BenchmarkData/AddToRaw-12           100000000   16.00 ns/op        0 B/op   0 allocs/op
BenchmarkLifetime/AddTo-12          100000000   14.60 ns/op        0 B/op   0 allocs/op
BenchmarkLifetime/GetFrom-12        200000000    7.28 ns/op        0 B/op   0 allocs/op
ok  	github.com/gortc/turn	10.606s
```