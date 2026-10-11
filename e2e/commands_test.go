package e2e

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/assert"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/tools/clientcmd"
)

// scriptCommands are the additional commands provided to scripts on top of testscript's builtins and our fakes.
func scriptCommands() map[string]func(*testscript.TestScript, bool, []string) {
	return map[string]func(*testscript.TestScript, bool, []string){
		"calls":     cmdCalls,
		"current":   cmdCurrent,
		"defines":   cmdDefines,
		"exitcode":  cmdExitcode,
		"history":   cmdHistory,
		"kapply":    cmdKapply,
		"kuba":      cmdKuba,
		"mkcluster": cmdMkcluster,
		"mkcontext": cmdMkcontext,
		"ptyexec":   cmdPtyexec,
		"reqs":      cmdReqs,
		"rows":      cmdRows,
		"shell":     cmdShell,
		"shellenv":  cmdShellenv,
		"tui":       cmdTui,
	}
}

// cmdCalls prints the calls fakes recorded, oldest first: each command line shell-quoted, then any stdin it read, each
// line prefixed with "| ".
//
// Usage: [!] calls [NAME...]
//
// Example:
//
//	kuba get pod --select -o name
//	calls fzf
//	cmp stdout want-fzf.txt
//
// NAMEs limit printed calls to the named fakes (`calls fzf`); with none, it prints every fake's calls. With `!` it
// asserts the opposite: the script fails if any of those fakes was called at all (`! calls kubectl-stern`).
func cmdCalls(ts *testscript.TestScript, neg bool, args []string) {
	calls, err := readCalls(ts.Getenv(callsEnv))
	ts.Check(err)
	var b strings.Builder
	for _, c := range calls {
		if len(args) == 0 || slices.Contains(args, c.Name) {
			b.WriteString(c.String())
		}
	}
	if neg && b.Len() > 0 {
		ts.Fatalf("unexpected calls:\n%s", b.String())
	}
	_, _ = fmt.Fprint(ts.Stdout(), state(ts).normalizer(false).apply(b.String()))
}

// cmdCurrent asserts the kubeconfig's current context and, if given, that context's namespace.
//
// Usage: current CONTEXT [NAMESPACE]
//
// It merges every file in $KUBECONFIG, as kubectl does. NAMESPACE is the context's namespace as written, so the empty
// string when unset, where kubectl would report "default".
func cmdCurrent(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) == 0 || len(args) > 2 {
		ts.Fatalf("usage: current CONTEXT [NAMESPACE]")
	}

	// Use testscript's KUBECONFIG, not the process's
	rules := clientcmd.ClientConfigLoadingRules{Precedence: filepath.SplitList(ts.Getenv("KUBECONFIG"))}
	cfg, err := rules.Load()
	ts.Check(err)
	got := []string{cfg.CurrentContext}
	if len(args) == 2 {
		ns := ""
		if c := cfg.Contexts[cfg.CurrentContext]; c != nil {
			ns = c.Namespace
		}
		got = append(got, ns)
	}
	if !slices.Equal(got, args) {
		ts.Fatalf("kubeconfig is on %q, want %q", got, args)
	}
}

// cmdExitcode asserts the exit code of the last kuba, ptyexec, shell, or tui exit.
//
// Usage: exitcode N
func cmdExitcode(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 1 {
		ts.Fatalf("usage: exitcode N")
	}

	want, err := strconv.Atoi(args[0])
	ts.Check(err)
	if got := state(ts).lastExit; got != want {
		ts.Fatalf("exit code %d, want %d", got, want)
	}
}

