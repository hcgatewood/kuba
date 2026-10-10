package e2e

import (
	"cmp"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// normalizer strips what changes from run to run, so goldens hold across runs and across implementations.
type normalizer struct {
	// work is the absolute path to the test's working directory, which is replaced with $WORK.
	work string
	// servers maps each server's host:port to the name of the first context on it.
	servers map[string]string
	// markColor writes colors and styles as markup, e.g. {bold}NAME{/}; otherwise they're stripped with every other
	// ANSI code.
	markColor bool
}

var (
	// rfc3339Re matches RFC 3339 timestamps, the API's format for times like creationTimestamp.
	//
	// Ex: 2026-10-10T21:04:05Z, 2026-10-10T21:04:05.123456Z, 2026-10-10T16:04:05-05:00.
	//
	// REF: https://www.rfc-editor.org/rfc/rfc3339#section-5.6
	rfc3339Re = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)

	// rfc1123Re matches RFC 1123 timestamps with a numeric zone, as kubectl describe prints them.
	//
	// Ex: Sat, 10 Oct 2026 21:04:05 +0000 (from `Start Time:  Sat, 10 Oct 2026 21:04:05 +0000`).
	//
	// REF: https://pkg.go.dev/time#RFC1123Z
	rfc1123Re = regexp.MustCompile(`[A-Z][a-z]{2}, \d{2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2} [+-]\d{4}`)

	// uidRe matches UUIDs, the form of object UIDs, which the server picks at random on create.
	//
	// Ex: 3f2a9c1e-0b5d-4e8f-9a7c-1d2e3f4a5b6c (from `uid: 3f2a9c1e-...` or an ownerReference).
	//
	// REF: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#uids
	// REF: https://www.rfc-editor.org/rfc/rfc9562#section-4
	uidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

	// rvRe matches a resourceVersion in YAML or JSON, keeping the key and quotes in groups 1 and 2.
	//
	// The value is a server-wide write counter, so it depends on every write before it, including other scripts' on the
	// shared server.
	//
	// Ex: `resourceVersion: "4512"` (YAML), `"resourceVersion": "4512"` (JSON).
	//
	// REF: https://kubernetes.io/docs/reference/using-api/api-concepts/#resource-versions
	rvRe = regexp.MustCompile(`(resourceVersion"?:\s*")\d+(")`)

	// cellSepRe matches the padding between table cells: 2+ spaces, or a tab.
	//
	// kubectl get and kuba pad table columns with 3+ spaces, but kubectl describe with only 2+, hence 2. A single space,
	// like `--since 5m` in a printed command, stays inside a cell.
	//
	// Ex: `apple-1   1/1     Running` splits into apple-1, 1/1, Running.
	//
	// REF: https://github.com/kubernetes/cli-runtime/blob/v0.37.1/pkg/printers/tabwriter.go#L26-L30
	// REF: https://github.com/kubernetes/kubectl/blob/v0.37.1/pkg/describe/describe.go#L5114
	cellSepRe = regexp.MustCompile(`[ \t]{2,}|\t`)

	// ageRe matches a cell that's an age in kubectl's format.
	//
	// HACK: CPU quantities alone in a cell match too (`100m` in kubectl top, or describe's requests), so goldens show
	// them as <age> and can't catch a wrong value. Telling the two apart needs to know which column a cell is in, so for
	// clarity fixtures should stick to whole CPUs (`1`, not `100m`).
	//
	// Ex: 5s, 2m3s, 7h15m, 4d, 3y45d.
	//
	// REF: https://github.com/kubernetes/apimachinery/blob/v0.37.1/pkg/util/duration/duration.go#L45-L93
	ageRe = regexp.MustCompile(`^\d+[smhdy](\d+[smhd])?$`)
)

// apply rewrites volatile values to placeholders.
//
// Replacements:
//   - work dir -> $WORK
//   - server addresses -> <context>, the first context on that server
//   - timestamps -> <time>
//   - UIDs -> <uid>
//   - resourceVersions -> <rv>
//   - age cells -> <age>
func (n normalizer) apply(s string) string {
	normalizedGlobal := n.replaceValues(s)
	normalizedLines := normalizeLines(normalizedGlobal)
	return normalizedLines
}

