package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/hcgatewood/kuba/internal/action"
	"github.com/hcgatewood/kuba/internal/config"
	"github.com/hcgatewood/kuba/internal/exit"
	"github.com/hcgatewood/kuba/internal/flagspec"
	"github.com/hcgatewood/kuba/internal/kube"
	"github.com/hcgatewood/kuba/internal/render"
	"github.com/hcgatewood/kuba/internal/resolve"
)

// getCmd is `kuba get`.
func (a *app) getCmd(ctx context.Context) *cobra.Command {
	get := &cobra.Command{
		Use:                "get TYPE [NAME...] [flags]",
		Short:              "Display resources by fuzzy name, across namespaces and clusters",
		Long:               getLong,
		Example:            getExample,
		GroupID:            groupResources,
		DisableFlagParsing: true,
		RunE:               func(cmd *cobra.Command, args []string) error { return a.get(ctx, cmd, args) },
	}
	registerFlags(get.Flags(), flagspec.Get.LocalFlags())
	return get
}

// getLong is get's help text, above its usage line.
const getLong = `Display resources by fuzzy name, across namespaces and clusters.

TYPE is anything kubectl get takes: pod, po, deployments.apps, a CRD's short name, pod,deploy, or all.

Each NAME is a name or part of one. An exact name wins, then names containing it, then names with its letters in
order; --match picks another rule. A lone NAME containing = or ! is a label selector, as with -l.

Default, wide, and name output list every match. Other formats, like -o yaml, have you pick with fzf when several
resources match; --select, --no-select, and --one override that. Besides kubectl's formats, -o takes labels and
annotations, which print each resource's labels or annotations.

Every kubectl get flag works too, anywhere on the line, as do kubectl's global flags; see kubectl get --help and
kubectl options.`

// getExample is get's help examples, below its usage line.
const getExample = `  # List pods whose names match api, in the current namespace
  kuba get pod api

  # Print a matching deployment's YAML, picking with fzf if several match
  kuba get deploy api -o yaml

  # List matching pods in every namespace, on this cluster and its KUBA_CLUSTERS siblings
  kuba get pod api -A -m

  # Print the kubectl command rather than run it
  kuba get deploy api -o yaml --command`

// get runs `kuba get`, taking the first of three paths that fits.
//
//  1. Native: default, wide, or name output, and no flag only kubectl renders: a passthrough flag, --sort-by, or a
//     watch. Kuba looks up the rows, matches them, and prints them itself.
//  2. Listing handoff: nothing to look up, i.e. no query, no picker that might ask, no --leader, one cluster, and no
//     labels or annotations. kubectl lists, with every flag given, e.g. `kuba get pod --sort-by=.metadata.name`.
//     Formats other than default, wide, and name ask by default, so a plain `kuba get pod -o yaml` is named instead.
//  3. Named: anything else. Kuba resolves the names, then runs one kubectl get per (cluster, namespace), unless it
//     prints the output itself: labels, annotations, or json, yaml, or kyaml spanning several groups as one List.
func (a *app) get(ctx context.Context, cmd *cobra.Command, argv []string) error {
	cmdline, err := flagspec.Get.Parse(argv)
	if err != nil {
		return err
	}
	if cmdline.Bool("help") {
		return cmd.Help()
	}
	if f := cmdline.Value("output"); slices.Contains(unportedFormats, f) {
		return fmt.Errorf("-o %s isn't in the Go port yet", f)
	}
	g, err := a.newGetRun(cmdline)
	if err != nil {
		return err
	}
	switch {
	case g.native():
		return g.runNative(ctx)
	case g.listing():
		return g.runListing(ctx)
	default:
		return g.runNamed(ctx)
	}
}

// unportedFormats are kuba's own -o formats the Go port doesn't have yet, which get rejects rather than hand kubectl
// a format it doesn't know.
var unportedFormats = []string{
	"describe", "fx", "events", "logs", "logs-follow", "children", "parents", "pods", "node", "containers",
}

