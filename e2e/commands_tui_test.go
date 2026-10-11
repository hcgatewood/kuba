package e2e

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/rogpeppe/go-internal/testscript"
)

// The terminal tui runs commands on, how long it waits on one, and how long a screen holds still to count as drawn.
const (
	tuiCols    = 100
	tuiRows    = 16
	tuiTimeout = 10 * time.Second
	tuiSettle  = 50 * time.Millisecond
)

// cmdTui drives a terminal UI, like the fzf built into the Go kuba: it runs a command on a pseudo-terminal whose screen
// a terminal emulator keeps, so a script can type into it and check what it draws.
//
// Usage:
//
//	tui start CMD ARGS...  start CMD on a fresh terminal, one at a time
//	tui wait REGEX         wait for the screen to match REGEX, e.g. a prompt before typing into it
//	tui type TEXT...       type TEXT, its words joined by spaces
//	tui key KEY...         press keys, named as fzf names them: enter, tab, btab, esc, space, bspace, up, down, ctrl-c
//	tui screen             print the screen once it holds still, for stdout and cmp
//	tui exit               wait for CMD to exit 0, then print what it left on the screen
//	! tui exit             the same, but CMD must exit non-zero; then `exitcode N` checks which code
//
// Example:
//
//	tui start kuba get pod --select -o name
//	tui wait '3/3'
//	tui key btab btab
//	tui wait '\(2\)'
//	tui key enter
//	tui exit
//	cmp stdout want-two.txt
//
// Notes:
//   - screen and exit print plain, normalized text: as kuba's output is, with colors dropped, ages as <age>, column
//     padding shrunk to two spaces, and so on, and without the empty rows above and below what CMD drew.
//   - exit prints the screen CMD left: whatever it drew last, which for kuba is the names it printed once its picker
//     cleared away.
//   - Before typing, tui wait for CMD's prompt: until CMD switches the terminal to raw mode, which hands it each key as
//     it's pressed, the terminal does its own line editing, so earlier keys can be echoed, held back until Enter, or
//     lost.
//   - After a key, tui wait for what it does before tui screen: CMD takes a moment to redraw, and screen can't tell a
//     finished screen from one that hasn't reacted yet.
func cmdTui(ts *testscript.TestScript, neg bool, args []string) {
	const usage = "usage: tui start CMD ARGS... | wait REGEX | type TEXT... | key KEY... | screen | [!] exit"
	if len(args) == 0 || (neg && args[0] != "exit") {
		ts.Fatalf(usage)
	}

	st := state(ts)
	verb, rest := args[0], args[1:]
	switch {
	case verb == "start" && st.tui != nil:
		ts.Fatalf("a tui is running already; tui exit first")
	case verb != "start" && st.tui == nil:
		ts.Fatalf("no tui running; tui start first")
	}
	// Like exec, every verb replaces the output `stdout` checks:
	//	start            nothing (clearing the `env -0` output scriptEnv reads, see below)
	//	wait, type, key  nothing
	//	screen, exit     the screen
	stdout := ts.Stdout()

	switch {
	case verb == "start" && len(rest) > 0:
		s, err := startTui(ts, rest)
		ts.Check(err)
		ts.Defer(s.kill) // even if the script ends without tui exit, CMD can't outlive it
		st.tui = s
	case verb == "wait" && len(rest) == 1:
		re, err := regexp.Compile("(?m)" + rest[0])
		ts.Check(err)
		ts.Check(st.tui.waitFor(re, st.normalizer(false)))
	case verb == "type" && len(rest) > 0:
		ts.Check(st.tui.send(strings.Join(rest, " ")))
	case verb == "key" && len(rest) > 0:
		var keys strings.Builder
		for _, name := range rest {
			b, ok := tuiKeys[name]
			if !ok {
				ts.Fatalf("unknown key %q", name)
			}
			keys.WriteString(b)
		}
		ts.Check(st.tui.send(keys.String()))
	case verb == "screen" && len(rest) == 0:
		_, _ = fmt.Fprint(stdout, st.tui.settledScreen(st.normalizer(false)))
	case verb == "exit" && len(rest) == 0:
		code, err := st.tui.wait()
		ts.Check(err)
		st.lastExit = code
		_, _ = fmt.Fprint(stdout, st.tui.settledScreen(st.normalizer(false)))
		st.tui = nil
		switch {
		case code != 0 && !neg:
			ts.Fatalf("tui exited %d", code)
		case code == 0 && neg:
			ts.Fatalf("tui unexpectedly succeeded")
		}
	default:
		ts.Fatalf(usage)
	}
}