// replaceValues swaps volatile values anywhere in s for placeholders, and strips ANSI codes, marking up colors first if
// markColor.
func (n normalizer) replaceValues(s string) string {
	if n.markColor {
		s = colorMarkup(s)
	}
	s = ansi.Strip(s)
	if n.work != "" {
		// On macOS /var links to /private/var, so tools that resolve symlinks print the work dir as /private/var/...
		// Replace both, but longer form first to avoid partial replacement.
		s = strings.ReplaceAll(s, "/private"+n.work, "$WORK")
		s = strings.ReplaceAll(s, n.work, "$WORK")
	}
	longestFirst := func(a, b string) int { return cmp.Compare(len(b), len(a)) } // avoid partial replacements
	for _, hostPort := range slices.SortedFunc(maps.Keys(n.servers), longestFirst) {
		s = strings.ReplaceAll(s, hostPort, "<"+n.servers[hostPort]+">")
	}
	s = rfc3339Re.ReplaceAllString(s, "<time>")
	s = rfc1123Re.ReplaceAllString(s, "<time>")
	s = uidRe.ReplaceAllString(s, "<uid>")
	s = rvRe.ReplaceAllString(s, "${1}<rv>${2}")
	return s
}

// sgrRe matches an SGR escape sequence, which sets colors and styles, keeping its parameters in group 1.
//
// Ex: \x1b[1;33m (bold yellow), \x1b[38;5;208m (256-color orange), \x1b[0m and \x1b[m (reset).
//
// REF: https://en.wikipedia.org/wiki/ANSI_escape_code#Select_Graphic_Rendition_parameters
var sgrRe = regexp.MustCompile(`\x1b\[([0-9;:]*)m`)

// colorMarkup rewrites each SGR escape sequence in s as one {...} of readable names, so a golden shows what's colored
// how.
//
// Ex: \x1b[1;33m -> {bold yellow}, \x1b[38;5;208m -> {fg:208}, \x1b[48;2;0;0;255m -> {bg:#0000ff}, \x1b[0m -> {/}.
func colorMarkup(s string) string {
	return sgrRe.ReplaceAllStringFunc(s, func(seq string) string {
		params := strings.FieldsFunc(sgrRe.FindStringSubmatch(seq)[1], func(r rune) bool { return r == ';' || r == ':' })
		if len(params) == 0 {
			return "{/}"
		}
		var names []string
		for i := 0; i < len(params); i++ {
			name, extra := sgrName(params[i:])
			names = append(names, name)
			i += extra
		}
		return "{" + strings.Join(names, " ") + "}"
	})
}

// sgrName names the SGR parameter at params[0], returning how many more parameters it used (an extended color's).
//
// Unknown parameters keep their number, so nothing silently disappears from a golden.
func sgrName(params []string) (name string, extra int) {
	colors := []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}
	p, err := strconv.Atoi(params[0])
	switch {
	case err != nil:
		return params[0], 0
	case p == 0:
		return "/", 0
	case p >= 30 && p <= 37:
		return colors[p-30], 0
	case p >= 90 && p <= 97:
		return "bright-" + colors[p-90], 0
	case p >= 40 && p <= 47:
		return "on-" + colors[p-40], 0
	case p >= 100 && p <= 107:
		return "on-bright-" + colors[p-100], 0
	case p == 38 || p == 48:
		layer := "fg:"
		if p == 48 {
			layer = "bg:"
		}
		switch {
		case len(params) >= 3 && params[1] == "5":
			return layer + params[2], 2
		case len(params) >= 5 && params[1] == "2":
			var rgb [3]int
			for j := range rgb {
				rgb[j], _ = strconv.Atoi(params[2+j])
			}
			return fmt.Sprintf("%s#%02x%02x%02x", layer, rgb[0], rgb[1], rgb[2]), 4
		}
	}
	if name, ok := sgrStyles[p]; ok {
		return name, 0
	}
	return params[0], 0
}

// sgrStyles names the SGR parameters that aren't colors.
var sgrStyles = map[int]string{
	1: "bold", 2: "faint", 3: "italic", 4: "underline", 5: "blink", 7: "reverse", 8: "hidden", 9: "strike",
	22: "/bold", 23: "/italic", 24: "/underline", 25: "/blink", 27: "/reverse", 28: "/hidden", 29: "/strike",
	39: "/fg", 49: "/bg",
}

