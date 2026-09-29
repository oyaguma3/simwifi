package cli

import (
	"errors"
	"fmt"
)

// Code はプロセスの終了コード（DESIGN §6.6）。
type Code int

const (
	CodeOK           Code = 0 // 成功（connect の Ctrl-C / SIGTERM による終了を含む）
	CodeError        Code = 1 // 使い方の誤り、内部エラー、wpa_supplicant の消失
	CodePrecondition Code = 2 // 前提条件 NG、ロック競合、モデム抜去
	CodeAuthFailure  Code = 3 // 認証失敗
	CodeTimeout      Code = 4 // タイムアウト
)

// ExitError は終了コードを伴うエラー。
type ExitError struct {
	Code Code
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// exitf は終了コード付きのエラーを作る。
func exitf(code Code, format string, args ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}

// withCode は err に終了コードを付ける。
func withCode(code Code, err error) error {
	if err == nil {
		return nil
	}
	return &ExitError{Code: code, Err: err}
}

// codeOf は err から終了コードを決める。
// ExitError が付いていなければ、既知のセンチネルエラーから判定し、それ以外は CodeError。
func codeOf(err error) Code {
	if err == nil {
		return CodeOK
	}
	if e, ok := errors.AsType[*ExitError](err); ok {
		return e.Code
	}
	if isPrecondition(err) {
		return CodePrecondition
	}
	return CodeError
}
