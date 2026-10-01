// Package modem は ModemManager の D-Bus クライアント（DESIGN §4.4）。
package modem

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// D-Bus の名前。
const (
	BusName         = "org.freedesktop.ModemManager1"
	RootPath        = dbus.ObjectPath("/org/freedesktop/ModemManager1")
	IfaceManager    = "org.freedesktop.ModemManager1"
	IfaceModem      = "org.freedesktop.ModemManager1.Modem"
	IfaceSim        = "org.freedesktop.ModemManager1.Sim"
	ModemPathPrefix = "/org/freedesktop/ModemManager1/Modem/"

	ifaceObjectManager = "org.freedesktop.DBus.ObjectManager"
	ifaceProperties    = "org.freedesktop.DBus.Properties"
)

// ModemManager の列挙値（include/ModemManager-enums.h）。
const (
	PortTypeMBIM uint32 = 7

	LockUnknown uint32 = 0
	LockNone    uint32 = 1
	LockSIMPIN  uint32 = 2
	LockSIMPIN2 uint32 = 3
	LockSIMPUK  uint32 = 4
	LockSIMPUK2 uint32 = 5

	StateFailed       int32 = -1
	StateUnknown      int32 = 0
	StateInitializing int32 = 1
	StateLocked       int32 = 2
	StateDisabled     int32 = 3
)

// DefaultTimeout は MM への 1 回の呼び出しの待ち時間（DESIGN §7.4）。
const DefaultTimeout = 10 * time.Second

var (
	// ErrNotRunning は ModemManager がバスに居ないことを示す。
	ErrNotRunning = errors.New("ModemManager is not running")
	// ErrNoModem はモデムが 1 台も無いことを示す。
	ErrNoModem = errors.New("no modem found")
	// ErrMultipleModems は複数のモデムがあり、--modem が必要なことを示す。
	ErrMultipleModems = errors.New("multiple modems found; specify --modem")
	// ErrModemNotFound は --modem で指定したモデムが無いことを示す。
	ErrModemNotFound = errors.New("specified modem not found")
	// ErrNoMBIMPort はモデムに MBIM ポートが無いことを示す。
	ErrNoMBIMPort = errors.New("modem has no MBIM port")
)

// Port はモデムのポート。
type Port struct {
	Name string
	Type uint32
}

// Modem は MM の Modem オブジェクトの情報。
type Modem struct {
	Path                dbus.ObjectPath
	Manufacturer        string
	Model               string
	Revision            string
	EquipmentIdentifier string
	DeviceIdentifier    string
	State               int32
	StateFailedReason   uint32
	UnlockRequired      uint32
	Ports               []Port
	PrimaryPort         string
	SIM                 dbus.ObjectPath
	SimSlots            []dbus.ObjectPath
	PrimarySimSlot      uint32
}

// Index はオブジェクトパス末尾の番号（mmcli の -m と同じ）を返す。
func (m Modem) Index() string { return path.Base(string(m.Path)) }

// MBIMDevice は MBIM ポートのデバイスパス（/dev/cdc-wdmN）を返す。
func (m Modem) MBIMDevice() (string, error) {
	for _, p := range m.Ports {
		if p.Type == PortTypeMBIM {
			return "/dev/" + p.Name, nil
		}
	}
	return "", ErrNoMBIMPort
}

// Locked は PIN などでロックされているかを返す。
// SIM-PIN2 / SIM-PUK2 は通常の利用を妨げないので、ModemManager と同じくロックとみなさない
// （Sierra EM7455 は SIM-PIN2 を報告することがある）。
func (m Modem) Locked() bool {
	switch m.UnlockRequired {
	case LockUnknown, LockNone, LockSIMPIN2, LockSIMPUK2:
		return m.State == StateLocked
	}
	return true
}

// StateName は MMModemState の名前を返す。
func StateName(s int32) string {
	names := map[int32]string{-1: "failed", 0: "unknown", 1: "initializing", 2: "locked", 3: "disabled",
		4: "disabling", 5: "enabling", 6: "enabled", 7: "searching", 8: "registered", 9: "disconnecting",
		10: "connecting", 11: "connected"}
	if n, ok := names[s]; ok {
		return n
	}
	return fmt.Sprint(s)
}

// FailedReasonName は MMModemStateFailedReason の名前を返す。
func FailedReasonName(r uint32) string {
	names := []string{"none", "unknown", "sim-missing", "sim-error", "unknown-capabilities", "esim-without-profiles"}
	if int(r) < len(names) {
		return names[r]
	}
	return fmt.Sprint(r)
}

// StateString は状態を表示用の文字列にする（failed なら理由を付ける）。
func (m Modem) StateString() string {
	if m.State == StateFailed {
		return "failed (" + FailedReasonName(m.StateFailedReason) + ")"
	}
	return StateName(m.State)
}

