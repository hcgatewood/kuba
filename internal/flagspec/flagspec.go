package flagspec

import (
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/samber/lo"
	"github.com/spf13/pflag"

	"github.com/hcgatewood/kuba/internal/exit"
)

// Role decides whether a flag shapes kuba's lookups, and how it reaches the final command.
//
// After kuba's own, the roles follow a request: reach a cluster, choose where to look, narrow the lookup, then render.
type Role string

const (
	// Kuba flags are kuba's own options, and never reach the final command.
	Kuba Role = "kuba"
	// Connection flags apply to every request, and reach the final command verbatim.
	Connection Role = "connection"
	// Target flags say where kuba looks and what it picks, e.g. -n, -A, --context.
	Target Role = "target"
	// Filter flags narrow lookups server-side, e.g. -l, --field-selector.
	Filter Role = "filter"
	// Output flags choose the final step, and reach it once, in canonical form, where it needs them.
	Output Role = "output"
	// Passthrough flags are every other kubectl flag; they reach the final command verbatim, in order.
	Passthrough Role = "passthrough"
)

// Flag is one flag a spec accepts.
type Flag struct {
	// Name is the long name, without dashes.
	Name string
	// Short is the one-letter name, if any.
	Short string
	// Role is what kuba does with the flag: use it itself, act on it, or pass it to kubectl (see Role).
	Role Role
	// Bool flags take no value, but accept --flag=false.
	Bool bool
	// Usage is the help line of one of kuba's own flags. A backticked word in it names the flag's value in help, as
	// pflag does, e.g. --kubectl command.
	Usage string
	// Hidden flags work, but stay out of help, e.g. ones only the shell functions use.
	Hidden bool
}

// AddTo declares the flag on a pflag set: a boolean as a bool flag, anything else as a string one.
func (f Flag) AddTo(fs *pflag.FlagSet) {
	if f.Bool {
		fs.BoolP(f.Name, f.Short, false, f.Usage)
	} else {
		fs.StringP(f.Name, f.Short, "", f.Usage)
	}
}

// Spec is the flags one command accepts, in declaration order: the common flags every command takes, then its own.
type Spec []Flag

// New composes a command's spec from the common flags, then groups of local ones.
//
// It panics when two flags share a name, e.g. a Kuba flag that clashes with one of kubectl's.
func New(groups ...[]Flag) Spec {
	var s Spec
	for _, f := range slices.Concat(common, lo.Flatten(groups)) {
		s = s.add(f)
	}
	return s
}

// LocalFlags lists the flags only this command has, i.e. all but the common ones, in declaration order.
func (s Spec) LocalFlags() []Flag {
	return s[len(common):]
}

// Lookup finds a flag by long name, e.g. output for --output, or returns nil.
func (s Spec) Lookup(name string) *Flag {
	return s.find(func(f Flag) bool { return f.Name == name })
}

// Parse splits argv into positionals and flags.
//
// Spec flags can go anywhere among the positionals, e.g. `pod -n kiwi apple`, until `--`, after which everything is
// positional. They take these forms:
//
//	flag            forms
//	----            -----
//	boolean         --watch, --watch=false, -w, -w=false, -Aw (for -A -w)
//	takes a value   --output wide, --output=wide, -o wide, -o=wide, -owide
//
// A boolean takes a value only after `=`, so in `--watch false`, false is a positional.
//
// Usage errors (exit 2):
//   - An unknown flag: a typo or one newer than the pinned kubectl, since kuba can't tell whether `--banana 1` names
//     the pod 1; `kuba kubectl` runs such commands as is
//   - A malformed flag: e.g. `-n` with no value, or `--watch=maybe`
func (s Spec) Parse(args []string) (*CommandLine, error) {
	cmdline := &CommandLine{spec: s}

	fs := pflag.NewFlagSet("kuba", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)

	for _, f := range s {
		f.AddTo(fs)
	}
	if err := fs.ParseAll(args, cmdline.addFlag); err != nil {
		return nil, exit.Usagef("%w", err)
	}

	cmdline.Args = fs.Args()
	cmdline.DashIndex = fs.ArgsLenAtDash()
	return cmdline, nil
}

// Emit renders a flag for a final command in canonical form: `--name=value`, or `--name` for a true boolean.
//
// It panics on a flag the spec doesn't declare, a Kuba flag, or a passthrough flag. Passthrough flags already reach
// the final command as given, so emitting one would pass it twice, with kuba's value silently overriding yours.
func (s Spec) Emit(name, value string) string {
	f := s.Lookup(name)
	switch {
	case f == nil:
		panic(fmt.Sprintf("flagspec: emit of undeclared --%s", name))
	case f.Role == Kuba:
		panic(fmt.Sprintf("flagspec: emit of kuba's own --%s", name))
	case f.Role == Passthrough:
		panic(fmt.Sprintf("flagspec: emit of passthrough --%s", name))
	}
	return FlagValue{Flag: *f, Value: value}.String()
}

