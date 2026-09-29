package lock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireRelease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")

	if locked, err := IsLocked(dir, "wlan0"); err != nil || locked {
		t.Fatalf("IsLocked before = %v, %v", locked, err)
	}

	l, err := Acquire(dir, "wlan0")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", fi.Mode().Perm())
	}

	// flock はファイル記述ごとなので、同一プロセスの別 open でも競合する
	if _, err := Acquire(dir, "wlan0"); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire err = %v, want ErrLocked", err)
	}
	if locked, err := IsLocked(dir, "wlan0"); err != nil || !locked {
		t.Fatalf("IsLocked while held = %v, %v", locked, err)
	}

	// 別の iface は独立
	l2, err := Acquire(dir, "wlan1")
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Release()

	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal("double release must be a no-op")
	}
	if locked, err := IsLocked(dir, "wlan0"); err != nil || locked {
		t.Fatalf("IsLocked after = %v, %v", locked, err)
	}
	l3, err := Acquire(dir, "wlan0")
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	l3.Release()
}

func TestInvalidIface(t *testing.T) {
	for _, name := range []string{"", "../x", "a/b", ".."} {
		if _, err := Acquire(t.TempDir(), name); err == nil {
			t.Errorf("Acquire(%q): expected error", name)
		}
	}
}