// LockName は MMModemLock の名前を返す。
func LockName(l uint32) string {
	names := []string{"unknown", "none", "sim-pin", "sim-pin2", "sim-puk", "sim-puk2", "ph-sp-pin", "ph-sp-puk",
		"ph-net-pin", "ph-net-puk", "ph-sim-pin", "ph-corp-pin", "ph-corp-puk", "ph-fsim-pin", "ph-fsim-puk",
		"ph-netsub-pin", "ph-netsub-puk"}
	if int(l) < len(names) {
		return names[l]
	}
	return fmt.Sprint(l)
}

// SIM は MM の Sim オブジェクトの情報。
type SIM struct {
	Path         dbus.ObjectPath
	Active       bool
	IMSI         string
	ICCID        string
	OperatorID   string
	OperatorName string
}

// Client は MM の D-Bus クライアント。
type Client struct {
	conn    *dbus.Conn
	log     *slog.Logger
	timeout time.Duration
}

// New は Client を作る。conn は system bus（テストではプライベートバス）。
func New(conn *dbus.Conn, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Client{conn: conn, log: logger.With("component", "modem"), timeout: DefaultTimeout}
}

func (c *Client) call(ctx context.Context, p dbus.ObjectPath, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.conn.Object(BusName, p).CallWithContext(ctx, method, 0, args...)
}

// Running は MM がバスに居るかを返す（D-Bus activation は起こさない）。
func (c *Client) Running(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var has bool
	err := c.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, BusName).Store(&has)
	return has, err
}

// Version は MM のバージョンを返す。
func (c *Client) Version(ctx context.Context) (string, error) {
	if ok, err := c.Running(ctx); err != nil {
		return "", err
	} else if !ok {
		return "", ErrNotRunning
	}
	var v dbus.Variant
	if err := c.call(ctx, RootPath, ifaceProperties+".Get", IfaceManager, "Version").Store(&v); err != nil {
		return "", fmt.Errorf("get ModemManager version: %w", err)
	}
	s, _ := v.Value().(string)
	return s, nil
}

// Modems はモデムの一覧を返す（パス順）。
func (c *Client) Modems(ctx context.Context) ([]Modem, error) {
	if ok, err := c.Running(ctx); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNotRunning
	}
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := c.call(ctx, RootPath, ifaceObjectManager+".GetManagedObjects").Store(&objs); err != nil {
		return nil, fmt.Errorf("list modems: %w", err)
	}
	var modems []Modem
	for p, ifaces := range objs {
		if props, ok := ifaces[IfaceModem]; ok {
			modems = append(modems, parseModem(p, props))
		}
	}
	slices.SortFunc(modems, func(a, b Modem) int { return strings.Compare(string(a.Path), string(b.Path)) })
	return modems, nil
}

// Modem は 1 台のモデムの情報を取り直す。
func (c *Client) Modem(ctx context.Context, p dbus.ObjectPath) (Modem, error) {
	var props map[string]dbus.Variant
	if err := c.call(ctx, p, ifaceProperties+".GetAll", IfaceModem).Store(&props); err != nil {
		return Modem{}, fmt.Errorf("get modem %s: %w", p, err)
	}
	return parseModem(p, props), nil
}

// SIM は SIM オブジェクトの情報を返す。
func (c *Client) SIM(ctx context.Context, p dbus.ObjectPath) (SIM, error) {
	if !valid(p) {
		return SIM{}, errors.New("no SIM")
	}
	var props map[string]dbus.Variant
	if err := c.call(ctx, p, ifaceProperties+".GetAll", IfaceSim).Store(&props); err != nil {
		return SIM{}, fmt.Errorf("get SIM %s: %w", p, err)
	}
	return SIM{
		Path:         p,
		Active:       get[bool](props, "Active"),
		IMSI:         get[string](props, "Imsi"),
		ICCID:        get[string](props, "SimIdentifier"),
		OperatorID:   get[string](props, "OperatorIdentifier"),
		OperatorName: get[string](props, "OperatorName"),
	}, nil
}

// Select は --modem の値（空、index、オブジェクトパス）でモデムを選ぶ。
func Select(modems []Modem, sel string) (Modem, error) {
	if sel == "" {
		switch len(modems) {
		case 0:
			return Modem{}, ErrNoModem
		case 1:
			return modems[0], nil
		}
		return Modem{}, ErrMultipleModems
	}
	for _, m := range modems {
		if string(m.Path) == sel || m.Index() == sel {
			return m, nil
		}
	}
	return Modem{}, fmt.Errorf("%w: %s", ErrModemNotFound, sel)
}

// SetPrimarySimSlot はアクティブな SIM スロットを切り替える（1 始まり）。
// 呼び出し後、モデムオブジェクトは作り直される（WaitModem で待つ）。
func (c *Client) SetPrimarySimSlot(ctx context.Context, p dbus.ObjectPath, slot uint32) error {
	if err := c.call(ctx, p, IfaceModem+".SetPrimarySimSlot", slot).Err; err != nil {
		return fmt.Errorf("switch SIM slot to %d: %w", slot, err)
	}
	return nil
}

