package resolve

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClosest(t *testing.T) {
	tests := []struct {
		name  string
		query string
		names []string
		want  []string
	}{
		{name: "transposition", query: "appel", names: []string{"apple-1", "banana-1"}, want: []string{"apple-1"}},
		{name: "nothing close", query: "cherry", names: []string{"apple-1", "banana-1"}, want: nil},
		{name: "nearest only", query: "apple-wbe", names: []string{"apple-db", "apple-web"}, want: []string{"apple-web"}},
		{name: "typo in a later segment", query: "wbe", names: []string{"apple-db", "apple-web"}, want: []string{"apple-web"}},
		{name: "short name compared whole", query: "postgres-1", names: []string{"apple-1", "postgres"}, want: []string{"postgres"}},
		{name: "ties by name, up to three", query: "bananna", names: []string{"banana-3", "banana-1", "banana-2", "bandana-1", "banana-4"}, want: []string{"banana-1", "banana-2", "banana-3"}},
		{name: "deduped", query: "aple", names: []string{"apple-1", "apple-1"}, want: []string{"apple-1"}},
		{name: "ignores case", query: "APPEL", names: []string{"apple-1"}, want: []string{"apple-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Closest(tt.query, tt.names))
		})
	}
}
