package milenage

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/oyaguma3/simwifi/internal/simauth"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTestSets(t *testing.T) {
	for _, ts := range testSets {
		t.Run(ts.Name, func(t *testing.T) {
			k, op, rand := mustHex(t, ts.K), mustHex(t, ts.OP), mustHex(t, ts.RAND)
			sqn, amf := mustHex(t, ts.SQN), mustHex(t, ts.AMF)

			opc, err := ComputeOPc(k, op)
			if err != nil {
				t.Fatal(err)
			}
			check(t, "OPc", opc[:], ts.OPc)

			m, err := New(k, opc[:])
			if err != nil {
				t.Fatal(err)
			}
			macA, macS := m.F1(rand, sqn, amf)
			check(t, "f1", macA[:], ts.F1)
			check(t, "f1*", macS[:], ts.F1Star)

			v := m.F2345(rand)
			check(t, "f2", v.RES[:], ts.F2)
			check(t, "f3", v.CK[:], ts.F3)
			check(t, "f4", v.IK[:], ts.F4)
			check(t, "f5", v.AK[:], ts.F5)

			akStar := m.F5Star(rand)
			check(t, "f5*", akStar[:], ts.F5Star)
		})
	}
}

func check(t *testing.T, name string, got []byte, wantHex string) {
	t.Helper()
	if h := hex.EncodeToString(got); h != wantHex {
		t.Errorf("%s = %s, want %s", name, h, wantHex)
	}
}

func TestSQNBytes(t *testing.T) {
	b := PutSQN(0xff9bb4d0b607)
	if !bytes.Equal(b[:], []byte{0xff, 0x9b, 0xb4, 0xd0, 0xb6, 0x07}) {
		t.Fatalf("PutSQN = %x", b)
	}
	if got := SQNFromBytes(b[:]); got != 0xff9bb4d0b607 {
		t.Fatalf("SQNFromBytes = %x", got)
	}
}

// newPair は同じ加入者情報を持つ網側（Milenage）と USIM を作る。
func newPair(t *testing.T, sqnMS uint64) (*Milenage, *USIM) {
	t.Helper()
	ts := testSets[0]
	k, opc := mustHex(t, ts.K), mustHex(t, ts.OPc)
	hss, err := New(k, opc)
	if err != nil {
		t.Fatal(err)
	}
	usim, err := NewUSIM(k, opc, sqnMS)
	if err != nil {
		t.Fatal(err)
	}
	return hss, usim
}

func TestUSIMSuccess(t *testing.T) {
	hss, usim := newPair(t, 0x20)
	rand := bytes.Repeat([]byte{0x11}, 16)
	autn, v := hss.GenerateAUTN(rand, 0x21, [2]byte{0x80, 0x00})

	res, err := usim.Authenticate(t.Context(), rand, autn[:])
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !bytes.Equal(res.RES, v.RES[:]) || res.CK != v.CK || res.IK != v.IK {
		t.Fatal("RES/CK/IK mismatch")
	}
	if usim.SQN() != 0x21 {
		t.Fatalf("SQN = %x, want 21", usim.SQN())
	}
	res.Clear()
	if res.RES != nil || res.CK != [16]byte{} || res.IK != [16]byte{} {
		t.Fatal("Clear did not zero key material")
	}
}

func TestUSIMResync(t *testing.T) {
	hss, usim := newPair(t, 0x100)
	rand := bytes.Repeat([]byte{0x22}, 16)
	// 受理済み SQN 以下の SQN は再同期になる
	autn, _ := hss.GenerateAUTN(rand, 0x100, [2]byte{})

	res, err := usim.Authenticate(t.Context(), rand, autn[:])
	if !errors.Is(err, simauth.ErrResync) {
		t.Fatalf("err = %v, want ErrResync", err)
	}
	if len(res.AUTS) != simauth.AUTSLen {
		t.Fatalf("AUTS len = %d", len(res.AUTS))
	}
	sqnMS, err := hss.ResyncSQN(rand, res.AUTS)
	if err != nil {
		t.Fatalf("ResyncSQN: %v", err)
	}
	if sqnMS != 0x100 {
		t.Fatalf("SQNms = %x, want 100", sqnMS)
	}

	// 網側が SQN を進めて再送すれば成功する
	autn, _ = hss.GenerateAUTN(rand, sqnMS+1, [2]byte{})
	if _, err := usim.Authenticate(t.Context(), rand, autn[:]); err != nil {
		t.Fatalf("after resync: %v", err)
	}

	// AUTS の改ざんは検出される
	res.AUTS[13] ^= 1
	if _, err := hss.ResyncSQN(rand, res.AUTS); !errors.Is(err, ErrBadAUTS) {
		t.Fatalf("tampered AUTS: err = %v", err)
	}
}

func TestUSIMMACFailure(t *testing.T) {
	hss, usim := newPair(t, 0)
	rand := bytes.Repeat([]byte{0x33}, 16)
	autn, _ := hss.GenerateAUTN(rand, 1, [2]byte{})
	autn[15] ^= 0x01

	if _, err := usim.Authenticate(t.Context(), rand, autn[:]); !errors.Is(err, simauth.ErrAuthReject) {
		t.Fatalf("err = %v, want ErrAuthReject", err)
	}
	if usim.SQN() != 0 {
		t.Fatal("SQN must not advance on MAC failure")
	}
}

func TestUSIMBadInput(t *testing.T) {
	_, usim := newPair(t, 0)
	if _, err := usim.Authenticate(t.Context(), make([]byte, 15), make([]byte, 16)); err == nil {
		t.Fatal("expected length error")
	}
	if _, err := New(make([]byte, 15), make([]byte, 16)); err == nil {
		t.Fatal("expected key length error")
	}
}
