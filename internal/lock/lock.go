// Package lock は iface ごとのロックファイル（/run/simwifi/<iface>.lock）を扱う（DESIGN §4.8）。
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// DefaultDir はロックファイルなどを置く実行時ディレクトリ。
const DefaultDir = "/run/simwifi"

// ErrLocked は別のインスタンスが同じ iface で実行中であることを示す。
var ErrLocked = errors.New("another simwifi instance is already running on this interface")

// Lock は取得済みのロック。
type Lock struct {
	f *os.File
}

// Path は dir 配下の iface 用ロックファイルのパスを返す。
func Path(dir, iface string) (string, error) {
	if iface == "" || strings.ContainsAny(iface, "/\x00") || iface == "." || iface == ".." {
		return "", fmt.Errorf("invalid interface name %q", iface)
	}
	return filepath.Join(dir, iface+".lock"), nil
}

// Acquire は flock(LOCK_EX|LOCK_NB) でロックを取る。取れなければ ErrLocked。
// ディレクトリが無ければ 0700 で作る。
func Acquire(dir, iface string) (*Lock, error) {
	path, err := Path(dir, iface)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, iface)
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	// 調査用に PID を書いておく（ロックの判定には使わない）
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f}, nil
}

// Release はロックを解放する。ファイルは消さない（消すと別プロセスとの競合が起きうるため）。
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close() // close で flock も外れる
	l.f = nil
	return err
}

// IsLocked は iface が別のプロセスにロックされているかを調べる（status 用）。
// ロックファイルもディレクトリも無ければ false。
func IsLocked(dir, iface string) (bool, error) {
	path, err := Path(dir, iface)
	if err != nil {
		return false, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	// flock は読み取り専用の fd でもかけられる
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}
