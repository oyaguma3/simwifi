// Package supplicant は wpa_supplicant への操作を抽象化する（DESIGN §4.1）。
// D-Bus 実装は wpadbus パッケージ。
package supplicant

import (
	"context"
	"errors"
)

// NetworkID は wpa_supplicant のネットワークの識別子（D-Bus ではオブジェクトパス）。
type NetworkID string

// NetworkConfig は AddNetwork に渡す設定。
type NetworkConfig struct {
	SSID     string
	EAP      string // "AKA" または "AKA'"
	Identity string // 永久 ID（NAI）
	WPA3     bool   // WPA-EAP-SHA256 + PMF 必須
}

// AttachOptions は Attach の設定。
type AttachOptions struct {
	// ConfigPath は simwifi が書く Interface 用設定ファイル（external_sim=1）。
	ConfigPath string
	// Driver は wpa_supplicant のドライバ名。既定は nl80211。
	Driver string
}

// SIMRequest は wpa_supplicant からの SIM 要求（NetworkRequest("SIM", text)）。
type SIMRequest struct {
	Network NetworkID
	Text    string // 例: UMTS-AUTH:<RAND>:<AUTN>
}

// SIMResponse は NetworkReply("SIM", value) に渡す値。
type SIMResponse string

// EventKind はイベントの種類。
type EventKind int

const (
	EventState            EventKind = iota // State プロパティの変化
	EventEAP                               // EAP シグナル
	EventDisconnectReason                  // DisconnectReason プロパティの変化
	EventGone                              // wpa_supplicant がバスから消えた
)

// Event は wpa_supplicant の状態変化。
type Event struct {
	Kind         EventKind
	State        string // EventState: "completed", "disconnected" など
	EAPStatus    string // EventEAP: "started", "completion" など
	EAPParameter string // EventEAP: "success", "failure" など
	Reason       int32  // EventDisconnectReason
}

// Capabilities は wpa_supplicant の能力。
type Capabilities struct {
	EAPMethods []string // ルートオブジェクトの EapMethods
	Global     []string // ルートオブジェクトの Capabilities（"interworking" など）
	KeyMgmt    []string // Interface の Capabilities.KeyMgmt（Attach 後のみ）
}

// InterfaceInfo は既存 Interface の情報（status 用）。
type InterfaceInfo struct {
	Exists     bool
	ConfigFile string
	State      string
}

// Supplicant は wpa_supplicant の操作。
type Supplicant interface {
	// Attach は iface の Interface を作る（simwifi の残骸なら回収して作り直す）。
	// 他者の Interface が既にあれば ErrInterfaceBusy。
	Attach(ctx context.Context, iface string, opts AttachOptions) error
	AddNetwork(ctx context.Context, cfg NetworkConfig) (NetworkID, error)
	SelectNetwork(ctx context.Context, id NetworkID) error
	RemoveNetwork(ctx context.Context, id NetworkID) error
	Disconnect(ctx context.Context) error
	// SIMRequests は SIM 要求のチャネルを返す。Attach 後に使う。
	SIMRequests(ctx context.Context) (<-chan SIMRequest, error)
	ReplySIM(ctx context.Context, req SIMRequest, resp SIMResponse) error
	// Events は状態変化のチャネルを返す。Attach 後に使う。
	Events(ctx context.Context) (<-chan Event, error)
	Capabilities(ctx context.Context) (Capabilities, error)
	// Close は Attach で自分が作った Interface を削除し、購読をやめる。
	Close(ctx context.Context) error
}

var (
	// ErrNotRunning は wpa_supplicant が D-Bus に居ない（activation も失敗した）ことを示す。
	ErrNotRunning = errors.New("wpa_supplicant is not available on D-Bus (enable wpa_supplicant.service)")
	// ErrInterfaceBusy は他者が作った Interface が既にあることを示す。
	ErrInterfaceBusy = errors.New("wireless interface is already managed by another wpa_supplicant client (NetworkManager?)")
	// ErrNotAttached は Attach 前の操作を示す。
	ErrNotAttached = errors.New("wpa_supplicant interface not attached")
)
