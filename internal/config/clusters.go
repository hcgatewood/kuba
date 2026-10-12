package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/samber/lo"
)

// Clusters is KUBA_CLUSTERS applied to a kubeconfig's contexts: which contexts are siblings, and their aliases.
//
// KUBA_CLUSTERS is a comma-separated list of `alias=pattern` entries. A pattern matches context names: `*` matches any
// text, everything else matches itself, and the pattern must match the whole name, so `cr-prod-*` matches cr-prod-east,
// but `prod-*` doesn't. For the contexts its pattern matches, each entry:
//   - Makes them siblings, which `-m` searches together
//   - Names each one for --clusters: the alias, its `*` (if any) filled with what the pattern's `*`s matched, e.g.
//     `p*=cr-prod-*` names cr-prod-east peast
//
// Ex: given contexts cr-prod-east, cr-prod-west, cr-stag-east, and minikube, `p*=cr-prod-*,s*=cr-stag-*,mini=minikube`
// makes:
//
//	entry           siblings                    aliases
//	p*=cr-prod-*    cr-prod-east, cr-prod-west  peast, pwest
//	s*=cr-stag-*    cr-stag-east                seast
//	mini=minikube   minikube                    mini
//
// So `-m` on cr-prod-east searches both prod contexts, and `--clusters peast,seast` searches both east ones.
//
// Spaces around entries and their sides are trimmed, and with several `*`s, earlier ones match as much as they can.
// `\` makes the punctuation after it literal, for names containing `,`, `=`, `*`, or `\`, e.g. `ta=team\=a` for a
// context named `team=a`, or `\\` for a backslash. Real context names rarely need it, e.g. EKS's are ARNs like
// arn:aws:eks:us-east-1:123456789012:cluster/prod.
type Clusters struct {
	// aliases are every (alias, context) pair the entries make, in entry order, then context order. A context gets one
	// alias per entry matching it, e.g. cherry-1 is both kcherry-1 and ch1 under `k*=*,ch*=cherry-*`.
	aliases []alias
	// groups are the sibling groups, one per entry: the contexts its pattern matched, sorted. A context can be in
	// several, and its siblings are every context in any of them (see Siblings).
	groups [][]string
}

// alias is one name a KUBA_CLUSTERS entry gives a context, which --clusters can pick the context by.
//
// Ex: `k*=*` gives context apple the alias kapple. An alias with no `*` names every context its entry matches, e.g.
// `prod=prod-*` names both prod-east and prod-west prod, so `--clusters prod` picks both.
type alias struct {
	// name is the entry's alias, its `*` replaced by what the pattern's `*`s captured, joined, e.g. kapple, or xprodeast
	// for cr-prod-east under `x*=cr-*-*`.
	name string
	// context is the kubeconfig context the alias names, e.g. apple.
	context string
}

// NewClusters parses KUBA_CLUSTERS (see Clusters), and applies it to the contexts.
func NewClusters(raw string, contexts []string) (*Clusters, error) {
	c := &Clusters{}
	for _, e := range splitList(raw) {
		aliasPat, contextPat, err := parseEntry(e)
		if err != nil {
			return nil, fmt.Errorf("KUBA_CLUSTERS entry %q: %w", e, err)
		}
		var group []string
		for _, context := range contexts {
			if captures, ok := contextPat.match(context); ok {
				c.aliases = append(c.aliases, alias{name: aliasPat.fill(strings.Join(captures, "")), context: context})
				group = append(group, context)
			}
		}
		slices.Sort(group)
		c.groups = append(c.groups, group)
	}
	return c, nil
}

// Siblings lists the context's sibling group, itself included: every context that shares an entry with it, sorted.
//
// A context in no group is an error, since searching it alone would look like a complete search.
func (c *Clusters) Siblings(context string) ([]string, error) {
	sibs := lo.Flatten(lo.Filter(c.groups, func(group []string, _ int) bool { return slices.Contains(group, context) }))
	if len(sibs) == 0 {
		return nil, fmt.Errorf("context %q is in no KUBA_CLUSTERS group; add a pattern matching it, or drop --multi-cluster", context)
	}
	slices.Sort(sibs)
	return slices.Compact(sibs), nil
}

// Select picks the contexts --clusters names, sorted.
//
// --clusters is comma-separated items, each a name or a pattern, in KUBA_CLUSTERS's syntax (see Clusters). An item
// picks the contexts of the aliases it matches, or, only if it matches no alias, the contexts it matches by name.
// Aliases win so that `p*` means the p aliases, not also every context whose name starts with p.
//
// Ex: with the aliases from Clusters's example (peast, pwest, seast, mini):
//   - `peast,seast` picks cr-prod-east and cr-stag-east, by alias
//   - `p*` picks cr-prod-east and cr-prod-west, by alias
//   - `cr-*-east` matches no alias, so picks cr-prod-east and cr-stag-east, by name
func (c *Clusters) Select(spec string, contexts []string) ([]string, error) {
	var picked []string
	for _, item := range splitList(spec) {
		pat, err := parsePattern(item)
		if err != nil {
			return nil, fmt.Errorf("--clusters %q: %w", item, err)
		}
		hits := lo.FilterMap(c.aliases, func(a alias, _ int) (string, bool) { return a.context, pat.matches(a.name) })
		if len(hits) == 0 {
			hits = lo.Filter(contexts, func(context string, _ int) bool { return pat.matches(context) })
		}
		if len(hits) == 0 {
			return nil, fmt.Errorf("--clusters: %q matches no cluster alias or context", item)
		}
		picked = append(picked, hits...)
	}
	if len(picked) == 0 {
		return nil, fmt.Errorf("--clusters names no clusters")
	}
	slices.Sort(picked)
	return slices.Compact(picked), nil
}

