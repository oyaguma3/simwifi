package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oyaguma3/simwifi/internal/simauth"
	"github.com/oyaguma3/simwifi/internal/simauth/milenage"
	"github.com/oyaguma3/simwifi/internal/supplicant"
)

// mockSup は supplicant.Supplicant のモック。
type mockSup struct {
	sims    chan supplicant.SIMRequest
	events  chan supplicant.Event
	replies chan supplicant.SIMResponse

	mu       sync.Mutex
	added    []supplicant.NetworkConfig
	selected []supplicant.NetworkID
	removed  []supplicant.NetworkID
}

func newMockSup() *mockSup {
	return &mockSup{
		sims:    make(chan supplicant.SIMRequest),
		events:  make(chan supplicant.Event),
		replies: make(chan supplicant.SIMResponse, 64),
	}
}

func (m *mockSup) Attach(context.Context, string, supplicant.AttachOptions) error { return nil }
func (m *mockSup) AddNetwork(_ context.Context, cfg supplicant.NetworkConfig) (supplicant.NetworkID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.added = append(m.added, cfg)
	return "/net/0", nil
}
func (m *mockSup) SelectNetwork(_ context.Context, id supplicant.NetworkID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.selected = append(m.selected, id)
	return nil
}
func (m *mockSup) RemoveNetwork(_ context.Context, id supplicant.NetworkID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removed = append(m.removed, id)
	return nil
}
func (m *mockSup) Disconnect(context.Context) error { return nil }
func (m *mockSup) SIMRequests(context.Context) (<-chan supplicant.SIMRequest, error) {
	return m.sims, nil
}
func (m *mockSup) ReplySIM(_ context.Context, _ supplicant.SIMRequest, r supplicant.SIMResponse) error {
	m.replies <- r
	return nil
}
func (m *mockSup) Events(context.Context) (<-chan supplicant.Event, error) { return m.events, nil }
func (m *mockSup) Capabilities(context.Context) (supplicant.Capabilities, error) {
	return supplicant.Capabilities{}, nil
}
func (m *mockSup) Close(context.Context) error { return nil }

func (m *mockSup) removedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.removed)
}

// hookLog はフックの実行記録。
type hookLog struct {
	mu    sync.Mutex
	calls []string
}

func (h *hookLog) run(_ context.Context, cmd string, env []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	ev := ""
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "SIMWIFI_EVENT="); ok {
			ev = v
		}
	}
	h.calls = append(h.calls, ev+":"+cmd)
	return nil
}

func (h *hookLog) get() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

var (
	tK   = mustHexT("5122250214c33e723a5dd523fc145fc0")
	tOPc = mustHexT("981d464c7c52eb6e5036234984ad0bcf")
)

