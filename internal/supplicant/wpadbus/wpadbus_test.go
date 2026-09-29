package wpadbus_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/oyaguma3/simwifi/internal/dbustest"
	"github.com/oyaguma3/simwifi/internal/supplicant"
	"github.com/oyaguma3/simwifi/internal/supplicant/wpadbus"
	"github.com/oyaguma3/simwifi/internal/supplicant/wpatest"
)

func setup(t *testing.T) (*wpadbus.Client, *wpatest.Fake, string) {
	t.Helper()
	bus := dbustest.Start(t)
	fake := wpatest.New(t, bus.Conn(t))
	c := wpadbus.New(bus.Conn(t), nil)
	cfg := filepath.Join(t.TempDir(), "run", "wpa-wlan0.conf")
	return c, fake, cfg
}

func recvEvent(t *testing.T, ch <-chan supplicant.Event) supplicant.Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event")
	}
	return supplicant.Event{}
}

func TestAttachAndNetwork(t *testing.T) {
	c, fake, cfg := setup(t)
	if err := c.Attach(t.Context(), "wlan0", supplicant.AttachOptions{ConfigPath: cfg}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	data, err := os.ReadFile(cfg)
	if err != nil || string(data) != wpadbus.ConfigContent {
		t.Fatalf("config = %q, %v", data, err)
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v", fi.Mode().Perm())
	}
	iface := fake.Interface("wlan0")
	if iface == nil || iface.ConfigFile != cfg {
		t.Fatalf("interface not created with config file")
	}

	id, err := c.AddNetwork(t.Context(), supplicant.NetworkConfig{SSID: `my "net"`, EAP: "AKA'", Identity: "6440103123453421@wlan.mnc010.mcc440.3gppnetwork.org", WPA3: true})
	if err != nil {
		t.Fatal(err)
	}
	args := iface.Networks()[dbus.ObjectPath(id)]
	if ssid, _ := args["ssid"].Value().([]byte); string(ssid) != `my "net"` {
		t.Errorf("ssid = %v", args["ssid"])
	}
	for k, want := range map[string]any{
		"key_mgmt": "WPA-EAP-SHA256", "eap": "AKA'", "phase1": "result_ind=1",
		"identity": "6440103123453421@wlan.mnc010.mcc440.3gppnetwork.org", "ieee80211w": uint32(2),
	} {
		if got := args[k].Value(); got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}

	caps, err := c.Capabilities(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(caps.EAPMethods, "AKA'") || !slices.Contains(caps.KeyMgmt, "wpa-eap-sha256") || !slices.Contains(caps.Global, "interworking") {
		t.Errorf("caps = %+v", caps)
	}

	if err := c.SelectNetwork(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveNetwork(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if fake.Interface("wlan0") != nil {
		t.Error("interface not removed on Close")
	}
	if _, err := os.Stat(cfg); !errors.Is(err, os.ErrNotExist) {
		t.Error("config file not removed on Close")
	}
}

func TestSignals(t *testing.T) {
	c, fake, cfg := setup(t)
	if err := c.Attach(t.Context(), "wlan0", supplicant.AttachOptions{ConfigPath: cfg}); err != nil {
		t.Fatal(err)
	}
	defer c.Close(t.Context())
	sims, _ := c.SIMRequests(t.Context())
	events, _ := c.Events(t.Context())
	iface := fake.Interface("wlan0")
	id, _ := c.AddNetwork(t.Context(), supplicant.NetworkConfig{SSID: "x", EAP: "AKA", Identity: "0@x"})

	iface.EmitEAP("started", "")
	if e := recvEvent(t, events); e.Kind != supplicant.EventEAP || e.EAPStatus != "started" {
		t.Fatalf("event = %+v", e)
	}

	iface.RequestSIM(dbus.ObjectPath(id), "UMTS-AUTH:00:11")
	var req supplicant.SIMRequest
	select {
	case req = <-sims:
	case <-time.After(5 * time.Second):
		t.Fatal("no SIM request")
	}
	if req.Network != id || req.Text != "UMTS-AUTH:00:11" {
		t.Fatalf("req = %+v", req)
	}
	if err := c.ReplySIM(t.Context(), req, supplicant.UMTSFail); err != nil {
		t.Fatal(err)
	}
	if r := iface.Replies(); len(r) != 1 || r[0].Field != "SIM" || r[0].Value != "UMTS-FAIL" || r[0].Network != dbus.ObjectPath(id) {
		t.Fatalf("replies = %+v", r)
	}

	iface.SetState("completed")
	if e := recvEvent(t, events); e.Kind != supplicant.EventState || e.State != "completed" {
		t.Fatalf("event = %+v", e)
	}
	iface.SetDisconnectReason(-3)
	if e := recvEvent(t, events); e.Kind != supplicant.EventDisconnectReason || e.Reason != -3 {
		t.Fatalf("event = %+v", e)
	}

	fake.Quit()
	if e := recvEvent(t, events); e.Kind != supplicant.EventGone {
		t.Fatalf("event = %+v", e)
	}
}

func TestForeignInterface(t *testing.T) {
	c, fake, cfg := setup(t)
	fake.AddInterface("wlan0", "") // NetworkManager などが作った Interface
	err := c.Attach(t.Context(), "wlan0", supplicant.AttachOptions{ConfigPath: cfg})
	if !errors.Is(err, supplicant.ErrInterfaceBusy) {
		t.Fatalf("err = %v, want ErrInterfaceBusy", err)
	}
	if fake.Interface("wlan0") == nil {
		t.Fatal("foreign interface must not be removed")
	}
	info, err := c.Inspect(t.Context(), "wlan0")
	if err != nil || !info.Exists || info.ConfigFile != "" || info.State != "disconnected" {
		t.Fatalf("Inspect = %+v, %v", info, err)
	}
}

func TestStaleInterface(t *testing.T) {
	c, fake, cfg := setup(t)
	old := fake.AddInterface("wlan0", cfg) // 前回の simwifi の残骸
	if err := c.Attach(t.Context(), "wlan0", supplicant.AttachOptions{ConfigPath: cfg}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer c.Close(t.Context())
	now := fake.Interface("wlan0")
	if now == nil || now.Path == old.Path {
		t.Fatal("stale interface was not recreated")
	}
}

func TestInspectMissing(t *testing.T) {
	c, _, _ := setup(t)
	info, err := c.Inspect(t.Context(), "wlan9")
	if err != nil || info.Exists {
		t.Fatalf("Inspect = %+v, %v", info, err)
	}
}

func TestNotRunning(t *testing.T) {
	bus := dbustest.Start(t)
	c := wpadbus.New(bus.Conn(t), nil)
	if ok, err := c.Running(t.Context()); err != nil || ok {
		t.Fatalf("Running = %v, %v", ok, err)
	}
	err := c.Attach(t.Context(), "wlan0", supplicant.AttachOptions{ConfigPath: filepath.Join(t.TempDir(), "c")})
	if !errors.Is(err, supplicant.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
	if _, err := c.AddNetwork(t.Context(), supplicant.NetworkConfig{}); !errors.Is(err, supplicant.ErrNotAttached) {
		t.Fatalf("AddNetwork before Attach: %v", err)
	}
}