// tuiSession is a command running on a pseudo-terminal, with a terminal emulator keeping its screen.
type tuiSession struct {
	// cmd is the command running on the slave side of the pseudo-terminal.
	cmd *exec.Cmd
	// pty is the master side of the pseudo-terminal.
	pty *os.File
	// vt is the terminal emulator, which keeps the screen in memory and answers the command's queries.
	vt *vt.SafeEmulator
	// exited closes once the command exits.
	exited chan struct{}
	// exitErr is the command's exit error, nil if it exited 0.
	exitErr error
	// drained closes once the emulator has read everything the command wrote, i.e. the terminal closed.
	drained chan struct{}
}

// startTui starts argv on a fresh terminal, with the script's environment, directory, and PATH.
//
// The emulator answers the command's queries, like fzf's for the cursor position, as a real terminal would.
func startTui(ts *testscript.TestScript, argv []string) (*tuiSession, error) {
	// Create a cmd with the executable's resolved path, the script's environment, and its working directory
	env, err := scriptEnv(ts)
	if err != nil {
		return nil, err
	}
	path, err := scriptLookPath(ts, argv[0])
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Dir: ts.MkAbs(".")}

	// Create a pseudo-terminal, start the command on its slave side, and return the master side
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: tuiCols, Rows: tuiRows})
	if err != nil {
		return nil, err
	}

	// Create a terminal emulator
	v := vt.NewSafeEmulator(tuiCols, tuiRows)

	// Track everything as a session
	s := &tuiSession{
		cmd:     cmd,
		pty:     f,
		vt:      v,
		exited:  make(chan struct{}),
		drained: make(chan struct{}),
	}
	// Mark the session as exited when the command exits
	go func() {
		defer close(s.exited)
		s.exitErr = cmd.Wait()
	}()
	// Finally, connect the emulator to the master side of the pseudo-terminal
	go s.read()
	go s.reply()

	return s, nil
}

// read feeds what the command prints to the emulator, until the terminal closes.
//
// The terminal closes once the command and anything it started have exited, or kill closes it.
func (s *tuiSession) read() {
	defer close(s.drained)
	_, _ = io.Copy(s.vt, s.pty)
}

// reply types the emulator's answers to the command's queries into the terminal, until kill closes the emulator.
//
// Ex: fzf prints ESC[6n to ask where the cursor is, and the emulator answers ESC[5;1R (row 5, column 1).
//
// The emulator holds read until something takes each answer, so without reply the first query would freeze the screen
// and leave the command waiting forever.
func (s *tuiSession) reply() {
	_, _ = io.Copy(s.pty, s.vt)
}

// send types text into the terminal.
func (s *tuiSession) send(text string) error {
	_, err := s.pty.WriteString(text)
	return err
}

// waitFor waits for the normalized screen to match re, failing early if the command exits first.
func (s *tuiSession) waitFor(re *regexp.Regexp, n normalizer) error {
	deadline := time.Now().Add(tuiTimeout)
	for {
		screen := s.screen(n)
		if re.MatchString(screen) {
			return nil
		}
		select {
		case <-s.drained:
			if screen = s.screen(n); re.MatchString(screen) {
				return nil
			}
			return fmt.Errorf("%s exited before the screen matched %s:\n%s", s.cmd.Args[0], re, screen)
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("screen didn't match %s in %s:\n%s", re, tuiTimeout, screen)
		}
	}
}

