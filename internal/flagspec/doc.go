// Package flagspec parses a kuba command's arguments, giving every flag exactly one role.
//
// # Specs
//
// A Spec lists every flag a command accepts:
//   - The flags every command takes, e.g. --debug
//   - The command's own kuba flags, e.g. get's --select
//   - Every flag of the kubectl command it wraps, from kubectlflags
//
// So any kubectl flag works anywhere on the line. A flag missing from the spec is a usage error, typo or not, since
// kuba can't tell whether an unknown flag takes a value. pflag does the tokenizing.
//
// # Roles
//
// A flag's Role says what kuba does with it. After kuba's own flags, the roles follow a request's path: reach a
// cluster, choose where to look, narrow the lookup, then show the result.
//
//	Kuba         kuba's own options, e.g. --match; never reach kubectl
//	Connection   how to reach a cluster, e.g. --as; apply to every request, and reach kubectl verbatim
//	Target       where to look and what to pick, e.g. -n, -A, --context
//	Filter       what to keep, e.g. -l; narrow lookups server-side
//	Output       how to show it, e.g. -o; choose the final step, and reach it once, in canonical form
//	Passthrough  every other kubectl flag; reach the final command verbatim, in order
//
// A kubectl flag gets Connection if it's global, or Passthrough if it's the command's own. kubectlRoles lists the
// exceptions: the flags that change what kuba does, e.g. -n, -l, and -o.
//
// # Parsing
//
// Parse returns a CommandLine: the positional arguments, plus every flag given, with its role, in order. Commands
// then:
//   - Read flags by name (Value, Bool, Values) or by role (ByRole), e.g. to pass every Connection flag on
//   - Write the flags they set with Emit. It refuses a passthrough flag, since the user's own already reaches the
//     final command.
package flagspec
