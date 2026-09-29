// Package mbimaka は MBIM の Auth サービス AKA CID で USIM の AKA を実行する（DESIGN §4.3 主経路）。
package mbimaka

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
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

// Authenticator は MBIM_CID_AKA による simauth.Authenticator。
type Authenticator struct {
	c   Commander
	log *slog.Logger
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
func (a *Authenticator) Authenticate(ctx context.Context, rand, autn []byte) (simauth.Result, error) {
	if err := simauth.CheckChallenge(rand, autn); err != nil {
		return simauth.Result{}, err
	}
	a.log.Debug("AKA request", "rand", logging.Hex(rand), "autn", logging.Hex(autn))
	buf, err := a.c.Command(ctx, mbim.ServiceAuth, mbim.CIDAuthAKA, mbim.Query, mbim.Encode(mbim.Fixed(rand), mbim.Fixed(autn)))
	defer clear(buf)
	if err != nil {
		st, ok := mbim.StatusOf(err)
		if !ok {
			return simauth.Result{}, err
		}
		switch st {
		case mbim.StatusAuthSyncFailure:
			if auts := parseAUTS(buf); auts != nil {
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
		return simauth.Result{AUTS: auts}, fmt.Errorf("%w (ResLen 0 with AUTS)", simauth.ErrResync)
	}
	if resLen < simauth.MinRES || resLen > simauth.MaxRES {
		return simauth.Result{}, fmt.Errorf("mbim AKA response: invalid RES length %d", resLen)
	}
	r := simauth.Result{RES: slices.Clone(res[:resLen])}
	copy(r.CK[:], ck)
	copy(r.IK[:], ik)
	a.log.Debug("AKA success", "result", r)
	return r, nil
}

// parseAUTS は失敗応答のバッファから AUTS を取り出す。無い・全ゼロなら nil。
func parseAUTS(buf []byte) []byte {
	if len(buf) < responseLen {
		return nil
	}
	auts := buf[responseLen-simauth.AUTSLen : responseLen]
	if allZero(auts) {
		return nil
	}
	return slices.Clone(auts)
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