// cmdKapply creates the objects in YAML files through a context (default apple), status included.
//
// Usage: kapply [-c CONTEXT] [--stagger] FILE...
//
// Examples:
//
//	kapply pods.yaml
//	kapply -c banana banana-pods.yaml
//	kapply --stagger old.yaml new.yaml
//
// Fixtures carry their own status because nothing else will set it: envtest runs only the API server, with no
// controllers or kubelet to fill in pod phases, container statuses, or node addresses.
//
// Objects that need a namespace but don't name one go in $NS.
//
// --stagger creates each FILE in a later second than the one before, so creation times sort in argument order. Fixtures
// can't set them: the API server stamps creationTimestamp itself, to the second.
func cmdKapply(ts *testscript.TestScript, neg bool, args []string) {
	fs := flag.NewFlagSet("kapply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("c", "", "")
	stagger := fs.Bool("stagger", false, "")
	if neg || fs.Parse(args) != nil || fs.NArg() == 0 {
		ts.Fatalf("usage: kapply [-c CONTEXT] [--stagger] FILE...")
	}

	kc, err := state(ts).context(*name)
	ts.Check(err)
	for i, file := range fs.Args() {
		if *stagger && i > 0 {
			// The last create was stamped before it returned, so the next second is late enough.
			time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
		}
		objs, err := decodeObjects(ts.ReadFile(file))
		ts.Check(err)
		for _, obj := range objs {
			ts.Check(kc.apply(obj))
		}
	}
}

// cmdKuba runs the implementation under test and normalizes its stdout and stderr (see normalizer).
//
// Usage: [!] kuba ARGS...
//
// `exec kuba` stays available for raw output.
func cmdKuba(ts *testscript.TestScript, neg bool, args []string) {
	runNormalized(ts, neg, false, "kuba", args...)
}

func runNormalized(ts *testscript.TestScript, neg, tty bool, name string, args ...string) {
	st := state(ts)

	code, err := exitCode(ts.Exec(name, args...))
	if err != nil {
		ts.Fatalf("%s: %v", name, err)
	}
	stderr := ts.ReadFile("stderr")
	st.lastExit = code
	st.lastStderr = stderr

	n := st.normalizer(tty)
	_, _ = fmt.Fprint(ts.Stdout(), n.apply(ts.ReadFile("stdout")))
	_, _ = fmt.Fprint(ts.Stderr(), n.apply(stderr))

	switch {
	case code != 0 && !neg:
		ts.Fatalf("%s exited %d", name, code)
	case code == 0 && neg:
		ts.Fatalf("%s unexpectedly succeeded", name)
	}
}

// exitCode turns an exec error into an exit code, reporting a command killed by signal n as 128+n, as shells do.
//
// err is non-nil only if the command couldn't run at all.
func exitCode(runErr error) (int, error) {
	if runErr == nil {
		return 0, nil
	}
	exitErr, ok := errors.AsType[*exec.ExitError](runErr)
	if !ok {
		return -1, runErr
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), nil
	}
	return exitErr.ExitCode(), nil
}

// cmdMkcluster gives each named context a fresh API server of its own, with namespace $NS.
//
// Usage: mkcluster NAME...
//
// apple starts on the shared server; naming it moves it to a fresh one, for scripts that see cluster-wide state (-A,
// nodes, CRDs). That goes first, since anything applied before it stays behind.
func cmdMkcluster(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) == 0 {
		ts.Fatalf("usage: mkcluster NAME...")
	}

	ns := ts.Getenv("NS")
	srvs := make([]*server, len(args))
	var g errgroup.Group
	for i := range args {
		g.Go(func() (err error) {
			if srvs[i], err = startServer(); err != nil {
				return err
			}
			return srvs[i].createNamespace(ns)
		})
	}
	err := g.Wait()
	for _, srv := range srvs {
		ts.Defer(srv.stop)
	}
	ts.Check(err)
	for i, name := range args {
		ts.Check(state(ts).setContext(name, srvs[i], ns))
	}
}

