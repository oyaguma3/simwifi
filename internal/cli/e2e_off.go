//go:build !e2e

package cli

import (
	"errors"
	"flag"

	"github.com/oyaguma3/simwifi/internal/nai"
	"github.com/oyaguma3/simwifi/internal/simauth"
)

// e2eSuffix はバージョン表示に付ける E2E ビルドの目印。
const e2eSuffix = ""

// e2eOptions は E2E ビルド専用のフラグ。通常ビルドでは空。
type e2eOptions struct{}

func (*e2eOptions) register(*flag.FlagSet) {}

func (*e2eOptions) validate() error { return nil }

// enabled は --auth-backend milenage が指定されたかを返す。通常ビルドでは常に false。
func (*e2eOptions) enabled() bool { return false }

func (*e2eOptions) build(nai.Method, string) (simauth.Authenticator, string, error) {
	return nil, "", errors.New("not an E2E build")
}
