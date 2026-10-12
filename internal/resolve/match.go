package resolve

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lithammer/fuzzysearch/fuzzy"
	"github.com/samber/lo"
)

// Mode decides which names a query matches.
//
// Every mode ignores case and takes exact names over any others, so typing a full name only ever gets that name. The
// modes differ in what they take short of an exact name. E.g. among [apple, apple-1, pineapple, purple]:
//
//	query  tiered                   exact  substring                fuzzy
//	-----  ------                   -----  ---------                -----
//	apple  apple                    apple  apple                    apple
//	pple   apple apple-1 pineapple  -      apple apple-1 pineapple  apple apple-1 pineapple purple
//	aple   apple apple-1 pineapple  -      -                        apple apple-1 pineapple
type Mode string

const (
	// Tiered is the default: it takes the best tier any name reaches, i.e. exact names, else names containing the
	// query, else names with the query's letters in order.
	//
	// One better hit hides every looser one, e.g. pple skips purple, as apple contains it: a name containing what you
	// typed is likelier what you meant than one that merely shares its letters.
	Tiered Mode = "tiered"

	// Exact takes exact names only.
	Exact Mode = "exact"
	// Substring takes exact names, else names containing the query.
	Substring Mode = "substring"
	// Fuzzy takes exact names, else every name with the query's letters in order, whether or not they're contiguous.
	Fuzzy Mode = "fuzzy"
)

// Modes lists every mode in help order: the default, then strictest to loosest, each taking one more tier.
var Modes = []Mode{Tiered, Exact, Substring, Fuzzy}

// ParseMode reads a mode by name.
func ParseMode(s string) (Mode, error) {
	if m := Mode(s); slices.Contains(Modes, m) {
		return m, nil
	}
	return "", fmt.Errorf("unknown match mode %q (want tiered, exact, substring, or fuzzy)", s)
}

// UnmarshalText reads a mode by name, so a Mode can be a config option, set from --match or KUBA_MATCH.
//
// config parses strings, bools, numbers, and durations itself, and any other type through UnmarshalText. A bad name
// fails as in ParseMode, and config prefixes the error with where the name came from:
//   - --match apple -> --match: unknown match mode "apple" (want ...), as a usage error
//   - KUBA_MATCH=apple -> KUBA_MATCH: unknown match mode "apple" (want ...)
func (m *Mode) UnmarshalText(text []byte) error {
	mode, err := ParseMode(string(text))
	if err != nil {
		return err
	}
	*m = mode
	return nil
}

// tier is how well a query matches a name; higher is better, and the zero tier is no match.
type tier int

const (
	// tierNone is no match.
	tierNone tier = iota
	// tierFuzzy is a name containing the query's letters in order.
	tierFuzzy
	// tierSubstring is a name containing the query.
	tierSubstring
	// tierExact is the name itself.
	tierExact
)

// minTiers is the lowest tier each mode takes.
var minTiers = map[Mode]tier{
	Tiered:    tierFuzzy, // won't auto-filter, i.e. only filters based on other names' tiers
	Exact:     tierExact,
	Substring: tierSubstring,
	Fuzzy:     tierFuzzy,
}

// match finds the names a query hits: exact names if any, else whichever names the mode takes (see Mode).
//
// Each name gets a tier for how well it matches (see tierOf). Short of exact names, a mode hits nothing unless some
// name reaches its minimum tier. Then Tiered takes just the best tier reached, and the other modes take every name at
// or above their minimum. query and names come lowercased from MatchAll, and hits are indices into names, in order.
//
// Under Tiered, among [apple, apple-1, purple]:
//   - apple: [0]
//   - pple: [0 1], as both contain it, which hides purple's looser match
//   - kiwi: none
//
// exact reports whether the hits are named exactly the query. If so, they're just what was asked for, so the picker
// needn't ask even if there are several, e.g. apple in two namespaces (see Matches.Settled).
func match(mode Mode, query string, names []string) (hits []int, exact bool) {
	tiers := lo.Map(names, func(name string, _ int) tier { return tierOf(query, name) })
	namesBest, modeWorst := lo.Max(tiers), minTiers[mode]
	filterBelow := func(floor tier) []int {
		return lo.FilterMap(tiers, func(t tier, i int) (int, bool) { return i, t >= floor })
	}
	switch {
	// Exact match or no match -> exact or nil
	// A name matched exact -> exact
	case namesBest == tierExact:
		return filterBelow(tierExact), true
	// No name reached the mode's minimum tier -> nil
	case namesBest < modeWorst:
		return nil, false

	// Some name reached the mode's minimum tier, and none were exact -> hits the mode takes
	// Tiered mode -> best seen tier and above (tiered hides looser matches)
	case mode == Tiered:
		return filterBelow(namesBest), false
	// Other modes -> mode's minimum tier and above (explicit mode takes everything it allows)
	default:
		return filterBelow(modeWorst), false
	}
}

// tierOf is the best tier a name reaches for a query, both lowercased (see MatchAll).
//
// The tiers nest (a name containing the query also has its letters in order), so the first check that holds wins.
// For pple:
//   - pple: exact
//   - apple: substring
//   - purple: fuzzy, with its letters in order but apart
//   - plum: none, with no second p
func tierOf(query, name string) tier {
	switch {
	case query == name:
		return tierExact
	case strings.Contains(name, query):
		return tierSubstring
	case fuzzy.Match(query, name):
		return tierFuzzy
	default:
		return tierNone
	}
}
