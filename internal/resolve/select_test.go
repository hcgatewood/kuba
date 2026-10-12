package resolve

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchAll(t *testing.T) {
	names := []string{"apple", "apple-1", "apple-2", "Banana-1", "apple"}
	tests := []struct {
		name    string
		mode    Mode
		queries []string
		want    Matches
	}{
		{name: "no queries", mode: Tiered, want: Matches{Hits: []int{0, 1, 2, 3, 4}}},
		{name: "exact across groupings settles", mode: Tiered, queries: []string{"apple"}, want: Matches{Hits: []int{0, 4}, Settled: true}},
		{name: "several partial hits", mode: Tiered, queries: []string{"aple"}, want: Matches{Hits: []int{0, 1, 2, 4}}},
		{name: "each query its own tier", mode: Tiered, queries: []string{"apple", "bnana"}, want: Matches{Hits: []int{0, 3, 4}, Settled: true}},
		{name: "deduped", mode: Tiered, queries: []string{"ap1", "ap1"}, want: Matches{Hits: []int{1}, Settled: true}},
		{name: "unmatched", mode: Tiered, queries: []string{"cherry", "bnana"}, want: Matches{Hits: []int{3}, Unmatched: []string{"cherry"}, Settled: true}},
		{name: "mode applies", mode: Exact, queries: []string{"aple"}, want: Matches{Unmatched: []string{"aple"}, Settled: true}},
		{name: "ignores case", mode: Tiered, queries: []string{"APPLE-1", "banana-1"}, want: Matches{Hits: []int{1, 3}, Settled: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MatchAll(tt.mode, tt.queries, names))
		})
	}
}

func TestPickWithoutAsking(t *testing.T) {
	p := Picker{Type: "pod", Lines: []string{"apple-1", "apple-2", "banana-1"}}
	tests := []struct {
		name string
		mode Select
		m    Matches
		want []int
	}{
		{name: "all", mode: SelectAll, m: Matches{Hits: []int{0, 1}}, want: []int{0, 1}},
		{name: "zero value", m: Matches{Hits: []int{0, 1}}, want: []int{0, 1}},
		{name: "settled", mode: SelectYes, m: Matches{Hits: []int{0, 2}, Settled: true}, want: []int{0, 2}},
		{name: "single hit", mode: SelectYes, m: Matches{Hits: []int{1}}, want: []int{1}},
		{name: "one of settled", mode: SelectOne, m: Matches{Hits: []int{0, 2}, Settled: true}, want: []int{0}},
		{name: "any", mode: SelectAny, m: Matches{Hits: []int{1, 2}}, want: []int{1}},
		{name: "nothing", mode: SelectYes, m: Matches{}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.Pick(tt.mode, tt.m)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
