package simauth

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// Auto は primary を使い、次の場合に fallback へ切り替える（DESIGN §4.3「経路の自動選択」）。
//   - primary が ErrUnsupported を返した: 同じ RAND / AUTN を fallback で実行する
//   - primary が ErrAuthReject を返した: 同じ RAND / AUTN を fallback で確かめ、
//     fallback が受理（または同期失敗）したら primary は信用できないとみなす
//     （MBIM AKA が正しい AUTN も拒否するモデムがある。docs/COMPAT.md）
//
// 一度切り替えたら、以後はそのプロセス内で fallback を使い続ける。
// AUTN を拒否した USIM の状態（SQN）は変わらないので、確かめ直しは無害。
// USIM への要求は直列に処理する。
type Auto struct {
	primary, fallback Authenticator
	log               *slog.Logger

	mu      sync.Mutex
	current Authenticator
}

var _ Authenticator = (*Auto)(nil)

// NewAuto は Auto を作る。startWithFallback が真なら最初から fallback を使う
// （probe の結果で primary が非対応と分かっている場合）。
func NewAuto(primary, fallback Authenticator, startWithFallback bool, logger *slog.Logger) *Auto {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	a := &Auto{primary: primary, fallback: fallback, current: primary, log: logger.With("component", "simauth")}
	if startWithFallback {
		a.current = fallback
	}
	return a
}

// Name は現在使っている経路名を返す。
func (a *Auto) Name() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current.Name()
}

// Authenticate は現在の経路で AKA を実行する。
func (a *Auto) Authenticate(ctx context.Context, rand, autn []byte) (Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := a.current.Authenticate(ctx, rand, autn)
	if a.current != a.primary {
		return r, err
	}
	switch {
	case errors.Is(err, ErrUnsupported):
		a.log.Warn("USIM access path not supported; switching", "from", a.primary.Name(), "to", a.fallback.Name(), "error", err)
		a.current = a.fallback
		return a.current.Authenticate(ctx, rand, autn)
	case errors.Is(err, ErrAuthReject):
		fr, ferr := a.fallback.Authenticate(ctx, rand, autn)
		if ferr == nil || errors.Is(ferr, ErrResync) {
			a.log.Warn("USIM accepted the challenge on another path; switching",
				"rejected_by", a.primary.Name(), "to", a.fallback.Name(), "primary_error", err)
			a.current = a.fallback
			return fr, ferr
		}
		fr.Clear()
		a.log.Debug("rejection confirmed on fallback path", "path", a.fallback.Name(), "error", ferr)
	}
	return r, err
}