// settledScreen returns the screen once it has held still for tuiSettle, so it's never caught mid-redraw.
//
// Holding still doesn't mean the command has caught up: one that hasn't reacted to the last key yet holds still too.
// Ex: just after `tui key down`, fzf may not have moved its cursor. tui wait, which knows what the screen should show,
// keeps polling until it does.
func (s *tuiSession) settledScreen(n normalizer) string {
	prev := s.screen(n)
	for deadline := time.Now().Add(tuiTimeout); time.Now().Before(deadline); {
		time.Sleep(tuiSettle)
		cur := s.screen(n)
		if cur == prev {
			return cur
		}
		prev = cur
	}
	return prev
}

// screen returns the screen's text normalized by n, minus the blank rows above and below.
//
// The emulator renders it with its colors, which n drops, as it does from kuba's output.
func (s *tuiSession) screen(n normalizer) string {
	return strings.Trim(n.apply(s.vt.Render()), "\n") + "\n"
}

// wait waits for the command to exit and the emulator to read all it wrote, returning the command's exit code.
func (s *tuiSession) wait() (int, error) {
	select {
	case <-s.exited:
	case <-time.After(tuiTimeout):
		return -1, fmt.Errorf("%s still running after %s:\n%s", s.cmd.Args[0], tuiTimeout, s.screen(normalizer{}))
	}
	select {
	case <-s.drained:
	case <-time.After(tuiTimeout):
		return -1, fmt.Errorf("%s exited but its terminal stayed open after %s", s.cmd.Args[0], tuiTimeout)
	}
	return exitCode(s.exitErr)
}

// kill stops the command if it's still running and closes the terminal and the emulator.
func (s *tuiSession) kill() {
	_ = s.cmd.Process.Kill()
	<-s.exited
	_ = s.vt.Close()
	_ = s.pty.Close()
}

// tuiKeys are the bytes a terminal sends for each key tui key can press, named as fzf names them.
//
// Holding ctrl while pressing a letter sends a single byte whose value is the letter's position in the alphabet: ctrl-a
// is \x01, ctrl-c is \x03. Add keys as scripts need them.
var tuiKeys = map[string]string{
	"enter":  "\r",
	"tab":    "\t",
	"btab":   "\x1b[Z",
	"esc":    "\x1b",
	"space":  " ",
	"bspace": "\x7f",
	"up":     "\x1b[A",
	"down":   "\x1b[B",
	"right":  "\x1b[C",
	"left":   "\x1b[D",
	"ctrl-c": "\x03",
}

// scriptEnv returns the script's environment variables, as NAME=VALUE pairs, for tui start to hand its command.
//
// Each script has its own environment: the harness's setup gives it HOME, PATH, KUBECONFIG, NS, and so on, and its
// `env NAME=VALUE` lines add to or change them. exec runs its command with that environment. tui start can't use exec,
// since its command has to run on a pseudo-terminal (fake terminal the harness types into and reads the screen of),
// so it starts the process itself and has to pass the environment along.
//
// testscript only looks up a variable whose name you already know (ts.Getenv); it has no way to list them all. So this
// runs `env -0` through ts.Exec, which, like exec, gets the script's environment and prints it. -0 ends each pair with
// a NUL byte rather than a newline, so a value with a newline in it survives the split.
func scriptEnv(ts *testscript.TestScript) ([]string, error) {
	if err := ts.Exec("env", "-0"); err != nil {
		return nil, fmt.Errorf("env: %w", err)
	}
	return strings.Split(strings.TrimSuffix(ts.ReadFile("stdout"), "\x00"), "\x00"), nil
}

// scriptLookPath finds the executable name on the script's PATH, as exec would.
func scriptLookPath(ts *testscript.TestScript, name string) (string, error) {
	if strings.Contains(name, "/") {
		return ts.MkAbs(name), nil
	}
	for _, dir := range filepath.SplitList(ts.Getenv("PATH")) {
		path := filepath.Join(dir, name)
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s: not found on the script's PATH", name)
}
