// Package nai は IMSI から EAP-AKA / EAP-AKA' の永久 ID（NAI）を生成する。
// 3GPP TS 23.003 §14 に従う（DESIGN §8）。
package nai

import (
	"errors"
	"fmt"
	"strings"
)

// Method は EAP メソッド。
type Method int

const (
	AKA      Method = iota // EAP-AKA（RFC 4187）
	AKAPrime               // EAP-AKA'（RFC 5448 / 9048）
)

// ParseMethod は CLI の --method の値（aka / akap）を解釈する。
func ParseMethod(s string) (Method, error) {
	switch s {
	case "aka":
		return AKA, nil
	case "akap":
		return AKAPrime, nil
	}
	return 0, fmt.Errorf("unknown EAP method %q (want aka or akap)", s)
}

// String は CLI 表記（aka / akap）を返す。
func (m Method) String() string {
	if m == AKAPrime {
		return "akap"
	}
	return "aka"
}

// EAPName は wpa_supplicant の eap= に書く名前を返す。
func (m Method) EAPName() string {
	if m == AKAPrime {
		return "AKA'"
	}
	return "AKA"
}

// Prefix は永久 ID の先頭文字を返す。
func (m Method) Prefix() string {
	if m == AKAPrime {
		return "6"
	}
	return "0"
}

var (
	// ErrInvalidIMSI は IMSI が 6〜15 桁の数字でないことを示す。
	ErrInvalidIMSI = errors.New("invalid IMSI")
	// ErrNoOperatorID は MCC/MNC を決められないことを示す。--realm の指定を促す。
	ErrNoOperatorID = errors.New("cannot determine MCC/MNC from SIM operator identifier; specify --realm")
)

// PLMN は MCC と MNC の組。
type PLMN struct {
	MCC string // 3 桁
	MNC string // 2 または 3 桁
}

// Realm は 3GPP 形式の realm（wlan.mncXXX.mccYYY.3gppnetwork.org）を返す。
func (p PLMN) Realm() string {
	mnc := p.MNC
	if len(mnc) == 2 {
		mnc = "0" + mnc
	}
	return "wlan.mnc" + mnc + ".mcc" + p.MCC + ".3gppnetwork.org"
}

// SplitOperatorID は MM の Sim.OperatorIdentifier（MCC+MNC）を分割する。
// 長さ 5 なら MNC 2 桁、6 なら 3 桁。IMSI の先頭と一致しなければエラー。
func SplitOperatorID(imsi, operatorID string) (PLMN, error) {
	if (len(operatorID) != 5 && len(operatorID) != 6) || !isDigits(operatorID) {
		return PLMN{}, fmt.Errorf("%w: operator identifier %q", ErrNoOperatorID, operatorID)
	}
	if !strings.HasPrefix(imsi, operatorID) {
		return PLMN{}, fmt.Errorf("%w: operator identifier %q does not match IMSI", ErrNoOperatorID, operatorID)
	}
	return PLMN{MCC: operatorID[:3], MNC: operatorID[3:]}, nil
}

// ValidateIMSI は IMSI が 6〜15 桁の数字であることを検査する。
func ValidateIMSI(imsi string) error {
	if len(imsi) < 6 || len(imsi) > 15 || !isDigits(imsi) {
		return fmt.Errorf("%w: %d characters", ErrInvalidIMSI, len(imsi))
	}
	return nil
}

// Generate は永久 ID を生成する。
// realmOverride が空でなければ @ 以降をそれで置き換え、operatorID は使わない。
func Generate(m Method, imsi, operatorID, realmOverride string) (string, error) {
	if err := ValidateIMSI(imsi); err != nil {
		return "", err
	}
	realm := realmOverride
	if realm == "" {
		p, err := SplitOperatorID(imsi, operatorID)
		if err != nil {
			return "", err
		}
		realm = p.Realm()
	} else if strings.ContainsAny(realm, "@ \t") {
		return "", fmt.Errorf("invalid realm %q", realm)
	}
	return m.Prefix() + imsi + "@" + realm, nil
}

func isDigits(s string) bool {
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
