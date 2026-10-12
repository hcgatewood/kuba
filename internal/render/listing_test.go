package render

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/hcgatewood/kuba/internal/kube"
)

// TestListing pins listings to kubectl's bytes, since e2e goldens collapse runs of spaces and so never see widths.
//
// Columns are their widest cell plus 3, and at least 6 including that padding, with the last unpadded. Widths count a
// cell's bytes before any cut, so a cut or non-ASCII cell widens its column past what's shown, as it does in kubectl.
func TestListing(t *testing.T) {
	cols := []metav1.TableColumnDefinition{
		{Name: "Name", Type: "string", Format: "name"},
		{Name: "Status", Type: "string"},
		{Name: "Nominated Node", Type: "string", Priority: 1},
	}
	row := func(cluster, ns, name string, cells ...any) Row {
		return Row{Cluster: cluster, Row: kube.Row{
			Cells: append([]any{name}, cells...),
			Meta:  metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app": "apple", "node.kubernetes.io/instance-type": "m5.large"}},
		}}
	}
	section := Section{Kind: schema.GroupKind{Kind: "Pod"}, Namespaced: true, Columns: cols, Rows: []Row{
		row("apple", "kiwi", "apple-1", "Running", nil),
		row("banana", "lemon", "apple-2", "Pending\nfor a while", "fig"),
	}}
	pods := Section{
		Columns: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Ready", Type: "string"},
			{Name: "Status", Type: "string"},
			{Name: "Restarts", Type: "integer"},
			{Name: "Age", Type: "string"},
		},
		Rows: []Row{
			row("", "", "apple-1", "0/1", "Running", json.Number("0"), "5m"),
			row("", "", "banana-10", "1/1", "CrashLoopBackOff", json.Number("12"), "3d4h"),
		},
	}
	tests := []struct {
		name      string
		section   Section
		opts      ListingOptions
		noHeaders bool
		want      string
	}{
		{
			name:    "pods",
			section: pods,
			want: "" +
				"NAME        READY   STATUS             RESTARTS   AGE\n" +
				"apple-1     0/1     Running            0          5m\n" +
				"banana-10   1/1     CrashLoopBackOff   12         3d4h\n",
		},
		{
			name:      "no headers",
			section:   pods,
			noHeaders: true,
			want: "" +
				"apple-1     0/1   Running            0     5m\n" +
				"banana-10   1/1   CrashLoopBackOff   12    3d4h\n",
		},
		{
			name: "narrow columns pad to 6",
			section: Section{
				Columns: []metav1.TableColumnDefinition{{Name: "A", Type: "string"}, {Name: "B", Type: "string"}, {Name: "C", Type: "string"}},
				Rows:    []Row{row("", "", "x", "y", "z")},
			},
			want: "A     B     C\nx     y     z\n",
		},
		{
			name: "nil cells print empty",
			section: Section{
				Columns: []metav1.TableColumnDefinition{{Name: "Name", Type: "string"}, {Name: "Version", Type: "string"}, {Name: "Type", Type: "string"}},
				Rows:    []Row{row("", "", "fig", nil, "m5.large")},
			},
			want: "NAME   VERSION   TYPE\nfig              m5.large\n",
		},
		{
			name:    "no rows print nothing",
			section: Section{Columns: cols},
		},
		{
			name:    "priority 0",
			section: section,
			want:    "NAME      STATUS\napple-1   Running\napple-2   Pending...\n",
		},
		{
			name:    "wide, the cut cell widening its column",
			section: section,
			opts:    ListingOptions{Wide: true},
			want: "" +
				"NAME      STATUS                NOMINATED NODE\n" +
				"apple-1   Running               \n" +
				"apple-2   Pending...            fig\n",
		},
		{
			name:    "cluster and namespace",
			section: section,
			opts:    ListingOptions{Cluster: true, AllNamespaces: true},
			want: "" +
				"CLUSTER   NAMESPACE   NAME      STATUS\n" +
				"apple     kiwi        apple-1   Running\n" +
				"banana    lemon       apple-2   Pending...\n",
		},
		{
			name:    "cluster-scoped type skips namespace",
			section: Section{Columns: cols, Rows: section.Rows[:1]},
			opts:    ListingOptions{AllNamespaces: true},
			want:    "NAME      STATUS\napple-1   Running\n",
		},
		{
			name:    "kind prefix and label columns",
			section: Section{Kind: schema.GroupKind{Group: "apps", Kind: "Deployment"}, Columns: cols, Rows: section.Rows[:1]},
			opts:    ListingOptions{WithKind: true, LabelColumns: []string{"node.kubernetes.io/instance-type", "missing"}},
			want: "" +
				"NAME                      STATUS    INSTANCE-TYPE   MISSING\n" +
				"deployment.apps/apple-1   Running   m5.large        \n",
		},
		{
			name:    "labels after label columns",
			section: section,
			opts:    ListingOptions{LabelColumns: []string{"app"}, ShowLabels: true},
			want: "" +
				"NAME      STATUS                APP     LABELS\n" +
				"apple-1   Running               apple   app=apple,node.kubernetes.io/instance-type=m5.large\n" +
				"apple-2   Pending...            apple   app=apple,node.kubernetes.io/instance-type=m5.large\n",
		},
		{
			name:    "terminal escapes",
			section: Section{Columns: cols, Rows: []Row{row("", "", "apple-1", "\x1b[2Jcleared\rRunning")}},
			want:    "NAME      STATUS\napple-1   ^[[2Jcleared...\n",
		},
		{
			name:    "non-ASCII widths count bytes",
			section: Section{Columns: cols, Rows: []Row{row("", "", "crème-1", "Running")}},
			want:    "NAME       STATUS\ncrème-1    Running\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			require.NoError(t, Listing{Section: tt.section, Options: tt.opts}.Write(&b, !tt.noHeaders))
			assert.Equal(t, tt.want, b.String())
		})
	}
}

func TestLines(t *testing.T) {
	section := Section{
		Columns: []metav1.TableColumnDefinition{{Name: "Name", Type: "string", Format: "name"}, {Name: "Age", Type: "string"}},
		Rows: []Row{
			{Row: kube.Row{Cells: []any{"apple-1", "5m"}}},
			{Row: kube.Row{Cells: []any{"banana-1", "2h"}}},
		},
	}
	header, rows := Listing{Section: section}.Lines()
	assert.Equal(t, "NAME       AGE", header)
	assert.Equal(t, []string{"apple-1    5m", "banana-1   2h"}, rows)

	header, rows = Listing{Section: Section{Columns: section.Columns}}.Lines()
	assert.Empty(t, header)
	assert.Empty(t, rows)
}
