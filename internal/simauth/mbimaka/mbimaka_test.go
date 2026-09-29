package mbimaka_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/mbim/mbimtest"
	"github.com/oyaguma3/simwifi/internal/simauth"
	"github.com/oyaguma3/simwifi/internal/simauth/mbimaka"
	"github.com/oyaguma3/simwifi/internal/simauth/milenage"
)

// 3GPP TS 35.208 Test Set 19（AMF 分離ビット ON。hostap の hlr_auc_gw.milenage_db と同じ加入者）
var (
	testK   = mustHex("5122250214c33e723a5dd523fc145fc0")
	testOPc = mustHex("981d464c7c52eb6e5036234984ad0bcf")
	testAMF = [2]byte{0xc3, 0xab}
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func setup(t *testing.T, mode mbimtest.AKAMode, sqnMS uint64) (*mbimaka.Authenticator, *milenage.Milenage, *milenage.USIM) {
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
	mbimtest.HandleAKA(s, usim, mode)
	c, err := mbim.Dial(t.Context(), mbim.Options{ProxyAddr: s.Addr, DevicePath: "/dev/cdc-wdm0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return mbimaka.New(c, nil), hss, usim
}

func TestSuccess(t *testing.T) {
	a, hss, _ := setup(t, mbimtest.ResyncInSuccess, 0)
	rand := bytes.Repeat([]byte{0x42}, 16)
	autn, v := hss.GenerateAUTN(rand, 1, testAMF)

	r, err := a.Authenticate(t.Context(), rand, autn[:])
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !bytes.Equal(r.RES, v.RES[:]) || r.CK != v.CK || r.IK != v.IK || r.AUTS != nil {
		t.Fatalf("result mismatch: RES=%x", r.RES)
	}
}

func TestResync(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode mbimtest.AKAMode
		want error
	}{
		{"ResLen0+AUTS", mbimtest.ResyncInSuccess, simauth.ErrResync},
		{"SYNC_FAILURE+AUTS", mbimtest.ResyncAsStatus, simauth.ErrResync},
		{"SYNC_FAILURE without AUTS", mbimtest.ResyncAsStatusNoBuffer, simauth.ErrAuthReject},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, hss, _ := setup(t, tt.mode, 0x500)
			rand := bytes.Repeat([]byte{0x43}, 16)
			autn, _ := hss.GenerateAUTN(rand, 0x10, testAMF) // 古い SQN

			r, err := a.Authenticate(t.Context(), rand, autn[:])
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.want != simauth.ErrResync {
				return
			}
			sqnMS, err := hss.ResyncSQN(rand, r.AUTS)
			if err != nil || sqnMS != 0x500 {
				t.Fatalf("ResyncSQN = %x, %v", sqnMS, err)
			}
		})
	}
}

func TestReject(t *testing.T) {
	a, hss, _ := setup(t, mbimtest.ResyncInSuccess, 0)
	rand := bytes.Repeat([]byte{0x44}, 16)
	autn, _ := hss.GenerateAUTN(rand, 1, testAMF)
	autn[9] ^= 0xff
	if _, err := a.Authenticate(t.Context(), rand, autn[:]); !errors.Is(err, simauth.ErrAuthReject) {
		t.Fatalf("err = %v, want ErrAuthReject", err)
	}
}

func TestUnsupported(t *testing.T) {
	s := mbimtest.NewServer(t, mbimtest.Options{}) // AKA 未登録 → NO_DEVICE_SUPPORT
	c, err := mbim.Dial(t.Context(), mbim.Options{ProxyAddr: s.Addr, DevicePath: "/dev/cdc-wdm0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = mbimaka.New(c, nil).Authenticate(t.Context(), make([]byte, 16), make([]byte, 16))
	if !errors.Is(err, simauth.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestOtherStatus(t *testing.T) {
	s := mbimtest.NewServer(t, mbimtest.Options{})
	s.Handle(mbim.ServiceAuth, mbim.CIDAuthAKA, func(mbimtest.Request) mbimtest.Response {
		return mbimtest.Response{Status: mbim.StatusBusy}
	})
	c, err := mbim.Dial(t.Context(), mbim.Options{ProxyAddr: s.Addr, DevicePath: "/dev/cdc-wdm0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = mbimaka.New(c, nil).Authenticate(t.Context(), make([]byte, 16), make([]byte, 16))
	if st, _ := mbim.StatusOf(err); st != mbim.StatusBusy ||
		errors.Is(err, simauth.ErrAuthReject) || errors.Is(err, simauth.ErrUnsupported) {
		t.Fatalf("err = %v, want plain busy error", err)
	}
}

func TestBadResponse(t *testing.T) {
	s := mbimtest.NewServer(t, mbimtest.Options{})
	s.Handle(mbim.ServiceAuth, mbim.CIDAuthAKA, func(mbimtest.Request) mbimtest.Response {
		return mbimtest.Response{Buffer: make([]byte, 66)} // ResLen 0、AUTS 全ゼロ
	})
	c, err := mbim.Dial(t.Context(), mbim.Options{ProxyAddr: s.Addr, DevicePath: "/dev/cdc-wdm0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = mbimaka.New(c, nil).Authenticate(t.Context(), make([]byte, 16), make([]byte, 16))
	if err == nil || errors.Is(err, simauth.ErrResync) {
		t.Fatalf("err = %v, want invalid response error", err)
	}
}
