package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMetadataMaps(t *testing.T) {
	tests := []struct {
		name           string
		objs           []Metadata
		escapeNewlines bool
		want           string
	}{
		{
			name: "one object",
			objs: []Metadata{{Cluster: "apple", Namespace: "kiwi", Name: "banana-web", Map: map[string]string{"tier": "web", "app": "banana"}}},
			want: "app    banana\ntier   web\n",
		},
		{
			name: "several, headed by what differs",
			objs: []Metadata{
				{Cluster: "apple", Namespace: "kiwi", Name: "apple-db", Map: map[string]string{"app": "apple"}},
				{Cluster: "banana", Namespace: "kiwi", Name: "apple-web", Map: map[string]string{"tier": "web"}},
			},
			want: "apple/apple-db\napp    apple\n\nbanana/apple-web\ntier   web\n",
		},
		{
			name: "empty maps print nothing but keep headings",
			objs: []Metadata{
				{Namespace: "kiwi", Name: "apple-db"},
				{Namespace: "kiwi", Name: "apple-web", Map: map[string]string{"tier": "web"}},
				{Namespace: "kiwi", Name: "apple-xyz", Map: map[string]string{}},
			},
			want: "apple-web\ntier   web\n",
		},
		{
			name: "one namespace beside a cluster-scoped object",
			objs: []Metadata{
				{Namespace: "kiwi", Name: "pod/apple-1", Map: map[string]string{"app": "apple"}},
				{Name: "node/fig", Map: map[string]string{"zone": "a"}},
			},
			want: "pod/apple-1\napp    apple\n\nnode/fig\nzone   a\n",
		},
		{
			name: "several namespaces beside a cluster-scoped object, whose is -",
			objs: []Metadata{
				{Namespace: "kiwi", Name: "pod/apple-1", Map: map[string]string{"app": "apple"}},
				{Namespace: "lemon", Name: "pod/apple-2", Map: map[string]string{"app": "apple"}},
				{Name: "node/fig", Map: map[string]string{"zone": "a"}},
			},
			want: "kiwi/pod/apple-1\napp    apple\n\nlemon/pod/apple-2\napp    apple\n\n-/node/fig\nzone   a\n",
		},
		{
			name: "later lines indented to the value column",
			objs: []Metadata{{Name: "cherry", Map: map[string]string{"empty": "", "note": "line 1\n\nline 3\ttabbed \\ é 日本"}}},
			want: "empty\nnote    line 1\n\n        line 3\ttabbed \\ é 日本\n",
		},
		{
			name: "terminal codes escaped",
			objs: []Metadata{{Name: "cherry", Map: map[string]string{"note": "\x1b[31mred\u202e\r\u009b\a\U000e0001"}}},
			want: "note   \\x1b[31mred\\u202e\\r\\u009b\\a\\U000e0001\n",
		},
		{
			name: "spaces of any script kept",
			objs: []Metadata{{Name: "cherry", Map: map[string]string{"note": "a\u00a0b\u3000c"}}},
			want: "note   a\u00a0b\u3000c\n",
		},
		{
			name:           "newlines escaped",
			objs:           []Metadata{{Name: "cherry", Map: map[string]string{"note": "line 1\nline 2\t\\ é 日本\x1b"}}},
			escapeNewlines: true,
			want:           "note   line 1\\nline 2\\t\\\\ é 日本\\x1b\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MetadataMaps(tt.objs, tt.escapeNewlines))
		})
	}
}
