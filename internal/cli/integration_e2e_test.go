//go:build e2e

package cli

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/oyaguma3/simwifi/internal/mbim"
)

// E2E ビルドの --auth-backend milenage は MM / mbim-proxy を使わずに接続できる。
func TestConnectMilenageBackend(t *testing.T) {
	w := newWorld(t, worldOptions{}) // モデムは AKA も UICC も非対応（使われないことの確認）
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done, _, errb := w.runAsync(ctx, "connect", "--iface", "wlan0", "--ssid", "corp",
		"--auth-backend", "milenage", "--milenage-imsi", "440103123453421",
		"--milenage-k", hex.EncodeToString(tK), "--milenage-opc", hex.EncodeToString(tOPc), "--milenage-sqn", "10")
	w.waitCompleted()
	cancel()
	if code := waitCode(t, done, errb); code != 0 {
		t.Fatalf("exit code %d; stderr:\n%s", code, errb.String())
	}
	if n := w.proxy.Count(mbim.ServiceAuth, mbim.CIDAuthAKA); n != 0 {
		t.Errorf("modem must not be used, got %d AKA requests", n)
	}
}

func TestMilenageBackendValidation(t *testing.T) {
	code, _, errOut := run(t, "connect", "--ssid", "x", "--auth-backend", "milenage")
	if code != 1 || errOut == "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}