// normalizeLines fixes each line's layout: it keeps indentation, collapses interior padding to two spaces, drops
// trailing spaces, and turns age cells into <age>.
//
// Padding collapses since column widths track cells that vary between runs (an age going 9s -> 10s widens its column).
func normalizeLines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n") // normalize line endings
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		indent, body := splitIndent(line)
		cells := cellSepRe.Split(body, -1)
		for j, c := range cells {
			if ageRe.MatchString(c) {
				cells[j] = "<age>"
			}
		}
		lines[i] = indent + strings.Join(cells, "  ")
	}
	return strings.Join(lines, "\n")
}

// splitIndent splits line into its leading whitespace and the rest, minus trailing whitespace.
func splitIndent(line string) (indent, body string) {
	trimmedLine := strings.TrimRight(line, " \t")
	body = strings.TrimLeft(trimmedLine, " \t")
	return trimmedLine[:len(trimmedLine)-len(body)], body
}

func TestNormalize(t *testing.T) {
	n := normalizer{
		work:    "/var/folders/x/script-fuzzy",
		servers: map[string]string{"127.0.0.1:5939": "banana", "127.0.0.1:59395": "apple"},
	}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"table padding and age", "apple-1   1/1     Running   0          5s\n", "apple-1  1/1  Running  0  <age>\n"},
		{"compound age", "egg   2m3s\n", "egg  <age>\n"},
		{"single-spaced command keeps its duration", "kubectl logs apple --since 5m\n", "kubectl logs apple --since 5m\n"},
		{"yaml indentation kept", "  labels:\n    app: apple\n", "  labels:\n    app: apple\n"},
		{"trailing spaces", "fig   \n", "fig\n"},
		{"whitespace-only line", "fig\n   \ngrape\n", "fig\n\ngrape\n"},
		{"work dir", "/private/var/folders/x/script-fuzzy/.home/.kube/config", "$WORK/.home/.kube/config"},
		{"servers, longest first", "https://127.0.0.1:59395/ and 127.0.0.1:5939", "https://<apple>/ and <banana>"},
		{"rfc3339", `creationTimestamp: "2026-10-10T21:04:05Z"`, `creationTimestamp: "<time>"`},
		{"rfc1123", "Start Time:   Sat, 10 Oct 2026 21:04:05 +0000", "Start Time:  <time>"},
		{"uid", "uid: 3f2a9c1e-0b5d-4e8f-9a7c-1d2e3f4a5b6c", "uid: <uid>"},
		{"resourceVersion yaml", `resourceVersion: "4512"`, `resourceVersion: "<rv>"`},
		{"resourceVersion json", `"resourceVersion": "4512",`, `"resourceVersion": "<rv>",`},
		{"ansi stripped", "\x1b[1mNAME\x1b[0m", "NAME"},
		{"ansi hyperlink stripped", "\x1b]8;;https://apple\x1b\\apple\x1b]8;;\x1b\\", "apple"},
		{"crlf", "grape\r\nhoney\r\n", "grape\nhoney\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, n.apply(tt.in))
		})
	}
}

func TestNormalizeMarkColor(t *testing.T) {
	n := normalizer{markColor: true}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bold then reset", "\x1b[1mNAME\x1b[0m", "{bold}NAME{/}"},
		{"bare reset", "\x1b[33mapple\x1b[m", "{yellow}apple{/}"},
		{"several params", "\x1b[1;36mapple\x1b[22;39m", "{bold cyan}apple{/bold /fg}"},
		{"bright and background", "\x1b[91;44mapple", "{bright-red on-blue}apple"},
		{"256 colors", "\x1b[38;5;208mapple", "{fg:208}apple"},
		{"truecolor", "\x1b[48;2;0;0;255mapple", "{bg:#0000ff}apple"},
		{"colon subparams", "\x1b[38:5:208mapple", "{fg:208}apple"},
		{"unknown param kept", "\x1b[53mapple", "{53}apple"},
		{"other escapes stripped", "\x1b[2K\x1b[1Gapple", "apple"},
		{"plain text untouched", "apple-1  Running", "apple-1  Running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, n.apply(tt.in))
		})
	}
}
