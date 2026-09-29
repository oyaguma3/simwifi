package milenage

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/oyaguma3/simwifi/internal/simauth"
)

// MaxSQN は 48 ビット SQN の最大値。
const MaxSQN = 1<<48 - 1

// PutSQN は 48 ビットの SQN を 6 バイトのビッグエンディアンに書き出す。
func PutSQN(sqn uint64) [SQNLen]byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], sqn&MaxSQN)
	return [SQNLen]byte(b[2:])
}

// SQNFromBytes は 6 バイトの SQN を数値にする。
func SQNFromBytes(b []byte) uint64 {
	var x [8]byte
	copy(x[2:], b[:SQNLen])
	return binary.BigEndian.Uint64(x[:])
}

// GenerateAUTN は網（HSS）側の認証ベクタを作る。AUTN = (SQN ⊕ AK) ‖ AMF ‖ MAC-A。
func (m *Milenage) GenerateAUTN(rand []byte, sqn uint64, amf [AMFLen]byte) ([simauth.AUTNLen]byte, Vector) {
	v := m.F2345(rand)
	sqnB := PutSQN(sqn)
	macA, _ := m.F1(rand, sqnB[:], amf[:])
	var autn [simauth.AUTNLen]byte
	subtle.XORBytes(autn[0:6], sqnB[:], v.AK[:])
	copy(autn[6:8], amf[:])
	copy(autn[8:16], macA[:])
	return autn, v
}

// ErrBadAUTS は AUTS の MAC-S が一致しないことを示す。
var ErrBadAUTS = errors.New("milenage: AUTS MAC-S mismatch")

// ResyncSQN は網側で AUTS を検証し、USIM の SQN（SQNms）を取り出す。
func (m *Milenage) ResyncSQN(rand, auts []byte) (uint64, error) {
	if len(auts) != simauth.AUTSLen {
		return 0, fmt.Errorf("milenage: AUTS must be %d bytes", simauth.AUTSLen)
	}
	akStar := m.F5Star(rand)
	var sqnMS [SQNLen]byte
	subtle.XORBytes(sqnMS[:], auts[0:6], akStar[:])
	_, macS := m.F1(rand, sqnMS[:], []byte{0, 0})
	if subtle.ConstantTimeCompare(macS[:], auts[6:14]) != 1 {
		return 0, ErrBadAUTS
	}
	return SQNFromBytes(sqnMS[:]), nil
}

// USIM は Milenage で動くソフトウェア USIM。simauth.Authenticator を実装する。
// SQN の鮮度検査は「受理済みの最大値より大きいこと」だけを見る簡易版。
type USIM struct {
	m *Milenage

	mu    sync.Mutex
	sqnMS uint64 // 受理済みの最大 SQN
}

var _ simauth.Authenticator = (*USIM)(nil)

// NewUSIM は K / OPc と、受理済みの最大 SQN から USIM を作る。
func NewUSIM(k, opc []byte, sqnMS uint64) (*USIM, error) {
	m, err := New(k, opc)
	if err != nil {
		return nil, err
	}
	return &USIM{m: m, sqnMS: sqnMS & MaxSQN}, nil
}

// Name は経路名を返す。
func (u *USIM) Name() string { return "milenage" }

// SQN は受理済みの最大 SQN を返す。
func (u *USIM) SQN() uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.sqnMS
}

// Authenticate は USIM の AUTHENTICATE（3G コンテキスト）を模擬する。
func (u *USIM) Authenticate(ctx context.Context, rand, autn []byte) (simauth.Result, error) {
	if err := simauth.CheckChallenge(rand, autn); err != nil {
		return simauth.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return simauth.Result{}, err
	}
	v := u.m.F2345(rand)
	var sqn [SQNLen]byte
	subtle.XORBytes(sqn[:], autn[0:6], v.AK[:])
	xmac, _ := u.m.F1(rand, sqn[:], autn[6:8])
	if subtle.ConstantTimeCompare(xmac[:], autn[8:16]) != 1 {
		return simauth.Result{}, simauth.ErrAuthReject
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if SQNFromBytes(sqn[:]) <= u.sqnMS {
		// AUTS = (SQNms ⊕ AK*) ‖ MAC-S。MAC-S の AMF はダミーの 0x0000（TS 33.102 §6.3.3）
		sqnMS := PutSQN(u.sqnMS)
		akStar := u.m.F5Star(rand)
		_, macS := u.m.F1(rand, sqnMS[:], []byte{0, 0})
		auts := make([]byte, simauth.AUTSLen)
		subtle.XORBytes(auts[0:6], sqnMS[:], akStar[:])
		copy(auts[6:], macS[:])
		return simauth.Result{AUTS: auts}, simauth.ErrResync
	}
	u.sqnMS = SQNFromBytes(sqn[:])
	return simauth.Result{RES: v.RES[:], CK: v.CK, IK: v.IK}, nil
}
