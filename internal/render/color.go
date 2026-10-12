package render

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gookit/color"
	kubecolor "github.com/kubecolor/kubecolor/command"
	"github.com/kubecolor/kubecolor/kubectl"
	"github.com/kubecolor/kubecolor/printer"
	"github.com/muesli/termenv"
	"github.com/xo/terminfo"
)

// Printer writes kuba's own tables to stdout, colored by kubecolor's printer.
type Printer struct {
	// Stdout is where the colored output goes.
	Stdout io.Writer
	// Stderr takes the warning when color fails, e.g. on kubecolor settings that don't parse.
	Stderr io.Writer
	// IsTTY reports whether Stdout is a terminal.
	IsTTY bool
}

// Print writes text to Stdout in the colors kubecolor gives the output of kubectl.
//
// kubectl doesn't run: kuba made text itself, and args just tell kubecolor which command's output to color it as.
//
// Color follows kubecolor's own rules and settings, so kuba's tables match kubecolor's wherever both show up:
//   - On by default when Stdout is a terminal, off otherwise
//   - Off with NO_COLOR or KUBECOLOR_FORCE_COLORS=none
//   - Forced on, terminal or not, with FORCE_COLOR or a KUBECOLOR_FORCE_COLORS level, e.g. truecolor
//   - Themed by ~/.kube/color.yaml and KUBECOLOR_* env vars
//
// When color fails, kuba warns and prints the text plain.
func (p Printer) Print(text string, args ...string) error {
	out, err := p.colorized(text, args)
	if err != nil {
		_, _ = fmt.Fprintf(p.Stderr, "Warning: printing without color: %v\n", err)
		out = text
	}

	_, err = io.WriteString(p.Stdout, out)
	return err
}

// colorized returns text colored, or as-is when color is off.
//
// It settles the cheap cases before reading kubecolor's settings, since that may query the terminal for its background
// color.
//
// REF: https://github.com/kubecolor/kubecolor/blob/v0.8.0/command/runner.go#L90-L98
func (p Printer) colorized(text string, args []string) (string, error) {
	force, _, err := kubecolor.ParseColorLevel(os.Getenv("KUBECOLOR_FORCE_COLORS"))
	switch {
	case err != nil:
		return "", fmt.Errorf("KUBECOLOR_FORCE_COLORS: %w", err)
	case text == "", os.Getenv("NO_COLOR") != "", force == kubecolor.ColorLevelNone:
		return text, nil
	case !p.IsTTY && force == kubecolor.ColorLevelUnset && os.Getenv("FORCE_COLOR") == "":
		return text, nil
	}

	cfg, err := kubecolor.ResolveConfig(args, termenv.NewOutput(p.Stdout))
	if err != nil {
		return "", err
	}

	setColorLevel(cfg.ForceColor)
	cfg.Theme.ComputeCache()
	info := kubectl.InspectSubcommandInfo(cfg.ArgsPassthrough, kubectl.DefaultPluginHandler{})

	return painted(&printer.KubectlOutputColoredPrinter{
		SubcommandInfo:    info,
		Recursive:         info.Recursive,
		ObjFreshThreshold: cfg.ObjFreshThreshold,
		Theme:             &cfg.Theme,
	}, text)
}

// setColorLevel sets gookit/color's process-wide color level as kubecolor does: the level KUBECOLOR_FORCE_COLORS names,
// else the one gookit detected from the environment at startup, e.g. 256 colors for TERM=xterm-256color, else basic
// when it found none.
//
// It also turns on gookit's own switch, which it reads from NO_COLOR once at init; colorized has already checked it.
//
// kubecolor first unsets FORCE_COLOR and calls color.DetectColorLevel, but that only returns a level, without setting
// it, so kuba skips both.
//
// REF: https://github.com/kubecolor/kubecolor/blob/v0.8.0/command/runner.go#L108-L123
func setColorLevel(force kubecolor.ColorLevel) {
	color.Enable = true
	switch {
	case force != kubecolor.ColorLevelUnset && force != kubecolor.ColorLevelAuto:
		color.ForceSetColorLevel(force.TerminfoColorLevel())
	case color.TermColorLevel() == terminfo.ColorLevelNone && os.Getenv("COLORTERM") == "":
		color.ForceSetColorLevel(terminfo.ColorLevelBasic)
	}
}

// painted runs text through a kubecolor printer, which can panic on input it doesn't expect.
func painted(p printer.Printer, text string) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("kubecolor: %v", r)
		}
	}()
	var b strings.Builder
	p.Print(strings.NewReader(text), &b)
	return b.String(), nil
}
