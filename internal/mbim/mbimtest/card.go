package mbimtest

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/simauth"
)

// USIMAID は fake カードの USIM の完全な AID（RID + PIX。末尾はカード固有の例）。
var USIMAID = []byte{0xa0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02, 0xff, 0x49, 0xff, 0x05, 0x89}

// AKAMode は Auth AKA CID で同期失敗をどう返すか（モデムによって異なる。DESIGN §4.3）。
type AKAMode int

const (
	// ResyncInSuccess は Status 成功、ResLen 0、Auts 非ゼロで返す。
	ResyncInSuccess AKAMode = iota
	// ResyncAsStatus は Status AUTH_SYNC_FAILURE と Auts 入りのバッファで返す。
	ResyncAsStatus
	// ResyncAsStatusNoBuffer は Status AUTH_SYNC_FAILURE だけを返す（AUTS 無し）。
	ResyncAsStatusNoBuffer
)

// HandleAKA は Auth AKA CID を auth で処理するハンドラを登録する。
func HandleAKA(s *Server, auth simauth.Authenticator, mode AKAMode) {
	s.Handle(mbim.ServiceAuth, mbim.CIDAuthAKA, func(r Request) Response {
		d := mbim.NewDecoder(r.Buffer)
		rand, autn := d.Fixed(16), d.Fixed(16)
		if r.Type != mbim.Query || d.Err() != nil {
			return Response{Status: mbim.StatusInvalidParameters}
		}
		res, err := auth.Authenticate(context.Background(), rand, autn)
		switch {
		case err == nil:
			return Response{Buffer: akaResponse(res.RES, res.CK[:], res.IK[:], nil)}
		case errors.Is(err, simauth.ErrResync):
			switch mode {
			case ResyncAsStatus:
				return Response{Status: mbim.StatusAuthSyncFailure, Buffer: akaResponse(nil, nil, nil, res.AUTS)}
			case ResyncAsStatusNoBuffer:
				return Response{Status: mbim.StatusAuthSyncFailure}
			}
			return Response{Buffer: akaResponse(nil, nil, nil, res.AUTS)}
		case errors.Is(err, simauth.ErrAuthReject):
			return Response{Status: mbim.StatusAuthIncorrectAUTN}
		}
		return Response{Status: mbim.StatusFailure}
	})
}

// akaResponse は Res[16], ResLen, IK[16], CK[16], Auts[14] を組み立てる。
func akaResponse(res, ck, ik, auts []byte) []byte {
	pad := func(b []byte, n int) []byte { return append(append([]byte(nil), b...), make([]byte, n-len(b))...) }
	return mbim.Encode(mbim.Fixed(pad(res, 16)), mbim.U32(uint32(len(res))),
		mbim.Fixed(pad(ik, 16)), mbim.Fixed(pad(ck, 16)), mbim.Fixed(pad(auts, 14)))
}

// UICC は MS UICC Low-Level Access を模擬する fake カード。
type UICC struct {
	Auth simauth.Authenticator
	// AppList なら APPLICATION_LIST に応答する（false なら NO_DEVICE_SUPPORT）。
	AppList bool
	// ExactAIDOnly なら部分 AID での SELECT を拒否する。
	ExactAIDOnly bool
	// NoLogicalChannels なら OPEN_CHANNEL を MS_NO_LOGICAL_CHANNELS で拒否する。
	NoLogicalChannels bool

	mu       sync.Mutex
	channels map[uint32]uint32 // channel → group
	next     uint32
	apdus    int
}

// SW は SW1 SW2 を MBIM の Status フィールド（BYTE[2] を u32 LE で読んだ値）にする。
func SW(sw1, sw2 byte) uint32 { return uint32(sw1) | uint32(sw2)<<8 }

// Install は UICC の各 CID のハンドラを登録する。
func (u *UICC) Install(s *Server) {
	u.channels = make(map[uint32]uint32)
	u.next = 1
	svc := mbim.ServiceMSUICCLowLevelAccess
	s.Handle(svc, mbim.CIDUICCATR, func(Request) Response {
		return Response{Buffer: mbim.Encode(mbim.UICCRefBytes([]byte{0x3b, 0x9f, 0x96, 0x80, 0x1f}))}
	})
	if u.AppList {
		s.Handle(svc, mbim.CIDUICCApplicationList, func(Request) Response {
			app := mbim.Encode(mbim.U32(4), mbim.RefBytes(USIMAID), mbim.RefBytes([]byte("USIM")), mbim.U32(1), mbim.RefBytes([]byte{0x01}))
			isim := mbim.Encode(mbim.U32(6), mbim.RefBytes([]byte{0xa0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x04}), mbim.RefBytes([]byte("ISIM")), mbim.U32(0), mbim.RefBytes(nil))
			return Response{Buffer: mbim.Encode(mbim.U32(1), mbim.U32(2), mbim.U32(0), mbim.U32(uint32(len(isim)+len(app))),
				mbim.RefBytes(isim), mbim.RefBytes(app))}
		})
	}
	s.Handle(svc, mbim.CIDUICCOpenChannel, u.openChannel)
	s.Handle(svc, mbim.CIDUICCCloseChannel, u.closeChannel)
	s.Handle(svc, mbim.CIDUICCAPDU, u.apdu)
}

