package exit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
)

// Run calls run to do kuba's work, then returns the code kuba should exit with, or ends kuba by a signal instead.
//
// A stop signal, e.g. Ctrl-C, doesn't kill kuba outright: it cancels run's ctx, so run can wind down and return. Once
// run returns, the first of these that applies decides how kuba ends:
//   - a command kuba ran exited (Exited), even one that outlived the signal, e.g. kubectl logs -f exits 1 on Ctrl-C:
//     exit with its code, 0 included, as if the command had run directly
//   - kuba caught a signal, or one killed a command kuba ran: die by it (see endedBy and raise)
//   - run succeeded: exit 0
//   - otherwise: exit with the code Code maps run's error to, printing the error if Code says to
func Run(stderr io.Writer, run func(ctx context.Context) error) int {
	ctx, stop := notifyContext()
	defer stop()

	err := run(ctx)
	err = endedBy(ctx, err)

	if sig, ok := errors.AsType[*SignalError](err); ok {
		raise(sig.Signal)
	}

	code, show := Code(err)
	if show {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
	}
	return code
}

// Interrupted is the code for a canceled fzf, which is what fzf itself exits with on a cancel.
//
// It's 128+SIGINT, the code shells report for a Ctrl-C, as a cancel means the same "stop", and it keeps a cancel apart
// from a failure, which exits 1. Kuba exits with it rather than dying by SIGINT since no signal ever arrives: fzf
// reads Ctrl-C as a key press, like Esc.
const Interrupted = 130

// Code maps an error to kuba's exit code, and reports whether kuba should print the error.
func Code(err error) (code int, show bool) {
	if err == nil {
		return 0, false
	}

	if exited, ok := errors.AsType[*Exited](err); ok {
		return exited.Code, false
	}
	if coded, ok := errors.AsType[*CodeError](err); ok {
		return coded.Code, false
	}
	if signaled, ok := errors.AsType[*SignalError](err); ok {
		// Shells report death by signal n as 128+n, so exiting with it reads the same, for when raise can't end kuba
		// by the signal itself
		return 128 + int(signaled.Signal), false
	}
	if errors.Is(err, context.Canceled) {
		return Interrupted, false
	}
	if _, ok := errors.AsType[*UsageError](err); ok {
		return 2, true
	}
	return 1, true
}

// Exited is a command kuba ran exiting with Code, which kuba exits with too, as if the command had run directly.
//
// It travels as an error only to come back through run, so Code 0 is a success like any other exit 0. It still differs
// from nil, which is kuba's own success: after a caught signal, a command's exit stands, as the command chose it, while
// kuba's own success doesn't (see endedBy).
type Exited struct {
	Code int
}

func (e *Exited) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// CodeError exits with a code of kuba's own and no message, e.g. when kuba already printed why. A command's code is an
// Exited instead.
type CodeError struct {
	Code int
}

