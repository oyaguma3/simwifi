package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/oyaguma3/simwifi/internal/lock"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Run(t.Context(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestVersion(t *testing.T) {
	code, out, _ := run(t, "version")
	if code != 0 || !strings.HasPrefix(out, "simwifi ") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestUsage(t *testing.T) {
	if code, _, errOut := run(t); code != 1 || !strings.Contains(errOut, "Usage:") {
		t.Fatalf("no args: code=%d stderr=%q", code, errOut)
	}
	if code, out, _ := run(t, "help"); code != 0 || !strings.Contains(out, "simwifi connect") {
		t.Fatalf("help: code=%d out=%q", code, out)
	}
	if code, _, errOut := run(t, "bogus"); code != 1 || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("unknown: code=%d stderr=%q", code, errOut)
	}
	if code, _, _ := run(t, "connect", "-h"); code != 0 {
		t.Fatalf("-h: code=%d", code)
	}
	if code, _, _ := run(t, "status", "--no-such-flag"); code != 1 {
		t.Fatalf("bad flag: code=%d", code)
	}
	if code, _, errOut := run(t, "status", "extra"); code != 1 || !strings.Contains(errOut, "unexpected arguments") {
		t.Fatalf("extra args: code=%d stderr=%q", code, errOut)
	}
}

func TestConnectValidation(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"connect"}, "--ssid is required"},
		{[]string{"connect", "--ssid", "x", "--method", "sim"}, "unknown EAP method"},
		{[]string{"connect", "--ssid", "x", "--auth-path", "pcsc"}, "--auth-path"},
		{[]string{"connect", "--ssid", "x", "--daemon"}, "--daemon is not implemented"},
		{[]string{"connect", "--ssid", "x", "--timeout", "0"}, "--timeout"},
		{[]string{"connect", "--ssid", "x", "--switch-slot"}, "--switch-slot requires --sim-slot"},
	} {
		code, _, errOut := run(t, tt.args...)
		if code != 1 || !strings.Contains(errOut, tt.want) {
			t.Errorf("%v: code=%d stderr=%q", tt.args, code, errOut)
		}
	}
}

func TestCodeOf(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want Code
	}{
		{nil, CodeOK},
		{errors.New("x"), CodeError},
		{exitf(CodeTimeout, "t"), CodeTimeout},
		{fmt.Errorf("wrap: %w", exitf(CodeAuthFailure, "a")), CodeAuthFailure},
		{fmt.Errorf("wrap: %w", lock.ErrLocked), CodePrecondition},
	} {
		if got := codeOf(tt.err); got != tt.want {
			t.Errorf("codeOf(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}
