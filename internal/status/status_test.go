package status

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/oyaguma3/simwifi/internal/lock"
	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/modem"
	"github.com/oyaguma3/simwifi/internal/nai"
	"github.com/oyaguma3/simwifi/internal/probe"
	"github.com/oyaguma3/simwifi/internal/supplicant"
)

type fakeMM struct {
	versionErr error
	modems     []modem.Modem
	sims       map[dbus.ObjectPath]modem.SIM
}

func (f *fakeMM) Version(context.Context) (string, error) { return "1.24.0", f.versionErr }
func (f *fakeMM) Modems(context.Context) ([]modem.Modem, error) {
	return f.modems, nil
}
func (f *fakeMM) SIM(_ context.Context, p dbus.ObjectPath) (modem.SIM, error) {
	s, ok := f.sims[p]
	if !ok {
		return modem.SIM{}, errors.New("no sim")
	}
	return s, nil
}

type fakeWPA struct {
	running, activatable bool
	caps                 supplicant.Capabilities
	iface                supplicant.InterfaceInfo
}

func (f *fakeWPA) Running(context.Context) (bool, error)     { return f.running, nil }
func (f *fakeWPA) Activatable(context.Context) (bool, error) { return f.activatable, nil }
func (f *fakeWPA) Capabilities(context.Context) (supplicant.Capabilities, error) {
	return f.caps, nil
}
func (f *fakeWPA) Inspect(context.Context, string) (supplicant.InterfaceInfo, error) {
	return f.iface, nil
}

const simPath = dbus.ObjectPath("/org/freedesktop/ModemManager1/SIM/0")

func goodModem() modem.Modem {
	return modem.Modem{
		Path: "/org/freedesktop/ModemManager1/Modem/0", Manufacturer: "Sierra", Model: "EM7455", Revision: "FW",
		EquipmentIdentifier: "861234567890123", State: 8, UnlockRequired: modem.LockNone,
		Ports: []modem.Port{{Name: "cdc-wdm0", Type: modem.PortTypeMBIM}},
		SIM:   simPath, SimSlots: []dbus.ObjectPath{simPath, "/"}, PrimarySimSlot: 1,
	}
}

// env は全チェックが OK になる依存を作る。各テストで一部を壊す。
type env struct {
	mm   *fakeMM
	wpa  *fakeWPA
	nm   NMInfo
	mbim error
	d    Deps
	o    Options
}

func newEnv(t *testing.T) *env {
	t.Helper()
	sys := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sys, "wlan0", "phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &env{
		mm: &fakeMM{modems: []modem.Modem{goodModem()}, sims: map[dbus.ObjectPath]modem.SIM{
			simPath: {Path: simPath, Active: true, IMSI: "440103123453421", ICCID: "8981100012345678901", OperatorID: "44010"},
		}},
		wpa: &fakeWPA{running: true, caps: supplicant.Capabilities{EAPMethods: []string{"AKA", "AKA'"}, Global: []string{"interworking"}}},
		nm:  NMInfo{Running: true, Known: true, Managed: false},
		o:   Options{Iface: "wlan0", Method: nai.AKA},
	}
	e.d = Deps{
		Euid: 0,
		MM:   e.mm,
		MBIM: func(context.Context, string) (mbim.DeviceCaps, error) {
			return mbim.DeviceCaps{FirmwareInfo: "FW"}, e.mbim
		},
		WPA:         e.wpa,
		NM:          func(context.Context, string) (NMInfo, error) { return e.nm, nil },
		SysClassNet: sys,
		RunDir:      filepath.Join(t.TempDir(), "run"),
	}
	return e
}

func (e *env) collect(t *testing.T) *Report {
	t.Helper()
	return Collect(t.Context(), e.d, e.o)
}

func TestAllOK(t *testing.T) {
	e := newEnv(t)
	r := e.collect(t)
	if f := r.Fatal(); len(f) != 0 {
		t.Fatalf("unexpected fatal: %+v", f)
	}
	if r.Device != "/dev/cdc-wdm0" || r.NAI != "0440103123453421@wlan.mnc010.mcc440.3gppnetwork.org" || r.SIM == nil || r.Modem == nil {
		t.Fatalf("report = %+v", r)
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"✔ root", "44010…3421 (active)", "2: empty", "· probe", "not run yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "440103123453421") {
		t.Error("IMSI must be masked")
	}
	buf.Reset()
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		OK     bool    `json:"ok"`
		Checks []Check `json:"checks"`
	}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil || !parsed.OK || len(parsed.Checks) != len(r.Checks) {
		t.Fatalf("json = %s, %v", buf.String(), err)
	}
}

