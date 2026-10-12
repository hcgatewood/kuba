package resolve

import (
	"slices"
	"strings"

	"github.com/samber/lo"
)

// Matches are what a command's queries matched among its candidates, and whether the picker still needs to ask.
//
// Candidates are every object the command's lookup found, across clusters, namespaces, and types, in one list. Names
// can repeat, e.g. apple in two namespaces, so Matches refers to candidates by index, never by name: each is its own
// object to act on.
//
// Each query picks its own tier across every candidate (see Mode), and the hits are their union. E.g. with candidates
// 0 apple, 1 apple-1, 2 apple-2, 3 banana-1, and 4 apple, the last in another namespace:
//
//	queries       Hits         Unmatched  Settled  why
//	apple         [0 4]                   yes      exact, so both apples
//	aple          [0 1 2 4]               no       partial, and several match
//	apple bnana   [0 3 4]                 yes      apple is exact; bnana is partial but matches just one
//	cherry bnana  [3]          [cherry]   yes      bnana matches just one; cherry matched nothing, so doesn't count
//	(none)        [0 1 2 3 4]             no       every candidate hits, and there are several
type Matches struct {
	// Hits are the indices of the candidates any query matched, in candidate order, each once even if several queries
	// matched it.
	Hits []int
	// Unmatched are the queries that matched nothing, in query order. They don't stop the command: it acts on the
	// other queries' hits, then reports these as not found, with the closest names (see Closest).
	Unmatched []string
	// Settled reports whether the hits are already just what was asked for, so the picker needn't ask, even under
	// SelectYes (see Picker.Pick).
	//
	// A query settles when it matches exact names, however many, or a single candidate at any tier. The hits settle
	// when every query that matched settles; one that matched nothing can't unsettle them.
	Settled bool
}

// MatchAll matches each query against the candidates' names, given in candidate order, ignoring case, and combines the
// hits.
//
// With no queries, every candidate is a hit. See Matches for how hits combine and settle.
func MatchAll(mode Mode, queries, names []string) Matches {
	if len(queries) == 0 {
		return Matches{Hits: lo.Range(len(names)), Settled: len(names) == 1}
	}
	names = lo.Map(names, func(name string, _ int) string { return strings.ToLower(name) })
	m := Matches{Settled: true}
	for _, query := range queries {
		matched, exact := match(mode, strings.ToLower(query), names)
		if len(matched) == 0 {
			m.Unmatched = append(m.Unmatched, query)
			continue
		}
		if !exact && len(matched) > 1 {
			m.Settled = false
		}
		m.Hits = append(m.Hits, matched...)
	}
	slices.Sort(m.Hits)
	m.Hits = slices.Compact(m.Hits)
	return m
}

// Select decides whether kuba asks fzf to pick among several matches.
type Select int

const (
	// SelectAll takes every match without asking. It's the zero value, so a command that never sets a mode can't
	// prompt by accident.
	SelectAll Select = iota
	// SelectYes asks fzf whenever several objects match.
	SelectYes
	// SelectOne asks fzf for exactly one pick.
	SelectOne
	// SelectAny takes one match without asking.
	SelectAny
)

// Prompts reports whether the mode might ask fzf, for choices a command makes before it has any matches.
//
// SelectYes and SelectOne might; SelectAll and SelectAny never do. Might, not will: Pick still skips fzf for a settled
// match or a single hit. But some choices can't wait for the matches, e.g. whether get can skip its lookup and hand
// kubectl the listing, which it can't if fzf may need the candidates, and whether to build fzf's lines at all.
func (s Select) Prompts() bool {
	return s == SelectYes || s == SelectOne
}

// Picker is what fzf shows when Pick has to ask: the candidates as listing rows, under a header and a prompt.
//
// It describes every candidate, as it's built before matching, and Pick shows only the hits' lines. A command whose
// select mode never prompts (see Select.Prompts) can leave Lines empty, since Pick never reads them then. fzf prints
// the picked lines back, and Pick maps them to candidates by text, so a line should identify its candidate, e.g. with
// CLUSTER and NAMESPACE columns when names repeat across groupings.
type Picker struct {
	// Lines are the candidates as fzf shows them, one per candidate, in candidate order, as Pick looks them up by hit.
	Lines []string
	// Header is the line fzf pins above them, e.g. the listing's column header; empty shows none.
	Header string

	// Type is what's picked, as the command was given it, e.g. pod, deploy, or pod,svc. It names the picks in fzf's
	// prompt, "Choose pod: ", and in the error when there are none, "no pod selected".
	Type string
	// Query is fzf's starting query, so the picker opens filtered to what was typed: the command's query if it has
	// exactly one, else empty, as several can't be one fzf query. Its case doesn't matter, as with matching.
	Query string

	// Fzf is the fzf command to run, by name or path, e.g. from KUBA_FZF; empty runs the fzf built into kuba, so kuba
	// needs no fzf installed.
	Fzf string
}

// Pick narrows the hits to what the command acts on, asking fzf only when the mode and the matches leave a choice.
//
// It returns candidate indices, sorted, as Hits are. Canceling fzf exits 130, as Ctrl-C does anywhere else, and
// leaving it with nothing picked is an error.
func (p Picker) Pick(mode Select, m Matches) ([]int, error) {
	switch {
	case len(m.Hits) == 0, mode == SelectAll:
		// Nothing to choose from, or no choosing: every hit
		return m.Hits, nil
	case m.Settled || len(m.Hits) == 1:
		// Already just what was asked for, e.g. an exact name: every hit, or under SelectOne the first
		if mode == SelectOne {
			return m.Hits[:1], nil
		}
		return m.Hits, nil
	case mode == SelectAny:
		// A choice, but not one to ask about: the first hit
		return m.Hits[:1], nil
	}
	// A real choice, so fzf asks: for any number of picks under SelectYes, or exactly one under SelectOne
	lines := lo.Map(m.Hits, func(hit, _ int) string { return p.Lines[hit] })
	picked, err := p.fzf(lines, mode == SelectYes)
	if err != nil {
		return nil, err
	}
	return lo.Map(picked, func(i, _ int) int { return m.Hits[i] }), nil
}
