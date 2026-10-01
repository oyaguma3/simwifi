package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/oyaguma3/simwifi/internal/dbustest"
	"github.com/oyaguma3/simwifi/internal/lock"
	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/mbim/mbimtest"
	"github.com/oyaguma3/simwifi/internal/modem/mmtest"
	"github.com/oyaguma3/simwifi/internal/simauth/milenage"
	"github.com/oyaguma3/simwifi/internal/supplicant/wpatest"
)

// world は connect を 1 プロセス内で動かすための fake 一式。
type world struct {
	t      *testing.T
	mm     *mmtest.FakeMM
	wpa    *wpatest.Fake
	proxy  *mbimtest.Server
	hss    *milenage.Milenage
	runDir string

	mu        sync.Mutex
	completed chan struct{}
	lastReply string
}

type worldOptions struct {
	aka       bool           // モデムが MBIM AKA に対応
	brokenAKA bool           // MBIM AKA が正しい AUTN も拒否する（Quectel EG25-G の挙動）
	uicc      *mbimtest.UICC // UICC Low-Level Access（nil なら非対応）
	spec      *mmtest.ModemSpec
}

func newWorld(t *testing.T, o worldOptions) *world {
	t.Helper()
	bus := dbustest.Start(t)
	w := &world{t: t, completed: make(chan struct{})}
	w.mm = mmtest.New(t, bus.Conn(t))
	w.wpa = wpatest.New(t, bus.Conn(t))
	spec := mmtest.DefaultModem()
	if o.spec != nil {
		spec = *o.spec
	}
	w.mm.AddModem(spec)

	usim, err := milenage.NewUSIM(tK, tOPc, 0x10)
	if err != nil {
		t.Fatal(err)
	}
	w.hss, _ = milenage.New(tK, tOPc)
	w.proxy = mbimtest.NewServer(t, mbimtest.Options{})
	w.proxy.Handle(mbim.ServiceBasicConnect, mbim.CIDBasicConnectDeviceCaps, func(mbimtest.Request) mbimtest.Response {
		return mbimtest.Response{Buffer: mbim.EncodeDeviceCaps(mbim.DeviceCaps{DeviceID: spec.IMEI, FirmwareInfo: "FW"})}
	})
	if o.aka {
		mbimtest.HandleAKA(w.proxy, usim, mbimtest.ResyncInSuccess)
	}
	if o.brokenAKA {
		w.proxy.Handle(mbim.ServiceAuth, mbim.CIDAuthAKA, func(mbimtest.Request) mbimtest.Response {
			return mbimtest.Response{Status: mbim.StatusAuthIncorrectAUTN}
		})
	}
	if o.uicc != nil {
		o.uicc.Auth = usim
		o.uicc.Install(w.proxy)
	}

	// sysfs
	sys := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sys, "wlan0", "phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.runDir = filepath.Join(t.TempDir(), "run")

	// パッケージ変数を差し替える
	saved := []any{runDir, sysClassNet, proxyAddr, geteuid, connectBus, hookRun, slotSwitchTimeout}
	runDir, sysClassNet, proxyAddr = w.runDir, sys, w.proxy.Addr
	geteuid = func() int { return 0 }
	connectBus = func() (*dbus.Conn, error) { return dbus.Connect(bus.Address) }
	hookRun = func(context.Context, string, []string) error { return nil }
	slotSwitchTimeout = 5 * time.Second
	t.Cleanup(func() {
		runDir, sysClassNet, proxyAddr = saved[0].(string), saved[1].(string), saved[2].(string)
		geteuid = saved[3].(func() int)
		connectBus = saved[4].(func() (*dbus.Conn, error))
		hookRun, _ = saved[5].(func(context.Context, string, []string) error)
		slotSwitchTimeout = saved[6].(time.Duration)
	})

	// wpa_supplicant の振る舞い: SelectNetwork → EAP started → SIM 要求、正しい応答なら completed
	rand := bytes.Repeat([]byte{0x5a}, 16)
	autn, v := w.hss.GenerateAUTN(rand, 0x11, [2]byte{0x80, 0})
	want := "UMTS-AUTH:" + hex.EncodeToString(v.IK[:]) + ":" + hex.EncodeToString(v.CK[:]) + ":" + hex.EncodeToString(v.RES[:])
	w.wpa.OnSelect(func(i *wpatest.Iface, n dbus.ObjectPath) {
		i.SetState("associating")
		i.EmitEAP("started", "")
		i.RequestSIM(n, "UMTS-AUTH:"+hex.EncodeToString(rand)+":"+hex.EncodeToString(autn[:]))
	})
	w.wpa.OnReply(func(i *wpatest.Iface, r wpatest.Reply) {
		w.mu.Lock()
		w.lastReply = r.Value
		w.mu.Unlock()
		if r.Field == "SIM" && r.Value == want {
			i.EmitEAP("completion", "success")
			i.SetState("completed")
			close(w.completed)
		} else {
			i.EmitEAP("completion", "failure")
		}
	})
	return w
}

// runAsync は simwifi をバックグラウンドで実行する。
func (w *world) runAsync(ctx context.Context, args ...string) (<-chan int, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- Run(ctx, args, &out, &errb) }()
	return done, &out, &errb
}

func (w *world) waitCompleted() {
	w.t.Helper()
	select {
	case <-w.completed:
	case <-time.After(10 * time.Second):
		w.mu.Lock()
		defer w.mu.Unlock()
		w.t.Fatalf("not completed (last reply %q)", w.lastReply)
	}
}