func (e *CodeError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// SignalError is a run ended by a signal: one kuba caught, e.g. a Ctrl-C, or one that killed the command kuba ran.
type SignalError struct {
	Signal syscall.Signal
}

func (e *SignalError) Error() string { return e.Signal.String() }

// Usagef returns a usage error, which exits 2.
func Usagef(format string, args ...any) error {
	return &UsageError{Err: fmt.Errorf(format, args...)}
}

// UsageError is a command line that can't work whatever the clusters hold, e.g. an unknown flag or conflicting flags.
type UsageError struct {
	Err error
}

func (e *UsageError) Error() string { return e.Err.Error() }

func (e *UsageError) Unwrap() error { return e.Err }

// endedBy returns why the run ended: run's err, or the stop signal kuba caught.
//
// With no signal caught, return err directly (nil included, as the common case).
//
// With a signal caught, which of the two to return depends on err:
//   - err is a command's exit code (*Exited), 0 included: return err, as the command, not the signal, decided how the
//     run ended, e.g. kubectl logs -f catches Ctrl-C and exits 1, and port-forward exits 0
//   - err is anything else, nil included: return the caught signal, so kuba dies by it
//
// An error in that second case is almost always the signal in disguise, i.e. a failure the signal itself caused:
//   - From kuba kubectl, "kubectl didn't start, or a signal killed it": the Ctrl-C reached kubectl as well as kuba, and
//     killed kubectl, as most kubectl commands (e.g. a plain get) don't catch Ctrl-C
//   - context.Canceled, often wrapped, as in `cluster apple: get pods banana: Get "https://...": context canceled`: an
//     API lookup that the canceled ctx cut short
//
// Rarely, it's a real failure that just happened to coincide with the signal, e.g. a cluster that was unreachable
// anyway; the signal still wins then, as stopping is what was asked for.
//
// And nil there means kuba's own work finished anyway (a command's exit 0 is an *Exited instead), but the signal still
// wins: exiting 0 after one would tell a shell kuba handled it on purpose, so a bash loop over kuba would go on.
func endedBy(ctx context.Context, err error) error {
	sig, caught := errors.AsType[*SignalError](context.Cause(ctx)) // via notifyContext
	_, exited := errors.AsType[*Exited](err)
	// No signal caught, just return err (may be nil)
	if !caught {
		return err
	}
	// Signal caught, but a command's exit code decided how the run ended: return that exit code
	if exited {
		return err
	}
	// Signal caught, and no command's exit code decided how the run ended: return the signal, so kuba dies by it
	return sig
}

// notifyContext returns a context that the first of stopSignals to arrive cancels, with a *SignalError cause.
//
// It's signal.NotifyContext for stopSignals, except that the cause carries the signal itself rather than just its
// name, and it leaves ignored signals ignored.
//
// Catching the signals turns off their default action, killing kuba, but only until the first of them arrives, which
// cancels ctx instead. That restores the default for all of them, so any second signal, e.g. another Ctrl-C, kills kuba
// at once, should winding down hang.
//
// If/when whoever started kuba set a signal to be ignored, e.g. nohup ignoring SIGHUP so kuba outlives its terminal,
// kuba leaves it ignored rather than catching it, as signal.Notify alone would. That keeps it ignored for the commands
// kuba runs too: they inherit an ignored signal, but not a caught one.
func notifyContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(context.Background())
	ch := make(chan os.Signal, 1)
	// One Notify per signal, not one for the filtered list: if that came up empty, Notify would catch every signal,
	// even a terminal resize.
	for s := range stopSignals {
		if !signal.Ignored(s) {
			signal.Notify(ch, s)
		}
	}
	go func() {
		select {
		case s := <-ch:
			signal.Stop(ch)
			cancel(&SignalError{Signal: s.(syscall.Signal)})
		case <-ctx.Done():
		}
	}()
	return ctx, func() { signal.Stop(ch); cancel(nil) }
}

// stopSignals are the signals that ask kuba to stop, which Run catches to wind down first, and raise re-raises.
//
// They're also the only signals raise can end kuba by: uncaught, Go's runtime dies by exactly these, printing
// nothing. Go ignores most others sent with kill, e.g. SIGPIPE and SIGUSR1, and takes the rest as crashes, e.g.
// SIGSEGV and SIGQUIT, dumping every goroutine's stack and exiting 2.
//
// So SIGQUIT (Ctrl-\) stays out, though kubectl catches it: raise couldn't end kuba by it, and its stack dump shows
// where a hung kuba is stuck.
var stopSignals = sets.New(
	// SIGHUP: the terminal went away, e.g. its window closed
	syscall.SIGHUP,
	// SIGINT: Ctrl-C
	syscall.SIGINT,
	// SIGTERM: a request to stop, e.g. kill's default, or Kubernetes shutting a pod down
	syscall.SIGTERM,
)

// raise ends kuba by sig, as if kuba had never caught it, so its parent can tell a signal ended the run.
//
// Shells act on the difference, which exit codes can't show: a bash loop stops on Ctrl-C only when the command it ran
// died of SIGINT, and goes on when it exited 130, taking that as a Ctrl-C the command handled.
//
// It re-raises only stopSignals, and only ones kuba wasn't started ignoring (see notifyContext). For any other it
// returns, and Run exits 128+sig, which is what a shell would report had kuba died of it.
func raise(sig syscall.Signal) {
	if signal.Ignored(sig) || !stopSignals.Has(sig) {
		return
	}
	signal.Reset(sig)
	_ = syscall.Kill(syscall.Getpid(), sig)
	// The signal lands on whichever thread the kernel picks, so wait for it there, as the runtime's own dieFromSignal
	// does. Returning is a backstop that shouldn't happen.
	time.Sleep(time.Second)
}