// parseEntry splits an `alias=pattern` entry into its alias pattern and its context pattern.
func parseEntry(e string) (aliasPat, contextPat pattern, err error) {
	var empty pattern
	parts := splitUnescaped(e, '=')
	if len(parts) != 2 || slices.Contains(parts, "") {
		return empty, empty, fmt.Errorf("want alias=pattern")
	}
	if aliasPat, err = parsePattern(parts[0]); err != nil {
		return empty, empty, err
	}
	// An alias gets at most one `*` (two lits), because everything the pattern's `*`s matched goes into that one spot.
	// A second `*` would get the same text again, e.g. `k*-*=cr-*-*` would name cr-prod-east kprodeast-prodeast, not
	// the kprod-east you'd expect; filling each `*` with its own match isn't supported yet.
	if len(aliasPat.lits) > 2 {
		return empty, empty, fmt.Errorf("an alias takes at most one *")
	}
	if contextPat, err = parsePattern(parts[1]); err != nil {
		return empty, empty, err
	}
	return aliasPat, contextPat, nil
}

// pattern is a parsed pattern, stored in two forms, one per way a pattern is used.
//
//   - re matches names: `cherry-*` becomes `^cherry-(.*)$`, which matches cherry-1, capturing 1
//   - lits fills in an alias: `k*` becomes {"k", ""}, which joined with apple gives kapple
type pattern struct {
	// re matches a whole name, capturing what each `*` matched.
	re *regexp.Regexp
	// lits is the pattern split at each `*`, escapes resolved.
	lits []string
}

// parsePattern reads a pattern, resolving escapes.
func parsePattern(s string) (pattern, error) {
	lits := []string{""}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '*':
			// Create a new lit for the next part
			lits = append(lits, "")
			continue
		case r == '\\' && i == len(rs)-1:
			return pattern{}, fmt.Errorf("trailing \\")
		case r == '\\':
			i++
			if r = rs[i]; unicode.IsLetter(r) || unicode.IsDigit(r) {
				return pattern{}, fmt.Errorf(`\%c escapes a letter or digit; write \\ for a backslash`, r)
			}
		}
		lits[len(lits)-1] += string(r)
	}

	// Example: `fig.*` -> lits {"fig.", ""}
	// Escape regex syntax in each lit, so it matches only itself, e.g. `fig.` -> `fig\.`
	quoted := lo.Map(lits, func(lit string, _ int) string { return regexp.QuoteMeta(lit) })
	// Put a capture group where each `*` was, e.g. -> `fig\.(.*)`
	withCaptures := strings.Join(quoted, `(.*)`)
	// Anchor to the whole name, e.g. -> `^fig\.(.*)$`
	withAnchors := fmt.Sprintf(`^%s$`, withCaptures)
	pat := pattern{
		lits: lits,
		re:   regexp.MustCompile(withAnchors),
	}

	return pat, nil
}

// match reports whether the pattern matches the whole name, and what its wildcards captured.
//
// Ex:
//   - `cr-*-*` matches cr-prod-east, capturing {"prod", "east"}
//   - `minikube` matches minikube, capturing nothing
//   - `cherry-*` doesn't match old-cherry-1, since it must match the whole name
func (p pattern) match(name string) ([]string, bool) {
	m := p.re.FindStringSubmatch(name)
	if m == nil {
		return nil, false
	}
	return m[1:], true
}

// matches reports whether the pattern matches the whole name.
func (p pattern) matches(name string) bool {
	return p.re.MatchString(name)
}

// fill renders an alias pattern, its wildcard (if any) replaced by s.
//
// Ex: `k*` filled with apple is kapple, and `prod`, having no wildcard, stays prod.
func (p pattern) fill(s string) string {
	return strings.Join(p.lits, s)
}

// splitList splits a comma-separated list, e.g. KUBA_CLUSTERS or --clusters, into its trimmed, non-empty items.
func splitList(s string) []string {
	return lo.Without(splitUnescaped(s, ','), "")
}

// splitUnescaped splits s at each sep not escaped by a backslash, into trimmed parts, keeping escapes for parsePattern.
//
// Ex:
//   - `ta=team\=a, k*=*` split at `,` is {`ta=team\=a`, `k*=*`}
//   - `ta=team\=a` split at `=` is {`ta`, `team\=a`}
//
// sep must be ASCII, which makes scanning bytes safe: no byte of a multi-byte UTF-8 rune is ASCII.
func splitUnescaped(s string, sep byte) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // skip the escaped byte
		case sep:
			parts = append(parts, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return append(parts, strings.TrimSpace(s[start:]))
}