// getRun is one `kuba get`.
type getRun struct {
	*app
	cmdline *flagspec.CommandLine
	opts    *config.Options
	log     *slog.Logger
	printer render.Printer

	// typeArg is TYPE as given, e.g. pod or deploy,svc.
	typeArg string
	// queries are the NAMEs, lowercased, e.g. api for `kuba get pod API`.
	queries []string
	// selector is -l, or a lone query that's a label selector.
	selector string
	// output is -o, e.g. wide or yaml, or "" for the default table.
	output string
	// mode is whether get has fzf pick among matches, and how many.
	mode resolve.Select
	// fanOut is set by --multi-cluster or --clusters, even when they reach one cluster.
	fanOut bool

	// kc is the kubeconfig lookups read, loaded once they start.
	kc *kube.Kubeconfig
	// contexts are the clusters lookups search, by kubeconfig context.
	contexts []string
	// namespace is where lookups search, or "" for every namespace.
	namespace string
}

// newGetRun reads the command line's args, options, and select mode, rejecting usage errors.
func (a *app) newGetRun(cmdline *flagspec.CommandLine) (*getRun, error) {
	if len(cmdline.Args) == 0 {
		return nil, exit.Usagef("get needs a TYPE, e.g. kuba get pod")
	}
	opts, err := config.Load(cmdline, os.Getenv)
	if err != nil {
		return nil, err
	}
	g := &getRun{
		app:      a,
		cmdline:  cmdline,
		opts:     opts,
		log:      a.newDebugLogger(opts.Debug.Value),
		printer:  render.Printer{Stdout: a.stdout, Stderr: a.stderr, IsTTY: a.isStdoutTTY},
		typeArg:  cmdline.Args[0],
		queries:  lo.Map(cmdline.Args[1:], func(arg string, _ int) string { return strings.ToLower(arg) }),
		selector: cmdline.Value("selector"),
		output:   cmdline.Value("output"),
		fanOut:   cmdline.Bool("multi-cluster") || cmdline.Has("clusters"),
	}
	if len(g.queries) == 1 && strings.ContainsAny(g.queries[0], "=!") {
		if cmdline.Has("selector") {
			return nil, exit.Usagef("%q is a label selector, as is -l; give one", cmdline.Args[1])
		}
		g.selector, g.queries = cmdline.Args[1], nil
	}
	if cmdline.Has("clusters") && (cmdline.Has("context") || cmdline.Bool("multi-cluster")) {
		return nil, exit.Usagef("--clusters names its clusters outright, so it can't go with --context or --multi-cluster")
	}
	if g.fanOut {
		for _, f := range singleEntryFlags {
			if cmdline.Has(f) {
				return nil, exit.Usagef("--%s picks one cluster entry, server, or credential, so it can't go with a fan-out", f)
			}
		}
	}
	g.mode = g.selectMode()

	for _, f := range cmdline.Flags {
		value := f.Value
		if f.Role == flagspec.Connection {
			value = "<redacted>"
		}
		g.log.Debug("flag", "name", f.Name, "role", f.Role, "value", value)
	}
	g.log.Debug("args", "type", g.typeArg, "queries", g.queries, "selector", g.selector)
	return g, nil
}

// singleEntryFlags pick one kubeconfig cluster entry, server, or credential, which a fan-out can't share.
var singleEntryFlags = []string{
	"cluster", "user", "server", "token", "username", "password",
	"certificate-authority", "client-certificate", "client-key",
}

// selectMode is the output format's default select mode, overridden by the last of --select and --no-select, then
// --one.
func (g *getRun) selectMode() resolve.Select {
	mode := resolve.SelectYes
	if isNativeFormat(g.output) {
		mode = resolve.SelectAll
	}
	for _, f := range g.cmdline.Flags {
		switch {
		case f.Name == "select":
			mode = lo.Ternary(f.Value == "true", resolve.SelectYes, resolve.SelectAll)
		case f.Name == "no-select" && f.Value == "true":
			mode = resolve.SelectAll
		}
	}
	if g.cmdline.Bool("one") {
		mode = resolve.SelectOne
	}
	return mode
}

// native reports whether kuba prints the output itself: default, wide, or name, with no flag only kubectl can honor.
func (g *getRun) native() bool {
	return isNativeFormat(g.output) && !g.kubectlOnly()
}

// kubectlOnly reports whether a flag given needs kubectl to render: a passthrough flag, --sort-by, or a watch.
func (g *getRun) kubectlOnly() bool {
	return g.firstPassthrough() != "" || g.cmdline.Has("sort-by") || g.watches()
}

// firstPassthrough is the first passthrough flag given, which only kubectl understands, or "" for none.
func (g *getRun) firstPassthrough() string {
	if ps := g.cmdline.ByRole(flagspec.Passthrough); len(ps) > 0 {
		return ps[0].String()
	}
	return ""
}

