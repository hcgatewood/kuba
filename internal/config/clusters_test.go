package config

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var contexts = []string{"apple", "banana", "cherry", "cherry-1", "cherry-2", "old-cherry-1", "donut-1", `egg\1`, "fig.1"}

func TestSiblings(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		context string
		want    []string
		wantErr string
	}{
		{name: "every context", raw: "k*=*", context: "banana", want: slices.Sorted(slices.Values(contexts))},
		{name: "star matches nothing", raw: "c*=cherry*,d*=donut-*", context: "cherry-1", want: []string{"cherry", "cherry-1", "cherry-2"}},
		{name: "whitespace trimmed", raw: " c*=cherry-* , d*=donut-* ", context: "cherry-1", want: []string{"cherry-1", "cherry-2"}},
		{name: "several entries union", raw: "x*=*-1,c*=cherry-*", context: "cherry-1", want: []string{"cherry-1", "cherry-2", "donut-1", "old-cherry-1"}},
		{name: "group of one", raw: "a=apple", context: "apple", want: []string{"apple"}},
		{name: "dot is literal", raw: "f*=fig.*", context: "fig.1", want: []string{"fig.1"}},
		{name: "escaped punctuation", raw: `c*=cherry\-*`, context: "cherry-1", want: []string{"cherry-1", "cherry-2"}},
		{name: "escaped backslash", raw: `e*=egg\\*`, context: `egg\1`, want: []string{`egg\1`}},
		{name: "in no group", raw: "d*=donut-*", context: "cherry-1", wantErr: `context "cherry-1" is in no KUBA_CLUSTERS group`},
		{name: "regex is literal", raw: "c*=cherry.*", context: "cherry-1", wantErr: "cherry-1"},
		{name: "bracket is literal", raw: "c*=cherry-[*", context: "cherry-1", wantErr: "cherry-1"},
		{name: "empty", raw: "", context: "apple", wantErr: "in no KUBA_CLUSTERS group"},
		{name: "malformed", raw: "nonsense", context: "apple", wantErr: `KUBA_CLUSTERS entry "nonsense": want alias=pattern`},
		{name: "escaped letter", raw: `c*=cherry-\d*`, context: "cherry-1", wantErr: `\d escapes a letter or digit`},
		{name: "trailing backslash", raw: `c*=cherry\`, context: "cherry-1", wantErr: `trailing \`},
		{name: "two stars in alias", raw: "c**=cherry-*", context: "cherry-1", wantErr: "at most one *"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClusters(tt.raw, contexts)
			if err == nil {
				var got []string
				got, err = c.Siblings(tt.context)
				if tt.wantErr == "" {
					require.NoError(t, err)
					assert.Equal(t, tt.want, got)
					return
				}
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestAliases(t *testing.T) {
	c, err := NewClusters(`k*=*,ch*=cherry-*,b=banana`, []string{"apple", "banana", "cherry-1"})
	require.NoError(t, err)
	want := []alias{
		{name: "kapple", context: "apple"},
		{name: "kbanana", context: "banana"},
		{name: "kcherry-1", context: "cherry-1"},
		{name: "ch1", context: "cherry-1"},
		{name: "b", context: "banana"},
	}
	assert.Equal(t, want, c.aliases)
}

func TestSelect(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		spec    string
		want    []string
		wantErr string
	}{
		{name: "alias and context", raw: "k*=*", spec: "kapple,banana", want: []string{"apple", "banana"}},
		{name: "alias glob", raw: "k*=*", spec: "kb*", want: []string{"banana"}},
		{name: "aliases before contexts", raw: "cherry=apple", spec: "cherry", want: []string{"apple"}},
		{name: "context glob", raw: "", spec: "cherry-*", want: []string{"cherry-1", "cherry-2"}},
		{name: "deduped and sorted", raw: "k*=*", spec: "banana,kapple,kbanana", want: []string{"apple", "banana"}},
		{name: "no match", raw: "k*=*", spec: "kiwi", wantErr: `"kiwi" matches no cluster alias or context`},
		{name: "empty", raw: "k*=*", spec: " , ", wantErr: "names no clusters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClusters(tt.raw, contexts)
			require.NoError(t, err)
			got, err := c.Select(tt.spec, contexts)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
