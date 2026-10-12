package config

import (
	"encoding"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hcgatewood/kuba/internal/exit"
	"github.com/hcgatewood/kuba/internal/flagspec"
	"github.com/hcgatewood/kuba/internal/resolve"
)

// Source is where a setting's value came from.
type Source string

// Sources, from weakest to strongest.
const (
	Default Source = "default"
	Env     Source = "env"
	Flag    Source = "flag"
)

// Setting is one option's value and where it came from.
type Setting[T any] struct {
	Value  T
	Source Source
}

// Options are kuba's settings for one invocation.
//
// Each field is a Setting, which Load fills from the field's tags: its flag, its env var, and its default, written as
// you'd set the env var.
type Options struct {
	// Clusters is KUBA_CLUSTERS, unparsed, since only commands that fan out read it.
	Clusters Setting[string] `flag:"cluster-groups" env:"KUBA_CLUSTERS"`
	// StrictClusters fails a fan-out when any cluster fails, rather than warning and going on with the rest.
	StrictClusters Setting[bool] `flag:"strict-clusters" env:"KUBA_STRICT_CLUSTERS"`
	// MaxParallel caps clusters a fan-out searches at once, or 0 for no cap, so exec auth plugins (e.g. browser logins)
	// don't all fire together.
	MaxParallel Setting[int] `flag:"max-parallel" env:"KUBA_MAX_PARALLEL" default:"8"`
	// ClusterTimeout bounds each cluster's lookups in a fan-out, or 0 for no bound, so one unreachable cluster can't
	// stall the rest. Its default gives way to --request-timeout, which bounds each request instead.
	ClusterTimeout Setting[time.Duration] `flag:"cluster-timeout" env:"KUBA_CLUSTER_TIMEOUT" default:"15s"`
	// ProgressDelay is how long a fan-out runs before a terminal shows the clusters it's still waiting on.
	ProgressDelay Setting[time.Duration] `flag:"progress-delay" env:"KUBA_PROGRESS_DELAY" default:"2s"`

	// Match is how a query matches resource names (see resolve.Mode).
	Match Setting[resolve.Mode] `flag:"match" env:"KUBA_MATCH" default:"tiered"`

	// EscapeAnnotations puts each value -o annotations prints on one line, escaping its newlines and tabs, e.g. for grep.
	EscapeAnnotations Setting[bool] `flag:"escape-annotations" env:"KUBA_ESCAPE_ANNOTATIONS"`

	// Kubectl is the command handoffs run as kubectl, as words: by default `kuba kubectl`, which colors kubectl's
	// output with the kubecolor built into kuba.
	Kubectl Setting[string] `flag:"kubectl" env:"KUBA_KUBECTL" default:"kuba kubectl"`
	// Fzf is the fzf command pickers run instead of the one built into kuba, or "".
	Fzf Setting[string] `flag:"fzf" env:"KUBA_FZF"`

	// QPS caps each cluster's API requests a second, or 0 for no cap. None by default, as kuba's requests are few, and
	// servers throttle fairly themselves (API Priority and Fairness).
	QPS Setting[int] `flag:"qps" env:"KUBA_QPS" default:"0"`
	// Burst is how many requests QPS lets through at once before its cap applies, or 0 for QPS's worth. Unused without
	// QPS, since then nothing's capped.
	Burst Setting[int] `flag:"burst" env:"KUBA_BURST" default:"0"`

	// Debug logs what kuba decides, and the commands it runs, to stderr.
	Debug Setting[bool] `flag:"debug" env:"KUBA_DEBUG"`
}

// Load reads kuba's options for one invocation, each from its flag, else its env var, else its default.
//
// Any command can call it: an option whose flag the command doesn't take comes from its env var or default.
//
// A bad flag value is a usage error (exit 2), since the command line is wrong. A bad env var is a plain error (exit 1),
// since the command line is fine, and it's the environment that needs fixing.
func Load(cmdline *flagspec.CommandLine, getenv func(string) string) (*Options, error) {
	load := loader{cmdline: cmdline, getenv: getenv}
	opts := &Options{}
	for _, opt := range opts.all() {
		raw, src := load.read(opt)
		if err := opt.field.set(raw, src); err != nil {
			return nil, opt.badValue(src, err)
		}
	}
	return opts, nil
}

// KubectlArgv is Kubectl split into words, to start a kubectl command line with, e.g. [kuba kubectl] or [kubecolor].
func (o *Options) KubectlArgv() []string {
	return strings.Fields(o.Kubectl.Value)
}

// all lists every option, reading each field's tags.
func (o *Options) all() []option {
	val := reflect.ValueOf(o).Elem()
	var opts []option
	for i := range val.NumField() {
		f := val.Type().Field(i)
		opts = append(opts, option{
			name:  f.Name,
			flag:  f.Tag.Get("flag"),
			env:   f.Tag.Get("env"),
			def:   f.Tag.Get("default"),
			field: val.Field(i).Addr().Interface().(setter),
		})
	}
	return opts
}

// option is one Options field, with the sources its tags name.
type option struct {
	// name is the field's name, e.g. Kubectl.
	name string
	// flag is its flag, e.g. kubectl.
	flag string
	// env is its env var, e.g. KUBA_KUBECTL.
	env string
	// def is its default, written as you'd set the env var, e.g. `kuba kubectl`.
	def string
	// field points at the field in Options, so set fills it in place, whatever its type.
	field setter
}

// badValue explains a value the option couldn't parse, by where the value came from.
//
// A bad flag is a usage error, and a bad env var a plain one (see Load). A bad default is a bug in the field's tags.
func (o option) badValue(src Source, err error) error {
	switch src {
	case Flag:
		return exit.Usagef("--%s: %w", o.flag, err)
	case Env:
		return fmt.Errorf("%s: %w", o.env, err)
	default:
		panic(fmt.Sprintf("config: bad default for %s: %v", o.name, err))
	}
}

// setter is a *Setting of any type, which Load fills without knowing the type.
type setter interface {
	set(raw string, src Source) error
}

// set parses raw into the setting's value, and records where it came from.
func (s *Setting[T]) set(raw string, src Source) error {
	switch v := any(&s.Value).(type) {
	case *bool:
		*v = parseBool(raw)
	case *string:
		*v = raw
	case *int:
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return fmt.Errorf("want a whole number, got %q", raw)
		}
		*v = n
	case *time.Duration:
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			return fmt.Errorf("want a duration, e.g. 30s, got %q", raw)
		}
		*v = d
	case encoding.TextUnmarshaler:
		if err := v.UnmarshalText([]byte(raw)); err != nil {
			return err
		}
	default:
		panic(fmt.Sprintf("config: can't load a %T", s.Value))
	}
	s.Source = src
	return nil
}

// parseBool reads a boolean option: true for 1, true, or yes (all case-insensitive); and false for anything else.
func parseBool(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// loader reads options with their precedence.
type loader struct {
	cmdline *flagspec.CommandLine
	getenv  func(string) string
}

// read finds an option's raw value, and where it came from (flag, env var, or default).
func (l loader) read(opt option) (string, Source) {
	if v, ok := l.cmdline.Lookup(opt.flag); ok {
		return v, Flag
	}
	if v := l.getenv(opt.env); v != "" {
		return v, Env
	}
	return opt.def, Default
}
