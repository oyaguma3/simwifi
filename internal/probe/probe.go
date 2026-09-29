// Package probe はモデムの能力（AKA / UICC Low-Level Access の可否）を実際に MBIM で確認し、
// 結果を保存する（DESIGN §6.3）。status と connect（経路の自動選択）が結果を参照する。
package probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/simauth"
	"github.com/oyaguma3/simwifi/internal/simauth/mbimaka"
	"github.com/oyaguma3/simwifi/internal/simauth/mbimuicc"
)

// 経路の判定結果。
const (
	Supported   = "supported"
	Unsupported = "unsupported"
	Error       = "error"
)

// PathResult は 1 つの経路の確認結果。
type PathResult struct {
	State  string `json:"state"`            // supported / unsupported / error
	Status string `json:"status,omitempty"` // 返った MBIM Status（あれば）
	Detail string `json:"detail,omitempty"`
}

// UICCResult は UICC Low-Level Access の確認結果。
type UICCResult struct {
	PathResult
	ATR             string `json:"atr,omitempty"`      // hex
	ApplicationList bool   `json:"application_list"`   // APPLICATION_LIST に対応しているか
	USIMAID         string `json:"usim_aid,omitempty"` // hex（APPLICATION_LIST から得た完全 AID、または部分 AID）
}

// Result は probe の結果。
type Result struct {
	Time         time.Time  `json:"time"`
	Model        string     `json:"model,omitempty"`
	EquipmentID  string     `json:"equipment_id"`
	Device       string     `json:"device"`
	FirmwareInfo string     `json:"firmware_info,omitempty"`
	AKA          PathResult `json:"aka"`
	UICC         UICCResult `json:"uicc"`
}

// AKAUnsupported は AKA CID が非対応と明確に分かっているかを返す（connect の経路選択用）。
func (r *Result) AKAUnsupported() bool { return r != nil && r.AKA.State == Unsupported }

// NoPath は AKA と UICC のどちらも非対応と分かっているかを返す（status の Fatal 判定用）。
func (r *Result) NoPath() bool {
	return r != nil && r.AKA.State == Unsupported && r.UICC.State == Unsupported
}

// Commander は probe に必要な MBIM 操作（*mbim.Client が実装する）。
type Commander interface {
	mbimaka.Commander
	DeviceCaps(ctx context.Context) (mbim.DeviceCaps, error)
}

// Run はモデムの能力を確認する。AKA を 1 回、ダミー値で実行する（SIM の認証を 1 回使う）。
func Run(ctx context.Context, c Commander, model, equipmentID, device string) (*Result, error) {
	r := &Result{Time: time.Now().UTC().Truncate(time.Second), Model: model, EquipmentID: equipmentID, Device: device}
	caps, err := c.DeviceCaps(ctx)
	if err != nil {
		return nil, fmt.Errorf("DEVICE_CAPS: %w", err)
	}
	r.FirmwareInfo = caps.FirmwareInfo

	// AKA: RAND はランダム、AUTN は全ゼロ。USIM は MAC 検証で止まるので SQN は進まない
	rnd := make([]byte, simauth.RANDLen)
	_, _ = rand.Read(rnd)
	_, err = mbimaka.New(c, nil).Authenticate(ctx, rnd, make([]byte, simauth.AUTNLen))
	r.AKA = classify(err)

	// UICC Low-Level Access: ATR、APPLICATION_LIST、OPEN / CLOSE（AUTHENTICATE は送らない）
	r.UICC = probeUICC(ctx, c)
	return r, nil
}

func classify(err error) PathResult {
	var pr PathResult
	if st, ok := mbim.StatusOf(err); ok {
		pr.Status = st.String()
	}
	switch {
	case err == nil, errors.Is(err, simauth.ErrAuthReject), errors.Is(err, simauth.ErrResync):
		pr.State = Supported
	case errors.Is(err, simauth.ErrUnsupported):
		pr.State = Unsupported
	default:
		pr.State = Error
		pr.Detail = err.Error()
	}
	return pr
}

func probeUICC(ctx context.Context, c Commander) UICCResult {
	var u UICCResult
	atr, err := mbimuicc.ATR(ctx, c)
	if err != nil {
		u.PathResult = classify(err)
		if u.State == Supported { // ATR が成功以外で「対応」はありえない
			u.State = Error
		}
		return u
	}
	u.ATR = hex.EncodeToString(atr)
	aid := mbimuicc.PartialUSIMAID
	if apps, err := mbimuicc.ApplicationList(ctx, c); err == nil {
		u.ApplicationList = true
		for _, app := range apps {
			if app.Type == 4 { // USIM
				aid = app.AID
				break
			}
		}
	}
	u.USIMAID = hex.EncodeToString(aid)
	if err := mbimuicc.CheckChannel(ctx, c, aid); err != nil {
		u.PathResult = classify(err)
		if u.State == Supported {
			u.State = Error
		}
		return u
	}
	u.State = Supported
	return u
}

// FilePath は結果ファイルのパス（dir/probe-<EquipmentIdentifier>.json）を返す。
func FilePath(dir, equipmentID string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, equipmentID)
	if safe == "" {
		safe = "unknown"
	}
	return filepath.Join(dir, "probe-"+safe+".json")
}

// Save は結果を保存する（ファイル 0600、ディレクトリ 0700）。
func Save(dir string, r *Result) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	data, err := Marshal(r)
	if err != nil {
		return "", err
	}
	p := FilePath(dir, r.EquipmentID)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// Load は保存済みの結果を読む。無ければ (nil, nil)。
func Load(dir, equipmentID string) (*Result, error) {
	data, err := os.ReadFile(FilePath(dir, equipmentID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse probe result: %w", err)
	}
	return &r, nil
}

// Marshal は結果を整形済み JSON にする。
func Marshal(r *Result) ([]byte, error) {
	data, err := json.Marshal(r, jsontext.WithIndent("  "))
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
