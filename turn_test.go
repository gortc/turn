package turn

import (
	"testing"

	"github.com/ernado/stun"
)

const allocRuns = 10

// wasAllocs returns true if f allocates memory.
func wasAllocs(f func()) bool {
	return testing.AllocsPerRun(allocRuns, f) > 0
}

func TestBadAttrLength_Error(t *testing.T) {
	b := &BadAttrLength{
		Attr:     stun.AttrData,
		Expected: 100,
		Got:      11,
	}
	if b.Error() != "incorrect length for DATA: got 11, expected 100" {
		t.Error("Bad value", b.Error())
	}
}
