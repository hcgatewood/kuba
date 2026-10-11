package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// versionCmd is `kuba version`.
func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Print kuba's version",
		Long:    "Print kuba's version, e.g. kuba v1.0.0.",
		GroupID: groupSetup,
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprintf(a.stdout, "kuba %s\n", a.version)
			return err
		},
	}
}

// resolveVersion is kuba's version, from the first of these that has one.
//
//   - Link time (-X main.version): builds from a source tarball, which has no git to read, as homebrew-core and other
//     packagers build.
//   - Build info: go install, and any build from a git checkout, releases included.
//   - Neither: dev.
func resolveVersion(linked string) string {
	if linked != "" && linked != "dev" {
		return linked
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
