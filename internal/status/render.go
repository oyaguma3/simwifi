package status

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"time"

	"github.com/godbus/dbus/v5"
)

// WriteText は 1 行 1 項目で出力する（✔ / ✘ / ·）。
func (r *Report) WriteText(w io.Writer) error {
	width := 0
	for _, c := range r.Checks {
		width = max(width, len(c.Name))
	}
	for _, c := range r.Checks {
		mark := "✔"
		switch {
		case c.Fatal || !c.OK:
			mark = "✘"
		case c.Info:
			mark = "·"
		}
		if _, err := fmt.Fprintf(w, "%s %-*s  %s\n", mark, width, c.Name, c.Detail); err != nil {
			return err
		}
	}
	return nil
}

// WriteJSON は JSON で出力する。
func (r *Report) WriteJSON(w io.Writer) error {
	out := struct {
		OK     bool    `json:"ok"`
		Checks []Check `json:"checks"`
	}{OK: len(r.Fatal()) == 0, Checks: r.Checks}
	if err := json.MarshalWrite(w, out, jsontext.WithIndent("  ")); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// NetworkManager の D-Bus 名。
const (
	nmBusName = "org.freedesktop.NetworkManager"
	nmPath    = dbus.ObjectPath("/org/freedesktop/NetworkManager")
)

// NMFunc は NetworkManager に iface の管理状態を問い合わせる関数を返す。
func NMFunc(conn *dbus.Conn) func(ctx context.Context, iface string) (NMInfo, error) {
	return func(ctx context.Context, iface string) (NMInfo, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var info NMInfo
		if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, nmBusName).Store(&info.Running); err != nil || !info.Running {
			return info, err
		}
		var dev dbus.ObjectPath
		if err := conn.Object(nmBusName, nmPath).CallWithContext(ctx, nmBusName+".GetDeviceByIpIface", 0, iface).Store(&dev); err != nil {
			// NM が知らないデバイス
			return info, nil
		}
		info.Known = true
		var v dbus.Variant
		if err := conn.Object(nmBusName, dev).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, nmBusName+".Device", "Managed").Store(&v); err != nil {
			return info, err
		}
		info.Managed, _ = v.Value().(bool)
		return info, nil
	}
}
