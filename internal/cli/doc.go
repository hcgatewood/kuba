// Package cli is kuba's commands, e.g. get and kubectl.
//
// Cobra dispatches commands and renders their help. Each command then parses and runs itself.
//
// Main is the entry point. It runs one command under exit.Run, which turns the result into kuba's exit code.
//
// # Parsing
//
// Commands parse their own arguments with flagspec, not cobra. Kuba accepts every flag of the kubectl command it
// wraps, anywhere on the line, and needs to know what each one is for, which cobra's parser can't tell it.
//
// Cobra still registers two kinds of flags:
//   - Kuba's own, so they show in help
//   - kubectl's global flags, hidden, so cobra can find the command word. Without them,
//     `kuba --insecure-skip-tls-verify get pod` would read get as that flag's value.
//
// # get
//
// get ends in one of three ways, checked in order:
//  1. Native: kuba prints the rows itself. This needs default, wide, or name output, and no flag only kubectl can
//     honor. A fan-out adds a CLUSTER column.
//  2. Listing handoff: kubectl lists, with every flag given, and kuba looks nothing up. This needs no query, no
//     picker, no --leader, and one cluster.
//  3. Named handoff: everything else. Kuba resolves names, then runs one kubectl get per cluster and namespace. For
//     json, yaml, or kyaml that would take several, kuba prints one List itself instead.
//
// Handoffs are action values, so --command prints exactly what would run.
//
// # kubectl
//
// `kuba kubectl` is kubectl with kubecolor built in. Handoffs run it by default (see --kubectl), so handed-off output
// is colored without installing kubecolor.
package cli
