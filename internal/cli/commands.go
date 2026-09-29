package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/oyaguma3/simwifi/internal/logging"
	"github.com/oyaguma3/simwifi/internal/nai"
	"github.com/oyaguma3/simwifi/internal/probe"
	"github.com/oyaguma3/simwifi/internal/status"
)

var statusCommand = command{
	name:    "status",
	summary: "Check prerequisites (read-only, no side effects).",
	usage:   "status   [--iface wlan0] [--modem N] [--json]",
	setup: func(fs *flag.FlagSet, common *commonFlags) func(context.Context, *Env) error {
		common.register(fs)
		var method, realm string
		fs.StringVar(&method, "method", "aka", "EAP method to check for: aka or akap")
		fs.StringVar(&realm, "realm", "", "NAI realm override (used when checking the identity)")
		return func(ctx context.Context, env *Env) error {
			m, err := nai.ParseMethod(method)
			if err != nil {
				return withCode(CodeError, err)
			}
			sys, err := openSystem(env.Log)
			if err != nil {
				return err
			}
			defer sys.Close()
			r := status.Collect(ctx, sys.statusDeps(), status.Options{
				Iface: common.iface, Modem: common.modem, Method: m, Realm: realm,
			})
			if common.json {
				err = r.WriteJSON(env.Stdout)
			} else {
				err = r.WriteText(env.Stdout)
			}
			if err != nil {
				return err
			}
			if n := len(r.Fatal()); n > 0 {
				return exitf(CodePrecondition, "%d prerequisite check(s) failed", n)
			}
			return nil
		}
	},
}

var probeCommand = command{
	name:    "probe",
	summary: "Probe modem capabilities over MBIM. Runs one AKA with dummy values (uses one SIM authentication).",
	usage:   "probe    [--modem N] [--json]",
	setup: func(fs *flag.FlagSet, common *commonFlags) func(context.Context, *Env) error {
		common.register(fs)
		return func(ctx context.Context, env *Env) error {
			if uid := geteuid(); uid != 0 {
				return exitf(CodePrecondition, "probe must run as root (mbim-proxy only accepts root)")
			}
			sys, err := openSystem(env.Log)
			if err != nil {
				return err
			}
			defer sys.Close()
			m, _, err := sys.selectModem(ctx, common.modem)
			if err != nil && !isSIMError(err) {
				return err
			}
			dev, err := m.MBIMDevice()
			if err != nil {
				return withCode(CodePrecondition, err)
			}
			c, err := sys.dialMBIM(ctx, dev)
			if err != nil {
				return err
			}
			defer c.Close()
			r, err := probe.Run(ctx, c, m.Model, m.EquipmentIdentifier, dev)
			if err != nil {
				return err
			}
			path, err := probe.Save(runDir, r)
			if err != nil {
				return fmt.Errorf("save probe result: %w", err)
			}
			env.Log.Info("probe result saved", "path", path)
			if common.json {
				data, err := probe.Marshal(r)
				if err != nil {
					return err
				}
				_, err = env.Stdout.Write(data)
				return err
			}
			fmt.Fprintf(env.Stdout, "modem:     %s (%s)\n", r.Model, r.FirmwareInfo)
			fmt.Fprintf(env.Stdout, "device:    %s\n", r.Device)
			fmt.Fprintf(env.Stdout, "MBIM AKA:  %s%s\n", r.AKA.State, paren(r.AKA.Status, r.AKA.Detail))
			fmt.Fprintf(env.Stdout, "UICC LLA:  %s%s\n", r.UICC.State, paren(r.UICC.Status, r.UICC.Detail))
			if r.UICC.ATR != "" {
				fmt.Fprintf(env.Stdout, "  ATR:              %s\n", r.UICC.ATR)
				fmt.Fprintf(env.Stdout, "  APPLICATION_LIST: %v\n", r.UICC.ApplicationList)
				fmt.Fprintf(env.Stdout, "  USIM AID:         %s\n", r.UICC.USIMAID)
			}
			if r.NoPath() {
				fmt.Fprintln(env.Stdout, "\nThis modem cannot be used by simwifi (neither path is supported).")
			}
			return nil
		}
	},
}

// isSIMError は SIM が無い・ロック中など、probe でも続行できるエラーかを返す。
func isSIMError(err error) bool {
	e, ok := errors.AsType[*ExitError](err)
	return ok && e.Code == CodePrecondition
}

func paren(status, detail string) string {
	switch {
	case status != "" && detail != "":
		return " (" + status + ": " + detail + ")"
	case status != "":
		return " (" + status + ")"
	case detail != "":
		return " (" + detail + ")"
	}
	return ""
}

