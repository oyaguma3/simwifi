// Package status は前提条件を収集・判定する（DESIGN §4.6、§6.2）。
// status コマンドと connect が同じ収集関数を使う。読み取り専用で、副作用は無い。
package status

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/oyaguma3/simwifi/internal/lock"
	"github.com/oyaguma3/simwifi/internal/logging"
	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/modem"
	"github.com/oyaguma3/simwifi/internal/nai"
	"github.com/oyaguma3/simwifi/internal/probe"
	"github.com/oyaguma3/simwifi/internal/supplicant"
)

// チェック名。
const (
	CheckRoot      = "root"
	CheckMM        = "ModemManager"
	CheckModem     = "modem"
	CheckMBIMPort  = "MBIM port"
	CheckSIMSlots  = "SIM slots"
	CheckPIN       = "SIM lock"
	CheckSIM       = "SIM"
	CheckNAI       = "identity"
	CheckMBIMProxy = "mbim-proxy"
	CheckWLAN      = "wlan"
	CheckNM        = "NetworkManager"
	CheckWPA       = "wpa_supplicant"
	CheckEAP       = "EAP methods"
	CheckIface     = "wpa interface"
	CheckInterwork = "Interworking/HS20"
	CheckProbe     = "probe"
	CheckLock      = "lock"
)

// Check は 1 項目の結果。
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Info   bool   `json:"info,omitzero"` // 情報表示のみ（判定対象外）
	Fatal  bool   `json:"fatal,omitzero"`
	Detail string `json:"detail,omitempty"`
}

// Report は収集結果。connect は Checks の Fatal を見て止まり、それ以外の値を使って進む。
type Report struct {
	Checks []Check

	Modem  *modem.Modem
	SIM    *modem.SIM
	Device string        // /dev/cdc-wdmN
	NAI    string        // 永久 ID
	Probe  *probe.Result // 直近の probe 結果（無ければ nil）
	// StaleInterface は simwifi の残骸 Interface があること（connect が回収する）。
	StaleInterface bool
}

// Fatal は Fatal なチェックを返す。
func (r *Report) Fatal() []Check {
	var out []Check
	for _, c := range r.Checks {
		if c.Fatal {
			out = append(out, c)
		}
	}
	return out
}

// Get は名前でチェックを探す。
func (r *Report) Get(name string) (Check, bool) {
	i := slices.IndexFunc(r.Checks, func(c Check) bool { return c.Name == name })
	if i < 0 {
		return Check{}, false
	}
	return r.Checks[i], true
}

func (r *Report) ok(name, format string, args ...any) {
	r.Checks = append(r.Checks, Check{Name: name, OK: true, Detail: fmt.Sprintf(format, args...)})
}

func (r *Report) info(name, format string, args ...any) {
	r.Checks = append(r.Checks, Check{Name: name, OK: true, Info: true, Detail: fmt.Sprintf(format, args...)})
}

func (r *Report) fatal(name, format string, args ...any) {
	r.Checks = append(r.Checks, Check{Name: name, Fatal: true, Detail: fmt.Sprintf(format, args...)})
}

// ModemManager は status が使う MM の操作。
type ModemManager interface {
	Version(ctx context.Context) (string, error)
	Modems(ctx context.Context) ([]modem.Modem, error)
	SIM(ctx context.Context, p dbus.ObjectPath) (modem.SIM, error)
}

// WPA は status が使う wpa_supplicant の操作（副作用の無いものだけ）。
type WPA interface {
	Running(ctx context.Context) (bool, error)
	Activatable(ctx context.Context) (bool, error)
	Capabilities(ctx context.Context) (supplicant.Capabilities, error)
	Inspect(ctx context.Context, iface string) (supplicant.InterfaceInfo, error)
}

// NMInfo は NetworkManager から見た iface の状態。
type NMInfo struct {
	Running bool
	Known   bool // NM がこの iface をデバイスとして知っているか
	Managed bool
}

// Deps は収集に使う依存（テストで差し替える）。
type Deps struct {
	Euid        int
	MM          ModemManager
	MBIM        func(ctx context.Context, device string) (mbim.DeviceCaps, error)
	WPA         WPA
	NM          func(ctx context.Context, iface string) (NMInfo, error)
	SysClassNet string // 通常は /sys/class/net
	RunDir      string // 通常は /run/simwifi
}

// Options は収集の設定。
type Options struct {
	Iface  string
	Modem  string // --modem
	Method nai.Method
	Realm  string
	// SkipModem は MM / mbim-proxy / SIM / probe のチェックを省く（E2E の Milenage バックエンド用）。
	SkipModem bool
	// SkipLock はロックのチェックを省く（connect は自分でロックを取った後に収集するため）。
	SkipLock bool
}