// cmdMkcontext adds contexts sharing an existing context's API server, for scripts about which contexts kuba reaches
// rather than what's on them.
//
// Usage: mkcontext [-c CONTEXT] NAME...
//
// Example:
//
//	mkcontext banana banana-east banana-west
//
// CONTEXT defaults to apple. Each new context defaults to namespace $NS, so it sees the same objects as CONTEXT, and a
// fan-out over several of them lists each object once per context.
//
// Like mkcluster's, each new context gets cluster and user entries named after itself rather than reusing CONTEXT's.
func cmdMkcontext(ts *testscript.TestScript, neg bool, args []string) {
	fs := flag.NewFlagSet("mkcontext", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	base := fs.String("c", "", "")
	if neg || fs.Parse(args) != nil || fs.NArg() == 0 {
		ts.Fatalf("usage: mkcontext [-c CONTEXT] NAME...")
	}

	kc, err := state(ts).context(*base)
	ts.Check(err)
	for _, name := range fs.Args() {
		ts.Check(state(ts).setContext(name, kc.srv, ts.Getenv("NS")))
	}
}

// cmdPtyexec is kuba's terminal twin: it runs a command with stdin, stdout, and stderr on a pseudo-terminal, for
// terminal-only behavior like headers and color. Output is normalized with colors as markup ({bold}NAME{/}); stderr
// merges into stdout.
//
// Usage: [!] ptyexec CMD ARGS...
func cmdPtyexec(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) == 0 {
		ts.Fatalf("usage: [!] ptyexec CMD ARGS...")
	}
	runNormalized(ts, neg, true, ptyCommand, args...)
}

// cmdReqs asserts how many API requests each kubeconfig context sent during the last kuba, counted from its debug log:
// at most N, discovery excluded (or with --discovery, discovery alone).
//
// Usage: reqs [--discovery] N
//
// Example:
//
//	env KUBA_DEBUG=1
//	kuba get pod apple-1
//	reqs 2
//
// The run needs KUBA_DEBUG=1; one with no debug log fails rather than pass as zero requests. A canceled request still
// counts, since the server may have seen it, e.g. the losing half of a GET-or-LIST race.
func cmdReqs(ts *testscript.TestScript, neg bool, args []string) {
	fs := flag.NewFlagSet("reqs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	discovery := fs.Bool("discovery", false, "")
	if neg || fs.Parse(args) != nil || fs.NArg() != 1 {
		ts.Fatalf("usage: reqs [--discovery] N")
	}
	limit, err := strconv.Atoi(fs.Arg(0))
	ts.Check(err)

	reqs, ok := requestsByContext(state(ts).lastStderr, *discovery)
	if !ok {
		ts.Fatalf("the last kuba wrote no debug log; set KUBA_DEBUG=1")
	}
	for _, name := range slices.Sorted(maps.Keys(reqs)) {
		if lines := reqs[name]; len(lines) > limit {
			ts.Fatalf("context %s sent %d requests, want at most %d:\n%s", name, len(lines), limit, strings.Join(lines, ""))
		}
	}
}

// requestsByContext groups a debug log's request lines by kubeconfig context, keeping either the discovery requests
// or the rest.
//
// ok is false if the log has no debug lines at all.
func requestsByContext(log string, discovery bool) (reqs map[string][]string, ok bool) {
	reqs = map[string][]string{} // kubeconfig context -> one debug log line per request it sent
	for line := range strings.Lines(log) {
		if !strings.HasPrefix(line, "level=DEBUG ") {
			continue
		}
		ok = true
		if !strings.Contains(line, " msg=request ") || !strings.Contains(line, " discovery="+strconv.FormatBool(discovery)) {
			continue
		}
		_, rest, _ := strings.Cut(line, " context=")
		name, _, _ := strings.Cut(rest, " ")
		reqs[name] = append(reqs[name], line)
	}
	return reqs, ok
}

// cmdRows asserts the last command's stdout rows, in order, going by each row's first column.
//
// Usage: rows ROW...
//
// Example:
//
//	kuba get node fig
//	rows fig-3 fig-1 fig-2
//
// Slashes span columns, the same number in every ROW: `kiwi/apple-web` matches an -A row, `banana/apple-web` a
// multi-cluster one. Header and blank lines are ignored. A mismatch prints the line that would pass. For no rows, use
// `! stdout .`.
func cmdRows(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) == 0 {
		ts.Fatalf("usage: rows ROW...")
	}
	slashes := strings.Count(args[0], "/")
	for _, row := range args {
		if strings.Count(row, "/") != slashes {
			ts.Fatalf("usage: rows ROW..., every ROW with the same number of slashes")
		}
	}

	if got := listedRows(ts.ReadFile("stdout"), slashes+1); !slices.Equal(got, args) {
		ts.Fatalf("stdout has other rows\nwant: %s\n got: %s", rowsLine(args), rowsLine(got))
	}
}

