// Package wpatest はテスト用の fake wpa_supplicant（D-Bus API の必要な部分だけ）を提供する。
package wpatest

import (
	"fmt"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/oyaguma3/simwifi/internal/supplicant/wpadbus"
)

// Reply は受け取った NetworkReply。
type Reply struct {
	Network dbus.ObjectPath
	Field   string
	Value   string
}

// Fake は fake wpa_supplicant。
type Fake struct {
	conn *dbus.Conn

	mu         sync.Mutex
	eapMethods []string
	ifaces     map[string]*Iface // ifname → Interface
	next       int
	onSelect   func(i *Iface, network dbus.ObjectPath)
	onReply    func(i *Iface, r Reply)
}

// OnSelect は SelectNetwork が呼ばれたときに（別 goroutine で）呼ぶ関数を設定する。
func (f *Fake) OnSelect(h func(i *Iface, network dbus.ObjectPath)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSelect = h
}

// OnReply は NetworkReply が呼ばれたときに（別 goroutine で）呼ぶ関数を設定する。
func (f *Fake) OnReply(h func(i *Iface, r Reply)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onReply = h
}

func (f *Fake) hooks() (func(*Iface, dbus.ObjectPath), func(*Iface, Reply)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.onSelect, f.onReply
}

// Iface は fake の Interface オブジェクト。
type Iface struct {
	f          *Fake
	Path       dbus.ObjectPath
	Name       string
	ConfigFile string

	mu       sync.Mutex
	state    string
	networks map[dbus.ObjectPath]map[string]dbus.Variant
	nextNet  int
	replies  []Reply
	selected dbus.ObjectPath
}

// New はバスに wpa_supplicant として登録する。
func New(t testing.TB, conn *dbus.Conn) *Fake {
	t.Helper()
	f := &Fake{conn: conn, eapMethods: []string{"MD5", "TLS", "SIM", "AKA", "AKA'", "PEAP"}, ifaces: make(map[string]*Iface)}
	if err := conn.Export(root{f}, wpadbus.RootPath, wpadbus.IfaceRoot); err != nil {
		t.Fatal(err)
	}
	if err := conn.Export(rootProps{f}, wpadbus.RootPath, "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
	reply, err := conn.RequestName(wpadbus.BusName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("wpatest: request name: %v (%v)", err, reply)
	}
	return f
}

// SetEAPMethods は EapMethods プロパティを差し替える。
func (f *Fake) SetEAPMethods(m []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eapMethods = m
}

// AddInterface は（他者が作った想定の）Interface を追加する。
func (f *Fake) AddInterface(ifname, configFile string) *Iface {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addLocked(ifname, configFile)
}

func (f *Fake) addLocked(ifname, configFile string) *Iface {
	i := &Iface{
		f:          f,
		Path:       dbus.ObjectPath(fmt.Sprintf("/fi/w1/wpa_supplicant1/Interfaces/%d", f.next)),
		Name:       ifname,
		ConfigFile: configFile,
		state:      "disconnected",
		networks:   make(map[dbus.ObjectPath]map[string]dbus.Variant),
	}
	f.next++
	f.ifaces[ifname] = i
	_ = f.conn.Export(i, i.Path, wpadbus.IfaceIface)
	_ = f.conn.Export(ifaceProps{i}, i.Path, "org.freedesktop.DBus.Properties")
	return i
}

// Interface は ifname の Interface を返す（無ければ nil）。
func (f *Fake) Interface(ifname string) *Iface {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ifaces[ifname]
}

// Quit はバスから名前を手放す（wpa_supplicant の終了を模擬する）。
func (f *Fake) Quit() {
	_, _ = f.conn.ReleaseName(wpadbus.BusName)
}

type root struct{ f *Fake }

// GetInterface は fi.w1.wpa_supplicant1.GetInterface。
func (r root) GetInterface(ifname string) (dbus.ObjectPath, *dbus.Error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if i, ok := r.f.ifaces[ifname]; ok {
		return i.Path, nil
	}
	return "", dbus.NewError(wpadbus.ErrIfaceUnknown, []any{"wpa_supplicant knows nothing about this interface."})
}

// CreateInterface は fi.w1.wpa_supplicant1.CreateInterface。
func (r root) CreateInterface(args map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	ifname, _ := args["Ifname"].Value().(string)
	cfg, _ := args["ConfigFile"].Value().(string)
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if _, ok := r.f.ifaces[ifname]; ok {
		return "", dbus.NewError("fi.w1.wpa_supplicant1.InterfaceExists", []any{"exists"})
	}
	return r.f.addLocked(ifname, cfg).Path, nil
}

// RemoveInterface は fi.w1.wpa_supplicant1.RemoveInterface。
func (r root) RemoveInterface(p dbus.ObjectPath) *dbus.Error {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	for name, i := range r.f.ifaces {
		if i.Path == p {
			delete(r.f.ifaces, name)
			_ = r.f.conn.Export(nil, p, wpadbus.IfaceIface)
			_ = r.f.conn.Export(nil, p, "org.freedesktop.DBus.Properties")
			return nil
		}
	}
	return dbus.NewError(wpadbus.ErrIfaceUnknown, []any{"unknown"})
}

type rootProps struct{ f *Fake }

// GetAll は Properties.GetAll（ルートオブジェクト）。
func (p rootProps) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	p.f.mu.Lock()
	defer p.f.mu.Unlock()
	return map[string]dbus.Variant{
		"EapMethods":   dbus.MakeVariant(p.f.eapMethods),
		"Capabilities": dbus.MakeVariant([]string{"ap", "interworking"}),
	}, nil
}

