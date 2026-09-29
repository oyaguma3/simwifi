package mbimuicc

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/mbim/mbimtest"
	"github.com/oyaguma3/simwifi/internal/simauth"
	"github.com/oyaguma3/simwifi/internal/simauth/milenage"
)

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

type env struct {
	a    *Authenticator
	c    *mbim.Client
	hss  *milenage.Milenage
	card *mbimtest.UICC
	srv  *mbimtest.Server
}

func setup(t *testing.T, card *mbimtest.UICC, sqnMS uint64) *env {
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
	if card != nil {
		card.Auth = usim
		card.Install(s)
	}
	c, err := mbim.Dial(t.Context(), mbim.Options{ProxyAddr: s.Addr, DevicePath: "/dev/cdc-wdm0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &env{a: New(c, nil), c: c, hss: hss, card: card, srv: s}
}

func TestSuccess(t *testing.T) {
	for _, tt := range []struct {
		name    string
		card    *mbimtest.UICC
		wantAID []byte
	}{
		{"application list", &mbimtest.UICC{AppList: true}, mbimtest.USIMAID},
		{"partial AID", &mbimtest.UICC{}, PartialUSIMAID},
		{"exact AID required", &mbimtest.UICC{AppList: true, ExactAIDOnly: true}, mbimtest.USIMAID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, tt.card, 0)
			for i := range 2 {
				rand := bytes.Repeat([]byte{byte(0x50 + i)}, 16)
				autn, v := e.hss.GenerateAUTN(rand, uint64(i+1), testAMF)
				r, err := e.a.Authenticate(t.Context(), rand, autn[:])
				if err != nil {
					t.Fatalf("Authenticate #%d: %v", i, err)
				}
				if !bytes.Equal(r.RES, v.RES[:]) || r.CK != v.CK || r.IK != v.IK {
					t.Fatalf("#%d: result mismatch", i)
				}
			}
			if !bytes.Equal(e.a.aid, tt.wantAID) {
				t.Errorf("AID = %x, want %x", e.a.aid, tt.wantAID)
			}
			if n := e.card.OpenChannels(); n != 0 {
				t.Errorf("%d channels left open", n)
			}
		})
	}
}

func TestResync(t *testing.T) {
	e := setup(t, &mbimtest.UICC{}, 0x900)
	rand := bytes.Repeat([]byte{0x61}, 16)
	autn, _ := e.hss.GenerateAUTN(rand, 0x20, testAMF)
	r, err := e.a.Authenticate(t.Context(), rand, autn[:])
	if !errors.Is(err, simauth.ErrResync) {
		t.Fatalf("err = %v, want ErrResync", err)
	}
	if sqn, err := e.hss.ResyncSQN(rand, r.AUTS); err != nil || sqn != 0x900 {
		t.Fatalf("ResyncSQN = %x, %v", sqn, err)
	}
	if e.card.OpenChannels() != 0 {
		t.Error("channel left open")
	}
}

func TestReject(t *testing.T) {
	e := setup(t, &mbimtest.UICC{}, 0)
	rand := bytes.Repeat([]byte{0x62}, 16)
	autn, _ := e.hss.GenerateAUTN(rand, 1, testAMF)
	autn[12] ^= 1
	if _, err := e.a.Authenticate(t.Context(), rand, autn[:]); !errors.Is(err, simauth.ErrAuthReject) {
		t.Fatalf("err = %v, want ErrAuthReject", err)
	}
	if e.card.OpenChannels() != 0 {
		t.Error("channel left open")
	}
}

