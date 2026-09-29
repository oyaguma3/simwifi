package main

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oyaguma3/simwifi/internal/simauth"
	"github.com/oyaguma3/simwifi/internal/simauth/milenage"
)

const db = `# IMSI Ki OPc AMF SQN
555444333222111 5122250214c33e723a5dd523fc145fc0 981d464c7c52eb6e5036234984ad0bcf c3ab 000000000020
`

func TestIncSQN(t *testing.T) {
	// SEQ と IND がそれぞれ 1 進み、IND は 5 ビットで折り返す
	if got := incSQN(0x20); got != 0x41 {
		t.Fatalf("incSQN(0x20) = %x", got)
	}
	if got := incSQN(0x3f); got != 0x40 {
		t.Fatalf("incSQN(0x3f) = %x", got)
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "milenage.db")
	if err := os.WriteFile(dbPath, []byte(db), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := loadDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "hlr.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := serve(conn, h); err != nil {
			t.Errorf("serve: %v", err)
		}
	}()
	defer conn.Close()

	// hostapd と同じく、自分のソケットを bind してから送る
	cli, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: filepath.Join(dir, "cli.sock"), Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ask := func(req string) string {
		t.Helper()
		if _, err := cli.WriteToUnix([]byte(req), &net.UnixAddr{Name: sock, Net: "unixgram"}); err != nil {
			t.Fatal(err)
		}
		_ = cli.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 1000)
		n, err := cli.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:n])
	}

	k, _ := hex.DecodeString("5122250214c33e723a5dd523fc145fc0")
	opc, _ := hex.DecodeString("981d464c7c52eb6e5036234984ad0bcf")
	usim, _ := milenage.NewUSIM(k, opc, 0x20)

	// 正常系: USIM が受理し、RES / CK / IK が一致する
	fs := strings.Fields(ask("AKA-REQ-AUTH 555444333222111"))
	if len(fs) != 7 || fs[0] != "AKA-RESP-AUTH" {
		t.Fatalf("response = %v", fs)
	}
	rnd, _ := hex.DecodeString(fs[2])
	autn, _ := hex.DecodeString(fs[3])
	r, err := usim.Authenticate(context.Background(), rnd, autn)
	if err != nil {
		t.Fatalf("USIM: %v", err)
	}
	if hex.EncodeToString(r.IK[:]) != fs[4] || hex.EncodeToString(r.CK[:]) != fs[5] || hex.EncodeToString(r.RES) != fs[6] {
		t.Fatal("IK/CK/RES mismatch")
	}
	if autn[6] != 0xc3 || autn[7] != 0xab {
		t.Fatalf("AMF = %x", autn[6:8])
	}

	// 再同期: USIM の SQN を進めておき、AUTS を HLR に返すと次のベクタが受理される
	usim2, _ := milenage.NewUSIM(k, opc, 0x1000)
	fs = strings.Fields(ask("AKA-REQ-AUTH 555444333222111"))
	rnd, _ = hex.DecodeString(fs[2])
	autn, _ = hex.DecodeString(fs[3])
	r, err = usim2.Authenticate(context.Background(), rnd, autn)
	if !errors.Is(err, simauth.ErrResync) {
		t.Fatalf("err = %v, want resync", err)
	}
	if _, err := cli.WriteToUnix([]byte("AKA-AUTS 555444333222111 "+hex.EncodeToString(r.AUTS)+" "+fs[2]), &net.UnixAddr{Name: sock, Net: "unixgram"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	fs = strings.Fields(ask("AKA-REQ-AUTH 555444333222111"))
	rnd, _ = hex.DecodeString(fs[2])
	autn, _ = hex.DecodeString(fs[3])
	if _, err := usim2.Authenticate(context.Background(), rnd, autn); err != nil {
		t.Fatalf("after resync: %v", err)
	}

	// 未知の IMSI
	if got := ask("AKA-REQ-AUTH 001010000000000"); got != "AKA-RESP-AUTH 001010000000000 FAILURE" {
		t.Fatalf("unknown IMSI: %q", got)
	}
}
