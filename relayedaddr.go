package turn

import (
	"net"

	"github.com/gortc/stun"
)

// RelayedAddress implements XOR-RELAYED-ADDRESS attribute.
//
// The XOR-PEER-ADDRESS specifies the address and port of the peer as
// seen from the TURN server. (For example, the peer's server-reflexive
// transport address if the peer is behind a NAT.)
//
// https://trac.tools.ietf.org/html/rfc5766#section-14.5
type RelayedAddress struct {
	IP   net.IP
	Port int
}

func (a RelayedAddress) String() string {
	return (stun.XORMappedAddress)(a).String()
}

// AddTo adds XOR-PEER-ADDRESS to message.
func (a RelayedAddress) AddTo(m *stun.Message) error {
	return (stun.XORMappedAddress)(a).AddToAs(m, stun.AttrXORRelayedAddress)
}

// AddTo decodes XOR-PEER-ADDRESS from message.
func (a *RelayedAddress) GetFrom(m *stun.Message) error {
	return (*stun.XORMappedAddress)(a).GetFromAs(m, stun.AttrXORRelayedAddress)
}
