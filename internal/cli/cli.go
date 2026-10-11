package cli

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/hcgatewood/kuba/internal/exit"
	"github.com/hcgatewood/kuba/internal/flagspec"
)

// Main runs kuba on its args (without the program name), returning the exit code.
func Main(args []string, version string) int {
	a := &app{
		version:     resolveVersion(version),
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		isStdoutTTY: term.IsTerminal(int(os.Stdout.Fd())),
		isStderrTTY: term.IsTerminal(int(os.Stderr.Fd())),
	}
	return exit.Run(a.stderr, func(ctx context.Context) error { return a.run(ctx, args) })
}

// app is one run of kuba: its stdio and version.
type app struct {
	// version is kuba's version, e.g. v1.0.0 or dev.
	version string

	stdout      io.Writer
	stderr      io.Writer
	isStdoutTTY bool
	isStderrTTY bool
}

// run dispatches args to a command.
func (a *app) run(ctx context.Context, args []string) error {
	root := a.root(ctx)
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// Help groups, in the order help lists them.
const (
	// groupResources holds the commands that act on cluster resources, e.g. get and kubectl.
	groupResources = "resources"
	// groupSetup holds the commands about kuba itself rather than a cluster, e.g. version.
	groupSetup = "setup"
)

// root builds the command tree.
func (a *app) root(ctx context.Context) *cobra.Command {
	root := &cobra.Command{
		Use:   "kuba",
		Short: "The magical kubectl companion",
		Long: `The magical kubectl companion.

Use kuba like kubectl, with fuzzy names, fzf pickers, and lookups across namespaces and clusters.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       a.version,
	}

	root.SetOut(a.stdout)
	root.SetErr(a.stderr)

	registerFlags(root.PersistentFlags(), flagspec.Root)
	// Before the version flag, so kubectl's -v (log level) claims -v, and cobra's --version goes without it
	registerKubectlGlobals(root.Flags())

	// Hide --version since we already have a version command; but it still works and is equivalent to `kuba version`
	root.SetVersionTemplate("kuba {{.Version}}\n")
	root.InitDefaultVersionFlag()
	_ = root.Flags().MarkHidden("version")

	// Off until flagspec drives completion, since cobra's alone can't complete TYPEs, NAMEs, or kubectl's flags.
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddGroup(
		&cobra.Group{ID: groupResources, Title: "Resources:"},
		&cobra.Group{ID: groupSetup, Title: "Setup:"},
	)
	root.SetHelpCommandGroupID(groupSetup)

	root.AddCommand(
		a.getCmd(ctx),
		a.kubectlCmd(),
		a.versionCmd(),
	)

	return root
}

// registerFlags puts kuba's visible flags on a cobra flag set, mostly for help, as flagspec parses each command's argv.
//
// Cobra still reads them in two places: to find the command name past root's flags (see registerKubectlGlobals), and
// to parse the flags of commands that don't disable flag parsing, e.g. `kuba version -h`.
//
// Root's flags go on root as persistent flags, and each command gets only its local ones, so help lists the shared ones
// apart, as Global Flags. A command's flags keep its spec's order, which groups related ones.
func registerFlags(fs *pflag.FlagSet, flags []flagspec.Flag) {
	fs.SortFlags = false
	for _, f := range flags {
		// Skip hidden, skip non-kuba (e.g. kubectl), since they don't belong in this help text
		if f.Hidden || f.Role != flagspec.Kuba {
			continue
		}
		f.AddTo(fs)
	}
}

// registerKubectlGlobals puts kubectl's global flags on root, hidden, primarily so cobra can tell which word is the
// command.
//
// Without them, cobra treats an unknown flag as taking a value, so in `kuba --insecure-skip-tls-verify get pod` it
// would think `get` is the flag's value, and `pod` the command.
func registerKubectlGlobals(fs *pflag.FlagSet) {
	for _, f := range flagspec.KubectlGlobals() {
		f.AddTo(fs)
		_ = fs.MarkHidden(f.Name)
	}
}

// newDebugLogger is kuba's only logger, for --debug: it logs to stderr when debug is on, and discards otherwise.
//
// Warnings and errors skip it, going straight to stderr, so they show without --debug.
//
// Lines drop slog's timestamp, the one part that changes every run, so two runs' logs diff to just their real
// differences. Little is lost: the timing that matters is how long each API call took, which request lines log as
// their latency.
func (a *app) newDebugLogger(debugOn bool) *slog.Logger {
	if !debugOn {
		return slog.New(slog.DiscardHandler)
	}

	return slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		// Drops the timestamp and nothing else
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) == 0 && attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	}))
}
