// Package config is kuba's options: each comes from its flag, else its env var, else its default.
//
// Options lists every option as a struct field, whose tags give its flag, env var, and default:
//
//	Match Setting[resolve.Mode] `flag:"match" env:"KUBA_MATCH" default:"tiered"`
//
// Defaults are written the way you'd set the env var. A config file will later slot in between env vars and defaults.
//
// Each field is a Setting, which remembers where its value came from (its Source). That's for debug logs and, later,
// `kuba config show`.
//
// # Loading
//
// Load reads every option for any command. An option whose flag the command doesn't take still gets its env var or
// default.
//
// A bad value fails based on where it came from:
//   - From a flag: a usage error (exit 2), since the command line is wrong
//   - From an env var: a plain error (exit 1), since the command line is fine and the environment needs fixing
//
// # Clusters
//
// Clusters reads KUBA_CLUSTERS, a list of `alias=pattern` entries over kubeconfig's contexts. They group contexts into
// siblings, which -m searches together, and name them for --clusters. See Clusters for the grammar.
package config
