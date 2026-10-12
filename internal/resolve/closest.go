package resolve

import (
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hbollon/go-edlib"
	"github.com/samber/lo"
)

// maxSuggestions caps the names a no-match error suggests.
const maxSuggestions = 3

// Closest suggests names for a query that matched nothing: the distinct names nearest by edit distance, up to three.
//
// It picks them in three steps:
//   - Each name is cut to the query's length, give or take a letter, from its start or any segment's (after a -, ., _,
//     /, or :), so the rest of it doesn't count: appel finds apple in apple-1, and wbe finds web in apple-web, each one
//     swap away
//   - Names further than a third of the query's length (at least one edit) are dropped
//   - Only names tied for nearest count, so a clear winner shows alone, and a family like apple-1 and apple-2 together
func Closest(query string, names []string) []string {
	query, names = strings.ToLower(query), lo.Uniq(names)
	dists := lo.Map(names, func(name string, _ int) int { return segmentDistance(query, strings.ToLower(name)) })
	best := lo.Min(dists)
	// Even the nearest name is too far off to be a typo: one edit per three letters is allowed, and always at least one
	if best > max(1, utf8.RuneCountInString(query)/3) {
		return nil
	}
	closest := lo.Filter(names, func(_ string, i int) bool { return dists[i] == best })
	slices.Sort(closest)
	return closest[:min(len(closest), maxSuggestions)]
}

// segmentDistance is how many edits query is from the nearest cut of name, i.e. from the part of it a typo was aiming
// at.
//
// Cuts start where name or any of its segments does, and run the query's length, or one letter shorter or longer, as a
// typo can drop or add one. A cut that would run past the name's end stops there.
//
// Example for wbe against apple-web:
//   - From the start, ap, app, and appl: 3 or more
//   - From web's start, we and web: 1, dropping the b or swapping the b and e
//
// Edits are insertions, deletions, substitutions, and swaps of adjacent letters (optimal string alignment).
func segmentDistance(query, name string) int {
	letters, n := []rune(name), utf8.RuneCountInString(query)
	best := math.MaxInt
	for _, start := range segmentStarts(letters) {
		// One shorter in case the typo added a letter (appple for apple), one longer in case it dropped one (aple)
		for k := max(n-1, 0); k <= n+1; k++ {
			cut := letters[start:min(len(letters), start+k)]
			best = min(best, edlib.OSADamerauLevenshteinDistance(query, string(cut)))
		}
	}
	return best
}

// segmentStarts are where name's segments start: at 0, and after each separator.
func segmentStarts(letters []rune) []int {
	starts := []int{0}
	for i, r := range letters {
		if strings.ContainsRune("-._/:", r) {
			starts = append(starts, i+1)
		}
	}
	return starts
}