// WPAConfigPath は iface 用の wpa_supplicant 設定ファイルのパス。
func WPAConfigPath(runDir, iface string) string {
	return filepath.Join(runDir, "wpa-"+iface+".conf")
}

// Collect は前提条件を収集する。
func Collect(ctx context.Context, d Deps, o Options) *Report {
	r := &Report{}
	if d.Euid == 0 {
		r.ok(CheckRoot, "running as root")
	} else {
		r.fatal(CheckRoot, "not running as root (uid %d); mbim-proxy only accepts root", d.Euid)
	}
	if !o.SkipModem {
		collectModem(ctx, d, o, r)
	}
	collectWLAN(ctx, d, o, r)
	collectWPA(ctx, d, o, r)
	if !o.SkipLock {
		if locked, err := lock.IsLocked(d.RunDir, o.Iface); err != nil {
			r.fatal(CheckLock, "cannot check lock: %v", err)
		} else if locked {
			r.fatal(CheckLock, "another simwifi instance is running on %s", o.Iface)
		} else {
			r.ok(CheckLock, "not locked")
		}
	}
	return r
}

func collectModem(ctx context.Context, d Deps, o Options, r *Report) {
	v, err := d.MM.Version(ctx)
	if err != nil {
		r.fatal(CheckMM, "%v", err)
		return
	}
	r.ok(CheckMM, "version %s", v)

	modems, err := d.MM.Modems(ctx)
	if err != nil {
		r.fatal(CheckModem, "%v", err)
		return
	}
	m, err := modem.Select(modems, o.Modem)
	if err != nil {
		detail := err.Error()
		if len(modems) > 1 {
			var idx []string
			for _, mm := range modems {
				idx = append(idx, mm.Index()+": "+mm.Model)
			}
			detail += " (" + strings.Join(idx, ", ") + ")"
		}
		r.fatal(CheckModem, "%s", detail)
		return
	}
	r.Modem = &m
	desc := fmt.Sprintf("[%s] %s %s (FW %s, IMEI %s) state=%s", m.Index(), m.Manufacturer, m.Model, m.Revision,
		logging.MaskIMSI(m.EquipmentIdentifier), m.StateString())
	if m.State == modem.StateFailed {
		r.fatal(CheckModem, "%s", desc)
		return
	}
	r.ok(CheckModem, "%s", desc)

	dev, err := m.MBIMDevice()
	if err != nil {
		r.fatal(CheckMBIMPort, "%v (is the modem in MBIM mode?)", err)
		return
	}
	r.Device = dev
	r.ok(CheckMBIMPort, "%s", dev)

	// SIM スロット
	var sim *modem.SIM
	if len(m.SimSlots) > 0 {
		var parts []string
		for i, p := range m.SimSlots {
			slot := uint32(i + 1)
			label := fmt.Sprintf("%d: ", slot)
			if !modem.ValidSIM(p) {
				label += "empty"
			} else if s, err := d.MM.SIM(ctx, p); err != nil {
				label += "unreadable"
			} else {
				label += logging.MaskIMSI(s.IMSI)
				if slot == m.PrimarySimSlot {
					sim = &s
				}
			}
			if slot == m.PrimarySimSlot {
				label += " (active)"
			}
			parts = append(parts, label)
		}
		r.info(CheckSIMSlots, "%s", strings.Join(parts, ", "))
	} else if modem.ValidSIM(m.SIM) {
		if s, err := d.MM.SIM(ctx, m.SIM); err == nil {
			sim = &s
		}
		r.info(CheckSIMSlots, "single slot")
	}

	if m.Locked() {
		r.fatal(CheckPIN, "SIM is locked (unlock required: %s); unlock it with ModemManager first", modem.LockName(m.UnlockRequired))
		return
	}
	r.ok(CheckPIN, "not locked")

	if sim == nil {
		r.fatal(CheckSIM, "no SIM in the active slot")
		return
	}
	r.SIM = sim
	if sim.IMSI == "" {
		r.fatal(CheckSIM, "IMSI not available (SIM not ready?)")
		return
	}
	r.ok(CheckSIM, "IMSI %s, ICCID %s, MCC/MNC %s %s", logging.MaskIMSI(sim.IMSI), logging.MaskICCID(sim.ICCID),
		orDash(sim.OperatorID), sim.OperatorName)

	id, err := nai.Generate(o.Method, sim.IMSI, sim.OperatorID, o.Realm)
	if err != nil {
		r.fatal(CheckNAI, "%v", err)
	} else {
		r.NAI = id
		r.ok(CheckNAI, "%s (%s)", logging.MaskNAI(id), o.Method.EAPName())
	}

	if caps, err := d.MBIM(ctx, dev); err != nil {
		if errors.Is(err, mbim.ErrProxyRejected) {
			r.fatal(CheckMBIMProxy, "%v", err)
		} else {
			r.fatal(CheckMBIMProxy, "%v (mbim-proxy is normally kept running by ModemManager)", err)
		}
	} else {
		r.ok(CheckMBIMProxy, "device caps OK (%s)", caps.FirmwareInfo)
	}

	pr, err := probe.Load(d.RunDir, m.EquipmentIdentifier)
	switch {
	case err != nil:
		r.info(CheckProbe, "cannot read probe result: %v", err)
	case pr == nil:
		r.info(CheckProbe, "not run yet (run 'simwifi probe')")
	case pr.NoPath():
		r.Probe = pr
		r.fatal(CheckProbe, "modem supports neither MBIM AKA nor UICC low-level access (probed %s)", pr.Time.Local().Format("2006-01-02 15:04"))
	default:
		r.Probe = pr
		r.info(CheckProbe, "AKA %s, UICC %s (probed %s)", pr.AKA.State, pr.UICC.State, pr.Time.Local().Format("2006-01-02 15:04"))
	}
}