// Watcher は MM の ObjectManager シグナル（モデムの追加・削除）を受け取る。
type Watcher struct {
	c  *Client
	ch chan *dbus.Signal
}

// Watch はモデムの追加・削除の監視を始める。使い終えたら Close する。
func (c *Client) Watch(ctx context.Context) (*Watcher, error) {
	opts := []dbus.MatchOption{dbus.WithMatchSender(BusName), dbus.WithMatchObjectPath(RootPath), dbus.WithMatchInterface(ifaceObjectManager)}
	if err := c.conn.AddMatchSignalContext(ctx, opts...); err != nil {
		return nil, fmt.Errorf("subscribe to ModemManager signals: %w", err)
	}
	w := &Watcher{c: c, ch: make(chan *dbus.Signal, 32)}
	c.conn.Signal(w.ch)
	return w, nil
}

// Close は監視をやめる。
func (w *Watcher) Close() {
	w.c.conn.RemoveSignal(w.ch)
	_ = w.c.conn.RemoveMatchSignal(dbus.WithMatchSender(BusName), dbus.WithMatchObjectPath(RootPath), dbus.WithMatchInterface(ifaceObjectManager))
}

// WaitRemoved は p のモデムが削除されるまで待つ。
func (w *Watcher) WaitRemoved(ctx context.Context, p dbus.ObjectPath) error {
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case s, ok := <-w.ch:
			if !ok {
				return errors.New("D-Bus connection closed")
			}
			if s.Path != RootPath || s.Name != ifaceObjectManager+".InterfacesRemoved" || len(s.Body) < 2 {
				continue
			}
			rp, _ := s.Body[0].(dbus.ObjectPath)
			ifaces, _ := s.Body[1].([]string)
			if rp == p && slices.Contains(ifaces, IfaceModem) {
				return nil
			}
		}
	}
}

// WaitAdded は match を満たすモデムが現れるまで待つ。既に居ればすぐ返す。
func (w *Watcher) WaitAdded(ctx context.Context, match func(Modem) bool) (Modem, error) {
	// 監視開始より前に現れていた場合に備えて、まず一覧を見る
	if modems, err := w.c.Modems(ctx); err == nil {
		for _, m := range modems {
			if match(m) {
				return m, nil
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return Modem{}, context.Cause(ctx)
		case s, ok := <-w.ch:
			if !ok {
				return Modem{}, errors.New("D-Bus connection closed")
			}
			if s.Path != RootPath || s.Name != ifaceObjectManager+".InterfacesAdded" || len(s.Body) < 2 {
				continue
			}
			p, _ := s.Body[0].(dbus.ObjectPath)
			ifaces, _ := s.Body[1].(map[string]map[string]dbus.Variant)
			props, ok := ifaces[IfaceModem]
			if !ok {
				continue
			}
			if m := parseModem(p, props); match(m) {
				return m, nil
			}
		}
	}
}

func valid(p dbus.ObjectPath) bool { return p != "" && p != "/" && p.IsValid() }

// ValidSIM は SIM のパスが空（"/"）でないかを返す。
func ValidSIM(p dbus.ObjectPath) bool { return valid(p) }

func parseModem(p dbus.ObjectPath, props map[string]dbus.Variant) Modem {
	m := Modem{
		Path:                p,
		Manufacturer:        get[string](props, "Manufacturer"),
		Model:               get[string](props, "Model"),
		Revision:            get[string](props, "Revision"),
		EquipmentIdentifier: get[string](props, "EquipmentIdentifier"),
		DeviceIdentifier:    get[string](props, "DeviceIdentifier"),
		State:               get[int32](props, "State"),
		StateFailedReason:   get[uint32](props, "StateFailedReason"),
		UnlockRequired:      get[uint32](props, "UnlockRequired"),
		PrimaryPort:         get[string](props, "PrimaryPort"),
		SIM:                 get[dbus.ObjectPath](props, "Sim"),
		SimSlots:            get[[]dbus.ObjectPath](props, "SimSlots"),
		PrimarySimSlot:      get[uint32](props, "PrimarySimSlot"),
	}
	if v, ok := props["Ports"]; ok {
		var ports []struct {
			Name string
			Type uint32
		}
		if err := v.Store(&ports); err == nil {
			for _, pt := range ports {
				m.Ports = append(m.Ports, Port{Name: pt.Name, Type: pt.Type})
			}
		}
	}
	return m
}

// get はプロパティを型付きで取り出す。無いか型が違えばゼロ値。
func get[T any](props map[string]dbus.Variant, name string) T {
	var zero T
	v, ok := props[name]
	if !ok {
		return zero
	}
	if t, ok := v.Value().(T); ok {
		return t
	}
	return zero
}