// watches reports whether -w or --watch-only was given.
func (g *getRun) watches() bool {
	return g.cmdline.Bool("watch") || g.cmdline.Bool("watch-only")
}

// listing reports whether kubectl can do the whole listing, with no lookup: no query, no picker, no --leader, one
// cluster, and no format of kuba's own.
func (g *getRun) listing() bool {
	return len(g.queries) == 0 && !g.mode.Prompts() && !g.cmdline.Bool("leader") && !g.fanOut && !isMetadataFormat(g.output)
}

// runNative looks up, matches, and picks the rows, then prints them.
func (g *getRun) runNative(ctx context.Context) error {
	l, err := g.lookup(ctx)
	if err != nil {
		return err
	}
	picked, noMatch, err := g.resolve(l)
	if err != nil || len(picked) == 0 {
		return err
	}
	return cmp.Or(g.printRows(l, picked), noMatch)
}

// runListing hands the whole listing to kubectl, with every flag given; kuba makes no lookup.
func (g *getRun) runListing(ctx context.Context) error {
	argv := append(g.opts.KubectlArgv(), "get", g.typeArg)
	for _, name := range []string{"namespace", "all-namespaces", "context", "field-selector"} {
		if g.cmdline.Has(name) {
			argv = append(argv, flagspec.Get.Emit(name, g.cmdline.Value(name)))
		}
	}
	if g.selector != "" {
		argv = append(argv, flagspec.Get.Emit("selector", g.selector))
	}
	argv = append(argv, g.handoffFlags()...)
	return g.act(ctx, []action.Action{action.Command(argv...)}, nil)
}

// runNamed resolves names, then hands them to kubectl, one command per (cluster, namespace) group, unless kuba prints
// the output itself: label and annotation maps, or json, yaml, or kyaml spanning several groups as one List.
func (g *getRun) runNamed(ctx context.Context) error {
	l, err := g.lookup(ctx)
	if err != nil {
		return err
	}
	picked, noMatch, err := g.resolve(l)
	if err != nil || len(picked) == 0 {
		return err
	}
	if isMetadataFormat(g.output) {
		return cmp.Or(g.printMetadata(l, picked), noMatch)
	}

	groups := groupByTarget(picked)
	switch {
	case len(groups) > 1 && g.watches():
		return fmt.Errorf("can't watch %s across %d namespaces or clusters in one command; narrow the query, -n, or --context", g.typeArg, len(groups))
	case len(picked) > 1 && g.cmdline.Has("sort-by"):
		return fmt.Errorf("--sort-by sorts only what kubectl lists itself, and %d %s are named; drop --sort-by, or the query", len(picked), g.typeArg)
	case len(groups) > 1 && slices.Contains(listFormats, g.output):
		return cmp.Or(g.printList(ctx, l, picked), noMatch)
	}
	actions := lo.Map(groups, func(grp []candidate, _ int) action.Action { return g.namedCommand(l, grp) })
	return g.act(ctx, actions, noMatch)
}

// namedCommand is the kubectl get naming a group's objects, which share a cluster and namespace.
//
// It passes -n and --context only where kubectl's defaults might point elsewhere: under -n, -A, --context, or a
// fan-out.
func (g *getRun) namedCommand(l *lookup, grp []candidate) action.Action {
	argv := append(g.opts.KubectlArgv(), "get")
	if !l.severalTypes() {
		argv = append(argv, g.typeArg)
	}
	for _, c := range grp {
		argv = append(argv, l.qualifiedName(c))
	}
	ns, cluster := grp[0].row.Meta.Namespace, grp[0].row.Cluster
	if ns != "" && (g.cmdline.Has("namespace") || g.cmdline.Bool("all-namespaces") || g.fanOut) {
		argv = append(argv, flagspec.Get.Emit("namespace", ns))
	}
	if g.cmdline.Has("context") || g.fanOut {
		argv = append(argv, flagspec.Get.Emit("context", cluster))
	}
	return action.Command(append(argv, g.handoffFlags()...)...)
}

