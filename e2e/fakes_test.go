package e2e

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unicode"

	"al.essio.dev/pkg/shellescape"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/lithammer/fuzzysearch/fuzzy"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/hcgatewood/kuba/internal/kubectlflags"
)

const (
	// callsEnv names the file each fake appends its calls to, one per script, which `calls` reads back.
	//
	// Ex: with E2E_CALLS=$WORK/.calls, kuba piping pod names to `fzf --multi` appends
	// {"name":"fzf","args":["--multi"],"stdin":"apple\nbanana\n"} there, and `calls fzf` prints it as
	// `fzf --multi`, `| apple`, `| banana`.
	callsEnv = "E2E_CALLS"

	// ptyCommand is the fake that ptyexec runs commands under, to give them a pseudo-terminal.
	//
	// Ex: `ptyexec kuba get pods` runs `e2e-pty kuba get pods`, so kuba sees a terminal and prints headers.
	ptyCommand = "e2e-pty"
)

// fakeCommands maps each fake's name to its main; testscript.Main installs them as executables first on PATH.
func fakeCommands() map[string]func() {
	cmds := map[string]func(){
		"fzf":         fzfMain,
		"kubecolor":   kubecolorMain,
		"kubectl-rec": kubectlRecMain,
		ptyCommand:    ptyMain,
	}
	for _, name := range recorderNames {
		cmds[name] = recorderMain
	}
	return cmds
}

// recorderNames are the tools kuba hands off to that the recorder stands in for.
var recorderNames = []string{"fx", "jdd", "kubectl-klock", "kubectl-lineage", "kubectl-stern", "ssh"}

// fzfMain stands in for fzf: it records its args and input, then picks per FZF_PICK. Nothing to pick exits 1.
//
// FZF_PICK modes:
//   - first (the default): the first match
//   - all: every match
//   - index:N: the Nth match, 0-based
//   - match:REGEX: the matches REGEX matches
//   - cancel: nothing, exiting 130 (like Esc)
//
// Flags it honors, as in fzf:
//   - --query: narrows the input to matches first (a smart-case subsequence match per space-separated term)
//   - --ansi: strips ANSI codes from the output
//   - --multi: allows more than one pick; without it, all and match:REGEX keep just the first
func fzfMain() {
	args := os.Args[1:]
	input := readStdin()
	record("fzf", args, input)
	multi, stripANSI, query := parseFzfArgs(args)
	var matches []string
	for _, line := range strings.Split(input, "\n") {
		if line != "" && fzfMatches(query, ansi.Strip(line)) {
			matches = append(matches, line)
		}
	}
	picks, code, err := fzfPick(os.Getenv("FZF_PICK"), matches, multi)
	if err != nil {
		fatalf("%v", err)
	}
	for _, p := range picks {
		if stripANSI {
			p = ansi.Strip(p)
		}
		if _, err := fmt.Println(p); err != nil {
			fatalf("writing stdout: %v", err)
		}
	}
	os.Exit(code)
}

// parseFzfArgs pulls out the flags the fake honors, ignoring the rest.
func parseFzfArgs(args []string) (multi, stripANSI bool, query string) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--multi" || a == "-m":
			multi = true
		case a == "--ansi":
			stripANSI = true
		case strings.HasPrefix(a, "--query="):
			query = strings.TrimPrefix(a, "--query=")
		case (a == "--query" || a == "-q") && i+1 < len(args):
			i++
			query = args[i]
		}
	}
	return multi, stripANSI, query
}

// fzfMatches reports whether every term of query is a subsequence of line.
//
// Terms with an uppercase letter match case-sensitively (fzf's smart case).
func fzfMatches(query, line string) bool {
	for _, needle := range strings.Fields(query) {
		// Like real fzf: an all-lowercase term ignores case, so "apple" matches "Apple" but "Apple" doesn't match "apple"
		match := fuzzy.MatchFold
		if strings.ContainsFunc(needle, unicode.IsUpper) {
			match = fuzzy.Match
		}
		if !match(needle, line) {
			return false
		}
	}
	return true
}

