package e2e

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/rogpeppe/go-internal/testscript"
)

// Files under $WORK: what shellenv generated, for shell and defines to source, and the newest history entry a zsh run
// left, for history to check.
const (
	shellenvFile = ".shell/env"
	historyFile  = ".shell/history"
)

// cmdShellenv runs `kuba shellenv ARGS` and saves its output to a file under $WORK, for later `shell` and `defines`
// commands to source.
//
// Usage: shellenv ARGS...
//
// Example:
//
//	env SHELL=/bin/zsh
//	shellenv --kubectl kubectl-rec
//	shell 'kl apple-1'
//	history '^kubectl-rec logs apple-1'
//
// As for a user, SHELL decides the shell: kuba writes zsh code when it names zsh and POSIX shell code otherwise, and
// `shell` and `defines` run the shell it names.
//
// The output never reaches stdout: scripts check what the defined names do, not the generated text, and a failure log
// would print all of it.
func cmdShellenv(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("usage: shellenv ARGS...")
	}

	path := workPath(ts, shellenvFile)
	ts.Check(os.MkdirAll(filepath.Dir(path), 0o755))

	code := fmt.Sprintf("exec kuba shellenv %s >%s", shellescape.QuoteCommand(args), shellescape.Quote(path))
	if err := ts.Exec("sh", "-c", code); err != nil {
		ts.Fatalf("kuba shellenv: %v\n%s", err, ts.ReadFile("stderr"))
	}
}

// cmdShell runs a command line in $SHELL, having sourced the last shellenv's output, normalizing its output as kuba
// does.
//
// Usage: [!] shell CMDLINE
//
// Example:
//
//	shell kpn
//	rows apple-1 apple-2
//
// A zsh run also leaves its newest history entry for history to check.
func cmdShell(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: [!] shell CMDLINE")
	}
	sh := scriptShell(ts)

	hist := workPath(ts, historyFile)
	if err := os.Remove(hist); err != nil && !errors.Is(err, fs.ErrNotExist) {
		ts.Fatalf("%v", err)
	}

	// eval, so CMDLINE's aliases expand: see shellSpec.args.
	body := fmt.Sprintf("eval %s", shellescape.Quote(args[0]))
	if sh.name == "zsh" {
		// The redirect creates the file even when fc finds no entry, which is how `! history` tells "none" from "no run".
		body = fmt.Sprintf("%s; rc=$?; fc -ln -1 >%s 2>/dev/null; exit $rc", body, shellescape.Quote(hist))
	}
	runNormalized(ts, neg, false, sh.path, sh.args(ts, body)...)
}

// cmdHistory checks the newest history entry the last `shell` left, run in zsh.
//
// Usage:
//
//	history REGEX    the entry matches REGEX
//	! history REGEX  there's no entry, or it doesn't match REGEX
//	history          there's an entry
//	! history        there's no entry
//
// Example:
//
//	env SHELL=/bin/zsh
//	shellenv
//	shell 'kssh fig'
//	history '^ssh -J jump fig$'
//
// Kuba's zsh functions add the command they run to history (`print -s`), so you can rerun it. After its command line,
// `shell` in zsh saves the newest entry (`fc -ln -1`) to a file under $WORK, which history reads: an empty file means
// no entry, and no file means the last shell run wasn't zsh.
func cmdHistory(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) > 1 {
		ts.Fatalf("usage: [!] history [REGEX]")
	}

	data, err := os.ReadFile(workPath(ts, historyFile))
	if errors.Is(err, fs.ErrNotExist) {
		ts.Fatalf("no history to check; the last shell run wasn't zsh")
	}
	ts.Check(err)
	entry := strings.TrimRight(string(data), "\n")

	if len(args) == 0 {
		switch {
		case entry != "" && neg:
			ts.Fatalf("history has %q, want no entry", entry)
		case entry == "" && !neg:
			ts.Fatalf("history has no entry")
		}
		return
	}
	re, err := regexp.Compile(args[0])
	ts.Check(err)

	switch matched := re.MatchString(entry); {
	case matched && neg:
		ts.Fatalf("history entry %q matches %s", entry, args[0])
	case !matched && !neg:
		ts.Fatalf("history entry %q doesn't match %s", entry, args[0])
	}
}

// cmdDefines asserts the last shellenv's output, sourced in $SHELL, defines every NAME as an alias or a function; with
// !, that it defines none of them.
//
// Usage: [!] defines NAME...
//
// Example:
//
//	shellenv --no-containers
//	defines kpn kns
//	! defines ktn kl
func cmdDefines(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) == 0 {
		ts.Fatalf("usage: [!] defines NAME...")
	}
	sh := scriptShell(ts)

	body := fmt.Sprintf(`for n in %s; do %s && echo "$n"; done; true`, shellescape.QuoteCommand(args), sh.isDefined)
	if err := ts.Exec(sh.path, sh.args(ts, body)...); err != nil {
		ts.Fatalf("%s: %v\n%s", sh.name, err, ts.ReadFile("stderr"))
	}
	defined := strings.Fields(ts.ReadFile("stdout"))

	for _, n := range args {
		switch has := slices.Contains(defined, n); {
		case has && neg:
			ts.Fatalf("%s defines %s", sh.name, n)
		case !has && !neg:
			ts.Fatalf("%s doesn't define %s", sh.name, n)
		}
	}
}

