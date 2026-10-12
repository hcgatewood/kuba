package flagspec

// Root is the spec of `kuba` with no command, e.g. `kuba --debug`: just the common flags.
var Root = New()

// Get is the spec of `kuba get`: kuba's own flags, then every flag of `kubectl get`, each with its role.
//
// Help lists kuba's flags in the order New gets them, which follows a lookup: where to look, what matches, which to act
// on, how to print, then what to do.
var Get = New(clustersGroup, matchGroup, leaderGroup, selectGroup, outputGroup, commandGroup, Kubectl("get"))

// common flags work on every command, before or after its name, so New puts them in every spec. Help lists them under
// Global Flags.
var common = []Flag{
	{Name: "help", Short: "h", Role: Kuba, Bool: true, Usage: "Show this help."},
	{Name: "debug", Role: Kuba, Bool: true, Usage: "Log parsed flags, API requests, and the final action to stderr (env: KUBA_DEBUG)."},
	{Name: "kubectl", Role: Kuba, Usage: "The `command` to hand off to, e.g. kubecolor; defaults to kuba kubectl (env: KUBA_KUBECTL)."},
	{Name: "fzf", Role: Kuba, Usage: "The fzf `command` pickers run, rather than the one built into kuba (env: KUBA_FZF)."},
	{Name: "qps", Role: Kuba, Usage: "Cap each cluster's API requests at `n` a second; 0, the default, doesn't cap them (env: KUBA_QPS)."},
	{Name: "burst", Role: Kuba, Usage: "Under --qps, let `n` requests through at once before the cap applies; 0, the default, means the --qps value (env: KUBA_BURST)."},
}

// clustersGroup searches several clusters, i.e. kubeconfig contexts, at once.
var clustersGroup = []Flag{
	// Picks which clusters: the current one and its siblings
	{Name: "multi-cluster", Short: "m", Role: Kuba, Bool: true, Usage: "Search the current cluster and its siblings, as KUBA_CLUSTERS groups them."},
	// Picks which clusters: the ones it names
	{Name: "clusters", Role: Kuba, Usage: "Search this comma-separated `list` of contexts or KUBA_CLUSTERS aliases, e.g. 'prod-*'."},
	// Decides what a failing cluster does
	{Name: "strict-clusters", Role: Kuba, Bool: true, Usage: "Fail if any cluster errors, rather than warn and show the rest (env: KUBA_STRICT_CLUSTERS)."},
	// Paces the search, so exec auth plugins (e.g. browser logins) don't all fire together
	{Name: "max-parallel", Role: Kuba, Usage: "Search at most `n` clusters at once; 8 by default, and 0 means no cap (env: KUBA_MAX_PARALLEL)."},
	// Bounds the search, so one unreachable cluster can't stall the rest
	{Name: "cluster-timeout", Role: Kuba, Usage: "Give up on a cluster after this `duration`, e.g. 30s, or 0 for never; 15s by default, but never under --request-timeout (env: KUBA_CLUSTER_TIMEOUT)."},
	// Shows who's slow, on a terminal
	{Name: "progress-delay", Role: Kuba, Usage: "Show the clusters still pending once a search runs this `duration`; 2s by default (env: KUBA_PROGRESS_DELAY)."},
	// Defines the sibling groups -m uses and the aliases --clusters takes (KUBA_CLUSTERS)
	{Name: "cluster-groups", Role: Kuba, Usage: "Group and alias clusters by these comma-separated alias=pattern `entries`, e.g. 'p*=prod-*' (env: KUBA_CLUSTERS)."},
}

// matchGroup chooses how a query matches resource names (see resolve.Mode).
var matchGroup = []Flag{
	{Name: "match", Role: Kuba, Usage: "Name-matching `mode`: tiered (default), exact, substring, or fuzzy (env: KUBA_MATCH)."},
}

// leaderGroup narrows matches to resources holding a leader-election lease, e.g. a controller's active replica.
var leaderGroup = []Flag{
	{Name: "leader", Role: Kuba, Bool: true, Usage: "Only match resources holding a leader-election lease."},
}

// selectGroup overrides what the output format does when several resources match.
var selectGroup = []Flag{
	// Asks fzf when several match, for any number of picks, as formats like -o yaml do by default
	{Name: "select", Role: Kuba, Bool: true, Usage: "Pick with fzf when several resources match, whatever the output format."},
	// Takes every match, as default, wide, and name output do by default; the later of it and --select wins
	{Name: "no-select", Role: Kuba, Bool: true, Usage: "Act on every match rather than pick, whatever the output format."},
	// Asks fzf for exactly one pick, whatever the other two say
	{Name: "one", Role: Kuba, Bool: true, Usage: "Act on exactly one resource, picking with fzf if several match."},
}

// outputGroup tunes the output kuba renders itself, rather than kubectl's, e.g. -o annotations.
var outputGroup = []Flag{
	{Name: "escape-annotations", Role: Kuba, Bool: true, Usage: "Escape -o annotations values onto one line each, e.g. for grep (env: KUBA_ESCAPE_ANNOTATIONS)."},
}

// commandGroup shapes kuba's last step, the kubectl command it runs, mostly for the shell functions.
var commandGroup = []Flag{
	// Prints the command instead, which a shell function can then run in your shell, e.g. to add it to history
	{Name: "command", Role: Kuba, Bool: true, Usage: "Print the kubectl command rather than run it (output kuba renders itself is unchanged)."},
	// Hidden, as only the shell functions pass it: fails with the given reason if kuba needs several commands, e.g.
	// "using fx" when the output goes to fx
	{Name: "must-single-command", Role: Kuba, Hidden: true, Usage: "Fail with this reason if it would take several commands."},
}
