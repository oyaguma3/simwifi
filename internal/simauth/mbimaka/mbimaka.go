// Package mbimaka は MBIM の Auth サービス AKA CID で USIM の AKA を実行する（DESIGN §4.3 主経路）。
package mbimaka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"uuid"

	"github.com/oyaguma3/simwifi/internal/logging"
	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/simauth"
)

// Commander は MBIM コマンドを送る口（*mbim.Client が実装する）。
type Commander interface {
	Command(ctx context.Context, service uuid.UUID, cid uint32, typ mbim.CommandType, buf []byte) ([]byte, error)
}

// 応答: Res[16], ResLen u32, IK[16], CK[16], Auts[14]
const responseLen = 16 + 4 + 16 + 16 + simauth.AUTSLen

// byteOrder は RAND / AUTN と応答の値のバイト順。
//
// Qualcomm 系のモデム（Quectel EG25-G、Sierra EM7455）は、値を 128 ビットの
// リトルエンディアン整数として扱う。RAND / AUTN を逆順で渡し、RES / CK / IK / AUTS も
// 逆順で返す（Windows のドライバも逆順で渡している）。標準の順で渡すと MAC 不一致で拒否される。
type byteOrder int

const (
	orderUnknown  byteOrder = iota
	orderReversed           // 値を逆順にする（Qualcomm 系）
	orderStandard           // 3GPP の並びのまま
)

func (o byteOrder) String() string {
	switch o {
	case orderReversed:
		return "reversed"
	case orderStandard:
		return "standard"
	}
	return "unknown"
}

// Authenticator は MBIM_CID_AKA による simauth.Authenticator。
type Authenticator struct {
	c   Commander
	log *slog.Logger

	mu    sync.Mutex
	order byteOrder // 判明したバイト順（最初の成功または同期失敗で決まる）
}

var _ simauth.Authenticator = (*Authenticator)(nil)

// New は Authenticator を作る。
func New(c Commander, logger *slog.Logger) *Authenticator {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Authenticator{c: c, log: logger.With("component", "simauth", "path", "mbim-aka")}
}

// Name は経路名を返す。
func (a *Authenticator) Name() string { return "mbim-aka" }

// Authenticate は AKA Query を送り、結果を simauth の規約に正規化する（DESIGN §4.3 の表）。
// バイト順が分かっていなければ、逆順 → 標準の順で試し、AUTN を受理された順を以後も使う。
// どちらの順でも拒否されたら、本当の拒否として扱う。
func (a *Authenticator) Authenticate(ctx context.Context, rand, autn []byte) (simauth.Result, error) {
	if err := simauth.CheckChallenge(rand, autn); err != nil {
		return simauth.Result{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.log.Debug("AKA request", "rand", logging.Hex(rand), "autn", logging.Hex(autn))

	orders := []byteOrder{orderReversed, orderStandard}
	if a.order != orderUnknown {
		orders = []byteOrder{a.order}
	}
	var r simauth.Result
	var err error
	for _, o := range orders {
		r, err = a.try(ctx, rand, autn, o)
		if err == nil || errors.Is(err, simauth.ErrResync) {
			if a.order == orderUnknown {
				a.order = o
				a.log.Info("MBIM AKA byte order detected", "order", o.String())
			}
			break
		}
		if !errors.Is(err, simauth.ErrAuthReject) {
			break // 非対応・ビジーなどは順を変えても同じ
		}
		a.log.Debug("AKA rejected", "order", o.String(), "error", err)
	}
	if err == nil {
		a.log.Debug("AKA success", "result", r)
	}
	return r, err
}

// try は 1 つのバイト順で AKA Query を送る。
func (a *Authenticator) try(ctx context.Context, rand, autn []byte, o byteOrder) (simauth.Result, error) {
	if o == orderReversed {
		rand, autn = reversed(rand), reversed(autn)
	}
	buf, err := a.c.Command(ctx, mbim.ServiceAuth, mbim.CIDAuthAKA, mbim.Query, mbim.Encode(mbim.Fixed(rand), mbim.Fixed(autn)))
	defer clear(buf)
	if err != nil {
		st, ok := mbim.StatusOf(err)
		if !ok {
			return simauth.Result{}, err
		}
		switch st {
		case mbim.StatusAuthSyncFailure:
			if auts := parseAUTS(buf, o); auts != nil {
				return simauth.Result{AUTS: auts}, fmt.Errorf("%w: %w", simauth.ErrResync, err)
			}
			// AUTS が無ければ再同期できないので拒否として扱う
			return simauth.Result{}, fmt.Errorf("%w: %w (without AUTS)", simauth.ErrAuthReject, err)
		case mbim.StatusAuthIncorrectAUTN, mbim.StatusAuthAMFNotSet:
			return simauth.Result{}, fmt.Errorf("%w: %w", simauth.ErrAuthReject, err)
		case mbim.StatusNoDeviceSupport:
			return simauth.Result{}, fmt.Errorf("%w: %w", simauth.ErrUnsupported, err)
		}
		return simauth.Result{}, err
	}
	if len(buf) < responseLen {
		return simauth.Result{}, fmt.Errorf("mbim AKA response too short (%d bytes)", len(buf))
	}
	d := mbim.NewDecoder(buf)
	res := d.Fixed(16)
	resLen := d.U32()
	ik := d.Fixed(16)
	ck := d.Fixed(16)
	auts := d.Fixed(simauth.AUTSLen)
	defer clear(res)
	defer clear(ik)
	defer clear(ck)

	if resLen == 0 && !allZero(auts) {
		return simauth.Result{AUTS: fromWire(auts, o)}, fmt.Errorf("%w (ResLen 0 with AUTS)", simauth.ErrResync)
	}
	if resLen < simauth.MinRES || resLen > simauth.MaxRES {
		return simauth.Result{}, fmt.Errorf("mbim AKA response: invalid RES length %d", resLen)
	}
	// 逆順のモデムは RES を（ゼロ拡張した）リトルエンディアン整数として返すので、値は先頭 ResLen バイトにある
	r := simauth.Result{RES: fromWire(res[:resLen], o)}
	copy(r.CK[:], fromWire(ck, o))
	copy(r.IK[:], fromWire(ik, o))
	return r, nil
}

// fromWire はモデムの応答の値を 3GPP の並びに直す（コピーを返す）。
func fromWire(b []byte, o byteOrder) []byte {
	if o == orderReversed {
		return reversed(b)
	}
	return slices.Clone(b)
}

func reversed(b []byte) []byte {
	out := slices.Clone(b)
	slices.Reverse(out)
	return out
}

// parseAUTS は失敗応答のバッファから AUTS を取り出す。無い・全ゼロなら nil。
func parseAUTS(buf []byte, o byteOrder) []byte {
	if len(buf) < responseLen {
		return nil
	}
	auts := buf[responseLen-simauth.AUTSLen : responseLen]
	if allZero(auts) {
		return nil
	}
	return fromWire(auts, o)
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
