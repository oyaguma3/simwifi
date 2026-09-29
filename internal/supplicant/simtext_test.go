package supplicant

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/oyaguma3/simwifi/internal/simauth"
)

func TestParseUMTSAuth(t *testing.T) {
	rand, autn, err := ParseUMTSAuth("UMTS-AUTH:00112233445566778899aabbccddeeff:ffeeddccbbaa99887766554433221100")
	if err != nil {
		t.Fatal(err)
	}
	if rand[0] != 0x00 || rand[15] != 0xff || autn[0] != 0xff || autn[15] != 0x00 {
		t.Fatalf("rand=%x autn=%x", rand, autn)
	}
	for _, bad := range []string{
		"UMTS-AUTH:0011",
		"UMTS-AUTH:00112233445566778899aabbccddeeff",
		"UMTS-AUTH:zz112233445566778899aabbccddeeff:ffeeddccbbaa99887766554433221100",
		"FOO:bar",
	} {
		if _, _, err := ParseUMTSAuth(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
	if _, _, err := ParseUMTSAuth("GSM-AUTH:00112233445566778899aabbccddeeff"); !errors.Is(err, ErrGSMAuth) {
		t.Errorf("GSM-AUTH: err = %v", err)
	}
}

func TestResponses(t *testing.T) {
	r := simauth.Result{RES: []byte{0xde, 0xad, 0xbe, 0xef}}
	r.IK[0], r.CK[0] = 0x11, 0x22
	got := string(UMTSAuthResponse(r))
	want := "UMTS-AUTH:11" + strings.Repeat("00", 15) + ":22" + strings.Repeat("00", 15) + ":deadbeef"
	if got != want {
		t.Fatalf("UMTSAuthResponse = %q", got)
	}
	if got := string(UMTSAUTSResponse(bytes.Repeat([]byte{0xab}, 14))); got != "UMTS-AUTS:"+strings.Repeat("ab", 14) {
		t.Fatalf("UMTSAUTSResponse = %q", got)
	}
	// %v / ログでは鍵素材を出さない
	if s := fmt.Sprintf("%v", UMTSAuthResponse(r)); s != "UMTS-AUTH" {
		t.Fatalf("String() = %q", s)
	}
}