// OpenChannels は開いたままの論理チャネル数を返す。
func (u *UICC) OpenChannels() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.channels)
}

// APDUs は受け取った APDU の数を返す。
func (u *UICC) APDUs() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.apdus
}

func (u *UICC) openChannel(r Request) Response {
	d := mbim.NewDecoder(r.Buffer)
	aid := d.UICCRefBytes()
	_ = d.U32() // SelectP2Arg
	group := d.U32()
	if r.Type != mbim.Set || d.Err() != nil {
		return Response{Status: mbim.StatusInvalidParameters}
	}
	fail := func(st mbim.Status, sw1, sw2 byte) Response {
		return Response{Status: st, Buffer: mbim.Encode(mbim.U32(SW(sw1, sw2)), mbim.U32(0), mbim.UICCRefBytes(nil))}
	}
	if u.NoLogicalChannels {
		return fail(mbim.StatusMSNoLogicalChannels, 0x68, 0x81)
	}
	if len(aid) == 0 || !bytes.HasPrefix(USIMAID, aid) || (u.ExactAIDOnly && len(aid) != len(USIMAID)) {
		return fail(mbim.StatusMSSelectFailed, 0x6a, 0x82)
	}
	u.mu.Lock()
	ch := u.next
	u.next++
	u.channels[ch] = group
	u.mu.Unlock()
	fcp := []byte{0x62, 0x03, 0x82, 0x01, 0x38}
	return Response{Buffer: mbim.Encode(mbim.U32(SW(0x90, 0x00)), mbim.U32(ch), mbim.UICCRefBytes(fcp))}
}

func (u *UICC) closeChannel(r Request) Response {
	d := mbim.NewDecoder(r.Buffer)
	ch, group := d.U32(), d.U32()
	if d.Err() != nil {
		return Response{Status: mbim.StatusInvalidParameters}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if ch != 0 {
		if _, ok := u.channels[ch]; !ok {
			return Response{Status: mbim.StatusMSInvalidLogicalChannel}
		}
		delete(u.channels, ch)
	} else {
		for c, g := range u.channels {
			if g == group {
				delete(u.channels, c)
			}
		}
	}
	return Response{Buffer: mbim.Encode(mbim.U32(SW(0x90, 0x00)))}
}

func (u *UICC) apdu(r Request) Response {
	d := mbim.NewDecoder(r.Buffer)
	ch := d.U32()
	_ = d.U32() // SecureMessaging
	_ = d.U32() // ClassByteType
	cmd := d.UICCRefBytes()
	if r.Type != mbim.Set || d.Err() != nil {
		return Response{Status: mbim.StatusInvalidParameters}
	}
	u.mu.Lock()
	_, open := u.channels[ch]
	u.apdus++
	u.mu.Unlock()
	if !open {
		return Response{Status: mbim.StatusMSInvalidLogicalChannel}
	}
	reply := func(sw1, sw2 byte, data []byte) Response {
		return Response{Buffer: mbim.Encode(mbim.U32(SW(sw1, sw2)), mbim.UICCRefBytes(data))}
	}
	// AUTHENTICATE: CLA 88 00 81 22 10 RAND 10 AUTN [Le]
	if len(cmd) < 5+34 || cmd[1] != 0x88 || cmd[3] != 0x81 || cmd[4] != 0x22 || cmd[5] != 0x10 || cmd[22] != 0x10 {
		return reply(0x6d, 0x00, nil) // INS not supported
	}
	rand, autn := cmd[6:22], cmd[23:39]
	res, err := u.Auth.Authenticate(context.Background(), rand, autn)
	switch {
	case err == nil:
		out := []byte{0xdb, byte(len(res.RES))}
		out = append(out, res.RES...)
		out = append(out, 0x10)
		out = append(out, res.CK[:]...)
		out = append(out, 0x10)
		out = append(out, res.IK[:]...)
		return reply(0x90, 0x00, out)
	case errors.Is(err, simauth.ErrResync):
		return reply(0x90, 0x00, append([]byte{0xdc, 0x0e}, res.AUTS...))
	case errors.Is(err, simauth.ErrAuthReject):
		return reply(0x98, 0x62, nil)
	}
	return reply(0x6f, 0x00, nil)
}
