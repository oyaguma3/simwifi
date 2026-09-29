package mbim_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oyaguma3/simwifi/internal/logging"
	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/mbim/mbimtest"
)

const testDevice = "/dev/cdc-wdm0"

func dial(t *testing.T, s *mbimtest.Server, opts mbim.Options) *mbim.Client {
	t.Helper()
	opts.ProxyAddr = s.Addr
	opts.DevicePath = testDevice
	c, err := mbim.Dial(t.Context(), opts)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

var caps = mbim.DeviceCaps{
	DeviceType:    1,
	CellularClass: 1,
	SIMClass:      2,
	DataClass:     0x3f,
	MaxSessions:   8,
	DeviceID:      "861234567890123",
	FirmwareInfo:  "EM7455 SWI9X30C_02.33.03.00",
	HardwareInfo:  "EM7455 日本語",
}

func handleCaps(s *mbimtest.Server) {
	s.Handle(mbim.ServiceBasicConnect, mbim.CIDBasicConnectDeviceCaps, func(r mbimtest.Request) mbimtest.Response {
		if r.Type != mbim.Query {
			return mbimtest.Response{Status: mbim.StatusInvalidParameters}
		}
		return mbimtest.Response{Buffer: mbim.EncodeDeviceCaps(caps)}
	})
}

func TestDialAndDeviceCaps(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts mbimtest.Options
	}{
		{"plain", mbimtest.Options{}},
		{"fragmented+indications", mbimtest.Options{FragmentSize: 40, Indications: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := mbimtest.NewServer(t, tt.opts)
			handleCaps(s)
			c := dial(t, s, mbim.Options{})
			if s.DevicePath() != testDevice {
				t.Errorf("proxy device path = %q", s.DevicePath())
			}
			got, err := c.DeviceCaps(t.Context())
			if err != nil {
				t.Fatalf("DeviceCaps: %v", err)
			}
			if got != caps {
				t.Errorf("DeviceCaps = %+v, want %+v", got, caps)
			}
		})
	}
}

func TestProxyRejected(t *testing.T) {
	s := mbimtest.NewServer(t, mbimtest.Options{RejectAll: true})
	_, err := mbim.Dial(t.Context(), mbim.Options{ProxyAddr: s.Addr, DevicePath: testDevice})
	if !errors.Is(err, mbim.ErrProxyRejected) {
		t.Fatalf("err = %v, want ErrProxyRejected", err)
	}
}

func TestProxyNotRunning(t *testing.T) {
	_, err := mbim.Dial(t.Context(), mbim.Options{ProxyAddr: "@simwifi-no-such-proxy", DevicePath: testDevice})
	if err == nil || errors.Is(err, mbim.ErrProxyRejected) {
		t.Fatalf("err = %v, want connection error", err)
	}
}

func TestTimeout(t *testing.T) {
	s := mbimtest.NewServer(t, mbimtest.Options{})
	s.Handle(mbim.ServiceAuth, mbim.CIDAuthAKA, func(mbimtest.Request) mbimtest.Response {
		return mbimtest.Response{Drop: true}
	})
	c := dial(t, s, mbim.Options{Timeout: 50 * time.Millisecond})
	start := time.Now()
	_, err := c.Query(t.Context(), mbim.ServiceAuth, mbim.CIDAuthAKA, make([]byte, 32))
	if !errors.Is(err, mbim.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout took too long")
	}
	// タイムアウト後も接続は使える
	handleCaps(s)
	if _, err := c.DeviceCaps(t.Context()); err != nil {
		t.Fatalf("after timeout: %v", err)
	}
}

func TestStatusError(t *testing.T) {
	s := mbimtest.NewServer(t, mbimtest.Options{})
	s.Handle(mbim.ServiceAuth, mbim.CIDAuthAKA, func(mbimtest.Request) mbimtest.Response {
		return mbimtest.Response{Status: mbim.StatusAuthSyncFailure, Buffer: []byte{1, 2, 3, 4}}
	})
	c := dial(t, s, mbim.Options{})

	buf, err := c.Query(t.Context(), mbim.ServiceAuth, mbim.CIDAuthAKA, nil)
	st, ok := mbim.StatusOf(err)
	if !ok || st != mbim.StatusAuthSyncFailure {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(buf, []byte{1, 2, 3, 4}) {
		t.Errorf("buffer on error = %x", buf)
	}
	if !strings.Contains(err.Error(), "auth-sync-failure") {
		t.Errorf("error text = %q", err)
	}

	// 未登録の CID はモデム非対応として返る
	_, err = c.Query(t.Context(), mbim.ServiceMSUICCLowLevelAccess, mbim.CIDUICCATR, nil)
	if st, _ := mbim.StatusOf(err); st != mbim.StatusNoDeviceSupport {
		t.Fatalf("unhandled CID: err = %v", err)
	}
}

func TestConcurrentCommands(t *testing.T) {
	s := mbimtest.NewServer(t, mbimtest.Options{FragmentSize: 64})
	handleCaps(s)
	c := dial(t, s, mbim.Options{})
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			if _, err := c.DeviceCaps(t.Context()); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := s.Count(mbim.ServiceBasicConnect, mbim.CIDBasicConnectDeviceCaps); n != 20 {
		t.Errorf("server saw %d requests", n)
	}
}

func TestClosed(t *testing.T) {
	s := mbimtest.NewServer(t, mbimtest.Options{})
	handleCaps(s)
	c := dial(t, s, mbim.Options{})
	c.Close()
	if _, err := c.DeviceCaps(t.Context()); !errors.Is(err, mbim.ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

func TestTraceDoesNotLeakSecrets(t *testing.T) {
	s := mbimtest.NewServer(t, mbimtest.Options{})
	secret := bytes.Repeat([]byte{0xab}, 66)
	s.Handle(mbim.ServiceAuth, mbim.CIDAuthAKA, func(mbimtest.Request) mbimtest.Response {
		return mbimtest.Response{Buffer: secret}
	})
	handleCaps(s)
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: logging.LevelTrace}))
	c := dial(t, s, mbim.Options{Logger: logger})

	if _, err := c.Query(t.Context(), mbim.ServiceAuth, mbim.CIDAuthAKA, bytes.Repeat([]byte{0xcd}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeviceCaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	log := out.String()
	if strings.Contains(log, "abab") || strings.Contains(log, "cdcd") {
		t.Errorf("AKA buffer leaked into trace log:\n%s", log)
	}
	if !strings.Contains(log, "buffer.len=66") {
		t.Errorf("expected length-only AKA trace:\n%s", log)
	}
	if !strings.Contains(log, "service=basic-connect") || !strings.Contains(log, "buffer=0100") {
		t.Errorf("expected hexdump for non-sensitive command:\n%s", log)
	}
}
