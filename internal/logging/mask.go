package logging

import (
	"encoding/hex"
	"log/slog"
	"strings"
	"sync/atomic"
)

// showIMSI は --log-imsi の指定。プロセス全体で 1 つの設定。
var showIMSI atomic.Bool

// SetShowIMSI は IMSI をマスクせずに出すかどうかを設定する。
func SetShowIMSI(v bool) { showIMSI.Store(v) }

// MaskIMSI は IMSI の先頭 5 桁と末尾 4 桁だけを残す（例: 44010…3421）。
// --log-imsi が指定されていればそのまま返す。
func MaskIMSI(imsi string) string {
	if showIMSI.Load() {
		return imsi
	}
	return maskMiddle(imsi, 5, 4)
}

// MaskICCID は ICCID の先頭 4 桁と末尾 4 桁だけを残す。
func MaskICCID(iccid string) string {
	return maskMiddle(iccid, 4, 4)
}

// MaskNAI は NAI の IMSI 部分をマスクする（先頭のプレフィックス 1 文字と realm は残す）。
func MaskNAI(nai string) string {
	local, realm, ok := strings.Cut(nai, "@")
	if len(local) < 2 {
		return nai
	}
	masked := local[:1] + MaskIMSI(local[1:])
	if !ok {
		return masked
	}
	return masked + "@" + realm
}

func maskMiddle(s string, head, tail int) string {
	if len(s) <= head+tail {
		return "…"
	}
	return s[:head] + "…" + s[len(s)-tail:]
}

// IMSI はログ出力時にマスクされる IMSI。
type IMSI string

// LogValue は slog.LogValuer を実装する。
func (v IMSI) LogValue() slog.Value { return slog.StringValue(MaskIMSI(string(v))) }

// NAI はログ出力時にマスクされる NAI。
type NAI string

// LogValue は slog.LogValuer を実装する。
func (v NAI) LogValue() slog.Value { return slog.StringValue(MaskNAI(string(v))) }

// Secret は鍵素材。ログには長さだけを出す。
type Secret []byte

// LogValue は slog.LogValuer を実装する。
func (s Secret) LogValue() slog.Value {
	return slog.GroupValue(slog.Int("len", len(s)))
}

// Hex はログ出力時に hex 文字列になるバイト列（秘密でない値用）。
type Hex []byte

// LogValue は slog.LogValuer を実装する。
func (h Hex) LogValue() slog.Value { return slog.StringValue(hex.EncodeToString(h)) }