// handoffFlags are the flags every handoff passes on: connection flags, output flags (each once, as last given, but
// every -L, as their columns add up), and passthrough flags as given.
//
// Off a terminal, a table also gets --no-headers, since kuba's tables print headers only on one. --command leaves it
// out, since the printed command runs later, in a terminal.
func (g *getRun) handoffFlags() []string {
	var out []string
	for _, f := range g.cmdline.ByRole(flagspec.Connection) {
		out = append(out, flagspec.Get.Emit(f.Name, f.Value))
	}
	outputs := g.cmdline.ByRole(flagspec.Output)
	for i, f := range outputs {
		givenAgain := slices.ContainsFunc(outputs[i+1:], func(later flagspec.FlagValue) bool { return later.Name == f.Name })
		if f.Name == "label-columns" || !givenAgain {
			out = append(out, flagspec.Get.Emit(f.Name, f.Value))
		}
	}
	table := g.output == "" || g.output == "wide" || strings.HasPrefix(g.output, "custom-columns")
	if table && !g.isStdoutTTY && !g.cmdline.Bool("command") && !g.cmdline.Has("no-headers") {
		out = append(out, flagspec.Get.Emit("no-headers", "true"))
	}
	for _, f := range g.cmdline.ByRole(flagspec.Passthrough) {
		out = append(out, f.String())
	}
	return out
}

// act prints or runs the final commands.
//
// Several are an error under --command, which prints exactly one, and under --must-single-command, which names why
// one is needed. With unmatched queries, kuba reports them first, still runs the rest, then exits 1.
func (g *getRun) act(ctx context.Context, actions []action.Action, noMatch error) error {
	if len(actions) > 1 {
		switch {
		case g.cmdline.Bool("command"):
			return fmt.Errorf("--command prints one command, and this needs %d, one per cluster and namespace", len(actions))
		case g.cmdline.Has("must-single-command"):
			return errors.New(g.cmdline.Value("must-single-command"))
		}
	}
	for _, a := range actions {
		g.log.Debug("action", "command", a.String())
	}
	switch {
	case g.cmdline.Bool("command"):
		_, err := fmt.Fprintln(g.stdout, actions[0].String())
		return cmp.Or(err, noMatch)
	case noMatch != nil:
		_, _ = fmt.Fprintf(g.stderr, "Error: %v\n", noMatch)
		return cmp.Or(action.RunEach(ctx, actions, g.stdout), error(&exit.CodeError{Code: 1}))
	default:
		return action.RunAll(ctx, actions, g.stdout)
	}
}

// lookup is every row kuba's lookups turned up, by section (see gather).
type lookup struct {
	sections []*render.Section
	// mappings are each section's type.
	mappings []*meta.RESTMapping
	// candidates are every row, in section order, each section's rows in cluster order.
	candidates []candidate
}

// candidate is one row a query can match: an object, its type, and where it lives.
type candidate struct {
	// section indexes the lookup's sections and mappings, and so gives the object's type.
	section int
	// row is the object's row, with its cluster.
	row render.Row
}

// qualifiedName is a candidate's name, prefixed with its type when the lookup spans several, as kubectl takes it.
func (l *lookup) qualifiedName(c candidate) string {
	if l.severalTypes() {
		return strings.ToLower(l.sections[c.section].Kind.String()) + "/" + c.row.Meta.Name
	}
	return c.row.Meta.Name
}

// severalTypes reports whether the lookup spans several types, whose names kubectl tells apart by prefixing them, e.g.
// pod/apple. A type split over sections by its columns counts once.
func (l *lookup) severalTypes() bool {
	return len(lo.UniqBy(l.mappings, func(m *meta.RESTMapping) schema.GroupResource { return m.Resource.GroupResource() })) > 1
}

// clusterRows is one cluster's lookup: each type's Table, plus its lease holders under --leader.
type clusterRows struct {
	// mappings are the types TYPE names on the cluster.
	mappings []*meta.RESTMapping
	// tables are each mapping's rows, in the same order.
	tables []*kube.Table
	// holders are the cluster's lease holders, read only under --leader.
	holders kube.LeaseHolders
}

// lookup searches every cluster at once, then gathers their rows by type.
//
// Under a fan-out, a failed cluster is a warning naming it, unless --strict-clusters (or every cluster failing) makes
// it an error.
func (g *getRun) lookup(ctx context.Context) (*lookup, error) {
	if err := g.loadClusters(); err != nil {
		return nil, err
	}
	clusters := make([]*kube.Cluster, len(g.contexts))
	for i, name := range g.contexts {
		c, err := g.kc.Cluster(name)
		if err != nil {
			return nil, err
		}
		clusters[i] = c
	}
	if err := g.stripTypePrefixes(ctx, clusters[0]); err != nil {
		return nil, err
	}

	results := runFanOut(ctx, g, clusters, g.lookupCluster)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := g.checkFailures(results); err != nil {
		return nil, err
	}
	return g.gather(results), nil
}

