package render

import (
	"bytes"
	"io"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/printers"

	"github.com/hcgatewood/kuba/internal/kube"
)

// Section is one table of get's output: a type's rows from every cluster, merged under one header, as
// `kubectl get pod,svc` prints a table per type.
//
// It merges each cluster's kube.Table of the type, and prints through a Listing.
//
// Types can't share a table, as each has its own columns, e.g. a pod's READY and STATUS, a service's TYPE and
// CLUSTER-IP. So `kuba get pod,svc -m` prints two sections, pods then services, each holding rows from every cluster.
// For the same reason, a cluster serving a type with other columns, e.g. an older CRD version's, gets its own section.
type Section struct {
	// Kind is the section's type, e.g. deployment.apps, which prefixes each name under ListingOptions.WithKind.
	Kind schema.GroupKind
	// Namespaced types get a NAMESPACE column when the listing spans namespaces.
	Namespaced bool
	// Columns are the server's column definitions, which every row's cells fill, whatever its cluster.
	Columns []metav1.TableColumnDefinition
	// Rows are its objects, in print order, each with its cluster.
	Rows []Row
}

// Row is one object's row, and the cluster it came from.
type Row struct {
	kube.Row
	Cluster string
}

// Listing is a section ready to print as kubectl prints a Table, under the columns Options shape, e.g. with a leading
// CLUSTER column on a fan-out (see Write and Lines).
type Listing struct {
	Section Section
	Options ListingOptions
}

// ListingOptions shape a listing's columns.
type ListingOptions struct {
	// Wide shows every column, not just priority 0 ones, as -o wide does.
	Wide bool
	// Cluster adds a leading CLUSTER column, for a listing that spans clusters.
	Cluster bool
	// AllNamespaces adds a NAMESPACE column for namespaced types, as -A does.
	AllNamespaces bool
	// WithKind prefixes each name with the section's Kind, e.g. deployment.apps/apple, as kubectl does when listing
	// several types, or with --show-kind.
	WithKind bool
	// LabelColumns adds a column per label key, headed by the key's last segment, as -L does.
	LabelColumns []string
	// ShowLabels adds a last LABELS column, each row's labels as key=value pairs, as --show-labels does.
	ShowLabels bool
}

// Write prints the listing to w with kubectl's table printer, led by a header row of column names when headers is set.
//
// The printer does what kubectl get does with a Table:
//   - Pads each column to its widest cell plus 3, and to at least 6 including that padding; the last stays unpadded
//   - Drops columns of priority above 0, unless Wide
//   - Cuts a cell at its first line break, marking the cut with "..."
//   - Escapes terminal control characters, e.g. ESC as ^[, so a cell can't restyle or rewrite the terminal
//   - Prefixes names with the section's Kind under WithKind, and appends the label columns, then LABELS
//   - Prints nothing at all for no rows, not even headers
//
// REF: https://github.com/kubernetes/cli-runtime/blob/v0.37.1/pkg/printers/tableprinter.go#L105-L182
func (l Listing) Write(w io.Writer, headers bool) error {
	return printers.NewTablePrinter(l.printOptions(headers)).PrintObj(l.table(), w)
}

// Lines prints the listing, returning its header and each row as one line, e.g. for fzf. Both are empty for no rows.
//
// Unlike a printed table, which may have no header, e.g. off a terminal or under --no-headers, Lines always returns
// one when there are rows: fzf pins it above the rows, where it can't be picked.
func (l Listing) Lines() (header string, rows []string) {
	var b bytes.Buffer
	_ = l.Write(&b, true)
	if b.Len() == 0 {
		return "", nil
	}
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	return lines[0], lines[1:]
}

// table is the section as a Table for the printer, led by kuba's CLUSTER column, then NAMESPACE.
//
// kuba adds NAMESPACE itself: the printer's own would come before CLUSTER. Each row's object carries the metadata the
// printer reads labels from.
func (l Listing) table() *metav1.Table {
	var lead []metav1.TableColumnDefinition
	showCluster := l.Options.Cluster
	if showCluster {
		lead = append(lead, metav1.TableColumnDefinition{Name: "Cluster", Type: "string"})
	}
	showNamespace := l.Options.AllNamespaces && l.Section.Namespaced
	if showNamespace {
		lead = append(lead, metav1.TableColumnDefinition{Name: "Namespace", Type: "string"})
	}

	table := &metav1.Table{ColumnDefinitions: slices.Concat(lead, l.Section.Columns)}
	for _, row := range l.Section.Rows {
		var cells []any
		if showCluster {
			cells = append(cells, row.Cluster)
		}
		if showNamespace {
			cells = append(cells, row.Meta.Namespace)
		}
		table.Rows = append(table.Rows, metav1.TableRow{
			Cells:  append(cells, row.Cells...), // a copy, since the printer edits cells in place
			Object: runtime.RawExtension{Object: &metav1.PartialObjectMetadata{ObjectMeta: row.Meta}},
		})
	}
	return table
}

// printOptions are kubectl's printer options for the listing: the columns kubectl adds itself, and whether to head them.
func (l Listing) printOptions(headers bool) printers.PrintOptions {
	return printers.PrintOptions{
		NoHeaders:    !headers,
		Wide:         l.Options.Wide,
		WithKind:     l.Options.WithKind,
		Kind:         l.Section.Kind,
		ColumnLabels: l.Options.LabelColumns,
		ShowLabels:   l.Options.ShowLabels,
	}
}
