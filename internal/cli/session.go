package cli

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/oyaguma3/simwifi/internal/logging"
	"github.com/oyaguma3/simwifi/internal/simauth"
	"github.com/oyaguma3/simwifi/internal/supplicant"
)

// maxResync は 1 回の EAP セッション内で許す再同期の回数（DESIGN D9）。
const maxResync = 32

// cleanupTimeout は後始末の D-Bus 呼び出しの上限。
const cleanupTimeout = 10 * time.Second

// session は connect のイベントループ（DESIGN §7.1 の 7〜10）。
// 依存はすべて注入され、D-Bus や MBIM に触れずにテストできる。
type session struct {
	log             *slog.Logger
	sup             supplicant.Supplicant
	auth            simauth.Authenticator
	network         supplicant.NetworkConfig
	timeout         time.Duration
	maxAuthFailures int
	hooks           *hookRunner
	// modemGone はモデムが抜かれたら値が届く（モデムを使わない場合は nil）。
	modemGone <-chan struct{}

	// 状態
	connected     bool // 一度でも completed になったか
	up            bool // exec-up を実行済みで、まだ down していないか
	authFailures  int  // 連続した認証失敗（EAP セッション単位）
	sessionFailed bool // 今の EAP セッションを失敗として数えたか
	resync        int  // 今の EAP セッションでの再同期回数
	gone          bool // wpa_supplicant が消えた
}

// run はネットワークを投入し、終了条件までイベントを処理する。後始末もここで行う。
func (s *session) run(ctx context.Context) (err error) {
	defer s.hooks.wait() // 最後に、待ち中のフック（exec-down を含む）の完了を待つ
	sims, err := s.sup.SIMRequests(ctx)
	if err != nil {
		return err
	}
	events, err := s.sup.Events(ctx)
	if err != nil {
		return err
	}
	id, err := s.sup.AddNetwork(ctx, s.network)
	if err != nil {
		return err
	}
	defer s.cleanup(ctx, id)
	if err := s.sup.SelectNetwork(ctx, id); err != nil {
		return err
	}
	s.log.Info("connecting", "ssid", s.network.SSID, "eap", s.network.EAP, "identity", logging.NAI(s.network.Identity),
		"auth_path", s.auth.Name(), "timeout", s.timeout)

	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("signal received; disconnecting")
			return nil
		case <-s.modemGone:
			return exitf(CodePrecondition, "modem was removed")
		case <-timer.C:
			if !s.connected {
				return exitf(CodeTimeout, "not connected within %s", s.timeout)
			}
		case req := <-sims:
			s.handleSIM(ctx, req)
		case ev := <-events:
			if err := s.handleEvent(ev, timer); err != nil {
				return err
			}
		}
		if s.authFailures >= s.maxAuthFailures {
			return exitf(CodeAuthFailure, "authentication failed %d times in a row", s.authFailures)
		}
		if s.resync > maxResync {
			return exitf(CodeAuthFailure, "too many synchronization failures (%d) in one EAP session", s.resync)
		}
	}
}

// handleSIM は SIM 要求に応答する（DESIGN §7.2）。必ず何かを返す。
func (s *session) handleSIM(ctx context.Context, req supplicant.SIMRequest) {
	rand, autn, err := supplicant.ParseUMTSAuth(req.Text)
	if errors.Is(err, supplicant.ErrGSMAuth) {
		s.log.Error("EAP-SIM (GSM-AUTH) is not supported; rejecting")
		s.reply(ctx, req, supplicant.GSMFail)
		return
	}
	if err != nil {
		s.log.Error("malformed SIM request", "error", err)
		s.markFailed()
		s.reply(ctx, req, supplicant.UMTSFail)
		return
	}
	s.log.Debug("SIM request", "rand", logging.Hex(rand), "autn", logging.Hex(autn))
	res, err := s.auth.Authenticate(ctx, rand, autn)
	defer res.Clear()
	switch {
	case err == nil:
		s.log.Info("USIM authentication succeeded", "path", s.auth.Name(), "result", res)
		s.reply(ctx, req, supplicant.UMTSAuthResponse(res))
	case errors.Is(err, simauth.ErrResync):
		s.resync++
		s.log.Warn("USIM synchronization failure; sending AUTS", "count", s.resync)
		s.reply(ctx, req, supplicant.UMTSAUTSResponse(res.AUTS))
	case errors.Is(err, simauth.ErrAuthReject):
		s.log.Error("USIM rejected AUTN (network authentication failed)", "error", err)
		s.markFailed()
		s.reply(ctx, req, supplicant.UMTSFail)
	default:
		s.log.Error("USIM authentication error", "path", s.auth.Name(), "error", err)
		s.markFailed()
		s.reply(ctx, req, supplicant.UMTSFail)
	}
}

func (s *session) reply(ctx context.Context, req supplicant.SIMRequest, resp supplicant.SIMResponse) {
	if err := s.sup.ReplySIM(ctx, req, resp); err != nil {
		s.log.Error("failed to reply to SIM request", "error", err)
	}
}

// markFailed は今の EAP セッションを失敗として数える（セッションにつき 1 回だけ）。
func (s *session) markFailed() {
	if !s.sessionFailed {
		s.sessionFailed = true
		s.authFailures++
	}
}

func (s *session) handleEvent(ev supplicant.Event, timer *time.Timer) error {
	switch ev.Kind {
	case supplicant.EventGone:
		s.gone = true
		return exitf(CodeError, "wpa_supplicant disappeared from D-Bus")
	case supplicant.EventEAP:
		s.log.Debug("EAP event", "status", ev.EAPStatus, "parameter", ev.EAPParameter)
		switch {
		case ev.EAPStatus == "started":
			s.resync = 0
			s.sessionFailed = false
		case ev.EAPStatus == "completion" && ev.EAPParameter == "failure":
			s.log.Warn("EAP authentication failed")
			s.markFailed()
		case ev.EAPStatus == "completion" && ev.EAPParameter == "success":
			s.log.Info("EAP authentication succeeded")
		}
	case supplicant.EventState:
		s.log.Debug("wpa_supplicant state", "state", ev.State)
		switch {
		case ev.State == "completed":
			if !s.connected {
				timer.Stop()
			}
			s.connected = true
			s.authFailures = 0
			if !s.up {
				s.log.Info("connected", "ssid", s.network.SSID)
				s.up = true
				s.hooks.fire("up")
			}
		case s.up && (ev.State == "4way_handshake" || ev.State == "group_handshake"):
			// 再認証や鍵の更新の途中。アソシエーションは保たれているので切断とはみなさない
		case s.up:
			s.log.Warn("connection lost", "state", ev.State)
			s.up = false
			s.hooks.fire("down")
		}
	case supplicant.EventDisconnectReason:
		if ev.Reason != 0 {
			s.log.Info("disconnected", "reason", ev.Reason)
		}
	}
	return nil
}

// cleanup は exec-down、RemoveNetwork を行う（Interface の削除は呼び出し元の Close）。
func (s *session) cleanup(ctx context.Context, id supplicant.NetworkID) {
	if s.up {
		s.up = false
		s.hooks.fire("down")
	}
	if s.gone {
		// 呼ぶと D-Bus activation で wpa_supplicant が起動してしまう
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := s.sup.RemoveNetwork(ctx, id); err != nil {
		s.log.Warn("failed to remove network", "error", err)
	}
}
