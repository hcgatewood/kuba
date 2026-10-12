package resolve

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	fzf "github.com/junegunn/fzf/src"
	"github.com/samber/lo"

	"github.com/hcgatewood/kuba/internal/exit"
)

// fzf asks fzf to pick among lines, returning the picks' indices, sorted.
func (p Picker) fzf(lines []string, multi bool) ([]int, error) {
	printed, code, err := p.runFzf(p.fzfArgs(multi), lines)
	printed, err = fzfResult(printed, code, err)
	if err != nil {
		return nil, err
	}
	picked := pickedIndices(lines, printed)
	if len(picked) == 0 {
		return nil, fmt.Errorf("no %s selected", p.Type)
	}
	return picked, nil
}

// fzfArgs are the options kuba gives fzf, in order, from how picking works to how it looks:
//   - --multi for any number of picks; else, for exactly one, --bind so tab and shift-tab move instead of marking
//   - --query, the starting query, lowered so fzf's smart case doesn't read it as case-sensitive and hide matches
//   - --prompt naming what's picked, e.g. "Choose pod: "
//   - --header, if the Picker has one
//   - --ansi, to show the lines' colors
func (p Picker) fzfArgs(multi bool) []string {
	var args []string
	if multi {
		args = append(args, "--multi")
	} else {
		args = append(args, "--bind=shift-tab:up,tab:down") // tab/shift-tab move down/up like in --multi
	}
	args = append(args, fmt.Sprintf("--query=%s", strings.ToLower(p.Query)))
	args = append(args, fmt.Sprintf("--prompt=Choose %s: ", p.Type))
	if p.Header != "" {
		args = append(args, fmt.Sprintf("--header=%s", p.Header))
	}
	return append(args, "--ansi")
}

// runFzf runs fzf on lines: the Picker's Fzf command if set, else the fzf built into kuba.
//
// Either way fzf reads the user's FZF_DEFAULT_OPTS, as the binary does.
func (p Picker) runFzf(args, lines []string) (printed []string, code int, err error) {
	if p.Fzf != "" {
		return runExternalFzf(p.Fzf, args, lines)
	}
	return runBuiltinFzf(args, lines)
}

// runExternalFzf runs the fzf command name on lines, returning the lines it printed and its exit code.
func runExternalFzf(name string, args, lines []string) ([]string, int, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	code := fzf.ExitOk

	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code, err = ee.ExitCode(), nil
	}

	return lo.Compact(strings.Split(string(out), "\n")), code, err
}

// runBuiltinFzf runs the fzf built into kuba on lines, returning the lines it printed and its exit code.
//
// It adds two options of its own, which fzf reads after FZF_DEFAULT_OPTS, so they win over the user's:
//   - --scheme=default ranks lines as plain text. Without it, fzf takes a terminal stdin to mean it'll list files
//     itself, and ranks lines as paths, but kuba sends its own lines, as a pipe would
//   - --no-tmux turns popups off. A popup reruns os.Args, which here is kuba's command line, not fzf's
func runBuiltinFzf(args, lines []string) ([]string, int, error) {
	opts, err := fzf.ParseOptions(true, append([]string{"--scheme=default", "--no-tmux"}, args...))
	if err != nil {
		return nil, fzf.ExitError, err
	}

	opts.Input = make(chan string, len(lines))
	for _, line := range lines {
		opts.Input <- line
	}
	close(opts.Input)

	opts.Output = make(chan string)
	var printed []string
	var wg sync.WaitGroup
	wg.Go(func() {
		for line := range opts.Output {
			printed = append(printed, line)
		}
	})

	code, err := fzf.Run(opts)
	close(opts.Output)
	wg.Wait()

	return printed, code, err
}

// fzfResult maps fzf's outcome to kuba's: a cancel exits 130, as Ctrl-C does anywhere else, and no match picks nothing.
func fzfResult(printed []string, code int, err error) ([]string, error) {
	switch {
	case code == fzf.ExitInterrupt:
		return nil, &exit.CodeError{Code: exit.Interrupted}
	case code == fzf.ExitNoMatch:
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("fzf: %w", err)
	case code != fzf.ExitOk:
		return nil, fmt.Errorf("fzf exited %d", code)
	}
	return printed, nil
}

// pickedIndices maps the lines fzf printed back to their indices in lines, sorted.
//
// The user's FZF_DEFAULT_OPTS can make fzf print more than the picks, and since those extra lines match no candidate,
// they're skipped. E.g. with --print-query, typing aple and picking apple-db prints aple, then apple-db's line; only
// apple-db is picked.
func pickedIndices(lines, printed []string) []int {
	// A count, not a set, in case two candidates share a line: marking one prints it once, so only one gets picked
	toPick := lo.CountValues(printed)
	var picked []int
	for i, line := range lines {
		if toPick[line] == 0 {
			continue
		}
		picked = append(picked, i)
		toPick[line]--
	}
	return picked
}
