// Package simauth は USIM の AKA 認証（AUTHENTICATE）を抽象化する（DESIGN §4.3）。
package simauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// RAND / AUTN / AUTS などの長さ（バイト）。
const (
	RANDLen = 16
	AUTNLen = 16
	KeyLen  = 16
	AUTSLen = 14
	MinRES  = 4
	MaxRES  = 16
)

var (
	// ErrResync は SQN の同期失敗。Result.AUTS に再同期トークンが入る。
	ErrResync = errors.New("AKA synchronization failure")
	// ErrAuthReject は AUTN の MAC 不正（またはモデムが AUTN を拒否した）。
	ErrAuthReject = errors.New("AKA authentication rejected (AUTN)")
	// ErrUnsupported はこの経路がモデムで使えないことを示す。
	ErrUnsupported = errors.New("AKA path not supported by modem")
)

// Authenticator は RAND / AUTN を USIM に渡して AKA を実行する。
type Authenticator interface {
	// Authenticate は RAND / AUTN（各 16 バイト）から RES / CK / IK を得る。
	//   同期失敗:     Result.AUTS をセットして ErrResync
	//   MAC 不正:     ErrAuthReject
	//   経路が非対応: ErrUnsupported
	Authenticate(ctx context.Context, rand, autn []byte) (Result, error)
	Name() string
}

// Result は AKA の結果。鍵素材を含むため、使い終えたら Clear する。
type Result struct {
	RES  []byte // 4..16 バイト
	CK   [KeyLen]byte
	IK   [KeyLen]byte
	AUTS []byte // 14 バイト。同期失敗時のみ
}

// Clear は鍵素材をゼロクリアする。
func (r *Result) Clear() {
	clear(r.RES)
	clear(r.CK[:])
	clear(r.IK[:])
	clear(r.AUTS)
	r.RES = nil
	r.AUTS = nil
}

// LogValue は鍵素材を出さず、長さだけをログに出す（DESIGN §11）。
func (r Result) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("res_len", len(r.RES)),
		slog.Int("auts_len", len(r.AUTS)),
	)
}

// CheckChallenge は RAND / AUTN の長さを検査する。
func CheckChallenge(rand, autn []byte) error {
	if len(rand) != RANDLen || len(autn) != AUTNLen {
		return fmt.Errorf("invalid challenge length: rand=%d autn=%d", len(rand), len(autn))
	}
	return nil
}