func collectWLAN(ctx context.Context, d Deps, o Options, r *Report) {
	dir := filepath.Join(d.SysClassNet, o.Iface)
	if _, err := os.Stat(dir); err != nil {
		r.fatal(CheckWLAN, "interface %s not found", o.Iface)
	} else if _, err := os.Stat(filepath.Join(dir, "phy80211")); err != nil {
		r.fatal(CheckWLAN, "%s is not a wireless (nl80211) interface", o.Iface)
	} else {
		driver := "unknown driver"
		if target, err := os.Readlink(filepath.Join(dir, "device", "driver")); err == nil {
			driver = filepath.Base(target)
		}
		r.ok(CheckWLAN, "%s (%s)", o.Iface, driver)
	}

	if d.NM == nil {
		return
	}
	nm, err := d.NM(ctx, o.Iface)
	switch {
	case err != nil:
		r.info(CheckNM, "cannot query NetworkManager: %v", err)
	case !nm.Running:
		r.ok(CheckNM, "not running")
	case nm.Known && nm.Managed:
		r.fatal(CheckNM, "%s is managed by NetworkManager; run: nmcli device set %s managed no", o.Iface, o.Iface)
	default:
		r.ok(CheckNM, "%s is not managed", o.Iface)
	}
}

func collectWPA(ctx context.Context, d Deps, o Options, r *Report) {
	running, err := d.WPA.Running(ctx)
	if err != nil {
		r.fatal(CheckWPA, "%v", err)
		return
	}
	if !running {
		// status は副作用を持たないので起動はしない。activation できれば connect 時に起動される
		if ok, err := d.WPA.Activatable(ctx); err == nil && ok {
			r.ok(CheckWPA, "not running (will be started by D-Bus activation)")
		} else {
			r.fatal(CheckWPA, "not available on D-Bus; run: systemctl enable --now wpa_supplicant")
		}
		return
	}
	r.ok(CheckWPA, "running")

	caps, err := d.WPA.Capabilities(ctx)
	if err != nil {
		r.fatal(CheckEAP, "%v", err)
		return
	}
	want := o.Method.EAPName()
	have := []string{}
	for _, m := range []string{"AKA", "AKA'"} {
		if slices.Contains(caps.EAPMethods, m) {
			have = append(have, m)
		}
	}
	if !slices.Contains(caps.EAPMethods, want) {
		r.fatal(CheckEAP, "wpa_supplicant lacks EAP method %s (has: %s)", want, strings.Join(have, ", "))
	} else {
		r.ok(CheckEAP, "%s", strings.Join(have, ", "))
	}
	if slices.Contains(caps.Global, "interworking") {
		r.info(CheckInterwork, "supported")
	} else {
		r.info(CheckInterwork, "not supported")
	}

	info, err := d.WPA.Inspect(ctx, o.Iface)
	switch {
	case err != nil:
		r.fatal(CheckIface, "%v", err)
	case !info.Exists:
		r.ok(CheckIface, "no interface for %s yet", o.Iface)
	case info.ConfigFile == WPAConfigPath(d.RunDir, o.Iface):
		r.StaleInterface = true
		r.info(CheckIface, "stale interface left by simwifi (will be recreated)")
	default:
		r.fatal(CheckIface, "%s is already used by another wpa_supplicant client (state %s)", o.Iface, info.State)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
