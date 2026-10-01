package mbimaka_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/mbim/mbimtest"
	"github.com/oyaguma3/simwifi/internal/simauth"
	"github.com/oyaguma3/simwifi/internal/simauth/mbimaka"
	"github.com/oyaguma3/simwifi/internal/simauth/milenage"
)

// 逆順にすると値が変わる RAND（並びの判定の試験用）。
func asymRand(seed byte) []byte {
	r := make([]byte, 16)
	for i := range r {
		r[i] = seed + byte(i)*7
	}
	return r
}

type orderEnv struct {
	a   *mbimaka.Authenticator
	hss *milenage.Milenage
	srv *mbimtest.Server
}

func setupOrder(t *testing.T, reversed bool, mode mbimtest.AKAMode, sqnMS uint64) *orderEnv {
	t.Helper()
	usim, err := milenage.NewUSIM(testK, testOPc, sqnMS)
	if err != nil {
		t.Fatal(err)
	}
	hss, err := milenage.New(testK, testOPc)
	if err != nil {
		t.Fatal(err)
	}
	s := mbimtest.NewServer(t, mbimtest.Options{})
	if reversed {
		mbimtest.HandleAKAReversed(s, usim, mode)
	} else {
		mbimtest.HandleAKA(s, usim, mode)
	}
	c, err := mbim.Dial(t.Context(), mbim.Options{ProxyAddr: s.Addr, DevicePath: "/dev/cdc-wdm0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &orderEnv{a: mbimaka.New(c, nil), hss: hss, srv: s}
}

func (e *orderEnv) requests() int { return e.srv.Count(mbim.ServiceAuth, mbim.CIDAuthAKA) }

// 逆順のモデム（Quectel EG25-G、Sierra EM7455 の実機の挙動）でも 3GPP の並びのモデムでも、
// 正しい RES / CK / IK が得られ、判明した並びが以後も使われる。
func TestByteOrderSuccess(t *testing.T) {
	for _, tt := range []struct {
		name          string
		reversed      bool
		firstRequests int // 1 回目の認証で送る要求の数
	}{
		{"reversed modem", true, 1},
		{"standard modem", false, 2}, // 逆順で拒否されてから標準の並びで成功
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := setupOrder(t, tt.reversed, mbimtest.ResyncInSuccess, 0)
			for i, wantTotal := range []int{tt.firstRequests, tt.firstRequests + 1} {
				rand := asymRand(byte(0x10 * (i + 1)))
				autn, v := e.hss.GenerateAUTN(rand, uint64(i+1), testAMF)
				r, err := e.a.Authenticate(t.Context(), rand, autn[:])
				if err != nil {
					t.Fatalf("#%d: %v", i, err)
				}
				if !bytes.Equal(r.RES, v.RES[:]) || r.CK != v.CK || r.IK != v.IK {
					t.Fatalf("#%d: RES/CK/IK mismatch", i)
				}
				if n := e.requests(); n != wantTotal {
					t.Fatalf("#%d: %d AKA requests sent, want %d", i, n, wantTotal)
				}
			}
		})
	}
}

func TestByteOrderResync(t *testing.T) {
	for _, reversed := range []bool{true, false} {
		for _, mode := range []mbimtest.AKAMode{mbimtest.ResyncInSuccess, mbimtest.ResyncAsStatus} {
			e := setupOrder(t, reversed, mode, 0x900)
			rand := asymRand(0x31)
			autn, _ := e.hss.GenerateAUTN(rand, 0x20, testAMF) // 古い SQN
			r, err := e.a.Authenticate(t.Context(), rand, autn[:])
			if !errors.Is(err, simauth.ErrResync) {
				t.Fatalf("reversed=%v mode=%d: err = %v", reversed, mode, err)
			}
			if sqn, err := e.hss.ResyncSQN(rand, r.AUTS); err != nil || sqn != 0x900 {
				t.Fatalf("reversed=%v mode=%d: AUTS not decoded correctly (%x, %v)", reversed, mode, sqn, err)
			}
		}
	}
}

// 本当に AUTN が不正なら、両方の並びで拒否され、並びは決まらないまま。
func TestByteOrderGenuineReject(t *testing.T) {
	e := setupOrder(t, true, mbimtest.ResyncInSuccess, 0)
	rand := asymRand(0x55)
	autn, _ := e.hss.GenerateAUTN(rand, 1, testAMF)
	autn[15] ^= 1
	if _, err := e.a.Authenticate(t.Context(), rand, autn[:]); !errors.Is(err, simauth.ErrAuthReject) {
		t.Fatalf("err = %v, want ErrAuthReject", err)
	}
	if n := e.requests(); n != 2 {
		t.Fatalf("%d requests, want 2 (both orders)", n)
	}
	// 並びは決まっていないので、次の正しいチャレンジは逆順で 1 回目に成功する
	autn2, v := e.hss.GenerateAUTN(rand, 2, testAMF)
	r, err := e.a.Authenticate(t.Context(), rand, autn2[:])
	if err != nil || !bytes.Equal(r.RES, v.RES[:]) {
		t.Fatalf("after reject: %v", err)
	}
	if n := e.requests(); n != 3 {
		t.Fatalf("%d requests, want 3", n)
	}
}
