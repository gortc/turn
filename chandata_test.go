package turn

import (
	"io"
	"testing"
)

func TestChannelData_Encode(t *testing.T) {
	d := &ChannelData{
		Data:   []byte{1, 2, 3, 4},
		Number: minChannelNumber + 1,
	}
	d.Encode()
	b := &ChannelData{}
	b.Raw = append(b.Raw, d.Raw...)
	if err := b.Decode(); err != nil {
		t.Error(err)
	}
	if !b.Equal(d) {
		t.Error("not equal")
	}
	if !IsChannelData(b.Raw) || !IsChannelData(d.Raw) {
		t.Error("unexpected IsChannelData")
	}
}

func TestChannelData_Equal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		a, b  *ChannelData
		value bool
	}{
		{
			name:  "nil",
			value: true,
		},
		{
			name: "nil to non-nil",
			b:    &ChannelData{},
		},
		{
			name: "equal",
			b: &ChannelData{
				Number: minChannelNumber,
				Data:   []byte{1, 2, 3},
			},
			a: &ChannelData{
				Number: minChannelNumber,
				Data:   []byte{1, 2, 3},
			},
			value: true,
		},
		{
			name: "number",
			b: &ChannelData{
				Number: minChannelNumber,
				Data:   []byte{1, 2, 3},
			},
			a: &ChannelData{
				Number: minChannelNumber + 1,
				Data:   []byte{1, 2, 3},
			},
		},
		{
			name: "length",
			b: &ChannelData{
				Number: minChannelNumber,
				Data:   []byte{1, 2, 3},
			},
			a: &ChannelData{
				Number: minChannelNumber,
				Data:   []byte{1, 2, 3, 4},
			},
		},
		{
			name: "data",
			b: &ChannelData{
				Number: minChannelNumber,
				Data:   []byte{1, 2, 3},
			},
			a: &ChannelData{
				Number: minChannelNumber,
				Data:   []byte{1, 2, 2},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if v := tc.a.Equal(tc.b); v != tc.value {
				t.Errorf("unexpected: %v != %v", tc.value, v)
			}
		})
	}
}

func TestChannelData_Decode(t *testing.T) {
	for _, tc := range []struct {
		name string
		buf  []byte
		err  error
	}{
		{
			name: "nil",
			err:  io.ErrUnexpectedEOF,
		},
		{
			name: "small",
			buf:  []byte{1, 2, 3, 4},
			err:  io.ErrUnexpectedEOF,
		},
		{
			name: "zeroes",
			buf:  []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			err:  ErrBadChannelDataLength,
		},
		{
			name: "bad chan number",
			buf:  []byte{63, 255, 0, 0, 0, 4, 0, 0, 1, 2, 3, 4},
			err:  ErrInvalidChannelNumber,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &ChannelData{
				Raw: tc.buf,
			}
			if err := m.Decode(); err != tc.err {
				t.Errorf("unexpected: %v != %v", tc.err, err)
			}
		})
	}
}

func TestChannelData_Reset(t *testing.T) {
	d := &ChannelData{
		Data:   []byte{1, 2, 3, 4},
		Number: minChannelNumber + 1,
	}
	d.Encode()
	buf := make([]byte, len(d.Raw))
	copy(buf, d.Raw)
	d.Reset()
	d.Raw = buf
	if err := d.Decode(); err != nil {
		t.Fatal(err)
	}
}

func TestIsChannelData(t *testing.T) {
	for _, tc := range []struct {
		name  string
		buf   []byte
		value bool
	}{
		{
			name: "nil",
		},
		{
			name: "small",
			buf:  []byte{1, 2, 3, 4},
		},
		{
			name: "zeroes",
			buf:  []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		},
		{
			name: "bad length",
			buf:  []byte{64, 0, 0, 0, 0, 4, 0, 0, 1, 2, 3},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if v := IsChannelData(tc.buf); v != tc.value {
				t.Errorf("unexpected: %v != %v", tc.value, v)
			}
		})
	}
}

func BenchmarkIsChannelData(b *testing.B) {
	buf := []byte{64, 0, 0, 0, 0, 4, 0, 0, 1, 2, 3}
	b.ReportAllocs()
	b.SetBytes(int64(len(buf)))
	for i := 0; i < b.N; i++ {
		IsChannelData(buf)
	}
}


func BenchmarkChannelData_Encode(b *testing.B) {
	d := &ChannelData{
		Data:   []byte{1, 2, 3, 4},
		Number: minChannelNumber + 1,
	}
	b.ReportAllocs()
	b.SetBytes(4 + channelDataHeaderSize)
	for i := 0; i < b.N; i++ {
		d.Encode()
	}
}