// Package resolve turns a command's queries into the objects it acts on: it matches names, then picks among matches.
//
// # Matching
//
// Candidates are every object the command's lookup found, across every cluster, namespace, and type, in one list.
//
// Each query picks its tier once, across all of them. By default that's exact names, else names containing the query,
// else names with its letters in order. Mode lists the other rules.
//
// Clusters, namespaces, and types are all just groupings. So a cluster missing the best match shows nothing, rather
// than its next-best names.
//
// MatchAll returns Matches:
//   - Hits: the candidates any query matched
//   - Unmatched: the queries that matched nothing. They don't stop the rest, and Closest suggests names for their
//     error.
//   - Settled: whether the hits are already just what was asked for
//
// # Picking
//
// Picker.Pick narrows the hits by the Select mode. It asks fzf only when a real choice remains, using the fzf built
// into kuba unless --fzf names another.
package resolve