// loadClusters reads kubeconfig, then picks the clusters to search and the namespace to search them in.
//
// A fan-out searches every cluster in the starting cluster's namespace, unless -n or -A says otherwise.
func (g *getRun) loadClusters() error {
	limit := kube.RateLimit{QPS: g.opts.QPS.Value, Burst: g.opts.Burst.Value}
	kc, err := kube.LoadKubeconfig(g.cmdline.ByRole(flagspec.Connection), g.cmdline.Value("context"), limit, g.log, g.stderr)
	if err != nil {
		return err
	}
	g.kc = kc
	start := kc.Current()
	if !g.cmdline.Bool("all-namespaces") {
		g.namespace = cmp.Or(g.cmdline.Value("namespace"), kc.Namespace(start))
	}

	if !g.fanOut {
		g.contexts = []string{start}
		return nil
	}
	clusters, err := config.NewClusters(g.opts.Clusters.Value, kc.Contexts())
	if err != nil {
		return err
	}
	if g.cmdline.Has("clusters") {
		g.contexts, err = clusters.Select(g.cmdline.Value("clusters"), kc.Contexts())
	} else {
		g.contexts, err = clusters.Siblings(start)
	}
	return err
}

// stripTypePrefixes drops a type prefix from each query that names the same type, e.g. pod/apple or pods/apple
// under `get Pod`.
func (g *getRun) stripTypePrefixes(ctx context.Context, c *kube.Cluster) error {
	var types []*meta.RESTMapping
	for i, q := range g.queries {
		prefix, name, ok := strings.Cut(q, "/")
		if !ok {
			continue
		}
		if types == nil {
			var err error
			if types, err = c.Mappings(ctx, g.typeArg); err != nil {
				return err
			}
		}
		named, err := c.Mappings(ctx, prefix)
		if err != nil {
			continue
		}
		if slices.ContainsFunc(named, func(n *meta.RESTMapping) bool {
			return slices.ContainsFunc(types, func(t *meta.RESTMapping) bool { return t.Resource == n.Resource })
		}) {
			g.queries[i] = name
		}
	}
	return nil
}

// runFanOut calls fn on each cluster, tuned by fanOutOptions, with the progress line on a terminal past
// --progress-delay, erased before it returns.
func runFanOut[T any](ctx context.Context, g *getRun, clusters []*kube.Cluster, fn func(context.Context, *kube.Cluster) (T, error)) []kube.Result[T] {
	opts := g.fanOutOptions()
	var progress *render.Progress
	if g.isStderrTTY {
		names := lo.Map(clusters, func(c *kube.Cluster, _ int) string { return c.Name })
		progress = render.StartProgress(g.stderr, g.opts.ProgressDelay.Value, names)
		opts.OnDone = func(c *kube.Cluster) { progress.Done(c.Name) }
	}
	results := kube.FanOut(ctx, clusters, opts, fn)
	progress.Stop()
	return results
}

// fanOutOptions tune each fan-out: --max-parallel and --cluster-timeout.
//
// --request-timeout bounds each request instead of each cluster, so the default cluster timeout gives way to it, lest
// 15s cut short a request given longer. A --cluster-timeout set by flag or env var still applies.
func (g *getRun) fanOutOptions() kube.FanOutOptions {
	opts := kube.FanOutOptions{Timeout: g.opts.ClusterTimeout.Value, MaxParallel: g.opts.MaxParallel.Value}
	if g.opts.ClusterTimeout.Source == config.Default && g.cmdline.Has("request-timeout") {
		opts.Timeout = 0
	}
	return opts
}

