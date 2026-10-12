// Package exit ends a run of kuba, with the right exit code or by the signal that stopped it.
//
// Run wraps a whole command. Code maps the error it returns to an exit code:
//   - 0: success
//   - 1: any other error, including one that depends on what kuba finds or reads
//   - 2: a usage error (UsageError), i.e. a command line that can't work whatever the clusters hold
//   - 130: canceling fzf, the code shells use for Ctrl-C
//   - 128+n: signal n stopped the run. Where it can, kuba dies by the signal itself, which shells report the same way.
//   - Otherwise: the final command's own code (Exited), as if you'd run it directly
//
// # Ctrl-C
//
// Ctrl-C stops kuba in two steps:
//  1. The first cancels the run's context, so lookups stop and the run winds down. Then kuba dies by the signal, so a
//     shell loop running kuba stops too.
//  2. A second kills kuba at once, in case winding down hangs.
//
// One exception: if a command kuba ran picks its own exit code on Ctrl-C, that code stands. E.g. kubectl logs -f
// exits 1.
package exit
