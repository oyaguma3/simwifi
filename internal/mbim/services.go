// Package mbim は mbim-proxy 経由でモデムに MBIM コマンドを送る最小限のクライアント（DESIGN §4.2）。
package mbim

import (
	"fmt"
	"uuid"
)

// サービス UUID（libmbim mbim-uuid.c と照合済み。ワイヤ上もこのバイト順）。
var (
	ServiceBasicConnect         = uuid.MustParse("a289cc33-bcbb-8b4f-b6b0-133ec2aae6df")
	ServiceAuth                 = uuid.MustParse("1d2b5ff7-0aa1-48b2-aa52-50f15767174e")
	ServiceProxyControl         = uuid.MustParse("838cf7fb-8d0d-4d7f-871e-d71dbefbb39b")
	ServiceMSUICCLowLevelAccess = uuid.MustParse("c2f6588e-f037-4bc9-8665-f4d44bd09367")
)

// CID 定数。
const (
	CIDBasicConnectDeviceCaps uint32 = 1

	CIDAuthAKA  uint32 = 1
	CIDAuthAKAP uint32 = 2
	CIDAuthSIM  uint32 = 3

	CIDProxyControlConfiguration uint32 = 1
	CIDProxyControlVersion       uint32 = 2

	CIDUICCATR             uint32 = 1
	CIDUICCOpenChannel     uint32 = 2
	CIDUICCCloseChannel    uint32 = 3
	CIDUICCAPDU            uint32 = 4
	CIDUICCApplicationList uint32 = 7
)

// ServiceName はログ用のサービス名を返す。
func ServiceName(s uuid.UUID) string {
	switch s {
	case ServiceBasicConnect:
		return "basic-connect"
	case ServiceAuth:
		return "auth"
	case ServiceProxyControl:
		return "proxy-control"
	case ServiceMSUICCLowLevelAccess:
		return "ms-uicc-low-level-access"
	}
	return s.String()
}

// sensitive は情報バッファに鍵素材を含みうるコマンドかを返す。
// これらは Trace でも hexdump せず、長さだけを出す（DESIGN §11）。
func sensitive(s uuid.UUID, cid uint32) bool {
	switch s {
	case ServiceAuth:
		return true
	case ServiceMSUICCLowLevelAccess:
		return cid == CIDUICCAPDU
	}
	return false
}

// CommandType は COMMAND_MSG の種別。
type CommandType uint32

const (
	Query CommandType = 0
	Set   CommandType = 1
)

func (t CommandType) String() string {
	switch t {
	case Query:
		return "query"
	case Set:
		return "set"
	}
	return fmt.Sprintf("command-type(%d)", uint32(t))
}