// CommandLine is a command line as Parse read it: its positionals, and its flags, each with its role.
type CommandLine struct {
	// Args are the positionals, in order.
	Args []string
	// Flags are the flags given, in order.
	Flags []FlagValue

	// DashIndex is where a `--` fell in Args, so Args[:DashIndex] came before it, or -1 without one.
	DashIndex int

	// spec is what Flags were parsed against, so Value et al can panic on a name it lacks, i.e. a typo in kuba's code.
	spec Spec
}

// Has reports whether a flag was given at all.
func (c *CommandLine) Has(name string) bool {
	c.spec.mustHave(name)
	return slices.ContainsFunc(c.Flags, func(f FlagValue) bool { return f.Name == name })
}

// Value is a flag's last value, or "" if it wasn't given.
func (c *CommandLine) Value(name string) string {
	return lo.LastOrEmpty(c.Values(name))
}

// Values lists every value a flag was given, in order, e.g. for repeated -L.
func (c *CommandLine) Values(name string) []string {
	c.spec.mustHave(name)
	return lo.FilterMap(c.Flags, func(f FlagValue, _ int) (string, bool) { return f.Value, f.Name == name })
}

// Lookup is a flag's last value, and whether it was given.
//
// Unlike Value, it doesn't panic on a flag the spec lacks, but reports it as not given. That's for code that runs under
// any command's spec, e.g. config.Load, which looks up --match even under Root, whose spec lacks it.
func (c *CommandLine) Lookup(name string) (string, bool) {
	if c.spec.Lookup(name) == nil {
		return "", false
	}
	return lo.Last(c.Values(name))
}

// Bool reports whether a boolean flag's last value is true.
func (c *CommandLine) Bool(name string) bool {
	return c.Value(name) == "true"
}

// ByRole lists the flags given with any of the roles, in the order given.
func (c *CommandLine) ByRole(roles ...Role) []FlagValue {
	return lo.Filter(c.Flags, func(f FlagValue, _ int) bool { return slices.Contains(roles, f.Role) })
}

// addFlag records a flag pflag parsed, checking a boolean's value and normalizing it to true or false.
//
// pflag passes a bare boolean, e.g. --watch, as true, so a boolean's value is never empty.
func (c *CommandLine) addFlag(pf *pflag.Flag, value string) error {
	f := c.spec.Lookup(pf.Name)
	if f.Bool {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid argument %q for --%s", value, f.Name)
		}
		value = strconv.FormatBool(b) // normalized to "true" or "false"
	}
	c.Flags = append(c.Flags, FlagValue{Flag: *f, Value: value})
	return nil
}

// FlagValue is a flag with one value, e.g. one given on the command line, or one kuba passes to kubectl (see Emit).
type FlagValue struct {
	Flag
	// Value is the flag's value; a boolean's is "true" or "false".
	Value string
}

// String renders the flag in canonical form: `--name=value`, or `--name` for a true boolean.
func (v FlagValue) String() string {
	if v.Bool && v.Value == "true" {
		return "--" + v.Name
	}
	return "--" + v.Name + "=" + v.Value
}

// add is s with a flag appended, panicking if its long or short name is taken.
func (s Spec) add(f Flag) Spec {
	if s.Lookup(f.Name) != nil {
		panic(fmt.Sprintf("flagspec: --%s declared twice", f.Name))
	}
	if s.lookupShort(f.Short) != nil {
		panic(fmt.Sprintf("flagspec: -%s declared twice", f.Short))
	}
	return append(s, f)
}

// lookupShort finds a flag by one-letter name, e.g. o for -o, or returns nil, as it does for "", i.e. no short name.
func (s Spec) lookupShort(short string) *Flag {
	if short == "" {
		return nil
	}
	return s.find(func(f Flag) bool { return f.Short == short })
}

// mustHave panics unless the spec declares a flag, so a typo in kuba's own code fails loudly, rather than reading as a
// flag never given.
func (s Spec) mustHave(name string) {
	if s.Lookup(name) == nil {
		panic(fmt.Sprintf("flagspec: no flag --%s in spec", name))
	}
}

// find is the first flag that matches, or nil.
func (s Spec) find(match func(Flag) bool) *Flag {
	if i := slices.IndexFunc(s, match); i >= 0 {
		return &s[i]
	}
	return nil
}
