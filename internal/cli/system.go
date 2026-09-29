package cli

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/godbus/dbus/v5"

	"github.com/oyaguma3/simwifi/internal/lock"
	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/modem"
	"github.com/oyaguma3/simwifi/internal/nai"
	"github.com/oyaguma3/simwifi/internal/status"
	"github.com/oyaguma3/simwifi/internal/supplicant"
	"github.com/oyaguma3/simwifi/internal/supplicant/wpadbus"
)

// 実環境のパスなど。テストで差し替える。
var (
	runDir      = lock.DefaultDir
	sysClassNet = "/sys/class/net"
	proxyAddr   = mbim.DefaultProxyAddr
	geteuid     = os.Geteuid
	// connectBus は system bus に接続する（テストでは DBUS_SYSTEM_BUS_ADDRESS でプライベートバスを指す）。
	connectBus = func() (*dbus.Conn, error) { return dbus.ConnectSystemBus() }
)

// system は system bus 上のクライアント群。
type system struct {
	conn *dbus.Conn
	mm   *modem.Client
	wpa  *wpadbus.Client
	log  *slog.Logger
}

func openSystem(log *slog.Logger) (*system, error) {
	conn, err := connectBus()
	if err != nil {
		return nil, exitf(CodePrecondition, "connect to D-Bus system bus: %v", err)
	}
	return &system{conn: conn, mm: modem.New(conn, log), wpa: wpadbus.New(conn, log), log: log}, nil
}

func (s *system) Close() { s.conn.Close() }

// dialMBIM は mbim-proxy 経由でデバイスを開く。
func (s *system) dialMBIM(ctx context.Context, device string) (*mbim.Client, error) {
	return mbim.Dial(ctx, mbim.Options{ProxyAddr: proxyAddr, DevicePath: device, Logger: s.log})
}

// statusDeps は status.Collect の依存を組み立てる。
func (s *system) statusDeps() status.Deps {
	return status.Deps{
		Euid: geteuid(),
		MM:   s.mm,
		MBIM: func(ctx context.Context, device string) (mbim.DeviceCaps, error) {
			c, err := s.dialMBIM(ctx, device)
			if err != nil {
				return mbim.DeviceCaps{}, err
			}
			defer c.Close()
			return c.DeviceCaps(ctx)
		},
		WPA:         s.wpa,
		NM:          status.NMFunc(s.conn),
		SysClassNet: sysClassNet,
		RunDir:      runDir,
	}
}

// selectModem は --modem に従ってモデムと、そのアクティブな SIM を返す。
func (s *system) selectModem(ctx context.Context, sel string) (modem.Modem, modem.SIM, error) {
	modems, err := s.mm.Modems(ctx)
	if err != nil {
		return modem.Modem{}, modem.SIM{}, err
	}
	m, err := modem.Select(modems, sel)
	if err != nil {
		return modem.Modem{}, modem.SIM{}, err
	}
	if m.Locked() {
		return m, modem.SIM{}, exitf(CodePrecondition, "SIM is locked; unlock it with ModemManager first")
	}
	if !modem.ValidSIM(m.SIM) {
		return m, modem.SIM{}, exitf(CodePrecondition, "no SIM in the active slot")
	}
	sim, err := s.mm.SIM(ctx, m.SIM)
	return m, sim, err
}

// preconditionErrors は前提条件 NG（終了コード 2）とみなすエラー。
var preconditionErrors = []error{
	lock.ErrLocked,
	modem.ErrNotRunning, modem.ErrNoModem, modem.ErrMultipleModems, modem.ErrModemNotFound, modem.ErrNoMBIMPort,
	mbim.ErrProxyRejected,
	supplicant.ErrNotRunning, supplicant.ErrInterfaceBusy,
	nai.ErrNoOperatorID, nai.ErrInvalidIMSI,
}

func isPrecondition(err error) bool {
	for _, e := range preconditionErrors {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// reportFatal は Fatal なチェックをログに出し、終了コード 2 のエラーを返す。
func reportFatal(log *slog.Logger, r *status.Report) error {
	fatal := r.Fatal()
	if len(fatal) == 0 {
		return nil
	}
	for _, c := range fatal {
		log.Error("prerequisite check failed", "check", c.Name, "detail", c.Detail)
	}
	return exitf(CodePrecondition, "%d prerequisite check(s) failed (see 'simwifi status')", len(fatal))
}