// fzfPick picks from matches per an FZF_PICK mode, returning the picks and fzf's exit code.
func fzfPick(mode string, matches []string, multi bool) ([]string, int, error) {
	var picks []string
	switch {
	case mode == "" || mode == "first":
		picks = matches[:min(1, len(matches))]
	case mode == "all":
		picks = matches
	case strings.HasPrefix(mode, "index:"):
		i, err := strconv.Atoi(strings.TrimPrefix(mode, "index:"))
		if err != nil || i < 0 {
			return nil, 0, fmt.Errorf("bad FZF_PICK %q", mode)
		}
		if i < len(matches) {
			picks = matches[i : i+1]
		}
	case strings.HasPrefix(mode, "match:"):
		re, err := regexp.Compile(strings.TrimPrefix(mode, "match:"))
		if err != nil {
			return nil, 0, fmt.Errorf("bad FZF_PICK %q: %w", mode, err)
		}
		for _, m := range matches {
			if re.MatchString(ansi.Strip(m)) {
				picks = append(picks, m)
			}
		}
	case mode == "cancel":
		return nil, 130, nil
	default:
		return nil, 0, fmt.Errorf("bad FZF_PICK %q", mode)
	}

	if !multi && len(picks) > 1 {
		picks = picks[:1]
	}

	if len(picks) == 0 {
		return nil, 1, nil
	}
	return picks, 0, nil
}

// kubeletVerbs are the verbs that need a kubelet, which envtest lacks, so kubectl-rec fakes them.
var kubeletVerbs = sets.New("attach", "cp", "debug", "exec", "logs", "port-forward")

// kubectlRecMain stands in for kubectl via KUBA_KUBECTL: it records argv, then execs or fakes a kubectl call.
//
// Most verbs run the real kubectl, but some need a kubelet, which envtest lacks, so kubectl-rec fakes them with a
// canned reply instead (E2E_OUT_KUBECTL, E2E_EXIT_KUBECTL).
func kubectlRecMain() {
	args := os.Args[1:]

	if kubeletVerbs.Has(kubectlflags.Verb(args)) {
		exitWithCannedReply("kubectl")
	}

	record("kubectl", args, "") // leave stdin unread, for the real kubectl
	err := execKubectl(args)

	fatalf("%v", err)
}

// kubecolorFlags are the kubecolor flags kuba passes, which kubectl itself would reject.
var kubecolorFlags = []string{"--force-colors", "--plain"}

// kubecolorMain stands in for kubecolor via KUBA_KUBECTL: it records argv, drops kubecolor's own flags, then execs the
// real kubectl.
//
// It never colors, so output reads the same as plain kubectl's.
func kubecolorMain() {
	args := os.Args[1:]

	record("kubecolor", args, "") // leave stdin unread, for the real kubectl
	err := execKubectl(lo.Without(args, kubecolorFlags...))

	fatalf("%v", err)
}

// execKubectl replaces this process with the real kubectl, so it returns only on failure.
func execKubectl(args []string) error {
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		return err
	}
	if err := syscall.Exec(kubectl, append([]string{"kubectl"}, args...), os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", kubectl, err)
	}
	return nil
}

// ptyMain runs argv on a fresh pseudo-terminal (stdin, stdout, and stderr) and copies what it writes to stdout.
func ptyMain() {
	if len(os.Args) < 2 {
		fatalf("usage: %s CMD ARGS...", ptyCommand)
	}

	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		fatalf("%v", err)
	}
	_, _ = io.Copy(os.Stdout, terminal)
	code, err := exitCode(cmd.Wait())
	if err != nil {
		fatalf("%v", err)
	}
	os.Exit(code)
}

// recorderMain stands in for each tool in recorderNames: it records argv and stdin, then prints canned output.
func recorderMain() {
	exitWithCannedReply(filepath.Base(os.Args[0]))
}

// exitWithCannedReply records argv and stdin as a call to name, prints E2E_OUT_<NAME>, then exits E2E_EXIT_<NAME>.
//
// NAME is name upper-cased with dashes as underscores (e.g. E2E_OUT_KUBECTL_STERN). Exit code defaults to 0.
func exitWithCannedReply(name string) {
	record(name, os.Args[1:], readStdin())

	key := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))

	stdout := os.Getenv("E2E_OUT_" + key)
	exit := os.Getenv("E2E_EXIT_" + key)
	code, err := strconv.Atoi(cmp.Or(exit, "0"))
	if err != nil {
		fatalf("bad E2E_EXIT_%s %q", key, exit)
	}

	if _, err := fmt.Print(stdout); err != nil {
		fatalf("writing stdout: %v", err)
	}
	os.Exit(code)
}