// headerRe matches the first column of a table header (NAME, NAMESPACE, ...), which no object name can be.
var headerRe = regexp.MustCompile(`^[A-Z][A-Z-]*$`)

// listedRows returns stdout's rows as ROWs, each the row's first columns joined by slashes.
func listedRows(stdout string, columns int) []string {
	var rows []string
	for line := range strings.Lines(stdout) {
		fields := strings.Fields(line)
		if len(fields) == 0 || headerRe.MatchString(fields[0]) {
			continue
		}
		// Don't overflow row's width: `-o name` lists `pod/apple-web` as one column.
		rows = append(rows, strings.Join(fields[:min(len(fields), columns)], "/"))
	}
	return rows
}

// rowsLine returns the script line asserting stdout has exactly rows.
func rowsLine(rows []string) string {
	if len(rows) == 0 {
		return "! stdout ."
	}
	return shellescape.QuoteCommand(append([]string{"rows"}, rows...))
}

func TestRequestsByContext(t *testing.T) {
	const (
		appleList  = `level=DEBUG msg=request context=apple verb=GET path=/api/v1/pods query="limit=500" status=200 latency=3ms discovery=false` + "\n"
		appleGet   = `level=DEBUG msg=request context=apple verb=GET path=/api/v1/pods/banana query="" status=canceled latency=1ms discovery=false` + "\n"
		appleAPI   = `level=DEBUG msg=request context=apple verb=GET path=/api query="" status=200 latency=2ms discovery=true` + "\n"
		bananaList = `level=DEBUG msg=request context=banana verb=GET path=/api/v1/pods query="limit=500" status=200 latency=3ms discovery=false` + "\n"
		action     = `level=DEBUG msg=action command="kubectl get pod --sort-by=.metadata.name"` + "\n"
	)
	tests := []struct {
		name      string
		log       string
		discovery bool
		want      map[string][]string
		wantOK    bool
	}{
		{"grouped by context", appleList + bananaList + appleGet, false, map[string][]string{"apple": {appleList, appleGet}, "banana": {bananaList}}, true},
		{"discovery apart", appleList + appleAPI, true, map[string][]string{"apple": {appleAPI}}, true},
		{"debug log with no requests", action, false, map[string][]string{}, true},
		{"no debug log", "Error: pods \"apple\" not found\n", false, map[string][]string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := requestsByContext(tt.log, tt.discovery)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func TestListedRows(t *testing.T) {
	tests := []struct {
		name    string
		stdout  string
		columns int
		want    []string
	}{
		{"first column", "apple-db  0/1  Running\napple-web  0/1  Running\n", 1, []string{"apple-db", "apple-web"}},
		{"headers and blank lines", "NAME  READY\napple-db  0/1\n\nNAME  READY\nbanana-web  0/1\n", 1, []string{"apple-db", "banana-web"}},
		{"namespace column", "NAMESPACE  NAME\nkiwi  apple-db\nlemon  apple-web\n", 2, []string{"kiwi/apple-db", "lemon/apple-web"}},
		{"cluster and namespace columns", "banana  kiwi  apple-web  0/1\n", 3, []string{"banana/kiwi/apple-web"}},
		{"capped at the row's width", "pod/apple-db\n", 2, []string{"pod/apple-db"}},
		{"no rows", "\n", 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, listedRows(tt.stdout, tt.columns))
		})
	}
}