func waitCode(t *testing.T, done <-chan int, errb *bytes.Buffer) int {
	t.Helper()
	select {
	case c := <-done:
		return c
	case <-time.After(15 * time.Second):
		t.Fatalf("simwifi did not exit; stderr:\n%s", errb.String())
	}
	return -1
}

func TestConnectEndToEnd(t *testing.T) {
	for _, tt := range []struct {
		name string
		o    worldOptions
		args []string
	}{
		{"mbim aka", worldOptions{aka: true}, nil},
		{"auto falls back to uicc", worldOptions{uicc: &mbimtest.UICC{AppList: true}}, nil},
		{"forced uicc", worldOptions{aka: true, uicc: &mbimtest.UICC{}}, []string{"--auth-path", "uicc"}},
		{"auto cross-checks a broken mbim aka", worldOptions{brokenAKA: true, uicc: &mbimtest.UICC{}}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t, tt.o)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			args := append([]string{"connect", "--iface", "wlan0", "--ssid", "corp", "--timeout", "10"}, tt.args...)
			done, _, errb := w.runAsync(ctx, args...)
			w.waitCompleted()
			if locked, _ := lock.IsLocked(w.runDir, "wlan0"); !locked {
				t.Error("lock must be held while connected")
			}
			cancel() // Ctrl-C
			if code := waitCode(t, done, errb); code != 0 {
				t.Fatalf("exit code %d; stderr:\n%s", code, errb.String())
			}
			if w.wpa.Interface("wlan0") != nil {
				t.Error("interface not removed")
			}
			if _, err := os.Stat(filepath.Join(w.runDir, "wpa-wlan0.conf")); !errors.Is(err, os.ErrNotExist) {
				t.Error("config file not removed")
			}
			if locked, _ := lock.IsLocked(w.runDir, "wlan0"); locked {
				t.Error("lock not released")
			}
			if tt.o.uicc != nil && tt.o.uicc.OpenChannels() != 0 {
				t.Error("UICC channel left open")
			}
		})
	}
}

func TestConnectPreconditionFails(t *testing.T) {
	w := newWorld(t, worldOptions{aka: true})
	w.wpa.AddInterface("wlan0", "") // NetworkManager などが使っている
	done, _, errb := w.runAsync(t.Context(), "connect", "--iface", "wlan0", "--ssid", "corp")
	if code := waitCode(t, done, errb); code != int(CodePrecondition) {
		t.Fatalf("exit code %d; stderr:\n%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "wpa interface") {
		t.Errorf("stderr should name the failed check:\n%s", errb.String())
	}
}

func TestConnectSlotSwitch(t *testing.T) {
	spec := mmtest.DefaultModem()
	spec.Slots = []*mmtest.SIMSpec{
		{IMSI: "001010000000001", OperatorID: "00101"},
		{IMSI: "440103123453421", ICCID: "8981100012345678901", OperatorID: "44010"},
	}
	spec.PrimarySlot = 1
	spec.SwitchDelay = 100 * time.Millisecond

	t.Run("without --switch-slot", func(t *testing.T) {
		w := newWorld(t, worldOptions{aka: true, spec: &spec})
		done, _, errb := w.runAsync(t.Context(), "connect", "--iface", "wlan0", "--ssid", "corp", "--sim-slot", "2")
		if code := waitCode(t, done, errb); code != int(CodePrecondition) {
			t.Fatalf("exit code %d; stderr:\n%s", code, errb.String())
		}
	})
	t.Run("with --switch-slot", func(t *testing.T) {
		w := newWorld(t, worldOptions{aka: true, spec: &spec})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done, _, errb := w.runAsync(ctx, "connect", "--iface", "wlan0", "--ssid", "corp", "--sim-slot", "2", "--switch-slot", "-v")
		w.waitCompleted()
		cancel()
		if code := waitCode(t, done, errb); code != 0 {
			t.Fatalf("exit code %d; stderr:\n%s", code, errb.String())
		}
		if !strings.Contains(errb.String(), "SIM slot switched") {
			t.Errorf("stderr:\n%s", errb.String())
		}
	})
}

func TestConnectModemRemoved(t *testing.T) {
	w := newWorld(t, worldOptions{aka: true})
	done, _, errb := w.runAsync(t.Context(), "connect", "--iface", "wlan0", "--ssid", "corp")
	w.waitCompleted()
	for _, p := range w.mm.Modems() {
		w.mm.RemoveModem(p)
	}
	if code := waitCode(t, done, errb); code != int(CodePrecondition) {
		t.Fatalf("exit code %d; stderr:\n%s", code, errb.String())
	}
	if w.wpa.Interface("wlan0") != nil {
		t.Error("interface not removed")
	}
}

func TestStatusAndIdentityCommands(t *testing.T) {
	w := newWorld(t, worldOptions{aka: true})
	code, out, errOut := run(t, "status")
	if code != 0 || !strings.Contains(out, "✔ identity") || !strings.Contains(out, "44010…3421") {
		t.Fatalf("status: code=%d\n%s\n%s", code, out, errOut)
	}
	code, out, _ = run(t, "identity", "--method", "akap")
	if code != 0 || strings.TrimSpace(out) != "6440103123453421@wlan.mnc010.mcc440.3gppnetwork.org" {
		t.Fatalf("identity: code=%d out=%q", code, out)
	}
	code, out, errOut = run(t, "probe")
	if code != 0 || !strings.Contains(out, "MBIM AKA:  supported") {
		t.Fatalf("probe: code=%d\n%s\n%s", code, out, errOut)
	}
	// probe の結果が status に出る
	if _, out, _ = run(t, "status", "--json"); !strings.Contains(out, "AKA supported") {
		t.Fatalf("status after probe:\n%s", out)
	}
	_ = w
}
