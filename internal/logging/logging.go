// Package logging は slog の設定（stderr + ファイルのファンアウト）と、
// 秘匿情報のマスクを提供する（DESIGN §11）。
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// LevelTrace は -vv で有効になる、MBIM / D-Bus の生メッセージ用のレベル。
const LevelTrace = slog.Level(-8)

// Options はロガーの設定。
type Options struct {
	Verbosity int       // 0: Info, 1: Debug, 2 以上: Trace
	Stderr    io.Writer // テキスト出力先（通常は os.Stderr）
	FilePath  string    // 空でなければ JSON でも出力する
}

// Level は Verbosity に対応するレベルを返す。
func (o Options) Level() slog.Level {
	switch {
	case o.Verbosity >= 2:
		return LevelTrace
	case o.Verbosity == 1:
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// New はロガーを作る。返す Closer でログファイルを閉じる。
func New(opts Options) (*slog.Logger, io.Closer, error) {
	hopts := &slog.HandlerOptions{Level: opts.Level(), ReplaceAttr: replaceLevel}
	handlers := []slog.Handler{slog.NewTextHandler(opts.Stderr, hopts)}
	var closer io.Closer = nopCloser{}
	if opts.FilePath != "" {
		f, err := openLogFile(opts.FilePath)
		if err != nil {
			return nil, nil, err
		}
		handlers = append(handlers, slog.NewJSONHandler(f, hopts))
		closer = f
	}
	if len(handlers) == 1 {
		return slog.New(handlers[0]), closer, nil
	}
	return slog.New(fanout(handlers)), closer, nil
}

// openLogFile はログファイルを 0600（ディレクトリは 0700）で開く。
func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	// 既存ファイルの権限が緩い場合に備えて絞る
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, fmt.Errorf("chmod log file: %w", err)
	}
	return f, nil
}

// replaceLevel は Trace レベルを "TRACE" と表示する。
func replaceLevel(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.LevelKey {
		if l, ok := a.Value.Any().(slog.Level); ok && l <= LevelTrace {
			a.Value = slog.StringValue("TRACE")
		}
	}
	return a
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// fanout は複数の Handler に同じレコードを配る。
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
