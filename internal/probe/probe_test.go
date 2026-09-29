package probe

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/mbim/mbimtest"
	"github.com/oyaguma3/simwifi/internal/simauth/milenage"
)

func dial(t *testing.T, s *mbimtest.Server) *mbim.Client {
	t.Helper()
	c, err := mbim.Dial(t.Context(), mbim.Options{ProxyAddr: s.Addr, DevicePath: "/dev/cdc-wdm0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func newServer(t *testing.T, aka bool, uicc *mbimtest.UICC) *mbimtest.Server {
	t.Helper()
	usim, err := milenage.NewUSIM(make([]byte, 16), make([]byte, 16), 0)
	if err != nil {
		t.Fatal(err)
	}
	s := mbimtest.NewServer(t, mbimtest.Options{})
	s.Handle(mbim.ServiceBasicConnect, mbim.CIDBasicConnectDeviceCaps, func(mbimtest.Request) mbimtest.Response {
		return mbimtest.Response{Buffer: mbim.EncodeDeviceCaps(mbim.DeviceCaps{DeviceID: "861234567890123", FirmwareInfo: "FW1"})}
	})
	if aka {
		mbimtest.HandleAKA(s, usim, mbimtest.ResyncInSuccess)
	}
	if uicc != nil {
		uicc.Auth = usim
		uicc.Install(s)
	}
	return s
}

func TestRunBothPaths(t *testing.T) {
	card := &mbimtest.UICC{AppList: true}
	s := newServer(t, true, card)
	r, err := Run(t.Context(), dial(t, s), "EM7455", "861234567890123", "/dev/cdc-wdm0")
	if err != nil {
		t.Fatal(err)
	}
	if r.AKA.State != Supported || r.AKA.Status != "auth-incorrect-autn" {
		t.Errorf("AKA = %+v", r.AKA)
	}
	if r.UICC.State != Supported || !r.UICC.ApplicationList || r.UICC.USIMAID != "a0000000871002ff49ff0589" || r.UICC.ATR == "" {
		t.Errorf("UICC = %+v", r.UICC)
	}
	if r.FirmwareInfo != "FW1" || r.AKAUnsupported() || r.NoPath() {
		t.Errorf("result = %+v", r)
	}
	if card.APDUs() != 0 || card.OpenChannels() != 0 {
		t.Error("probe must not send AUTHENTICATE or leave channels open")
	}
}

func TestRunNoPaths(t *testing.T) {
	s := newServer(t, false, nil)
	r, err := Run(t.Context(), dial(t, s), "X", "imei", "/dev/cdc-wdm0")
	if err != nil {
		t.Fatal(err)
	}
	if r.AKA.State != Unsupported || r.UICC.State != Unsupported || !r.AKAUnsupported() || !r.NoPath() {
		t.Errorf("result = %+v", r)
	}
}

func TestRunUICCOnlyPartialAID(t *testing.T) {
	s := newServer(t, false, &mbimtest.UICC{})
	r, err := Run(t.Context(), dial(t, s), "X", "imei", "/dev/cdc-wdm0")
	if err != nil {
		t.Fatal(err)
	}
	if r.AKA.State != Unsupported || r.UICC.State != Supported || r.UICC.ApplicationList || r.UICC.USIMAID != "a0000000871002" {
		t.Errorf("result = %+v", r)
	}
}

func TestSaveLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if r, err := Load(dir, "861234567890123"); err != nil || r != nil {
		t.Fatalf("Load missing = %v, %v", r, err)
	}
	in := &Result{EquipmentID: "861234567890123", Device: "/dev/cdc-wdm0", AKA: PathResult{State: Supported}}
	p, err := Save(dir, in)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	out, err := Load(dir, "861234567890123")
	if err != nil || out.AKA.State != Supported || out.Device != "/dev/cdc-wdm0" {
		t.Fatalf("Load = %+v, %v", out, err)
	}
	if got := FilePath("/run/simwifi", "../x y"); got != "/run/simwifi/probe-___x_y.json" {
		t.Errorf("FilePath = %q", got)
	}
}
