package mbim

import (
	"errors"
	"fmt"
	"uuid"
)

// Status は COMMAND_DONE の Status フィールド（MBIM 1.0 §9.4.5 と MS 拡張）。
type Status uint32

const (
	StatusSuccess                 Status = 0
	StatusBusy                    Status = 1
	StatusFailure                 Status = 2
	StatusSIMNotInserted          Status = 3
	StatusBadSIM                  Status = 4
	StatusPINRequired             Status = 5
	StatusNoDeviceSupport         Status = 9
	StatusNotInitialized          Status = 14
	StatusInvalidParameters       Status = 21
	StatusOperationNotAllowed     Status = 28
	StatusAuthIncorrectAUTN       Status = 35
	StatusAuthSyncFailure         Status = 36
	StatusAuthAMFNotSet           Status = 37
	StatusMSNoLogicalChannels     Status = 0x87430001
	StatusMSSelectFailed          Status = 0x87430002
	StatusMSInvalidLogicalChannel Status = 0x87430003
)

var statusNames = map[Status]string{
	StatusSuccess:                 "success",
	StatusBusy:                    "busy",
	StatusFailure:                 "failure",
	StatusSIMNotInserted:          "sim-not-inserted",
	StatusBadSIM:                  "bad-sim",
	StatusPINRequired:             "pin-required",
	6:                             "pin-disabled",
	7:                             "not-registered",
	8:                             "providers-not-found",
	StatusNoDeviceSupport:         "no-device-support",
	10:                            "provider-not-visible",
	11:                            "data-class-not-available",
	12:                            "packet-service-detached",
	13:                            "max-activated-contexts",
	StatusNotInitialized:          "not-initialized",
	15:                            "voice-call-in-progress",
	16:                            "context-not-activated",
	17:                            "service-not-activated",
	18:                            "invalid-access-string",
	19:                            "invalid-user-name-pwd",
	20:                            "radio-power-off",
	StatusInvalidParameters:       "invalid-parameters",
	22:                            "read-failure",
	23:                            "write-failure",
	25:                            "no-phonebook",
	26:                            "parameter-too-long",
	27:                            "stk-busy",
	StatusOperationNotAllowed:     "operation-not-allowed",
	29:                            "memory-failure",
	30:                            "invalid-memory-index",
	31:                            "memory-full",
	32:                            "filter-not-supported",
	33:                            "dss-instance-limit",
	34:                            "invalid-device-service-operation",
	StatusAuthIncorrectAUTN:       "auth-incorrect-autn",
	StatusAuthSyncFailure:         "auth-sync-failure",
	StatusAuthAMFNotSet:           "auth-amf-not-set",
	38:                            "context-not-supported",
	StatusMSNoLogicalChannels:     "ms-no-logical-channels",
	StatusMSSelectFailed:          "ms-select-failed",
	StatusMSInvalidLogicalChannel: "ms-invalid-logical-channel",
}

func (s Status) String() string {
	if n, ok := statusNames[s]; ok {
		return n
	}
	return fmt.Sprintf("status(0x%08x)", uint32(s))
}

// StatusError は COMMAND_DONE の Status が成功でないことを示す。
type StatusError struct {
	Service uuid.UUID
	CID     uint32
	Status  Status
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("mbim %s cid %d: %s", ServiceName(e.Service), e.CID, e.Status)
}

// StatusOf は err が StatusError ならその Status を返す。
func StatusOf(err error) (Status, bool) {
	if e, ok := errors.AsType[*StatusError](err); ok {
		return e.Status, true
	}
	return 0, false
}

// ProtocolError は FUNCTION_ERROR_MSG の ErrorStatusCode。
type ProtocolError uint32

// FunctionError は FUNCTION_ERROR_MSG を受け取ったことを示す。
type FunctionError struct {
	Code ProtocolError
}

func (e *FunctionError) Error() string {
	names := []string{"invalid", "timeout-fragment", "fragment-out-of-sequence", "length-mismatch",
		"duplicated-tid", "not-opened", "unknown", "cancel", "max-transfer"}
	if int(e.Code) < len(names) {
		return "mbim function error: " + names[e.Code]
	}
	return fmt.Sprintf("mbim function error: 0x%08x", uint32(e.Code))
}

var (
	// ErrProxyRejected は mbim-proxy が接続直後に切断したことを示す。
	// 既定ビルドの mbim-proxy は root 以外を応答なしで切断する（DESIGN §16.1）。
	ErrProxyRejected = errors.New("mbim-proxy closed the connection immediately (simwifi must run as root)")
	// ErrClosed はクライアントが閉じられた後の操作を示す。
	ErrClosed = errors.New("mbim client closed")
)
