//go:build e2e

package cli

import (
	"encoding/hex"
	"flag"
	"strconv"

	"github.com/oyaguma3/simwifi/internal/nai"
	"github.com/oyaguma3/simwifi/internal/simauth"
	"github.com/oyaguma3/simwifi/internal/simauth/milenage"
)

// e2eSuffix はバージョン表示に付ける E2E ビルドの目印。
const e2eSuffix = ", e2e build"

// e2eOptions は E2E ビルド専用のフラグ（DESIGN §6.1、D23）。
type e2eOptions struct {
	authBackend string
	imsi        string
	k           string
	opc         string
	sqn         string
}

func (o *e2eOptions) register(fs *flag.FlagSet) {
	fs.StringVar(&o.authBackend, "auth-backend", "", "E2E only: 'milenage' answers SIM requests in software")
	fs.StringVar(&o.imsi, "milenage-imsi", "", "E2E only: IMSI of the software USIM")
	fs.StringVar(&o.k, "milenage-k", "", "E2E only: K (hex)")
	fs.StringVar(&o.opc, "milenage-opc", "", "E2E only: OPc (hex)")
	fs.StringVar(&o.sqn, "milenage-sqn", "0", "E2E only: highest accepted SQN (hex)")
}

// enabled は --auth-backend milenage が指定されたかを返す。
func (o *e2eOptions) enabled() bool { return o.authBackend == "milenage" }

func (o *e2eOptions) validate() error {
	switch {
	case o.authBackend == "":
		return nil
	case o.authBackend != "milenage":
		return exitf(CodeError, "--auth-backend must be milenage")
	case o.imsi == "" || o.k == "" || o.opc == "":
		return exitf(CodeError, "--auth-backend milenage requires --milenage-imsi, --milenage-k and --milenage-opc")
	}
	return nil
}

// build はソフトウェア USIM と、その IMSI から作った NAI を返す。
// realm が空なら MNC 2 桁とみなして IMSI から realm を作る。
func (o *e2eOptions) build(m nai.Method, realm string) (simauth.Authenticator, string, error) {
	k, err1 := hex.DecodeString(o.k)
	opc, err2 := hex.DecodeString(o.opc)
	sqn, err3 := strconv.ParseUint(o.sqn, 16, 48)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, "", exitf(CodeError, "invalid --milenage-k / --milenage-opc / --milenage-sqn")
	}
	usim, err := milenage.NewUSIM(k, opc, sqn)
	if err != nil {
		return nil, "", withCode(CodeError, err)
	}
	opID := ""
	if len(o.imsi) >= 5 {
		opID = o.imsi[:5]
	}
	id, err := nai.Generate(m, o.imsi, opID, realm)
	if err != nil {
		return nil, "", withCode(CodeError, err)
	}
	return usim, id, nil
}
