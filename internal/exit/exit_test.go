package exit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runEnv makes the test binary, run again by TestRun, Ctrl-C itself inside Run, then return the named case's err.
const runEnv = "EXIT_TEST_RUN"

func TestRun(t *testing.T) {
	tests := []struct {
		name string
		// secondCtrlC Ctrl-Cs again while run winds down, which should kill kuba at once.
		secondCtrlC bool
		// err is what run returns after the Ctrl-C.
		err error
		// wantCode is kuba's exit code, or -1 if kuba should die by SIGINT.
		wantCode int
	}{
		{name: "command exited 0", err: &Exited{Code: 0}, wantCode: 0},
		{name: "command's own code", err: &Exited{Code: 3}, wantCode: 3},
		{name: "kuba succeeded anyway", err: nil, wantCode: -1},
		{name: "kuba's own code", err: &CodeError{Code: 1}, wantCode: -1},
		{name: "any other error", err: errors.New("apple"), wantCode: -1},
		{name: "second Ctrl-C", secondCtrlC: true, err: &Exited{Code: 3}, wantCode: -1},
	}
	if name := os.Getenv(runEnv); name != "" {
		for _, tt := range tests {
			if tt.name == name {
				os.Exit(Run(io.Discard, func(ctx context.Context) error {
					_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)
					<-ctx.Done()
					if tt.secondCtrlC {
						_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)
						time.Sleep(time.Second)
					}
					return tt.err
				}))
			}
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(os.Args[0], "-test.run=^TestRun$")
			cmd.Env = append(os.Environ(), runEnv+"="+tt.name)
			_ = cmd.Run()
			ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			require.True(t, ok)
			if tt.wantCode < 0 {
				require.True(t, ws.Signaled(), "exited %d", ws.ExitStatus())
				assert.Equal(t, syscall.SIGINT, ws.Signal())
			} else {
				require.False(t, ws.Signaled(), "killed by %v", ws.Signal())
				assert.Equal(t, tt.wantCode, ws.ExitStatus())
			}
		})
	}
}

func TestCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantShow bool
	}{
		{name: "success", err: nil, wantCode: 0},
		{name: "exited", err: &Exited{Code: 3}, wantCode: 3},
		{name: "exited 0", err: &Exited{Code: 0}, wantCode: 0},
		{name: "code", err: &CodeError{Code: 3}, wantCode: 3},
		{name: "signal", err: &SignalError{Signal: syscall.SIGTERM}, wantCode: 143},
		{name: "wrapped signal", err: fmt.Errorf("apple: %w", &SignalError{Signal: syscall.SIGINT}), wantCode: 130},
		{name: "canceled", err: context.Canceled, wantCode: 130},
		{name: "usage", err: Usagef("banana"), wantCode: 2, wantShow: true},
		{name: "other", err: errors.New("cherry"), wantCode: 1, wantShow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, show := Code(tt.err)
			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantShow, show)
		})
	}
}

// notifyEnv makes the test binary, run again by TestNotifyContext, send itself the named case's signal under
// notifyContext, then exit 0 if that canceled ctx with the signal as its cause, or 3 if ctx is still live.
//
// Each case gets a process of its own since signal.Reset can't undo signal.Ignore for SIGHUP, so the test binary would
// go on ignoring it, and pass that on to every process it starts after, e.g. TestRaise's.
const notifyEnv = "EXIT_TEST_NOTIFY"

func TestNotifyContext(t *testing.T) {
	tests := []struct {
		name string
		sig  syscall.Signal
		// ignored starts the signal ignored, as nohup does SIGHUP.
		ignored bool
	}{
		{name: "caught", sig: syscall.SIGTERM},
		{name: "ignored stays ignored", sig: syscall.SIGHUP, ignored: true},
	}
	if name := os.Getenv(notifyEnv); name != "" {
		for _, tt := range tests {
			if tt.name == name {
				if tt.ignored {
					signal.Ignore(tt.sig)
				}
				ctx, _ := notifyContext()
				_ = syscall.Kill(syscall.Getpid(), tt.sig)
				select {
				case <-ctx.Done():
				case <-time.After(250 * time.Millisecond):
					os.Exit(3)
				}
				if sig, ok := errors.AsType[*SignalError](context.Cause(ctx)); !ok || sig.Signal != tt.sig {
					os.Exit(1)
				}
				os.Exit(0)
			}
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(os.Args[0], "-test.run=^TestNotifyContext$")
			cmd.Env = append(os.Environ(), notifyEnv+"="+tt.name)
			_ = cmd.Run()
			ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			require.True(t, ok)
			require.False(t, ws.Signaled(), "killed by %v", ws.Signal())
			wantCode := 0
			if tt.ignored {
				wantCode = 3
			}
			assert.Equal(t, wantCode, ws.ExitStatus())
		})
	}
}

// raiseEnv makes the test binary, run again by TestRaise, raise the signal it names, then exit 3 if it's still alive.
const raiseEnv = "EXIT_TEST_RAISE"

func TestRaise(t *testing.T) {
	if s := os.Getenv(raiseEnv); s != "" {
		n, err := strconv.Atoi(s)
		require.NoError(t, err)
		raise(syscall.Signal(n))
		os.Exit(3)
	}

	tests := []struct {
		name string
		sig  syscall.Signal
		// died reports whether raise ended the process by sig, rather than returning.
		died bool
	}{
		{name: "interrupt", sig: syscall.SIGINT, died: true},
		{name: "terminate", sig: syscall.SIGTERM, died: true},
		{name: "hangup", sig: syscall.SIGHUP, died: true},
		{name: "pipe", sig: syscall.SIGPIPE},
		{name: "segfault", sig: syscall.SIGSEGV},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(os.Args[0], "-test.run=^TestRaise$")
			cmd.Env = append(os.Environ(), raiseEnv+"="+strconv.Itoa(int(tt.sig)))
			_ = cmd.Run()
			ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			require.True(t, ok)
			if tt.died {
				require.True(t, ws.Signaled(), "exited %d", ws.ExitStatus())
				assert.Equal(t, tt.sig, ws.Signal())
			} else {
				require.False(t, ws.Signaled(), "killed by %v", ws.Signal())
				assert.Equal(t, 3, ws.ExitStatus())
			}
		})
	}
}
