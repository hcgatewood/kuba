package resolve

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatch(t *testing.T) {
	names := []string{"api-server", "apple-pie-indexer", "frontend-api-2", "api"}
	tests := []struct {
		name  string
		mode  Mode
		query string
		want  []string
		exact bool
	}{
		{name: "tiered exact", mode: Tiered, query: "frontend-api-2", want: []string{"frontend-api-2"}, exact: true},
		{name: "tiered exact hides substrings", mode: Tiered, query: "api", want: []string{"api"}, exact: true},
		{name: "tiered substring", mode: Tiered, query: "pi-", want: []string{"api-server", "frontend-api-2"}},
		{name: "tiered substring beats fuzzy", mode: Tiered, query: "ap", want: []string{"api-server", "apple-pie-indexer", "frontend-api-2", "api"}},
		{name: "tiered fuzzy", mode: Tiered, query: "apin", want: []string{"apple-pie-indexer"}},
		{name: "exact", mode: Exact, query: "api-server", want: []string{"api-server"}, exact: true},
		{name: "exact skips substring", mode: Exact, query: "server"},
		{name: "substring", mode: Substring, query: "server", want: []string{"api-server"}},
		{name: "substring skips fuzzy", mode: Substring, query: "apin"},
		{name: "fuzzy exact wins", mode: Fuzzy, query: "frontend-api-2", want: []string{"frontend-api-2"}, exact: true},
		{name: "fuzzy folds substring", mode: Fuzzy, query: "api-", want: []string{"api-server", "apple-pie-indexer", "frontend-api-2"}},
		{name: "no match", mode: Tiered, query: "cherry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits, exact := match(tt.mode, tt.query, names)
			var got []string
			for _, i := range hits {
				got = append(got, names[i])
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.exact, exact)
		})
	}
}

func TestParseMode(t *testing.T) {
	for _, m := range Modes {
		got, err := ParseMode(string(m))
		assert.NoError(t, err)
		assert.Equal(t, m, got)
	}
	_, err := ParseMode("closest")
	assert.ErrorContains(t, err, `unknown match mode "closest"`)
}
