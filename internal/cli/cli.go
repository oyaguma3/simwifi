// Package cli は simwifi のサブコマンドを定義する（DESIGN §6）。
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/oyaguma3/simwifi/internal/logging"
)

// Env はサブコマンドに渡す実行環境。
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Log    *slog.Logger
}

// command はサブコマンドの定義。
type command struct {
	name    string
	summary string
	usage   string
	// setup はフラグを登録し、フラグ解析後に呼ぶ実行関数を返す。
	setup func(fs *flag.FlagSet, common *commonFlags) func(ctx context.Context, env *Env) error
}

var commands = []command{
	statusCommand,
	probeCommand,
	identityCommand,
	connectCommand,
	versionCommand,
}

// commonFlags は全サブコマンド共通のフラグ（DESIGN §6.1）。
type commonFlags struct {
	modem   string
	iface   string
	logFile string
	v, vv   bool
	logIMSI bool
	json    bool
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.modem, "modem", "", "ModemManager modem index or object path (required if several modems exist)")
	fs.StringVar(&c.iface, "iface", "wlan0", "wireless interface")
	fs.StringVar(&c.logFile, "log-file", "", "also write JSON logs to `PATH`")
	fs.BoolVar(&c.v, "v", false, "debug logging")
	fs.BoolVar(&c.vv, "vv", false, "trace logging (raw MBIM / D-Bus messages)")
	fs.BoolVar(&c.logIMSI, "log-imsi", false, "do not mask the IMSI in logs and output")
	fs.BoolVar(&c.json, "json", false, "machine-readable output (status / probe)")
}

func (c *commonFlags) verbosity() int {
	switch {
	case c.vv:
		return 2
	case c.v:
		return 1
	}
	return 0
}

// Run は引数（プログラム名を除く）を解釈してサブコマンドを実行し、終了コードを返す。
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return int(CodeError)
	}
	name := args[0]
	if name == "help" || name == "-h" || name == "--help" {
		printUsage(stdout)
		return int(CodeOK)
	}
	i := slices.IndexFunc(commands, func(c command) bool { return c.name == name })
	if i < 0 {
		fmt.Fprintf(stderr, "simwifi: unknown command %q\n\n", name)
		printUsage(stderr)
		return int(CodeError)
	}
	cmd := commands[i]

	fs := flag.NewFlagSet("simwifi "+cmd.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: simwifi %s\n\n%s\n\nFlags:\n", cmd.usage, cmd.summary)
		fs.PrintDefaults()
	}
	var common commonFlags
	run := cmd.setup(fs, &common)
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return int(CodeOK)
		}
		return int(CodeError)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "simwifi %s: unexpected arguments: %s\n", cmd.name, strings.Join(fs.Args(), " "))
		return int(CodeError)
	}

	logging.SetShowIMSI(common.logIMSI)
	logger, closer, err := logging.New(logging.Options{
		Verbosity: common.verbosity(),
		Stderr:    stderr,
		FilePath:  common.logFile,
	})
	if err != nil {
		fmt.Fprintf(stderr, "simwifi: %v\n", err)
		return int(CodeError)
	}
	defer closer.Close()

	env := &Env{Stdout: stdout, Stderr: stderr, Log: logger.With("component", "cli")}
	if err := run(ctx, env); err != nil {
		code := codeOf(err)
		logger.Error(err.Error(), "component", "cli", "exit_code", int(code))
		return int(code)
	}
	return int(CodeOK)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "simwifi - EAP-AKA / EAP-AKA' Wi-Fi authentication with the SIM in an MBIM modem")
	fmt.Fprintln(w, "\nUsage:")
	for _, c := range commands {
		fmt.Fprintf(w, "  simwifi %s\n", c.usage)
	}
	fmt.Fprintln(w, "\nRun 'simwifi <command> -h' for details.")
}
