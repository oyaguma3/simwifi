// Command simwifi は MBIM モデムの SIM を使い、EAP-AKA / EAP-AKA' で無線 LAN に接続する。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/oyaguma3/simwifi/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