// readStdin reads all of stdin, or nothing if stdin is a terminal (which would hang).
func readStdin() string {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return ""
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatalf("reading stdin: %v", err)
	}
	return string(b)
}

// record appends a call to the script's call log, or exits 2 if it can't.
//
// A missed call would let `! calls NAME` pass when it shouldn't, hence failing loudly.
func record(name string, args []string, stdin string) {
	if err := appendCall(os.Getenv(callsEnv), call{Name: name, Args: args, Stdin: stdin}); err != nil {
		fatalf("recording call: %v", err)
	}
}

// appendCall writes c to the call log at path as one JSON line, in one write so concurrent fakes don't interleave.
func appendCall(path string, c call) error {
	if path == "" {
		return fmt.Errorf("%s not set", callsEnv)
	}
	line, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}

// fatalf reports a broken fake on stderr as `<name> (fake): <message>`, then exits 2.
//
// The "(fake)" pins the failure on the harness rather than the tool it stands in for.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s (fake): %s\n", filepath.Base(os.Args[0]), fmt.Sprintf(format, args...))
	os.Exit(2)
}

// readCalls parses the call log at path, oldest first; no log means no calls.
func readCalls(path string) ([]call, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var calls []call
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var c call
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		calls = append(calls, c)
	}
	return calls, nil
}

// call is one recorded invocation of a fake.
type call struct {
	Name  string   `json:"name"`
	Args  []string `json:"args"`
	Stdin string   `json:"stdin,omitempty"`
}

// String renders the call as its shell-quoted command line, then each stdin line prefixed with "| ".
func (c call) String() string {
	var b strings.Builder
	b.WriteString(shellescape.QuoteCommand(append([]string{c.Name}, c.Args...)) + "\n")
	for line := range strings.Lines(c.Stdin) {
		b.WriteString("| " + strings.TrimSuffix(line, "\n") + "\n") // force include trailing newline
	}
	return b.String()
}

func TestFzfMatches(t *testing.T) {
	tests := []struct {
		query string
		line  string
		want  bool
	}{
		{"", "anything", true},
		{"aple", "apple-1  1/1  Running", true},
		{"apl run", "apple-1  1/1  Running", true},
		{"Apple", "apple-1", false},
		{"Apple", "Apple-1", true},
		{"cherry", "apple-1", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, fzfMatches(tt.query, tt.line), "fzfMatches(%q, %q)", tt.query, tt.line)
	}
}

func TestFzfPick(t *testing.T) {
	matches := []string{"apple-1", "banana-1", "apple-2"}
	tests := []struct {
		mode     string
		multi    bool
		want     []string
		wantCode int
	}{
		{"", false, []string{"apple-1"}, 0},
		{"all", true, matches, 0},
		{"all", false, []string{"apple-1"}, 0},
		{"index:1", false, []string{"banana-1"}, 0},
		{"index:9", false, nil, 1},
		{"match:^apple", true, []string{"apple-1", "apple-2"}, 0},
		{"cancel", true, nil, 130},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/multi=%v", tt.mode, tt.multi), func(t *testing.T) {
			got, code, err := fzfPick(tt.mode, matches, tt.multi)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantCode, code)
		})
	}
}

func TestFzfPickBadMode(t *testing.T) {
	for _, mode := range []string{"index:-1", "index:apple", "match:(", "bogus"} {
		_, _, err := fzfPick(mode, []string{"apple-1"}, false)
		assert.ErrorContains(t, err, "bad FZF_PICK", "mode %q", mode)
	}
}

func TestCallString(t *testing.T) {
	tests := []struct {
		name string
		c    call
		want string
	}{
		{
			"quoted args and stdin",
			call{Name: "fzf", Args: []string{"--multi", "--prompt=Choose pod: ", "it's"}, Stdin: "apple\nbanana\n"},
			"fzf --multi '--prompt=Choose pod: ' 'it'\"'\"'s'\n| apple\n| banana\n",
		},
		{"no stdin", call{Name: "ssh", Args: []string{"fig"}}, "ssh fig\n"},
		{"stdin without trailing newline", call{Name: "fx", Stdin: "{}"}, "fx\n| {}\n"},
		{"blank stdin line", call{Name: "fx", Stdin: "grape\n\nhoney\n"}, "fx\n| grape\n| \n| honey\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.c.String())
		})
	}
}