// Get は Properties.Get（ルートオブジェクト）。
func (p rootProps) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	all, _ := p.GetAll(iface)
	return all[name], nil
}

type ifaceProps struct{ i *Iface }

// GetAll は Properties.GetAll（Interface）。
func (p ifaceProps) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	p.i.mu.Lock()
	defer p.i.mu.Unlock()
	return map[string]dbus.Variant{
		"State":      dbus.MakeVariant(p.i.state),
		"ConfigFile": dbus.MakeVariant(p.i.ConfigFile),
		"Ifname":     dbus.MakeVariant(p.i.Name),
		"Capabilities": dbus.MakeVariant(map[string]dbus.Variant{
			"KeyMgmt": dbus.MakeVariant([]string{"wpa-psk", "wpa-eap", "wpa-eap-sha256", "sae"}),
		}),
	}, nil
}

// Get は Properties.Get（Interface）。
func (p ifaceProps) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	all, _ := p.GetAll(iface)
	v, ok := all[name]
	if !ok {
		return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("no property %s", name))
	}
	return v, nil
}

// AddNetwork は Interface.AddNetwork。
func (i *Iface) AddNetwork(args map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	p := dbus.ObjectPath(fmt.Sprintf("%s/Networks/%d", i.Path, i.nextNet))
	i.nextNet++
	i.networks[p] = args
	return p, nil
}

// SelectNetwork は Interface.SelectNetwork。
func (i *Iface) SelectNetwork(p dbus.ObjectPath) *dbus.Error {
	i.mu.Lock()
	if _, ok := i.networks[p]; !ok {
		i.mu.Unlock()
		return dbus.NewError("fi.w1.wpa_supplicant1.NetworkUnknown", []any{"unknown"})
	}
	i.selected = p
	i.mu.Unlock()
	if h, _ := i.f.hooks(); h != nil {
		go h(i, p)
	}
	return nil
}

// RemoveNetwork は Interface.RemoveNetwork。
func (i *Iface) RemoveNetwork(p dbus.ObjectPath) *dbus.Error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.networks[p]; !ok {
		return dbus.NewError("fi.w1.wpa_supplicant1.NetworkUnknown", []any{"unknown"})
	}
	delete(i.networks, p)
	return nil
}

// Disconnect は Interface.Disconnect。
func (i *Iface) Disconnect() *dbus.Error {
	i.SetState("disconnected")
	return nil
}

// NetworkReply は Interface.NetworkReply。
func (i *Iface) NetworkReply(p dbus.ObjectPath, field, value string) *dbus.Error {
	r := Reply{Network: p, Field: field, Value: value}
	i.mu.Lock()
	i.replies = append(i.replies, r)
	i.mu.Unlock()
	if _, h := i.f.hooks(); h != nil {
		go h(i, r)
	}
	return nil
}

// Networks は追加されたネットワークの設定を返す。
func (i *Iface) Networks() map[dbus.ObjectPath]map[string]dbus.Variant {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make(map[dbus.ObjectPath]map[string]dbus.Variant, len(i.networks))
	for k, v := range i.networks {
		out[k] = v
	}
	return out
}

// Replies は受け取った NetworkReply を返す。
func (i *Iface) Replies() []Reply {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]Reply(nil), i.replies...)
}

// SetState は State を変え、PropertiesChanged を送る。
func (i *Iface) SetState(s string) {
	i.mu.Lock()
	i.state = s
	i.mu.Unlock()
	_ = i.f.conn.Emit(i.Path, "org.freedesktop.DBus.Properties.PropertiesChanged", wpadbus.IfaceIface,
		map[string]dbus.Variant{"State": dbus.MakeVariant(s)}, []string{})
}

// SetDisconnectReason は DisconnectReason の PropertiesChanged を送る。
func (i *Iface) SetDisconnectReason(r int32) {
	_ = i.f.conn.Emit(i.Path, "org.freedesktop.DBus.Properties.PropertiesChanged", wpadbus.IfaceIface,
		map[string]dbus.Variant{"DisconnectReason": dbus.MakeVariant(r)}, []string{})
}

// EmitEAP は EAP シグナルを送る。
func (i *Iface) EmitEAP(status, parameter string) {
	_ = i.f.conn.Emit(i.Path, wpadbus.IfaceIface+".EAP", status, parameter)
}

// RequestSIM は NetworkRequest(network, "SIM", text) を送る。
func (i *Iface) RequestSIM(network dbus.ObjectPath, text string) {
	_ = i.f.conn.Emit(i.Path, wpadbus.IfaceIface+".NetworkRequest", network, "SIM", text)
}