// identityOptions は identity の固有フラグ。
type identityOptions struct {
	method string
	realm  string
}

var identityCommand = command{
	name:    "identity",
	summary: "Print the permanent identity (NAI) generated from the SIM.",
	usage:   "identity [--modem N] [--method aka|akap] [--realm REALM]",
	setup: func(fs *flag.FlagSet, common *commonFlags) func(context.Context, *Env) error {
		common.register(fs)
		var o identityOptions
		fs.StringVar(&o.method, "method", "aka", "EAP method: aka or akap")
		fs.StringVar(&o.realm, "realm", "", "override the whole NAI realm")
		return func(ctx context.Context, env *Env) error {
			m, err := nai.ParseMethod(o.method)
			if err != nil {
				return withCode(CodeError, err)
			}
			sys, err := openSystem(env.Log)
			if err != nil {
				return err
			}
			defer sys.Close()
			_, sim, err := sys.selectModem(ctx, common.modem)
			if err != nil {
				return err
			}
			id, err := nai.Generate(m, sim.IMSI, sim.OperatorID, o.realm)
			if err != nil {
				return err
			}
			env.Log.Debug("identity generated", "nai", logging.NAI(id))
			fmt.Fprintln(env.Stdout, id)
			return nil
		}
	},
}

// connectOptions は connect の固有フラグ（DESIGN §6.1）。
type connectOptions struct {
	ssid            string
	method          string
	realm           string
	simSlot         uint
	switchSlot      bool
	authPath        string
	wpa3            bool
	timeoutSec      int
	maxAuthFailures int
	execUp          string
	execDown        string
	daemon          bool
	e2e             e2eOptions
}

func (o *connectOptions) register(fs *flag.FlagSet) {
	fs.StringVar(&o.ssid, "ssid", "", "SSID to connect to (required)")
	fs.StringVar(&o.method, "method", "aka", "EAP method: aka or akap")
	fs.StringVar(&o.realm, "realm", "", "override the whole NAI realm")
	fs.UintVar(&o.simSlot, "sim-slot", 0, "require SIM slot `N` to be active (default: current active slot)")
	fs.BoolVar(&o.switchSlot, "switch-slot", false, "switch to --sim-slot if it is not active (drops cellular)")
	fs.StringVar(&o.authPath, "auth-path", "auto", "USIM access path: auto, aka or uicc")
	fs.BoolVar(&o.wpa3, "wpa3", false, "use WPA-EAP-SHA256 with PMF required")
	fs.IntVar(&o.timeoutSec, "timeout", 60, "seconds to wait for the connection")
	fs.IntVar(&o.maxAuthFailures, "max-auth-failures", 3, "exit after `N` consecutive authentication failures")
	fs.StringVar(&o.execUp, "exec-up", "", "shell command to run when connected")
	fs.StringVar(&o.execDown, "exec-down", "", "shell command to run when disconnected")
	fs.BoolVar(&o.daemon, "daemon", false, "run in background (not implemented; use systemd)")
	o.e2e.register(fs)
}

// validate はフラグの組み合わせを検査する。
func (o *connectOptions) validate() error {
	switch {
	case o.ssid == "":
		return exitf(CodeError, "--ssid is required")
	case o.daemon:
		return exitf(CodeError, "--daemon is not implemented; run simwifi under systemd instead")
	case o.timeoutSec <= 0:
		return exitf(CodeError, "--timeout must be positive")
	case o.maxAuthFailures <= 0:
		return exitf(CodeError, "--max-auth-failures must be positive")
	case o.switchSlot && o.simSlot == 0:
		return exitf(CodeError, "--switch-slot requires --sim-slot")
	}
	if _, err := nai.ParseMethod(o.method); err != nil {
		return withCode(CodeError, err)
	}
	switch o.authPath {
	case "auto", "aka", "uicc":
	default:
		return exitf(CodeError, "--auth-path must be auto, aka or uicc")
	}
	return o.e2e.validate()
}

func (o *connectOptions) timeout() time.Duration {
	return time.Duration(o.timeoutSec) * time.Second
}

var connectCommand = command{
	name:    "connect",
	summary: "Connect to a WPA2/WPA3-Enterprise network with EAP-AKA / EAP-AKA' and stay in the foreground.",
	usage:   "connect  --iface wlan0 --ssid SSID [options]",
	setup: func(fs *flag.FlagSet, common *commonFlags) func(context.Context, *Env) error {
		common.register(fs)
		var o connectOptions
		o.register(fs)
		return func(ctx context.Context, env *Env) error {
			if err := o.validate(); err != nil {
				return err
			}
			return runConnect(ctx, env, common, &o)
		}
	},
}
