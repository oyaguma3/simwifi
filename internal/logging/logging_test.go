package logging

import (
	"bytes"
	"encoding/json/v2"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLevels(t *testing.T) {
	for _, tt := range []struct {
		v    int
		want slog.Level
	}{{0, slog.LevelInfo}, {1, slog.LevelDebug}, {2, LevelTrace}, {3, LevelTrace}} {
		if got := (Options{Verbosity: tt.v}).Level(); got != tt.want {
			t.Errorf("Verbosity %d: got %v, want %v", tt.v, got, tt.want)
		}
	}
}

func TestFanoutToFile(t *testing.T) {
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "sub", "simwifi.log")
	logger, closer, err := New(Options{Verbosity: 2, Stderr: &stderr, FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	logger.With("component", "mbim").Log(t.Context(), LevelTrace, "raw message", "data", Hex{0xde, 0xad})
	logger.Debug("secret", "ck", Secret(make([]byte, 16)))
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(stderr.String(), "level=TRACE") || !strings.Contains(stderr.String(), "data=dead") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "0000000000") {
		t.Error("secret leaked to stderr")
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", di.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), data)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["level"] != "TRACE" || rec["component"] != "mbim" || rec["data"] != "dead" {
		t.Errorf("record = %v", rec)
	}
	if !strings.Contains(lines[1], `"ck":{"len":16}`) {
		t.Errorf("secret record = %s", lines[1])
	}
}

func TestInfoFiltersDebug(t *testing.T) {
	var stderr bytes.Buffer
	logger, _, err := New(Options{Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("hidden")
	logger.Info("shown")
	if strings.Contains(stderr.String(), "hidden") || !strings.Contains(stderr.String(), "shown") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestMask(t *testing.T) {
	SetShowIMSI(false)
	t.Cleanup(func() { SetShowIMSI(false) })

	if got := MaskIMSI("440103123453421"); got != "44010…3421" {
		t.Errorf("MaskIMSI = %q", got)
	}
	if got := MaskIMSI("12345"); got != "…" {
		t.Errorf("short MaskIMSI = %q", got)
	}
	if got := MaskICCID("8981100012345678901"); got != "8981…8901" {
		t.Errorf("MaskICCID = %q", got)
	}
	if got := MaskNAI("0440103123453421@wlan.mnc010.mcc440.3gppnetwork.org"); got != "044010…3421@wlan.mnc010.mcc440.3gppnetwork.org" {
		t.Errorf("MaskNAI = %q", got)
	}
	if got := IMSI("440103123453421").LogValue().String(); got != "44010…3421" {
		t.Errorf("IMSI.LogValue = %q", got)
	}

	SetShowIMSI(true)
	if got := MaskIMSI("440103123453421"); got != "440103123453421" {
		t.Errorf("MaskIMSI with --log-imsi = %q", got)
	}
	if got := MaskICCID("8981100012345678901"); got != "8981…8901" {
		t.Errorf("ICCID must stay masked: %q", got)
	}
}
