package modem_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/oyaguma3/simwifi/internal/dbustest"
	"github.com/oyaguma3/simwifi/internal/modem"
	"github.com/oyaguma3/simwifi/internal/modem/mmtest"
)

func setup(t *testing.T) (*modem.Client, *mmtest.FakeMM) {
	t.Helper()
	bus := dbustest.Start(t)
	fake := mmtest.New(t, bus.Conn(t))
	return modem.New(bus.Conn(t), nil), fake
}

func TestNotRunning(t *testing.T) {
	bus := dbustest.Start(t)
	c := modem.New(bus.Conn(t), nil)
	if _, err := c.Version(t.Context()); !errors.Is(err, modem.ErrNotRunning) {
		t.Fatalf("Version err = %v", err)
	}
	if _, err := c.Modems(t.Context()); !errors.Is(err, modem.ErrNotRunning) {
		t.Fatalf("Modems err = %v", err)
	}
}

func TestModemAndSIM(t *testing.T) {
	c, fake := setup(t)
	if v, err := c.Version(t.Context()); err != nil || v != "1.24.0" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	fake.AddModem(mmtest.DefaultModem())

	modems, err := c.Modems(t.Context())
	if err != nil || len(modems) != 1 {
		t.Fatalf("Modems = %v, %v", modems, err)
	}
	m := modems[0]
	if m.Model != "EM7455" || m.EquipmentIdentifier != "861234567890123" || m.Index() != "0" || m.PrimarySimSlot != 1 {
		t.Errorf("modem = %+v", m)
	}
	if dev, err := m.MBIMDevice(); err != nil || dev != "/dev/cdc-wdm0" {
		t.Errorf("MBIMDevice = %q, %v", dev, err)
	}
	if m.Locked() {
		t.Error("modem must not be locked")
	}
	sim, err := c.SIM(t.Context(), m.SIM)
	if err != nil {
		t.Fatal(err)
	}
	if !sim.Active || sim.IMSI != "440103123453421" || sim.OperatorID != "44010" || sim.ICCID != "8981100012345678901" {
		t.Errorf("sim = %+v", sim)
	}
	if _, err := c.SIM(t.Context(), "/"); err == nil {
		t.Error("expected error for empty SIM path")
	}
}

func TestSelect(t *testing.T) {
	a := modem.Modem{Path: "/org/freedesktop/ModemManager1/Modem/0"}
	b := modem.Modem{Path: "/org/freedesktop/ModemManager1/Modem/3"}
	if _, err := modem.Select(nil, ""); !errors.Is(err, modem.ErrNoModem) {
		t.Error(err)
	}
	if m, err := modem.Select([]modem.Modem{a}, ""); err != nil || m.Path != a.Path {
		t.Error(err)
	}
	if _, err := modem.Select([]modem.Modem{a, b}, ""); !errors.Is(err, modem.ErrMultipleModems) {
		t.Error(err)
	}
	if m, err := modem.Select([]modem.Modem{a, b}, "3"); err != nil || m.Path != b.Path {
		t.Error(err)
	}
	if m, err := modem.Select([]modem.Modem{a, b}, string(a.Path)); err != nil || m.Path != a.Path {
		t.Error(err)
	}
	if _, err := modem.Select([]modem.Modem{a, b}, "7"); !errors.Is(err, modem.ErrModemNotFound) {
		t.Error(err)
	}
}

func TestLocked(t *testing.T) {
	for _, tt := range []struct {
		state  int32
		unlock uint32
		want   bool
	}{
		{8, modem.LockNone, false},
		{modem.StateLocked, 2, true},
		{modem.StateInitializing, modem.LockUnknown, false},
		{modem.StateDisabled, 2, true},
	} {
		if got := (modem.Modem{State: tt.state, UnlockRequired: tt.unlock}).Locked(); got != tt.want {
			t.Errorf("state=%d unlock=%d: Locked = %v", tt.state, tt.unlock, got)
		}
	}
}

func TestSwitchSlot(t *testing.T) {
	c, fake := setup(t)
	spec := mmtest.DefaultModem()
	spec.Slots = append(spec.Slots, &mmtest.SIMSpec{IMSI: "001010000000001", OperatorID: "00101"})
	spec.SwitchDelay = 50 * time.Millisecond
	p := fake.AddModem(spec)

	w, err := c.Watch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := c.SetPrimarySimSlot(t.Context(), p, 2); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := w.WaitRemoved(ctx, p); err != nil {
		t.Fatalf("WaitRemoved: %v", err)
	}
	m, err := w.WaitAdded(ctx, func(m modem.Modem) bool {
		return m.EquipmentIdentifier == spec.IMEI && m.PrimarySimSlot == 2
	})
	if err != nil {
		t.Fatalf("WaitAdded: %v", err)
	}
	if m.Path == p {
		t.Error("modem path should change after slot switch")
	}
	sim, err := c.SIM(t.Context(), m.SIM)
	if err != nil || sim.IMSI != "001010000000001" || !sim.Active {
		t.Fatalf("SIM after switch = %+v, %v", sim, err)
	}
}

func TestWaitAddedTimeout(t *testing.T) {
	c, _ := setup(t)
	w, err := c.Watch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := w.WaitAdded(ctx, func(modem.Modem) bool { return true }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitRemovedOtherModem(t *testing.T) {
	c, fake := setup(t)
	a := fake.AddModem(mmtest.DefaultModem())
	b := fake.AddModem(mmtest.DefaultModem())
	w, err := c.Watch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	fake.RemoveModem(b)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := w.WaitRemoved(ctx, a); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("removing another modem must not match: %v", err)
	}
	fake.RemoveModem(a)
	ctx2, cancel2 := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel2()
	if err := w.WaitRemoved(ctx2, dbus.ObjectPath(a)); err != nil {
		t.Fatal(err)
	}
}
