package render

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/gookit/color"
	kubecolor "github.com/kubecolor/kubecolor/command"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// colorTable is a pod table with each kind of cell kubecolor colors its own way: header, statuses, counts, and ages.
const colorTable = "NAME       READY   STATUS             RESTARTS   AGE\n" +
	"apple-1    1/1     Running            0          5m\n" +
	"apple-2    0/1     Pending            0          10s\n" +
	"banana-1   0/1     CrashLoopBackOff   12         2d\n"

// TestPrinter checks kuba colors its tables byte for byte as the kubecolor binary does, under each setting that turns
// color on or off, picks its level, or themes it.
//
// Each case prints the table both ways in this one process: with Print, and with kubecolor's own Run reading it as
// kubectl's output.
func TestPrinter(t *testing.T) {
	const bold = "\x1b[1m"
	// An orange header shows the level: kubecolor writes it in basic, 256-color, or 24-bit codes
	orange := map[string]string{"KUBECOLOR_THEME_TABLE_HEADER": "#ff8000"}
	const orangeBasic, orange256, orange24Bit = "\x1b[93m", "\x1b[38;5;208m", "\x1b[38;2;255;128;0m"
	tests := []struct {
		name      string
		tty       bool
		env       map[string]string
		colorYAML string
		// wantHeader is the codes before NAME, e.g. bold, or none when the table is plain.
		wantHeader string
	}{
		{name: "off a terminal"},
		{name: "on a terminal", tty: true, wantHeader: bold},
		{name: "NO_COLOR", tty: true, env: map[string]string{"NO_COLOR": "1"}},
		{name: "NO_COLOR over FORCE_COLOR", env: map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}},
		{name: "FORCE_COLOR off a terminal", env: map[string]string{"FORCE_COLOR": "1"}, wantHeader: bold},
		{name: "KUBECOLOR_FORCE_COLORS=none", tty: true, env: map[string]string{"KUBECOLOR_FORCE_COLORS": "none"}},
		{name: "KUBECOLOR_FORCE_COLORS=none over FORCE_COLOR", env: map[string]string{"KUBECOLOR_FORCE_COLORS": "none", "FORCE_COLOR": "1"}},
		{name: "KUBECOLOR_FORCE_COLORS=auto off a terminal", env: map[string]string{"KUBECOLOR_FORCE_COLORS": "auto"}, wantHeader: bold},
		{name: "KUBECOLOR_FORCE_COLORS=true, the old flag value", env: map[string]string{"KUBECOLOR_FORCE_COLORS": "true"}, wantHeader: bold},
		{name: "KUBECOLOR_FORCE_COLORS=false, the old flag value", env: map[string]string{"KUBECOLOR_FORCE_COLORS": "false"}},

		{name: "basic on an unknown terminal", tty: true, env: orange, wantHeader: orangeBasic},
		{name: "256 colors from TERM", tty: true, env: lo.Assign(orange, map[string]string{"TERM": "xterm-256color"}), wantHeader: orange256},
		{name: "24-bit from COLORTERM", tty: true, env: lo.Assign(orange, map[string]string{"COLORTERM": "truecolor"}), wantHeader: orange24Bit},
		{name: "FORCE_COLOR keeps TERM's level", tty: true, env: lo.Assign(orange, map[string]string{"FORCE_COLOR": "1", "TERM": "xterm-256color"}), wantHeader: orange256},
		{name: "KUBECOLOR_FORCE_COLORS=256", env: lo.Assign(orange, map[string]string{"KUBECOLOR_FORCE_COLORS": "256"}), wantHeader: orange256},
		{name: "KUBECOLOR_FORCE_COLORS=truecolor", env: lo.Assign(orange, map[string]string{"KUBECOLOR_FORCE_COLORS": "truecolor"}), wantHeader: orange24Bit},
		{name: "KUBECOLOR_FORCE_COLORS=basic over TERM", tty: true, env: lo.Assign(orange, map[string]string{"KUBECOLOR_FORCE_COLORS": "basic", "TERM": "xterm-256color"}), wantHeader: orangeBasic},

		{name: "light preset", tty: true, env: map[string]string{"KUBECOLOR_PRESET": "light"}, wantHeader: bold},
		{name: "KUBECOLOR_LIGHT_BACKGROUND", tty: true, env: map[string]string{"KUBECOLOR_LIGHT_BACKGROUND": "true"}, wantHeader: bold},
		{name: "theme from color.yaml", tty: true, colorYAML: "theme:\n  table:\n    header: green\n", wantHeader: "\x1b[32m"},
		{name: "theme env var over color.yaml", tty: true, env: map[string]string{"KUBECOLOR_THEME_TABLE_HEADER": "red"}, colorYAML: "theme:\n  table:\n    header: green\n", wantHeader: "\x1b[31m"},
		{name: "fresh ages", tty: true, env: map[string]string{"KUBECOLOR_OBJ_FRESH": "1m"}, wantHeader: bold},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateColorEnv(t, tt.env)
			if tt.colorYAML != "" {
				writeColorYAML(t, tt.colorYAML)
			}
			if tt.tty {
				stdoutOnTerminal(t)
			}

			var stdout, stderr bytes.Buffer
			p := Printer{Stdout: &stdout, Stderr: &stderr, IsTTY: tt.tty}
			freshGookit()
			require.NoError(t, p.Print(colorTable, "get", "pods"))
			assert.Empty(t, stderr.String())
			assert.Equal(t, colorTable, ansi.Strip(stdout.String()))
			header, _, _ := strings.Cut(stdout.String(), "NAME")
			assert.Equal(t, tt.wantHeader, header)

			// kubecolor goes second, as it unsets FORCE_COLOR for good
			assert.Equal(t, kubecolorPrint(t, colorTable, "get", "pods"), stdout.String())
		})
	}
}

