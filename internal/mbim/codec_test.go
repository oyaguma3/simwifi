package mbim

import (
	"bytes"
	"errors"
	"testing"
)

func TestEncodeLayout(t *testing.T) {
	// Proxy Control CONFIGURATION: DevicePath(offset,size), Timeout, データ
	got := Encode(String("/dev/a"), U32(30))
	want := []byte{
		12, 0, 0, 0, // offset
		12, 0, 0, 0, // size（UTF-16LE で 6 文字）
		30, 0, 0, 0, // Timeout
		'/', 0, 'd', 0, 'e', 0, 'v', 0, '/', 0, 'a', 0,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Encode = %x\nwant     %x", got, want)
	}

	// uicc-ref-byte-array は size, offset の順。可変長データは 4 バイト境界に揃える
	got = Encode(UICCRefBytes([]byte{0xa0, 0x00, 0x00}), U32(4), U32(1), RefBytes([]byte{0x55}))
	want = []byte{
		3, 0, 0, 0, 24, 0, 0, 0, // size, offset
		4, 0, 0, 0,
		1, 0, 0, 0,
		28, 0, 0, 0, 1, 0, 0, 0, // offset, size
		0xa0, 0x00, 0x00, 0x00, // データ + パディング
		0x55, 0x00, 0x00, 0x00,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Encode = %x\nwant     %x", got, want)
	}

	// 空の参照は offset 0, size 0
	got = Encode(RefBytes(nil))
	if !bytes.Equal(got, make([]byte, 8)) {
		t.Fatalf("empty ref = %x", got)
	}
}

func TestDecoderRoundTrip(t *testing.T) {
	buf := Encode(U32(7), Fixed([]byte{1, 2, 3, 4}), UICCRefBytes([]byte{9, 8, 7}), String("héllo"), RefBytes([]byte("utf8")))
	d := NewDecoder(buf)
	if d.U32() != 7 || !bytes.Equal(d.Fixed(4), []byte{1, 2, 3, 4}) ||
		!bytes.Equal(d.UICCRefBytes(), []byte{9, 8, 7}) || d.String() != "héllo" || d.StringUTF8() != "utf8" {
		t.Fatal("round trip mismatch")
	}
	if err := d.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestDecoderShort(t *testing.T) {
	d := NewDecoder([]byte{1, 2})
	_ = d.U32()
	_ = d.U32()
	if !errors.Is(d.Err(), ErrShortBuffer) {
		t.Fatalf("err = %v", d.Err())
	}

	// 参照先がバッファの外
	d = NewDecoder(Encode(U32(100), U32(4)))
	if d.RefBytes() != nil || !errors.Is(d.Err(), ErrShortBuffer) {
		t.Fatalf("out-of-range ref: err = %v", d.Err())
	}
}

func TestRefStructs(t *testing.T) {
	// 構造体内の offset は構造体の先頭が基準
	s1 := Encode(U32(4), RefBytes([]byte{0xa0, 0x01}), RefBytes([]byte("USIM")))
	s2 := Encode(U32(6), RefBytes([]byte{0xa0, 0x02, 0x03}), RefBytes([]byte("ISIM")))
	buf := Encode(U32(2), RefBytes(s1), RefBytes(s2))

	d := NewDecoder(buf)
	n := d.U32()
	structs := d.RefStructs(n)
	if d.Err() != nil || len(structs) != 2 {
		t.Fatalf("RefStructs: %v, %d", d.Err(), len(structs))
	}
	for i, want := range []struct {
		typ  uint32
		aid  []byte
		name string
	}{{4, []byte{0xa0, 0x01}, "USIM"}, {6, []byte{0xa0, 0x02, 0x03}, "ISIM"}} {
		s := structs[i]
		if s.U32() != want.typ || !bytes.Equal(s.RefBytes(), want.aid) || s.StringUTF8() != want.name || s.Err() != nil {
			t.Errorf("struct %d mismatch (err %v)", i, s.Err())
		}
	}
}

func TestMessageRoundTrip(t *testing.T) {
	for _, m := range []*Message{
		{Type: TypeOpen, TxID: 1, MaxControlTransfer: 4096},
		{Type: TypeOpenDone, TxID: 1, Status: 0},
		{Type: TypeCommand, TxID: 2, Service: ServiceAuth, CID: CIDAuthAKA, CommandType: Query, Buffer: make([]byte, 32)},
		{Type: TypeCommandDone, TxID: 2, Service: ServiceAuth, CID: CIDAuthAKA, Status: 36, Buffer: []byte{1}},
		{Type: TypeIndicateStatus, Service: ServiceProxyControl, CID: 2, Buffer: []byte{0, 2, 0, 3}},
		{Type: TypeFunctionError, TxID: 3, Status: 5},
	} {
		got, err := ParseMessage(m.Marshal())
		if err != nil {
			t.Fatalf("%s: %v", m.Type, err)
		}
		if got.Type != m.Type || got.TxID != m.TxID || got.Service != m.Service || got.CID != m.CID ||
			got.CommandType != m.CommandType || got.Status != m.Status || got.MaxControlTransfer != m.MaxControlTransfer ||
			!bytes.Equal(got.Buffer, m.Buffer) {
			t.Errorf("%s: got %+v, want %+v", m.Type, got, m)
		}
	}
}

func TestFragmentReassemble(t *testing.T) {
	m := &Message{Type: TypeCommandDone, TxID: 9, Service: ServiceBasicConnect, CID: 1, Buffer: bytes.Repeat([]byte{0x5a}, 200)}
	full := m.Marshal()
	frags := Fragment(full, 64)
	if len(frags) < 3 {
		t.Fatalf("expected several fragments, got %d", len(frags))
	}
	var r reassembler
	for i, f := range frags {
		out, err := r.add(f)
		if err != nil {
			t.Fatal(err)
		}
		if (out != nil) != (i == len(frags)-1) {
			t.Fatalf("fragment %d: premature or missing completion", i)
		}
		if out != nil && !bytes.Equal(out, full) {
			t.Fatal("reassembled message differs")
		}
	}

	// 順序違いはエラー
	var r2 reassembler
	if _, err := r2.add(frags[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.add(frags[2]); err == nil {
		t.Fatal("expected out-of-sequence error")
	}
}