func TestUnsupported(t *testing.T) {
	for _, tt := range []struct {
		name string
		card *mbimtest.UICC
	}{
		{"no UICC service", nil},
		{"no logical channels", &mbimtest.UICC{NoLogicalChannels: true}},
		{"partial AID rejected", &mbimtest.UICC{ExactAIDOnly: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, tt.card, 0)
			_, err := e.a.Authenticate(t.Context(), make([]byte, 16), make([]byte, 16))
			if !errors.Is(err, simauth.ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestStaleChannelCleanup(t *testing.T) {
	e := setup(t, &mbimtest.UICC{}, 0)
	// 前回のプロセスが閉じ忘れたチャネル
	if _, err := OpenChannel(t.Context(), e.c, PartialUSIMAID); err != nil {
		t.Fatal(err)
	}
	if e.card.OpenChannels() != 1 {
		t.Fatal("setup failed")
	}
	rand := bytes.Repeat([]byte{0x63}, 16)
	autn, _ := e.hss.GenerateAUTN(rand, 1, testAMF)
	if _, err := e.a.Authenticate(t.Context(), rand, autn[:]); err != nil {
		t.Fatal(err)
	}
	if n := e.card.OpenChannels(); n != 0 {
		t.Fatalf("%d channels left open", n)
	}
}

func TestProbeHelpers(t *testing.T) {
	e := setup(t, &mbimtest.UICC{AppList: true}, 0)
	atr, err := ATR(t.Context(), e.c)
	if err != nil || len(atr) == 0 {
		t.Fatalf("ATR = %x, %v", atr, err)
	}
	apps, err := ApplicationList(t.Context(), e.c)
	if err != nil || len(apps) != 2 {
		t.Fatalf("ApplicationList = %v, %v", apps, err)
	}
	if apps[1].Type != applicationTypeUSIM || apps[1].Name != "USIM" || !bytes.Equal(apps[1].AID, mbimtest.USIMAID) {
		t.Errorf("USIM entry = %+v", apps[1])
	}
	if err := CheckChannel(t.Context(), e.c, PartialUSIMAID); err != nil {
		t.Fatal(err)
	}
	if e.card.OpenChannels() != 0 || e.card.APDUs() != 0 {
		t.Error("CheckChannel must not leave channels or send APDUs")
	}

	e2 := setup(t, &mbimtest.UICC{}, 0)
	if _, err := ApplicationList(t.Context(), e2.c); !errors.Is(err, simauth.ErrUnsupported) {
		t.Errorf("ApplicationList without support: %v", err)
	}
}

func TestGetResponse(t *testing.T) {
	s := mbimtest.NewServer(t, mbimtest.Options{})
	svc := mbim.ServiceMSUICCLowLevelAccess
	s.Handle(svc, mbim.CIDUICCOpenChannel, func(mbimtest.Request) mbimtest.Response {
		return mbimtest.Response{Buffer: mbim.Encode(mbim.U32(mbimtest.SW(0x90, 0)), mbim.U32(2), mbim.UICCRefBytes(nil))}
	})
	s.Handle(svc, mbim.CIDUICCCloseChannel, func(mbimtest.Request) mbimtest.Response {
		return mbimtest.Response{Buffer: mbim.Encode(mbim.U32(mbimtest.SW(0x90, 0)))}
	})
	body := append([]byte{0xdb, 0x04, 1, 2, 3, 4, 0x10}, append(bytes.Repeat([]byte{0xcc}, 16), append([]byte{0x10}, bytes.Repeat([]byte{0x11}, 16)...)...)...)
	s.Handle(svc, mbim.CIDUICCAPDU, func(r mbimtest.Request) mbimtest.Response {
		d := mbim.NewDecoder(r.Buffer)
		_, _, _ = d.U32(), d.U32(), d.U32()
		cmd := d.UICCRefBytes()
		if cmd[1] == 0xc0 { // GET RESPONSE
			return mbimtest.Response{Buffer: mbim.Encode(mbim.U32(mbimtest.SW(0x90, 0)), mbim.UICCRefBytes(body))}
		}
		return mbimtest.Response{Buffer: mbim.Encode(mbim.U32(mbimtest.SW(0x61, byte(len(body)))), mbim.UICCRefBytes(nil))}
	})
	c, err := mbim.Dial(t.Context(), mbim.Options{ProxyAddr: s.Addr, DevicePath: "/dev/cdc-wdm0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r, err := New(c, nil).Authenticate(t.Context(), make([]byte, 16), make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.RES, []byte{1, 2, 3, 4}) || r.CK[0] != 0xcc || r.IK[0] != 0x11 {
		t.Fatalf("result = %+v", r)
	}
}

func TestParseAuthenticate(t *testing.T) {
	ck, ik := bytes.Repeat([]byte{0xc}, 16), bytes.Repeat([]byte{0x1}, 16)
	ok := append(append(append([]byte{0xdb, 8}, make([]byte, 8)...), append([]byte{16}, ck...)...), append([]byte{16}, ik...)...)
	for _, tt := range []struct {
		name string
		resp []byte
		sw   uint16
		want error // nil なら成功
		fail bool
	}{
		{"success", ok, 0x9000, nil, false},
		{"success with 91xx", ok, 0x9110, nil, false},
		{"success with Kc", append(slicesClone(ok), 8, 1, 2, 3, 4, 5, 6, 7, 8), 0x9000, nil, false},
		{"resync", append([]byte{0xdc, 14}, make([]byte, 14)...), 0x9000, simauth.ErrResync, true},
		{"mac failure", nil, 0x9862, simauth.ErrAuthReject, true},
		{"security status", nil, 0x6982, nil, true},
		{"truncated", ok[:20], 0x9000, nil, true},
		{"short RES", []byte{0xdb, 2, 1, 2, 16}, 0x9000, nil, true},
		{"bad AUTS", []byte{0xdc, 3, 1, 2, 3}, 0x9000, nil, true},
		{"unknown tag", []byte{0xaa}, 0x9000, nil, true},
		{"empty", nil, 0x9000, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseAuthenticate(tt.resp, tt.sw)
			if (err != nil) != tt.fail {
				t.Fatalf("err = %v", err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func slicesClone(b []byte) []byte { return append([]byte(nil), b...) }

func TestStatusWord(t *testing.T) {
	if got := statusWord(mbimtest.SW(0x98, 0x62)); got != 0x9862 {
		t.Fatalf("statusWord = %04x", got)
	}
}
