package action

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/coverage"
	"strings"
	"syscall"

	"al.essio.dev/pkg/shellescape"
	"github.com/samber/lo"

	"github.com/hcgatewood/kuba/internal/exit"
)

// Action is a command, a pipeline (`a | b`), or a sequence (`a && b`).
type Action struct {
	// argv is a plain command's words; empty for a pipeline or sequence.
	argv []string
	// parts are a pipeline's or sequence's actions.
	parts []Action
	// op joins parts: "|" or "&&".
	op string
}

// Command is a plain command.
func Command(argv ...string) Action {
	return Action{argv: argv}
}

// Self names kuba itself as a command's program, e.g. in `kuba kubectl get pod`.
//
// As a plain command's program, Exec and Run take it as the running binary, so a handoff reaches this kuba whatever
// else PATH holds. Inside a pipeline or sequence, /bin/sh looks it up on PATH like any other word.
const Self = "kuba"

// Pipeline pipes each action's stdout into the next one's stdin.
func Pipeline(parts ...Action) Action {
	return Action{parts: parts, op: "|"}
}

// Sequence runs each action while the previous one succeeds.
func Sequence(parts ...Action) Action {
	return Action{parts: parts, op: "&&"}
}

// RunAll runs actions, e.g. one per cluster and namespace: a lone action execs, replacing kuba, and several go to
// RunEach.
func RunAll(ctx context.Context, actions []Action, stdout io.Writer) error {
	if len(actions) == 1 {
		return actions[0].Exec()
	}
	return RunEach(ctx, actions, stdout)
}

// RunEach runs actions in turn as child processes, with a blank line between their outputs.
//
// As with kubectl given several names, a failure doesn't stop the rest: RunEach returns the first failure once all have
// run. Canceling ctx, e.g. with Ctrl-C, skips the rest once the current one ends, returning ctx's cause.
func RunEach(ctx context.Context, actions []Action, stdout io.Writer) error {
	var first error
	for i, a := range actions {
		if i > 0 {
			_, _ = fmt.Fprintln(stdout)
		}
		if err := a.Run(); err != nil && first == nil {
			first = err
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
	}
	return first
}

// Exec replaces kuba's process with the action; it returns only on failure to start.
func (a Action) Exec() error {
	path, argv, err := a.program()
	if err != nil {
		return err
	}
	flushCoverage()
	return syscall.Exec(path, argv, os.Environ())
}

// Run runs the action as a child process, wired to kuba's stdio, and waits.
//
// Success returns nil. A failure returns the action's exit code (*exit.Exited), or the signal that killed it
// (*exit.SignalError), for kuba to die by too.
func (a Action) Run() error {
	path, argv, err := a.program()
	if err != nil {
		return err
	}
	cmd := &exec.Cmd{Path: path, Args: argv, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	err = cmd.Run()
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return err
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return &exit.SignalError{Signal: ws.Signal()}
	}
	return &exit.Exited{Code: ee.ExitCode()}
}

// String renders the action as POSIX shell text.
//
// Words made only of `[A-Za-z0-9_@%+=:,./-]` stay bare; anything else is single-quoted, as Python's shlex.quote does.
func (a Action) String() string {
	if a.argv != nil {
		return strings.Join(lo.Map(a.argv, func(w string, _ int) string { return shellescape.Quote(w) }), " ")
	}
	return strings.Join(lo.Map(a.parts, func(p Action, _ int) string { return p.String() }), " "+a.op+" ")
}

// program is the path and argv that run the action: its own words, or /bin/sh -c with its text.
func (a Action) program() (string, []string, error) {
	argv := a.argv
	if argv == nil {
		argv = []string{"/bin/sh", "-c", a.String()}
	}
	path, err := lookPath(argv[0])
	return path, argv, err
}

// lookPath finds a program on PATH, except Self, which is the running binary.
func lookPath(name string) (string, error) {
	if name == Self {
		return os.Executable()
	}
	return exec.LookPath(name)
}

// flushCoverage writes a coverage build's counters, which exec would otherwise lose: they're written at exit, and an
// exec'd process never exits as kuba.
//
// A no-op unless GOCOVERDIR is set, as only `make cover` sets it, and harmless in a build without coverage, which has
// no counters to write.
func flushCoverage() {
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		_ = coverage.WriteMetaDir(dir)
		_ = coverage.WriteCountersDir(dir)
	}
}
