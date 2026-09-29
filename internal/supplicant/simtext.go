package supplicant

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/oyaguma3/simwifi/internal/simauth"
)

// SIM 要求と応答の文字列形式（wpa_supplicant eap_aka.c。DESIGN §7.2、§16.3）。

// ErrGSMAuth は EAP-SIM の GSM-AUTH 要求（スコープ外）を示す。
var ErrGSMAuth = errors.New("GSM-AUTH (EAP-SIM) is not supported")

// ParseUMTSAuth は "UMTS-AUTH:<RAND hex 32>:<AUTN hex 32>" を解析する。
func ParseUMTSAuth(text string) (rand, autn []byte, err error) {
	if strings.HasPrefix(text, "GSM-AUTH:") {
		return nil, nil, ErrGSMAuth
	}
	body, ok := strings.CutPrefix(text, "UMTS-AUTH:")
	if !ok {
		return nil, nil, fmt.Errorf("unknown SIM request %q", truncate(text))
	}
	r, a, ok := strings.Cut(body, ":")
	if !ok {
		return nil, nil, fmt.Errorf("malformed UMTS-AUTH request")
	}
	rand, err1 := hex.DecodeString(r)
	autn, err2 := hex.DecodeString(a)
	if err1 != nil || err2 != nil || len(rand) != simauth.RANDLen || len(autn) != simauth.AUTNLen {
		return nil, nil, fmt.Errorf("malformed UMTS-AUTH request")
	}
	return rand, autn, nil
}

func truncate(s string) string {
	if len(s) > 16 {
		return s[:16] + "…"
	}
	return s
}

// UMTSAuthResponse は "UMTS-AUTH:<IK>:<CK>:<RES>" を作る（IK が先、CK が後）。
func UMTSAuthResponse(r simauth.Result) SIMResponse {
	return SIMResponse("UMTS-AUTH:" + hex.EncodeToString(r.IK[:]) + ":" + hex.EncodeToString(r.CK[:]) + ":" + hex.EncodeToString(r.RES))
}

// UMTSAUTSResponse は "UMTS-AUTS:<AUTS>" を作る。
func UMTSAUTSResponse(auts []byte) SIMResponse {
	return SIMResponse("UMTS-AUTS:" + hex.EncodeToString(auts))
}

const (
	// UMTSFail は AUTN 不正・ローカルエラー時の応答。wpa_supplicant は Authentication-Reject を送る。
	UMTSFail SIMResponse = "UMTS-FAIL"
	// GSMFail は GSM-AUTH 要求への応答（EAP-SIM は非対応）。
	GSMFail SIMResponse = "GSM-FAIL"
)

// String は種別（UMTS-AUTH など）だけを返す。%v やログで鍵素材を出さないため。
// D-Bus に渡すときは string(r) を使う。
func (r SIMResponse) String() string {
	kind, _, _ := strings.Cut(string(r), ":")
	return kind
}
