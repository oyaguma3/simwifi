package mbim

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf16"
)

// 情報バッファ（InformationBuffer）の組み立てと解析。
// 可変長データは「静的部分に offset / size の組、データは後ろ」という MBIM の一般形で表す。
// offset は構造体の先頭（トップレベルなら情報バッファの先頭）からの相対値。

var le = binary.LittleEndian

// Field は Encode に渡すフィールド。
type Field struct {
	u32     *uint32
	fixed   []byte
	ref     []byte
	swapped bool // size, offset の順（libmbim の uicc-ref-byte-array）
}

// U32 は 32 ビット整数のフィールド。
func U32(v uint32) Field { return Field{u32: &v} }

// Fixed は固定長のバイト列（静的部分にそのまま置く）。
func Fixed(b []byte) Field { return Field{fixed: b} }

// RefBytes は offset, size の組で参照するバイト列。
func RefBytes(b []byte) Field { return Field{ref: nonNil(b)} }

// UICCRefBytes は size, offset の順で参照するバイト列（MS UICC Low-Level Access）。
func UICCRefBytes(b []byte) Field { return Field{ref: nonNil(b), swapped: true} }

// String は UTF-16LE の文字列（offset, size で参照）。
func String(s string) Field {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		le.PutUint16(b[2*i:], c)
	}
	return RefBytes(b)
}

func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func (f Field) staticSize() int {
	switch {
	case f.u32 != nil:
		return 4
	case f.ref != nil:
		return 8
	}
	return len(f.fixed)
}

// Encode は情報バッファを組み立てる。可変長データは 4 バイト境界に揃える。
func Encode(fields ...Field) []byte {
	static := 0
	for _, f := range fields {
		static += f.staticSize()
	}
	buf := make([]byte, 0, static)
	var data []byte
	for _, f := range fields {
		switch {
		case f.u32 != nil:
			buf = le.AppendUint32(buf, *f.u32)
		case f.ref != nil:
			off, size := uint32(0), uint32(len(f.ref))
			if size > 0 {
				off = uint32(static + len(data))
				data = append(data, f.ref...)
				data = append(data, make([]byte, pad4(len(f.ref)))...)
			}
			if f.swapped {
				buf = le.AppendUint32(buf, size)
				buf = le.AppendUint32(buf, off)
			} else {
				buf = le.AppendUint32(buf, off)
				buf = le.AppendUint32(buf, size)
			}
		default:
			buf = append(buf, f.fixed...)
		}
	}
	return append(buf, data...)
}

func pad4(n int) int { return (4 - n%4) % 4 }

// ErrShortBuffer は情報バッファが想定より短いことを示す。
var ErrShortBuffer = errors.New("mbim: information buffer too short")

// Decoder は情報バッファを先頭から読む。最初のエラー以降の読み取りはゼロ値を返す。
type Decoder struct {
	buf  []byte // 情報バッファ全体
	base int    // offset の基準（構造体の先頭）
	pos  int    // 次に読む位置（buf 内の絶対位置）
	err  error
}

// NewDecoder は情報バッファ全体を読む Decoder を作る。
func NewDecoder(buf []byte) *Decoder { return &Decoder{buf: buf} }

// Err は最初に起きたエラーを返す。
func (d *Decoder) Err() error { return d.err }

func (d *Decoder) fail(format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("%w: "+format, append([]any{ErrShortBuffer}, args...)...)
	}
}

// Fixed は n バイトをそのまま読む（コピーを返す）。
func (d *Decoder) Fixed(n int) []byte {
	if d.err != nil {
		return make([]byte, n)
	}
	if d.pos+n > len(d.buf) {
		d.fail("need %d bytes at %d, have %d", n, d.pos, len(d.buf))
		return make([]byte, n)
	}
	b := append([]byte(nil), d.buf[d.pos:d.pos+n]...)
	d.pos += n
	return b
}

// U32 は 32 ビット整数を読む。
func (d *Decoder) U32() uint32 {
	return le.Uint32(d.Fixed(4))
}

// RefBytes は offset, size の組を読み、参照先のバイト列を返す。
func (d *Decoder) RefBytes() []byte {
	off, size := d.U32(), d.U32()
	return d.deref(off, size)
}

// UICCRefBytes は size, offset の組を読み、参照先のバイト列を返す。
func (d *Decoder) UICCRefBytes() []byte {
	size, off := d.U32(), d.U32()
	return d.deref(off, size)
}

// String は offset, size で参照される UTF-16LE 文字列を読む。
func (d *Decoder) String() string {
	b := d.RefBytes()
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = le.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// StringUTF8 は offset, size で参照される UTF-8 文字列を読む。
func (d *Decoder) StringUTF8() string {
	return string(d.RefBytes())
}

func (d *Decoder) deref(off, size uint32) []byte {
	if d.err != nil || size == 0 {
		return nil
	}
	start := uint64(d.base) + uint64(off)
	end := start + uint64(size)
	if end > uint64(len(d.buf)) {
		d.fail("reference %d+%d beyond %d", start, size, len(d.buf))
		return nil
	}
	return append([]byte(nil), d.buf[start:end]...)
}

// RefStructs は ref-struct-array（count 個の offset, size の組）を読み、
// 各構造体を読む Decoder を返す。構造体内の offset はその構造体の先頭が基準。
func (d *Decoder) RefStructs(count uint32) []*Decoder {
	if count > 1024 {
		d.fail("too many structs (%d)", count)
		return nil
	}
	var out []*Decoder
	for range count {
		off, size := d.U32(), d.U32()
		if d.err != nil {
			return nil
		}
		start := uint64(d.base) + uint64(off)
		if start+uint64(size) > uint64(len(d.buf)) {
			d.fail("struct %d+%d beyond %d", start, size, len(d.buf))
			return nil
		}
		out = append(out, &Decoder{buf: d.buf, base: int(start), pos: int(start)})
	}
	return out
}