// shells for the shell and defines commands, by the file name SHELL gives (SHELL=/bin/zsh is zsh).
//
// Each runs non-interactively and reads none of your startup files, so nothing outside the script shapes what it sees.
// Each also has its own way to ask whether a name is an alias or a function, rather than a builtin or a command on
// PATH; where it takes `--`, that keeps a name from reading as an option.
var shells = map[string]shellSpec{
	"zsh": {
		// Even a non-interactive zsh reads ~/.zshenv; -f skips every startup file but /etc/zshenv.
		flags: []string{"-f"},
		// shellenv's zsh output registers completions with compdef, which only exists once compinit has loaded zsh's
		// completion system, at ~250ms a shell. Scripts check what the defined names do, never their completion, so a
		// compdef that does nothing (`:` is the no-op command) stands in to save execution time.
		setup: "compdef() { :; }",
		// whence -w prints "NAME: KIND", KIND being alias, function, builtin, command, and so on. Inside [[ ]], ==
		// matches a pattern, where (a|b) is either.
		isDefined: `[[ $(whence -w -- "$n") == *": "(alias|function) ]]`,
	},
	"bash": {
		// Kuba's POSIX output defines aliases, which bash expands only in an interactive shell, not under `bash -c`.
		// POSIX mode turns expansion on, since POSIX requires aliases to work in scripts too. It doesn't make bash
		// reject its own extensions, like [[ and the function keyword; sourcing the output in dash catches those.
		flags: []string{"--posix"},
		// type -t prints just the kind: alias, function, builtin, file, or keyword, and nothing for an unknown name.
		isDefined: `case $(type -t -- "$n") in alias|function) true ;; *) false ;; esac`,
	},
	"dash": {
		// dash's type has no -t, so this matches the sentence it prints instead: "NAME is an alias for VALUE" or
		// "NAME is a shell function".
		isDefined: `case $(type "$n") in *" is an alias for "*|*" is a shell function") true ;; *) false ;; esac`,
	},
}

// scriptShell returns the shell SHELL names, failing the script if SHELL is unset or names one shells doesn't have.
func scriptShell(ts *testscript.TestScript) userShell {
	path := ts.Getenv("SHELL")
	if path == "" {
		ts.Fatalf("SHELL unset; set it first, e.g. env SHELL=/bin/zsh")
	}
	name := filepath.Base(path)
	spec, ok := shells[name]
	if !ok {
		ts.Fatalf("SHELL=%s; want zsh, bash, or dash", path)
	}
	return userShell{shellSpec: spec, name: name, path: path}
}

// userShell is the shell SHELL names, as a user's login shell.
type userShell struct {
	shellSpec
	// name is the shell's file name, e.g. zsh.
	name string
	// path is SHELL itself, e.g. /bin/zsh.
	path string
}

// shellSpec is how shell and defines run one shell: bare, with no rc files, given code with -c.
type shellSpec struct {
	// flags go ahead of -c.
	flags []string
	// setup is code run ahead of sourcing shellenv's output.
	setup string
	// isDefined tests that $n names an alias or a function, rather than a command on PATH.
	isDefined string
}

// args returns the shell's arguments to source the last shellenv's output, then run shell code body.
//
// Ex: `shell kpn` with SHELL=/bin/bash runs
//
//	bash --posix -c '. $WORK/.shell/env; eval kpn'
//
// zsh's code also starts with setup and, for `shell`, ends by saving its history.
//
// Code in body that uses the source's aliases has to go through eval, as cmdShell's does. A shell expands aliases as it
// parses, and it parses a whole line before running any of it. So in `. env; kpn`, kpn is read before the source has
// defined the alias, and fails with "command not found". eval parses its argument only when it runs, after the source.
// A newline in place of the `;` would do for bash and dash, but zsh parses all of a -c string up front. Functions need
// no eval, since a shell looks them up when it runs them.
func (s shellSpec) args(ts *testscript.TestScript, body string) []string {
	env := workPath(ts, shellenvFile)
	if _, err := os.Stat(env); err != nil {
		ts.Fatalf("nothing to source; run shellenv first")
	}
	code := fmt.Sprintf(". %s; %s", shellescape.Quote(env), body)
	if s.setup != "" {
		code = fmt.Sprintf("%s; %s", s.setup, code)
	}
	return append(slices.Clone(s.flags), "-c", code)
}

// workPath returns a path under the script's $WORK, whatever directory the script has cd'd into.
func workPath(ts *testscript.TestScript, rel string) string {
	return filepath.Join(ts.Getenv("WORK"), rel)
}