func mustHexT(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

type harness struct {
	t      *testing.T
	sup    *mockSup
	hss    *milenage.Milenage
	hooks  *hookLog
	s      *session
	cancel context.CancelFunc
	done   chan error
	logs   *bytes.Buffer
	gone   chan struct{}
	sqn    uint64
}

func newHarness(t *testing.T, auth simauth.Authenticator, timeout time.Duration) *harness {
	t.Helper()
	hss, err := milenage.New(tK, tOPc)
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil {
		usim, err := milenage.NewUSIM(tK, tOPc, 0x100)
		if err != nil {
			t.Fatal(err)
		}
		auth = usim
	}
	h := &harness{t: t, sup: newMockSup(), hss: hss, hooks: &hookLog{}, logs: &bytes.Buffer{}, gone: make(chan struct{}), sqn: 0x100}
	log := slog.New(slog.NewTextHandler(&lockedWriter{w: h.logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.s = &session{
		log:             log,
		sup:             h.sup,
		auth:            auth,
		network:         supplicant.NetworkConfig{SSID: "test", EAP: "AKA", Identity: "0440103123453421@wlan.mnc010.mcc440.3gppnetwork.org"},
		timeout:         timeout,
		maxAuthFailures: 3,
		hooks:           newHookRunner("up-cmd", "down-cmd", []string{"SIMWIFI_IFACE=wlan0"}, log, h.hooks.run),
		modemGone:       h.gone,
	}
	return h
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(h.t.Context())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- h.s.run(ctx) }()
}

func (h *harness) wait() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("session did not finish")
	}
	return nil
}

func (h *harness) event(e supplicant.Event) {
	h.t.Helper()
	select {
	case h.sup.events <- e:
	case <-time.After(5 * time.Second):
		h.t.Fatal("event not consumed")
	}
}

// challenge は網側で次の SQN の RAND / AUTN を作り、SIM 要求を送って応答を返す。
func (h *harness) challenge(sqn uint64, tamper bool) (supplicant.SIMResponse, milenage.Vector) {
	h.t.Helper()
	rand := bytes.Repeat([]byte{byte(sqn)}, 16)
	autn, v := h.hss.GenerateAUTN(rand, sqn, [2]byte{0x80, 0})
	if tamper {
		autn[15] ^= 1
	}
	return h.request("UMTS-AUTH:" + hex.EncodeToString(rand) + ":" + hex.EncodeToString(autn[:])), v
}

func (h *harness) request(text string) supplicant.SIMResponse {
	h.t.Helper()
	select {
	case h.sup.sims <- supplicant.SIMRequest{Network: "/net/0", Text: text}:
	case <-time.After(5 * time.Second):
		h.t.Fatal("SIM request not consumed")
	}
	select {
	case r := <-h.sup.replies:
		return r
	case <-time.After(5 * time.Second):
		h.t.Fatal("no reply")
	}
	return ""
}

func exitCode(err error) Code { return codeOf(err) }

func TestSessionSuccessAndSignal(t *testing.T) {
	h := newHarness(t, nil, 5*time.Second)
	h.start()
	h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "started"})
	resp, v := h.challenge(0x101, false)
	want := supplicant.UMTSAuthResponse(simauth.Result{RES: v.RES[:], CK: v.CK, IK: v.IK})
	if resp != want {
		t.Fatalf("reply = %q, want %q", string(resp), string(want))
	}
	h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "completion", EAPParameter: "success"})
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "completed"})
	h.cancel() // SIGINT
	if err := h.wait(); err != nil {
		t.Fatalf("err = %v, want nil (exit 0)", err)
	}
	if got := h.hooks.get(); !slices.Equal(got, []string{"up:up-cmd", "down:down-cmd"}) {
		t.Errorf("hooks = %v", got)
	}
	if h.sup.removedCount() != 1 {
		t.Error("network not removed on exit")
	}
	if strings.Contains(h.logs.String(), hex.EncodeToString(v.CK[:])) {
		t.Error("CK leaked into logs")
	}
}

func TestSessionTimeout(t *testing.T) {
	h := newHarness(t, nil, 100*time.Millisecond)
	h.start()
	if err := h.wait(); exitCode(err) != CodeTimeout {
		t.Fatalf("err = %v, want timeout", err)
	}
	if h.sup.removedCount() != 1 {
		t.Error("network not removed")
	}
}

func TestSessionTimeoutStopsAfterConnect(t *testing.T) {
	h := newHarness(t, nil, 150*time.Millisecond)
	h.start()
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "completed"})
	time.Sleep(300 * time.Millisecond)
	h.cancel()
	if err := h.wait(); err != nil {
		t.Fatalf("err = %v, want nil (timeout must not fire after connecting)", err)
	}
}

func TestSessionResyncLimit(t *testing.T) {
	h := newHarness(t, nil, 5*time.Second)
	h.start()
	h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "started"})
	for i := range maxResync {
		if r, _ := h.challenge(0x50, false); !strings.HasPrefix(string(r), "UMTS-AUTS:") {
			t.Fatalf("#%d reply = %v", i, r)
		}
	}
	// 新しい EAP セッションで数え直し
	h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "started"})
	for range maxResync {
		h.challenge(0x50, false)
	}
	h.challenge(0x50, false) // 33 回目
	if err := h.wait(); exitCode(err) != CodeAuthFailure {
		t.Fatalf("err = %v, want auth failure", err)
	}
}

func TestSessionAuthFailures(t *testing.T) {
	h := newHarness(t, nil, 5*time.Second)
	h.start()
	for i := range 3 {
		h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "started"})
		if r, _ := h.challenge(0x200+uint64(i), true); r != supplicant.UMTSFail {
			t.Fatalf("reply = %v, want UMTS-FAIL", r)
		}
		if i < 2 {
			// UMTS-FAIL の後の EAP-Failure は同じセッションなので二重に数えない
			h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "completion", EAPParameter: "failure"})
		}
	}
	if err := h.wait(); exitCode(err) != CodeAuthFailure {
		t.Fatalf("err = %v, want auth failure", err)
	}
}