func TestFatal(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*env)
		check string
	}{
		{"non-root", func(e *env) { e.d.Euid = 1000 }, CheckRoot},
		{"no MM", func(e *env) { e.mm.versionErr = modem.ErrNotRunning }, CheckMM},
		{"no modem", func(e *env) { e.mm.modems = nil }, CheckModem},
		{"multiple modems", func(e *env) {
			m2 := goodModem()
			m2.Path = "/org/freedesktop/ModemManager1/Modem/1"
			e.mm.modems = append(e.mm.modems, m2)
		}, CheckModem},
		{"failed modem", func(e *env) { e.mm.modems[0].State = modem.StateFailed }, CheckModem},
		{"no MBIM port", func(e *env) { e.mm.modems[0].Ports = []modem.Port{{Name: "ttyUSB2", Type: 3}} }, CheckMBIMPort},
		{"PIN locked", func(e *env) {
			e.mm.modems[0].State = modem.StateLocked
			e.mm.modems[0].UnlockRequired = 2
		}, CheckPIN},
		{"empty active slot", func(e *env) { e.mm.modems[0].PrimarySimSlot = 2 }, CheckSIM},
		{"no IMSI", func(e *env) {
			s := e.mm.sims[simPath]
			s.IMSI = ""
			e.mm.sims[simPath] = s
		}, CheckSIM},
		{"no operator id", func(e *env) {
			s := e.mm.sims[simPath]
			s.OperatorID = ""
			e.mm.sims[simPath] = s
		}, CheckNAI},
		{"proxy rejected", func(e *env) { e.mbim = mbim.ErrProxyRejected }, CheckMBIMProxy},
		{"no wlan", func(e *env) { e.o.Iface = "wlan9" }, CheckWLAN},
		{"not wireless", func(e *env) {
			_ = os.MkdirAll(filepath.Join(e.d.SysClassNet, "eth0"), 0o755)
			e.o.Iface = "eth0"
		}, CheckWLAN},
		{"NM managed", func(e *env) { e.nm.Managed = true }, CheckNM},
		{"no wpa_supplicant", func(e *env) { e.wpa.running = false }, CheckWPA},
		{"no AKA'", func(e *env) {
			e.o.Method = nai.AKAPrime
			e.wpa.caps.EAPMethods = []string{"AKA"}
		}, CheckEAP},
		{"foreign interface", func(e *env) { e.wpa.iface = supplicant.InterfaceInfo{Exists: true, State: "completed"} }, CheckIface},
		{"probe: no path", func(e *env) {
			_, _ = probe.Save(e.d.RunDir, &probe.Result{EquipmentID: "861234567890123", Time: time.Now(),
				AKA: probe.PathResult{State: probe.Unsupported}, UICC: probe.UICCResult{PathResult: probe.PathResult{State: probe.Unsupported}}})
		}, CheckProbe},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			r := e.collect(t)
			c, ok := r.Get(tt.check)
			if !ok || !c.Fatal {
				t.Fatalf("check %q not fatal: %+v", tt.check, r.Checks)
			}
		})
	}
}

func TestNonFatal(t *testing.T) {
	t.Run("realm override without operator id", func(t *testing.T) {
		e := newEnv(t)
		s := e.mm.sims[simPath]
		s.OperatorID = ""
		e.mm.sims[simPath] = s
		e.o.Realm = "example.org"
		r := e.collect(t)
		if len(r.Fatal()) != 0 || r.NAI != "0440103123453421@example.org" {
			t.Fatalf("fatal=%+v nai=%q", r.Fatal(), r.NAI)
		}
	})
	t.Run("wpa_supplicant activatable", func(t *testing.T) {
		e := newEnv(t)
		e.wpa.running, e.wpa.activatable = false, true
		if r := e.collect(t); len(r.Fatal()) != 0 {
			t.Fatalf("fatal=%+v", r.Fatal())
		}
	})
	t.Run("stale simwifi interface", func(t *testing.T) {
		e := newEnv(t)
		e.wpa.iface = supplicant.InterfaceInfo{Exists: true, ConfigFile: WPAConfigPath(e.d.RunDir, "wlan0")}
		r := e.collect(t)
		if len(r.Fatal()) != 0 || !r.StaleInterface {
			t.Fatalf("fatal=%+v stale=%v", r.Fatal(), r.StaleInterface)
		}
	})
	t.Run("NM not running", func(t *testing.T) {
		e := newEnv(t)
		e.nm = NMInfo{}
		if r := e.collect(t); len(r.Fatal()) != 0 {
			t.Fatalf("fatal=%+v", r.Fatal())
		}
	})
	t.Run("select by --modem", func(t *testing.T) {
		e := newEnv(t)
		m2 := goodModem()
		m2.Path = "/org/freedesktop/ModemManager1/Modem/5"
		e.mm.modems = append(e.mm.modems, m2)
		e.o.Modem = "5"
		r := e.collect(t)
		if len(r.Fatal()) != 0 || r.Modem.Path != m2.Path {
			t.Fatalf("fatal=%+v", r.Fatal())
		}
	})
	t.Run("probe result loaded", func(t *testing.T) {
		e := newEnv(t)
		_, _ = probe.Save(e.d.RunDir, &probe.Result{EquipmentID: "861234567890123", Time: time.Now(),
			AKA: probe.PathResult{State: probe.Unsupported}, UICC: probe.UICCResult{PathResult: probe.PathResult{State: probe.Supported}}})
		r := e.collect(t)
		if len(r.Fatal()) != 0 || r.Probe == nil || !r.Probe.AKAUnsupported() {
			t.Fatalf("fatal=%+v probe=%+v", r.Fatal(), r.Probe)
		}
	})
	t.Run("skip modem", func(t *testing.T) {
		e := newEnv(t)
		e.mm.versionErr = modem.ErrNotRunning
		e.o.SkipModem = true
		if r := e.collect(t); len(r.Fatal()) != 0 {
			t.Fatalf("fatal=%+v", r.Fatal())
		}
	})
}

func TestLockCheck(t *testing.T) {
	e := newEnv(t)
	l, err := lock.Acquire(e.d.RunDir, "wlan0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if c, _ := e.collect(t).Get(CheckLock); !c.Fatal {
		t.Fatal("lock must be fatal while held")
	}
	e.o.SkipLock = true
	if _, ok := e.collect(t).Get(CheckLock); ok {
		t.Fatal("lock check must be skipped")
	}
}
