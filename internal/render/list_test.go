package render

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestList(t *testing.T) {
	items := []map[string]any{{"kind": "ConfigMap", "apiVersion": "v1", "metadata": map[string]any{"name": "apple-1", "generation": json.Number("2")}}}
	tests := []struct {
		format string
		items  []map[string]any
		want   string
	}{
		{format: "yaml", want: "apiVersion: v1\nitems: []\nkind: List\nmetadata:\n  resourceVersion: \"\"\n"},
		{format: "yaml", items: items, want: "apiVersion: v1\nitems:\n- apiVersion: v1\n  kind: ConfigMap\n  metadata:\n    generation: 2\n    name: apple-1\nkind: List\nmetadata:\n  resourceVersion: \"\"\n"},
		{format: "kyaml", items: items, want: `---
{
  apiVersion: "v1",
  items: [{
    apiVersion: "v1",
    kind: "ConfigMap",
    metadata: {
      generation: 2,
      name: "apple-1",
    },
  }],
  kind: "List",
  metadata: {
    resourceVersion: "",
  },
}
`},
		{format: "json", items: items, want: `{
    "apiVersion": "v1",
    "items": [
        {
            "apiVersion": "v1",
            "kind": "ConfigMap",
            "metadata": {
                "generation": 2,
                "name": "apple-1"
            }
        }
    ],
    "kind": "List",
    "metadata": {
        "resourceVersion": ""
    }
}
`},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			var b bytes.Buffer
			require.NoError(t, List(&b, tt.format, tt.items))
			assert.Equal(t, tt.want, b.String())
		})
	}
}
