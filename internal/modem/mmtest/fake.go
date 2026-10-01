// Package mmtest はテスト用の fake ModemManager を提供する。
// dbustest のプライベートバス上に ObjectManager / Modem / Sim オブジェクトを export する。
package mmtest

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/oyaguma3/simwifi/internal/modem"
)

// SIMSpec は fake SIM の内容。
type SIMSpec struct {
	IMSI         string
	ICCID        string
	OperatorID   string
	OperatorName string
}

// ModemSpec は fake モデムの内容。
type ModemSpec struct {
	Manufacturer   string
	Model          string
	Revision       string
	IMEI           string
	State          int32
	UnlockRequired uint32
	Ports          []modem.Port
	// Slots は SIM スロット（nil は空きスロット）。PrimarySlot が 0 なら Slots[0] を唯一の SIM とみなす。
	Slots       []*SIMSpec
	PrimarySlot uint32
	// SwitchDelay は SetPrimarySimSlot 後にモデムが再出現するまでの時間。
	SwitchDelay time.Duration
	// AfterSwitch は SetPrimarySimSlot 後に再出現するモデムの内容を変える（切り替えの失敗の再現用）。
	AfterSwitch func(*ModemSpec)
	// FailedReason は State が failed のときの理由（MMModemStateFailedReason）。
	FailedReason uint32
}

// DefaultModem は典型的な MBIM ドングル（SIM 1 枚）。
func DefaultModem() ModemSpec {
	return ModemSpec{
		Manufacturer:   "Sierra Wireless",
		Model:          "EM7455",
		Revision:       "SWI9X30C_02.33.03.00",
		IMEI:           "861234567890123",
		State:          8, // registered
		UnlockRequired: modem.LockNone,
		Ports:          []modem.Port{{Name: "cdc-wdm0", Type: modem.PortTypeMBIM}, {Name: "wwan0", Type: 2}},
		Slots:          []*SIMSpec{{IMSI: "440103123453421", ICCID: "8981100012345678901", OperatorID: "44010", OperatorName: "TEST"}},
		PrimarySlot:    1,
	}
}

// FakeMM は fake ModemManager。
type FakeMM struct {
	conn *dbus.Conn

	mu        sync.Mutex
	version   string
	modems    map[dbus.ObjectPath]*fakeModem
	nextModem int
	nextSIM   int
}

type fakeModem struct {
	f     *FakeMM
	path  dbus.ObjectPath
	spec  ModemSpec
	sims  []dbus.ObjectPath // Slots に対応（空きは "/"）
	props *properties
}