func TestSessionFailureCounterResetsOnConnect(t *testing.T) {
	h := newHarness(t, nil, 5*time.Second)
	h.start()
	for range 2 {
		h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "started"})
		h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "completion", EAPParameter: "failure"})
	}
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "completed"})
	for range 2 {
		h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "started"})
		h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "completion", EAPParameter: "failure"})
	}
	h.cancel()
	if err := h.wait(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestSessionGSMAndMalformed(t *testing.T) {
	h := newHarness(t, nil, 5*time.Second)
	h.start()
	h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "started"})
	if r := h.request("GSM-AUTH:00112233445566778899aabbccddeeff"); r != supplicant.GSMFail {
		t.Fatalf("GSM reply = %v", r)
	}
	if r := h.request("UMTS-AUTH:zz"); r != supplicant.UMTSFail {
		t.Fatalf("malformed reply = %v", r)
	}
	h.cancel()
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}

type errAuth struct{ err error }

func (e errAuth) Name() string { return "err" }
func (e errAuth) Authenticate(context.Context, []byte, []byte) (simauth.Result, error) {
	return simauth.Result{}, e.err
}

func TestSessionLocalError(t *testing.T) {
	h := newHarness(t, errAuth{errors.New("mbim command timed out")}, 5*time.Second)
	h.start()
	h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "started"})
	if r, _ := h.challenge(0x300, false); r != supplicant.UMTSFail {
		t.Fatalf("reply = %v, want UMTS-FAIL", r)
	}
	h.cancel()
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionModemRemoved(t *testing.T) {
	h := newHarness(t, nil, 5*time.Second)
	h.start()
	close(h.gone)
	if err := h.wait(); exitCode(err) != CodePrecondition {
		t.Fatalf("err = %v, want precondition", err)
	}
	if h.sup.removedCount() != 1 {
		t.Error("network not removed")
	}
}

func TestSessionSupplicantGone(t *testing.T) {
	h := newHarness(t, nil, 5*time.Second)
	h.start()
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "completed"})
	h.event(supplicant.Event{Kind: supplicant.EventGone})
	if err := h.wait(); exitCode(err) != CodeError {
		t.Fatalf("err = %v, want error", err)
	}
	if h.sup.removedCount() != 0 {
		t.Error("must not call RemoveNetwork after wpa_supplicant is gone")
	}
	if got := h.hooks.get(); !slices.Equal(got, []string{"up:up-cmd", "down:down-cmd"}) {
		t.Errorf("hooks = %v", got)
	}
}

func TestSessionReconnectHooks(t *testing.T) {
	h := newHarness(t, nil, 5*time.Second)
	h.start()
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "completed"})
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "disconnected"})
	h.event(supplicant.Event{Kind: supplicant.EventDisconnectReason, Reason: 3})
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "associating"})
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "completed"})
	h.cancel()
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
	want := []string{"up:up-cmd", "down:down-cmd", "up:up-cmd", "down:down-cmd"}
	if got := h.hooks.get(); !slices.Equal(got, want) {
		t.Errorf("hooks = %v, want %v", got, want)
	}
}

func TestSessionReauthKeepsUp(t *testing.T) {
	h := newHarness(t, nil, 5*time.Second)
	h.start()
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "completed"})
	// 再認証と鍵交換（実機の EAP-AKA' 再認証で観測した遷移）
	h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "started"})
	h.challenge(0x101, false)
	h.event(supplicant.Event{Kind: supplicant.EventEAP, EAPStatus: "completion", EAPParameter: "success"})
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "4way_handshake"})
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "completed"})
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "group_handshake"})
	h.event(supplicant.Event{Kind: supplicant.EventState, State: "completed"})
	h.cancel()
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
	if got := h.hooks.get(); !slices.Equal(got, []string{"up:up-cmd", "down:down-cmd"}) {
		t.Errorf("hooks = %v, want a single up/down pair", got)
	}
}
