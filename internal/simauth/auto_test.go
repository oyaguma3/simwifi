package simauth

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type fakeAuth struct {
	name  string
	err   error
	calls int
	rand  []byte
}

func (f *fakeAuth) Name() string { return f.name }

func (f *fakeAuth) Authenticate(_ context.Context, rand, _ []byte) (Result, error) {
	f.calls++
	f.rand = rand
	if f.err != nil {
		return Result{}, f.err
	}
	return Result{RES: []byte{1, 2, 3, 4}}, nil
}

func TestAutoFallback(t *testing.T) {
	primary := &fakeAuth{name: "aka", err: ErrUnsupported}
	fallback := &fakeAuth{name: "uicc"}
	a := NewAuto(primary, fallback, false, nil)
	rand := bytes.Repeat([]byte{7}, 16)

	if _, err := a.Authenticate(t.Context(), rand, make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 1 || fallback.calls != 1 || !bytes.Equal(fallback.rand, rand) {
		t.Fatalf("calls primary=%d fallback=%d", primary.calls, fallback.calls)
	}
	if a.Name() != "uicc" {
		t.Fatalf("Name = %q", a.Name())
	}
	// 以後は fallback のみ
	if _, err := a.Authenticate(t.Context(), rand, make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 1 || fallback.calls != 2 {
		t.Fatalf("calls primary=%d fallback=%d", primary.calls, fallback.calls)
	}
}

func TestAutoNoFallbackOnOtherErrors(t *testing.T) {
	for _, e := range []error{ErrAuthReject, ErrResync, errors.New("busy")} {
		primary := &fakeAuth{name: "aka", err: e}
		fallback := &fakeAuth{name: "uicc"}
		a := NewAuto(primary, fallback, false, nil)
		if _, err := a.Authenticate(t.Context(), make([]byte, 16), make([]byte, 16)); !errors.Is(err, e) {
			t.Fatalf("err = %v, want %v", err, e)
		}
		if fallback.calls != 0 || a.Name() != "aka" {
			t.Fatalf("%v: must not switch", e)
		}
	}
}

func TestAutoStartWithFallback(t *testing.T) {
	primary := &fakeAuth{name: "aka"}
	fallback := &fakeAuth{name: "uicc", err: ErrUnsupported}
	a := NewAuto(primary, fallback, true, nil)
	if _, err := a.Authenticate(t.Context(), make([]byte, 16), make([]byte, 16)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if primary.calls != 0 {
		t.Fatal("primary must not be used")
	}
}

func TestResultClearAndLog(t *testing.T) {
	r := Result{RES: []byte{1, 2, 3, 4}, AUTS: []byte{9}}
	r.CK[0], r.IK[0] = 1, 1
	if got := r.LogValue().String(); !bytes.Contains([]byte(got), []byte("res_len=4")) {
		t.Errorf("LogValue = %q", got)
	}
	res := r.RES
	r.Clear()
	if res[0] != 0 || r.CK[0] != 0 || r.IK[0] != 0 || r.RES != nil || r.AUTS != nil {
		t.Fatal("Clear did not zero")
	}
}
