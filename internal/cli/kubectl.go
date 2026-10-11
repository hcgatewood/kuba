package cli

import (
	"errors"

	kubecolor "github.com/kubecolor/kubecolor/command"
	"github.com/spf13/cobra"

	"github.com/hcgatewood/kuba/internal/exit"
)

// kubectlCmd is `kuba kubectl`: kubectl with its output colored by the kubecolor built into kuba, and what handoffs run
// as kubectl unless --kubectl says otherwise.
func (a *app) kubectlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kubectl [ARGS...]",
		Short: "Run kubectl with colored output",
		Long: `Run kubectl, its output colored just as kubecolor colors it.

Kuba runs this whenever it hands off to kubectl, unless --kubectl or KUBA_KUBECTL names another command, e.g.
kubecolor. Color follows kubecolor's settings: NO_COLOR or KUBECOLOR_FORCE_COLORS=none turns it off, and
~/.kube/color.yaml sets the theme.

Every argument goes to kubectl, so kuba kubectl --help shows kubectl's own help.`,
		GroupID:               groupResources,
		DisableFlagParsing:    true,
		DisableFlagsInUseLine: true,
		RunE:                  func(_ *cobra.Command, args []string) error { return a.kubectl(args) },
	}
	// No flag sections: every argument, kuba's global flags included, goes to kubectl.
	cmd.SetUsageTemplate("Usage:\n  {{.UseLine}}\n")
	return cmd
}

// kubectl runs kubecolor on args, exiting with kubectl's code.
//
// That includes 0, as an *exit.Exited rather than nil, so a Ctrl-C that kubectl ends on by exiting 0, as port-forward
// does, ends kuba with 0 too, rather than by the signal (see exit.Run).
//
// A kubectl without a code either never started, e.g. it isn't installed, or was killed by a signal, and kubecolor drops
// which, so kuba exits 1 with an error naming both. When kuba caught the signal too, e.g. a Ctrl-C, exit.Run ends kuba
// by that signal instead.
func (a *app) kubectl(args []string) error {
	err := kubecolor.Run(args, a.version)
	kubectlErr, ok := errors.AsType[*kubecolor.KubectlError](err)
	switch {
	case err == nil:
		return &exit.Exited{Code: 0}
	case !ok:
		return err
	case kubectlErr.ExitCode < 0:
		// HACK: kubecolor's KubectlError keeps only ExitCode, -1 both for a kubectl that never started and one a signal
		// killed, so kuba can't say which, nor die by the signal. Once it keeps the *exec.ExitError, check its
		// WaitStatus here, as action.Run does.
		return errors.New("kubectl didn't start, or a signal killed it")
	default:
		return &exit.Exited{Code: kubectlErr.ExitCode}
	}
}