// TestPrinterBadSetting pins where kuba parts from kubecolor: a setting that doesn't parse fails kubecolor's whole
// command, but only costs kuba its color.
func TestPrinterBadSetting(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		colorYAML  string
		wantStderr string
	}{
		{name: "env var", env: map[string]string{"KUBECOLOR_FORCE_COLORS": "purple"}, wantStderr: "KUBECOLOR_FORCE_COLORS: "},
		{name: "color.yaml", colorYAML: "theme: [", wantStderr: "While parsing config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateColorEnv(t, tt.env)
			if tt.colorYAML != "" {
				writeColorYAML(t, tt.colorYAML)
			}
			var stdout, stderr bytes.Buffer
			p := Printer{Stdout: &stdout, Stderr: &stderr, IsTTY: true}
			freshGookit()
			require.NoError(t, p.Print(colorTable, "get", "pods"))
			assert.Equal(t, colorTable, stdout.String())
			assert.Contains(t, stderr.String(), "Warning: printing without color: "+tt.wantStderr)
		})
	}
}

// isolateColorEnv clears every setting that steers color, so the developer's terminal can't leak in, then sets env.
//
// HOME is empty, so there's no ~/.kube/color.yaml, and the dark preset stops kubecolor asking the terminal for its
// background color, which nothing answers.
func isolateColorEnv(t *testing.T, env map[string]string) {
	t.Setenv("HOME", t.TempDir())
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "KUBECOLOR_") || slices.Contains(colorEnvVars, k) {
			t.Setenv(k, "")
			require.NoError(t, os.Unsetenv(k))
		}
	}
	t.Setenv("KUBECOLOR_PRESET", "dark")
	for k, v := range env {
		t.Setenv(k, v)
	}
}

// colorEnvVars are the env vars besides KUBECOLOR_* that kubecolor or gookit/color read to decide on color.
var colorEnvVars = []string{
	"NO_COLOR", "FORCE_COLOR", "COLORTERM", "TERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "TERMINAL_EMULATOR",
	"WSL_DISTRO_NAME",
}

// writeColorYAML writes kubecolor's config file, ~/.kube/color.yaml.
func writeColorYAML(t *testing.T, yaml string) {
	dir := filepath.Join(os.Getenv("HOME"), ".kube")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "color.yaml"), []byte(yaml), 0o600))
}

// stdoutOnTerminal points os.Stdout at a pseudo-terminal for the test, as kubecolor checks os.Stdout itself.
func stdoutOnTerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = tty
	t.Cleanup(func() {
		os.Stdout = stdout
		_ = tty.Close()
		_ = ptmx.Close()
	})
}

// kubecolorPrint is text as kubecolor's own command prints it for `kubecolor args...`, reading kubectl's output from a
// file rather than running kubectl.
func kubecolorPrint(t *testing.T, text string, args ...string) string {
	path := filepath.Join(t.TempDir(), "kubectl.out")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	var out bytes.Buffer
	stdout := kubecolor.Stdout
	kubecolor.Stdout = &out
	defer func() { kubecolor.Stdout = stdout }()
	freshGookit()
	require.NoError(t, kubecolor.Run(append(args, "--kubecolor-stdin="+path), "test"))
	return out.String()
}

// freshGookit resets gookit/color's process-wide state to what a new process starts with.
//
// gookit reads its color level and on-off switch from the environment once, at init; DetectColorLevel only returns a
// level, without setting it. Print and kubecolor's Run both start from that state, so each must start fresh, as in its
// own binary, rather than from the other's or the last case's leftovers.
func freshGookit() {
	color.Enable = os.Getenv("NO_COLOR") == ""
	color.ForceSetColorLevel(color.DetectColorLevel())
}
