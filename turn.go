// Package turn implements RFC 5766 Traversal Using Relays around NAT.
package turn

import (
	"encoding/binary"
	"fmt"

	"github.com/ernado/stun"
)

// bin is shorthand for binary.BigEndian.
var bin = binary.BigEndian

// BadAttrLength means that length for attribute is invalid.
type BadAttrLength struct {
	Attr     stun.AttrType
	Got      int
	Expected int
}

func (e BadAttrLength) Error() string {
	return fmt.Sprintf("incorrect length for %s: got %d, expected %d",
		e.Attr,
		e.Got,
		e.Expected,
	)
}

// Default ports for TURN from RFC 5766 Section 4.
const (
	// DefaultPort for TURN is same as STUN.
	DefaultPort = stun.DefaultPort
	// DefaultTLSPort is for TURN over TLS.
	DefaultTLSPort = 5349
)
