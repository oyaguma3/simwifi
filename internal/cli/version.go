package cli

import (
	"context"
	"flag"
	"fmt"
	"runtime"
	"runtime/debug"
)

// version はリリース時に -ldflags "-X .../internal/cli.version=..." で埋め込む。
var version = "dev"

var versionCommand = command{
	name:    "version",
	summary: "Print version information.",
	usage:   "version",
	setup: func(_ *flag.FlagSet, _ *commonFlags) func(context.Context, *Env) error {
		return func(_ context.Context, env *Env) error {
			fmt.Fprintln(env.Stdout, versionString())
			return nil
		}
	},
}

func versionString() string {
	v := version
	rev := ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				rev = s.Value[:12]
			}
		}
	}
	s := fmt.Sprintf("simwifi %s (%s %s/%s", v, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if rev != "" {
		s += ", " + rev
	}
	return s + e2eSuffix + ")"
}
