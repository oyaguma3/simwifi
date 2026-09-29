package cli

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/oyaguma3/simwifi/internal/lock"
	"github.com/oyaguma3/simwifi/internal/modem"
	"github.com/oyaguma3/simwifi/internal/nai"
	"github.com/oyaguma3/simwifi/internal/simauth"
	"github.com/oyaguma3/simwifi/internal/simauth/mbimaka"
	"github.com/oyaguma3/simwifi/internal/simauth/mbimuicc"
	"github.com/oyaguma3/simwifi/internal/status"
	"github.com/oyaguma3/simwifi/internal/supplicant"
)

// slotSwitchTimeout はスロット切替後にモデムが使える状態に戻るまでの上限（DESIGN §7.4）。
var slotSwitchTimeout = 60 * time.Second

// hookRun は exec フックの実行関数（テストで差し替える。nil なら /bin/sh -c）。
var hookRun func(ctx context.Context, cmd string, env []string) error

// runConnect は connect の手順 1〜10（DESIGN §7.1）を実行する。
func runConnect(ctx context.Context, env *Env, common *commonFlags, o *connectOptions) error {
	log := env.Log
	method, _ := nai.ParseMethod(o.method)
	iface := common.iface

	// 1. lock
	lk, err := lock.Acquire(runDir, iface)
	if err != nil {
		return withCode(CodePrecondition, err)
	}
	defer lk.Release()

	sys, err := openSystem(log)
	if err != nil {
		return err
	}
	defer sys.Close()

	// 2. status
	sopts := status.Options{Iface: iface, Modem: common.modem, Method: method, Realm: o.realm,
		SkipLock: true, SkipModem: o.e2e.enabled()}
	r := status.Collect(ctx, sys.statusDeps(), sopts)
	if err := reportFatal(log, r); err != nil {
		return err
	}

	// 3. SIM スロット
	if !o.e2e.enabled() && o.simSlot != 0 {
		if r, err = ensureSlot(ctx, log, sys, r, sopts, uint32(o.simSlot), o.switchSlot); err != nil {
			return err
		}
	}

	// 4. USIM への経路
	var auth simauth.Authenticator
	identity := r.NAI
	if o.e2e.enabled() {
		if auth, identity, err = o.e2e.build(method, o.realm); err != nil {
			return err
		}
		log.Warn("E2E build: answering SIM requests with the software Milenage USIM")
	} else {
		c, err := sys.dialMBIM(ctx, r.Device)
		if err != nil {
			return err
		}
		defer c.Close()
		if _, err := c.DeviceCaps(ctx); err != nil {
			return withCode(CodePrecondition, fmt.Errorf("MBIM device check: %w", err))
		}
		aka, uicc := mbimaka.New(c, log), mbimuicc.New(c, log)
		switch o.authPath {
		case "aka":
			auth = aka
		case "uicc":
			auth = uicc
		default:
			auth = simauth.NewAuto(aka, uicc, r.Probe.AKAUnsupported(), log)
		}
	}

	// モデム抜去の監視
	var modemGone chan struct{}
	if r.Modem != nil {
		w, err := sys.mm.Watch(ctx)
		if err != nil {
			return err
		}
		defer w.Close()
		modemGone = make(chan struct{})
		watchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			if w.WaitRemoved(watchCtx, r.Modem.Path) == nil {
				close(modemGone)
			}
		}()
	}

	// 6. wpa_supplicant の Interface
	sup := sys.wpa
	if err := sup.Attach(ctx, iface, supplicant.AttachOptions{ConfigPath: status.WPAConfigPath(runDir, iface)}); err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := sup.Close(cctx); err != nil {
			log.Warn("cleanup failed", "error", err)
		}
	}()
	if o.wpa3 {
		if caps, err := sup.Capabilities(ctx); err == nil && !slices.Contains(caps.KeyMgmt, "wpa-eap-sha256") {
			log.Warn("wpa_supplicant/driver does not report WPA-EAP-SHA256 support", "key_mgmt", caps.KeyMgmt)
		}
	}

	// 7〜10. ネットワーク投入とイベントループ
	s := &session{
		log:             log,
		sup:             sup,
		auth:            auth,
		network:         supplicant.NetworkConfig{SSID: o.ssid, EAP: method.EAPName(), Identity: identity, WPA3: o.wpa3},
		timeout:         o.timeout(),
		maxAuthFailures: o.maxAuthFailures,
		hooks: newHookRunner(o.execUp, o.execDown,
			[]string{"SIMWIFI_IFACE=" + iface, "SIMWIFI_SSID=" + o.ssid}, log, hookRun),
		modemGone: modemGone,
	}
	return s.run(ctx)
}

// ensureSlot は --sim-slot が有効なスロットであることを確かめ、必要なら切り替える（DESIGN §9）。
func ensureSlot(ctx context.Context, log *slog.Logger, sys *system, r *status.Report, o status.Options, want uint32, doSwitch bool) (*status.Report, error) {
	m := r.Modem
	if m.PrimarySimSlot == 0 {
		if want != 1 {
			return nil, exitf(CodePrecondition, "modem has a single SIM slot; --sim-slot %d is not available", want)
		}
		return r, nil
	}
	if int(want) > len(m.SimSlots) {
		return nil, exitf(CodePrecondition, "modem has %d SIM slots; --sim-slot %d is not available", len(m.SimSlots), want)
	}
	if m.PrimarySimSlot == want {
		return r, nil
	}
	if !doSwitch {
		return nil, exitf(CodePrecondition, "SIM slot %d is not active (active: %d); add --switch-slot to switch", want, m.PrimarySimSlot)
	}

	log.Warn("switching SIM slot; cellular connection will be interrupted", "from", m.PrimarySimSlot, "to", want)
	w, err := sys.mm.Watch(ctx)
	if err != nil {
		return nil, err
	}
	defer w.Close()
	if err := sys.mm.SetPrimarySimSlot(ctx, m.Path, want); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeoutCause(ctx, slotSwitchTimeout, fmt.Errorf("modem did not come back within %s after switching SIM slot", slotSwitchTimeout))
	defer cancel()
	imei := m.EquipmentIdentifier
	nm, err := w.WaitAdded(ctx, func(x modem.Modem) bool { return x.EquipmentIdentifier == imei && x.PrimarySimSlot == want })
	if err != nil {
		return nil, withCode(CodePrecondition, err)
	}
	log.Info("SIM slot switched", "modem", string(nm.Path), "slot", want)

	// モデムが再出現しても SIM の読み込みには時間がかかるので、前提条件が揃うまで取り直す
	o.Modem = string(nm.Path)
	for {
		r = status.Collect(ctx, sys.statusDeps(), o)
		if len(r.Fatal()) == 0 {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return nil, reportFatal(log, r)
		case <-time.After(2 * time.Second):
			log.Debug("waiting for the modem to become ready", "pending", r.Fatal()[0].Name)
		}
	}
}
