// Package milenage は 3GPP TS 35.206 の Milenage アルゴリズムと、
// それを使うソフトウェア USIM / HSS を実装する。
// 単体テスト、fake mbim-proxy、E2E（ビルドタグ e2e）で使う。実 SIM の代わりにはならない。
package milenage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"fmt"
)

// 各値の長さ（バイト）。
const (
	KeyLen = 16
	SQNLen = 6
	AMFLen = 2
	MACLen = 8
	RESLen = 8
	AKLen  = 6
)

// Milenage は K と OPc を保持した Milenage の計算器。
type Milenage struct {
	block cipher.Block
	opc   [KeyLen]byte
}

// New は K と OPc から計算器を作る。
func New(k, opc []byte) (*Milenage, error) {
	if len(k) != KeyLen || len(opc) != KeyLen {
		return nil, fmt.Errorf("milenage: K and OPc must be %d bytes", KeyLen)
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	m := &Milenage{block: block}
	copy(m.opc[:], opc)
	return m, nil
}

// ComputeOPc は OPc = E_K(OP) ⊕ OP を計算する。
func ComputeOPc(k, op []byte) ([KeyLen]byte, error) {
	var opc [KeyLen]byte
	if len(k) != KeyLen || len(op) != KeyLen {
		return opc, fmt.Errorf("milenage: K and OP must be %d bytes", KeyLen)
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return opc, err
	}
	block.Encrypt(opc[:], op)
	subtle.XORBytes(opc[:], opc[:], op)
	return opc, nil
}

// temp は TEMP = E_K(RAND ⊕ OPc) を計算する。
func (m *Milenage) temp(rand []byte) [KeyLen]byte {
	var t [KeyLen]byte
	subtle.XORBytes(t[:], rand, m.opc[:])
	m.block.Encrypt(t[:], t[:])
	return t
}

// out は OUTn = E_K(rot(in ⊕ OPc, r) ⊕ c) ⊕ OPc を計算する。
// r はバイト単位の回転量、c は最終バイトの定数（c1..c5）。
func (m *Milenage) out(in [KeyLen]byte, r int, c byte) [KeyLen]byte {
	var x, rot [KeyLen]byte
	subtle.XORBytes(x[:], in[:], m.opc[:])
	for i := range KeyLen {
		rot[i] = x[(i+r)%KeyLen]
	}
	rot[KeyLen-1] ^= c
	m.block.Encrypt(rot[:], rot[:])
	subtle.XORBytes(rot[:], rot[:], m.opc[:])
	return rot
}

// F1 は f1（MAC-A）と f1*（MAC-S）を計算する。
func (m *Milenage) F1(rand, sqn, amf []byte) (macA, macS [MACLen]byte) {
	t := m.temp(rand)
	var in1 [KeyLen]byte
	copy(in1[0:6], sqn)
	copy(in1[6:8], amf)
	copy(in1[8:14], sqn)
	copy(in1[14:16], amf)
	// OUT1 = E_K(TEMP ⊕ rot(IN1 ⊕ OPc, r1) ⊕ c1) ⊕ OPc、r1 = 64 ビット、c1 = 0
	var x, rot [KeyLen]byte
	subtle.XORBytes(x[:], in1[:], m.opc[:])
	for i := range KeyLen {
		rot[i] = x[(i+8)%KeyLen]
	}
	subtle.XORBytes(rot[:], rot[:], t[:])
	m.block.Encrypt(rot[:], rot[:])
	subtle.XORBytes(rot[:], rot[:], m.opc[:])
	copy(macA[:], rot[0:8])
	copy(macS[:], rot[8:16])
	return macA, macS
}

// Vector は f2〜f5 の出力。
type Vector struct {
	RES [RESLen]byte
	CK  [KeyLen]byte
	IK  [KeyLen]byte
	AK  [AKLen]byte
}

// F2345 は f2（RES）、f3（CK）、f4（IK）、f5（AK）を計算する。
func (m *Milenage) F2345(rand []byte) Vector {
	t := m.temp(rand)
	var v Vector
	out2 := m.out(t, 0, 1) // r2 = 0, c2 = 1
	copy(v.AK[:], out2[0:6])
	copy(v.RES[:], out2[8:16])
	v.CK = m.out(t, 4, 2) // r3 = 32, c3 = 2
	v.IK = m.out(t, 8, 4) // r4 = 64, c4 = 4
	return v
}

// F5Star は f5*（再同期用の AK）を計算する。
func (m *Milenage) F5Star(rand []byte) [AKLen]byte {
	t := m.temp(rand)
	out5 := m.out(t, 12, 8) // r5 = 96, c5 = 8
	var ak [AKLen]byte
	copy(ak[:], out5[0:6])
	return ak
}