// lookupCluster reads one cluster's rows for each type, plus its lease holders under --leader.
//
// On one cluster, with one plain query (no type/ prefix), one namespace, one type, and no selector, an exact GET of the
// query races the LIST.
func (g *getRun) lookupCluster(ctx context.Context, c *kube.Cluster) (*clusterRows, error) {
	mappings, err := c.Mappings(ctx, g.typeArg)
	if err != nil {
		return nil, err
	}
	out := &clusterRows{mappings: mappings}
	fieldSelector := g.cmdline.Value("field-selector")
	race := len(g.queries) == 1 && !strings.Contains(g.queries[0], "/") && g.namespace != "" &&
		g.selector == "" && fieldSelector == "" && len(mappings) == 1 && !g.fanOut
	for _, mapping := range mappings {
		var table *kube.Table
		if race {
			table, err = c.GetOrList(ctx, mapping, g.namespace, g.queries[0])
		} else {
			table, err = c.List(ctx, mapping, g.namespace, kube.ListOptions{LabelSelector: g.selector, FieldSelector: fieldSelector})
		}
		if err != nil {
			return nil, err
		}
		out.tables = append(out.tables, table)
	}
	if g.cmdline.Bool("leader") {
		if out.holders, err = c.LeaseHolders(ctx, g.namespace); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// checkFailures turns failed clusters into an error, or into warnings when a fan-out can go on without them.
func (g *getRun) checkFailures(results []kube.Result[*clusterRows]) error {
	var failed []string
	for _, r := range results {
		if r.Err != nil {
			failed = append(failed, fmt.Sprintf("cluster %s: %v", r.Cluster.Name, r.Err))
		}
	}
	switch {
	case len(failed) == 0:
		return nil
	case !g.fanOut:
		return results[0].Err
	case g.opts.StrictClusters.Value || len(failed) == len(results):
		return fmt.Errorf("%d of %d clusters failed:\n  %s", len(failed), len(results), strings.Join(failed, "\n  "))
	}
	for _, f := range failed {
		_, _ = fmt.Fprintf(g.stderr, "Warning: %s\n", f)
	}
	return nil
}

// gather merges the clusters' rows into a section per type, in order of first appearance, keeping only leaders under
// --leader. Nodes sort newest first.
//
// A cluster serving a type with other columns, e.g. an older CRD version's, gets a section of its own, as its rows
// wouldn't line up under the other clusters' header.
func (g *getRun) gather(results []kube.Result[*clusterRows]) *lookup {
	l := &lookup{}
	index := map[sectionKey]int{}
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		for i, m := range r.Value.mappings {
			table := r.Value.tables[i]
			key := sectionKey{resource: m.Resource.GroupResource(), columns: columnsKey(table.Columns)}
			section, ok := index[key]
			if !ok {
				section = len(l.sections)
				index[key] = section
				l.sections = append(l.sections, &render.Section{
					Kind:       m.GroupVersionKind.GroupKind(),
					Namespaced: m.Scope.Name() == meta.RESTScopeNameNamespace,
					Columns:    table.Columns,
				})
				l.mappings = append(l.mappings, m)
			}
			for _, row := range table.Rows {
				if g.cmdline.Bool("leader") && !r.Value.holders.Hold(row.Meta.Namespace, row.Meta.Name) {
					continue
				}
				l.sections[section].Rows = append(l.sections[section].Rows, render.Row{Cluster: r.Cluster.Name, Row: row})
			}
		}
	}
	for i, s := range l.sections {
		if isNodes(l.mappings[i]) {
			slices.SortStableFunc(s.Rows, func(a, b render.Row) int {
				return b.Meta.CreationTimestamp.Compare(a.Meta.CreationTimestamp.Time)
			})
		}
		for _, row := range s.Rows {
			l.candidates = append(l.candidates, candidate{section: i, row: row})
		}
	}
	return l
}

// sectionKey is what rows need in common to share a section: their type, and the columns their cells fill.
type sectionKey struct {
	resource schema.GroupResource
	columns  string
}

// columnsKey is a Table's columns as kubectl's printer reads them: names, types, formats, and priorities.
//
// Descriptions are left out, as servers word them differently across versions, which shouldn't split a section.
func columnsKey(cols []metav1.TableColumnDefinition) string {
	return strings.Join(lo.Map(cols, func(c metav1.TableColumnDefinition, _ int) string {
		return fmt.Sprintf("%s|%s|%s|%d", c.Name, c.Type, c.Format, c.Priority)
	}), ",")
}

// resolve matches the queries against the candidates, then picks among the hits.
//
// It returns what to act on, plus an error naming any query that matched nothing, which the caller reports after
// acting on the rest. Nothing at all to act on, with no query, prints kubectl's "No resources found" and succeeds.
func (g *getRun) resolve(l *lookup) (picked []candidate, noMatch, err error) {
	names := lo.Map(l.candidates, func(c candidate, _ int) string { return c.row.Meta.Name })
	m := resolve.MatchAll(g.opts.Match.Value, g.queries, names)
	if len(m.Unmatched) > 0 {
		noMatch = g.noMatchError(l, m.Unmatched, names)
	}
	if len(m.Hits) == 0 {
		if noMatch != nil {
			return nil, nil, noMatch
		}
		g.noResources(l)
		return nil, nil, nil
	}

	picker := resolve.Picker{Type: g.typeArg, Fzf: g.opts.Fzf.Value}
	if len(g.queries) == 1 {
		picker.Query = g.queries[0]
	}
	if g.mode.Prompts() {
		picker.Header, picker.Lines = g.pickerLines(l)
	}
	idx, err := picker.Pick(g.mode, m)
	if err != nil {
		return nil, nil, err
	}
	return lo.Map(idx, func(i, _ int) candidate { return l.candidates[i] }), noMatch, nil
}

// pickerLines renders every candidate as fzf shows it: its listing row, under the header of the first section with
// rows.
func (g *getRun) pickerLines(l *lookup) (header string, lines []string) {
	for i, s := range l.sections {
		h, rows := render.Listing{Section: *s, Options: g.listingOptions(l, i)}.Lines()
		header = cmp.Or(header, h)
		lines = append(lines, rows...)
	}
	return header, lines
}

// listingOptions shape a section's columns: wide, CLUSTER on a fan-out, NAMESPACE with -A, the kind prefix with
// several types or --show-kind, the node extras, then -L's label columns and --show-labels.
func (g *getRun) listingOptions(l *lookup, section int) render.ListingOptions {
	o := render.ListingOptions{
		Wide:          g.output == "wide",
		Cluster:       g.fanOut,
		AllNamespaces: g.namespace == "",
		WithKind:      l.severalTypes() || g.cmdline.Bool("show-kind"),
		ShowLabels:    g.cmdline.Bool("show-labels"),
	}
	if isNodes(l.mappings[section]) {
		o.LabelColumns = []string{"node.kubernetes.io/instance-type"}
		if o.Wide {
			o.LabelColumns = append(o.LabelColumns, "kubernetes.io/hostname")
		}
	}
	for _, v := range g.cmdline.Values("label-columns") {
		o.LabelColumns = append(o.LabelColumns, strings.Split(v, ",")...) // e.g. -L app,tier, as kubectl splits it
	}
	o.LabelColumns = lo.Uniq(o.LabelColumns) // e.g. -L node.kubernetes.io/instance-type, which nodes list already
	return o
}

// printRows prints picked rows as tables, one per section, or as names for -o name.
//
// Headers print only on a terminal, and not with --no-headers; with several types, a blank line separates tables.
func (g *getRun) printRows(l *lookup, picked []candidate) error {
	var b strings.Builder
	if g.output == "name" {
		for _, c := range picked {
			b.WriteString(l.qualifiedName(c) + "\n")
		}
		return g.printer.Print(b.String(), "get", g.typeArg, "--output=name")
	}

	headers := g.isStdoutTTY && !g.cmdline.Bool("no-headers")
	for i, s := range l.sections {
		sub := *s
		sub.Rows = lo.FilterMap(picked, func(c candidate, _ int) (render.Row, bool) { return c.row, c.section == i })
		if len(sub.Rows) == 0 {
			continue
		}
		if headers && b.Len() > 0 {
			b.WriteString("\n")
		}
		if err := (render.Listing{Section: sub, Options: g.listingOptions(l, i)}).Write(&b, headers); err != nil {
			return err
		}
	}
	args := []string{"get", g.typeArg}
	if g.output == "wide" {
		args = append(args, "--output=wide")
	}
	if !headers {
		args = append(args, "--no-headers")
	}
	return g.printer.Print(b.String(), args...)
}

// printMetadata prints the picked objects' labels or annotations, read from the lookup's own metadata.
func (g *getRun) printMetadata(l *lookup, picked []candidate) error {
	var objs []render.Metadata
	for _, c := range picked {
		name := l.qualifiedName(c)
		md := lo.Ternary(g.output == "annotations", c.row.Meta.Annotations, c.row.Meta.Labels)
		if len(md) == 0 {
			object := lo.Ternary(l.severalTypes(), name, g.typeArg+" "+name)
			_, _ = fmt.Fprintf(g.stderr, "Warning: No %s found for %s in %s/%s\n",
				g.output, object, c.row.Cluster, cmp.Or(c.row.Meta.Namespace, "-"))
		}
		objs = append(objs, render.Metadata{Cluster: c.row.Cluster, Namespace: c.row.Meta.Namespace, Name: name, Map: md})
	}
	_, err := fmt.Fprint(g.stdout, render.MetadataMaps(objs, g.opts.EscapeAnnotations.Value))
	return err
}

// printList fetches the picked objects in full, and prints them as one List, in candidate order.
//
// No single kubectl command produces that output, so a flag only kubectl could honor is an error naming it.
func (g *getRun) printList(ctx context.Context, l *lookup, picked []candidate) error {
	if f := g.firstPassthrough(); f != "" {
		return fmt.Errorf("%s needs kubectl, and no one kubectl command prints %s across namespaces or clusters", f, g.output)
	}

	byCluster := map[string][]int{}
	var clusters []*kube.Cluster
	for i, c := range picked {
		if _, ok := byCluster[c.row.Cluster]; !ok {
			kc, err := g.kc.Cluster(c.row.Cluster)
			if err != nil {
				return err
			}
			clusters = append(clusters, kc)
		}
		byCluster[c.row.Cluster] = append(byCluster[c.row.Cluster], i)
	}
	items := make([]map[string]any, len(picked))
	results := runFanOut(ctx, g, clusters,
		func(ctx context.Context, kc *kube.Cluster) (struct{}, error) {
			for _, i := range byCluster[kc.Name] {
				c := picked[i]
				obj, err := kc.Object(ctx, l.mappings[c.section], c.row.Meta.Namespace, c.row.Meta.Name)
				if err != nil {
					return struct{}{}, err
				}
				items[i] = obj
			}
			return struct{}{}, nil
		})
	for _, r := range results {
		if r.Err != nil {
			return fmt.Errorf("cluster %s: %w", r.Cluster.Name, r.Err)
		}
	}
	if !g.cmdline.Bool("show-managed-fields") {
		for _, obj := range items {
			unstructured.RemoveNestedField(obj, "metadata", "managedFields") // as kubectl prints objects
		}
	}
	return render.List(g.stdout, g.output, items)
}

// noMatchError names each query that matched nothing, where kuba looked, and the closest names to each (see
// resolve.Closest).
func (g *getRun) noMatchError(l *lookup, unmatched, names []string) error {
	var qs []string
	for _, q := range unmatched {
		s := fmt.Sprintf("%q", q)
		if closest := resolve.Closest(q, names); len(closest) > 0 {
			s += fmt.Sprintf(" (closest: %s)", strings.Join(closest, ", "))
		}
		qs = append(qs, s)
	}
	return fmt.Errorf("no %s matches %s%s", g.typeArg, strings.Join(qs, ", "), g.where(l))
}

// noResources says a listing found nothing, as kubectl does, and succeeds.
func (g *getRun) noResources(l *lookup) {
	_, _ = fmt.Fprintf(g.stderr, "No resources found%s.\n", g.where(l))
}

// where says where lookups searched, e.g. " in kiwi namespace" or " in any namespace on clusters apple, banana".
//
// The namespace shows unless every type searched is cluster-scoped, e.g. nodes.
func (g *getRun) where(l *lookup) string {
	var w string
	if len(l.sections) == 0 || slices.ContainsFunc(l.sections, func(s *render.Section) bool { return s.Namespaced }) {
		w = " in any namespace"
		if g.namespace != "" {
			w = fmt.Sprintf(" in %s namespace", g.namespace)
		}
	}
	if g.fanOut {
		w += " on clusters " + strings.Join(g.contexts, ", ")
	}
	return w
}

// groupByTarget groups candidates by (cluster, namespace), in order of first appearance.
func groupByTarget(candidates []candidate) [][]candidate {
	type target struct {
		cluster   string
		namespace string
	}
	return lo.PartitionBy(candidates, func(c candidate) target {
		return target{cluster: c.row.Cluster, namespace: c.row.Meta.Namespace}
	})
}

// isNativeFormat reports whether an output format is one kuba can print itself: default, wide, or name.
func isNativeFormat(output string) bool {
	return output == "" || output == "wide" || output == "name"
}

// listFormats are the output formats kuba merges into one List when a get spans several namespaces or clusters.
var listFormats = []string{"json", "yaml", "kyaml"}

// isMetadataFormat reports whether an output format is one of kuba's metadata maps.
func isMetadataFormat(output string) bool {
	return output == "labels" || output == "annotations"
}

// isNodes reports whether a mapping is the core nodes type, whose listings get kuba's node extras.
func isNodes(m *meta.RESTMapping) bool {
	return m.Resource.GroupResource() == schema.GroupResource{Resource: "nodes"}
}
