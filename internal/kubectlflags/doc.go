// Package kubectlflags lists kubectl's flags, generated from the kubectl version go.mod pins.
//
// It knows only kubectl. What kuba does with each flag lives in flagspec.
//
// Commands maps each kubectl command kuba wraps, e.g. get, to its flags: their names, and whether each takes a value.
// Its options entry holds the global flags every command takes, as `kubectl options` lists them. Verb uses those to
// find a command line's verb, past any global flags and their values.
//
// # Regenerating
//
// TestTables builds the tables from kubectl's own command tree, so they can't drift from what the pinned kubectl
// accepts:
//   - `make gen` (go generate) rewrites zz_generated.flags.go
//   - TestTables fails while it's stale
//
// So bumping kubectl is a go.mod change, then `make gen`.
package kubectlflags
