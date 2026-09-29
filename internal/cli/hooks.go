package cli

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

// hookTimeout は --exec-up / --exec-down の上限（DESIGN §6.5）。
const hookTimeout = 30 * time.Second

// hookRunner は --exec-up / --exec-down を専用の goroutine で順番に実行する。
// イベントループ（SIM 要求への応答）を止めないため。
type hookRunner struct {
	up, down string
	env      []string // SIMWIFI_IFACE など
	log      *slog.Logger
	run      func(ctx context.Context, cmd string, env []string) error

	queue chan string // 実行する event（"up" / "down"）
	wg    sync.WaitGroup
	once  sync.Once
}

// newHookRunner は hookRunner を起動する。run が nil なら /bin/sh -c で実行する。
func newHookRunner(up, down string, env []string, log *slog.Logger, run func(context.Context, string, []string) error) *hookRunner {
	if run == nil {
		run = runShell
	}
	h := &hookRunner{up: up, down: down, env: env, log: log, run: run, queue: make(chan string, 16)}
	h.wg.Go(h.loop)
	return h
}

func (h *hookRunner) loop() {
	for event := range h.queue {
		cmd := h.up
		if event == "down" {
			cmd = h.down
		}
		if cmd == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
		h.log.Info("running hook", "event", event, "command", cmd)
		err := h.run(ctx, cmd, append(h.env, "SIMWIFI_EVENT="+event))
		cancel()
		if err != nil {
			h.log.Error("hook failed", "event", event, "error", err)
		}
	}
}

// fire は event のフックを実行待ちに入れる。
func (h *hookRunner) fire(event string) {
	select {
	case h.queue <- event:
	default:
		h.log.Warn("hook queue full; dropping", "event", event)
	}
}

// wait は待ち中のフックをすべて実行し終えるまで待つ。以後 fire は呼べない。
func (h *hookRunner) wait() {
	h.once.Do(func() { close(h.queue) })
	h.wg.Wait()
}

// runShell は /bin/sh -c でコマンドを実行する。
func runShell(ctx context.Context, cmd string, env []string) error {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	c.Env = append(os.Environ(), env...)
	c.Stdout = os.Stderr // 標準出力は結果データ用に空けておく
	c.Stderr = os.Stderr
	c.WaitDelay = 5 * time.Second
	return c.Run()
}
