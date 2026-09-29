// Package dbustest はテスト用のプライベートな dbus-daemon を起動する。
// fake の ModemManager / wpa_supplicant をこのバス上に export して、D-Bus クライアントを検証する。
package dbustest

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const config = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=%SOCKET%</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// Bus はプライベートな dbus-daemon。
type Bus struct {
	Address string
}

// Start は dbus-daemon を起動する。dbus-daemon が無ければテストを skip する。
func Start(t testing.TB) *Bus {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not found")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(conf, []byte(strings.ReplaceAll(config, "%SOCKET%", filepath.Join(dir, "bus"))), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(daemon, "--config-file="+conf, "--nofork", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dbus-daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	addr := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(out).ReadString('\n')
		addr <- strings.TrimSpace(line)
	}()
	select {
	case a := <-addr:
		if a == "" {
			t.Fatal("dbus-daemon did not print an address")
		}
		return &Bus{Address: a}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for dbus-daemon")
	}
	return nil
}

// Conn はバスへの新しい接続を返す。テスト終了時に閉じる。
func (b *Bus) Conn(t testing.TB) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(b.Address)
	if err != nil {
		t.Fatalf("connect to test bus: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