// New はバスに ModemManager として登録する。
func New(t testing.TB, conn *dbus.Conn) *FakeMM {
	t.Helper()
	f := &FakeMM{conn: conn, version: "1.24.0", modems: make(map[dbus.ObjectPath]*fakeModem)}
	root := &properties{ifaces: map[string]map[string]dbus.Variant{
		modem.IfaceManager: {"Version": dbus.MakeVariant(f.version)},
	}}
	must(t, conn.Export(root, modem.RootPath, "org.freedesktop.DBus.Properties"))
	must(t, conn.Export(objectManager{f}, modem.RootPath, "org.freedesktop.DBus.ObjectManager"))
	reply, err := conn.RequestName(modem.BusName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("mmtest: request name: %v (%v)", err, reply)
	}
	return f
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// AddModem はモデムを追加し、InterfacesAdded を送る。
func (f *FakeMM) AddModem(spec ModemSpec) dbus.ObjectPath {
	f.mu.Lock()
	m := &fakeModem{f: f, spec: spec, path: dbus.ObjectPath(fmt.Sprintf("%s%d", modem.ModemPathPrefix, f.nextModem))}
	f.nextModem++
	for _, s := range spec.Slots {
		if s == nil {
			m.sims = append(m.sims, "/")
			continue
		}
		p := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/ModemManager1/SIM/%d", f.nextSIM))
		f.nextSIM++
		m.sims = append(m.sims, p)
	}
	f.modems[m.path] = m
	f.mu.Unlock()

	for i, s := range spec.Slots {
		if s == nil {
			continue
		}
		active := spec.PrimarySlot == 0 || uint32(i+1) == spec.PrimarySlot
		sp := &properties{ifaces: map[string]map[string]dbus.Variant{modem.IfaceSim: {
			"Active":             dbus.MakeVariant(active),
			"Imsi":               dbus.MakeVariant(s.IMSI),
			"SimIdentifier":      dbus.MakeVariant(s.ICCID),
			"OperatorIdentifier": dbus.MakeVariant(s.OperatorID),
			"OperatorName":       dbus.MakeVariant(s.OperatorName),
		}}}
		_ = f.conn.Export(sp, m.sims[i], "org.freedesktop.DBus.Properties")
	}
	m.props = &properties{ifaces: map[string]map[string]dbus.Variant{modem.IfaceModem: m.modemProps()}}
	_ = f.conn.Export(m.props, m.path, "org.freedesktop.DBus.Properties")
	_ = f.conn.Export(m, m.path, modem.IfaceModem)
	_ = f.conn.Emit(modem.RootPath, "org.freedesktop.DBus.ObjectManager.InterfacesAdded", m.path,
		map[string]map[string]dbus.Variant{modem.IfaceModem: m.modemProps()})
	return m.path
}

// RemoveModem はモデムを削除し、InterfacesRemoved を送る。
func (f *FakeMM) RemoveModem(p dbus.ObjectPath) {
	f.mu.Lock()
	m := f.modems[p]
	delete(f.modems, p)
	f.mu.Unlock()
	if m == nil {
		return
	}
	_ = f.conn.Export(nil, p, "org.freedesktop.DBus.Properties")
	_ = f.conn.Export(nil, p, modem.IfaceModem)
	for _, s := range m.sims {
		if s != "/" {
			_ = f.conn.Export(nil, s, "org.freedesktop.DBus.Properties")
		}
	}
	_ = f.conn.Emit(modem.RootPath, "org.freedesktop.DBus.ObjectManager.InterfacesRemoved", p,
		[]string{modem.IfaceModem})
}

// Modems は現在のモデムのパスを返す。
func (f *FakeMM) Modems() []dbus.ObjectPath {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []dbus.ObjectPath
	for p := range f.modems {
		out = append(out, p)
	}
	return out
}

func (m *fakeModem) modemProps() map[string]dbus.Variant {
	ports := make([]struct {
		Name string
		Type uint32
	}, len(m.spec.Ports))
	for i, p := range m.spec.Ports {
		ports[i].Name, ports[i].Type = p.Name, p.Type
	}
	sim := dbus.ObjectPath("/")
	switch {
	case m.spec.PrimarySlot == 0 && len(m.sims) > 0:
		sim = m.sims[0]
	case m.spec.PrimarySlot > 0 && int(m.spec.PrimarySlot) <= len(m.sims):
		sim = m.sims[m.spec.PrimarySlot-1]
	}
	slots := m.sims
	if m.spec.PrimarySlot == 0 {
		slots = []dbus.ObjectPath{}
	}
	primary := ""
	if len(m.spec.Ports) > 0 {
		primary = m.spec.Ports[0].Name
	}
	return map[string]dbus.Variant{
		"Manufacturer":        dbus.MakeVariant(m.spec.Manufacturer),
		"Model":               dbus.MakeVariant(m.spec.Model),
		"Revision":            dbus.MakeVariant(m.spec.Revision),
		"EquipmentIdentifier": dbus.MakeVariant(m.spec.IMEI),
		"DeviceIdentifier":    dbus.MakeVariant("dev-" + m.spec.IMEI),
		"State":               dbus.MakeVariant(m.spec.State),
		"StateFailedReason":   dbus.MakeVariant(m.spec.FailedReason),
		"UnlockRequired":      dbus.MakeVariant(m.spec.UnlockRequired),
		"Ports":               dbus.MakeVariant(ports),
		"PrimaryPort":         dbus.MakeVariant(primary),
		"Sim":                 dbus.MakeVariant(sim),
		"SimSlots":            dbus.MakeVariant(slots),
		"PrimarySimSlot":      dbus.MakeVariant(m.spec.PrimarySlot),
	}
}

// SetPrimarySimSlot は Modem.SetPrimarySimSlot を模擬する。
// 実機と同じく、モデムを削除してから新しいパスで再出現させる。
func (m *fakeModem) SetPrimarySimSlot(slot uint32) *dbus.Error {
	if slot == 0 || int(slot) > len(m.spec.Slots) {
		return dbus.MakeFailedError(fmt.Errorf("invalid slot %d", slot))
	}
	if slot == m.spec.PrimarySlot {
		return nil
	}
	spec := m.spec
	spec.PrimarySlot = slot
	if spec.AfterSwitch != nil {
		spec.AfterSwitch(&spec)
	}
	go func() {
		m.f.RemoveModem(m.path)
		time.Sleep(spec.SwitchDelay)
		m.f.AddModem(spec)
	}()
	return nil
}

type objectManager struct{ f *FakeMM }

// GetManagedObjects は ObjectManager のメソッド。
func (o objectManager) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	out := make(map[dbus.ObjectPath]map[string]map[string]dbus.Variant)
	for p, m := range o.f.modems {
		out[p] = map[string]map[string]dbus.Variant{modem.IfaceModem: m.modemProps()}
	}
	return out, nil
}

// properties は org.freedesktop.DBus.Properties の最小実装。
type properties struct {
	mu     sync.Mutex
	ifaces map[string]map[string]dbus.Variant
}

// Get は Properties.Get。
func (p *properties) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.ifaces[iface][name]; ok {
		return v, nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("no property %s.%s", iface, name))
}

// GetAll は Properties.GetAll。
func (p *properties) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	props, ok := p.ifaces[iface]
	if !ok {
		return nil, dbus.MakeFailedError(fmt.Errorf("no interface %s", iface))
	}
	return props, nil
}
