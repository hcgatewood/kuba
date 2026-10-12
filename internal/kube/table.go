package kube

import (
	"bytes"
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
)

// Table is rows from the server's Table API, each with its object's metadata.
type Table struct {
	Columns []metav1.TableColumnDefinition
	Rows    []Row
}

// Row is one object's cells, as the server printed them, plus its metadata.
type Row struct {
	// Cells are strings, json.Numbers, bools, or nil, one per column.
	Cells []any
	Meta  metav1.ObjectMeta
}

// decodeTable reads a Table response, returning the continue token of a chunked LIST.
//
// APIs that don't serve Tables (some aggregated APIs) answer with plain JSON instead, which fallbackTable turns into a
// Table, reading it as the shape kuba asked for.
//
// REF: https://github.com/kubernetes/kubectl/blob/v0.37.1/pkg/cmd/get/table_printer.go#L58-L94
func decodeTable(raw []byte, asked shape) (*Table, string, error) {
	var t metav1.Table
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&t); err != nil {
		return nil, "", err
	}

	if t.Kind != "Table" {
		out, err := fallbackTable(raw, asked)
		return out, t.Continue, err // a plain list's continue token sits where a Table's does
	}

	out := &Table{Columns: t.ColumnDefinitions}
	for _, row := range t.Rows {
		var obj metav1.PartialObjectMetadata
		if len(row.Object.Raw) > 0 {
			if err := json.Unmarshal(row.Object.Raw, &obj); err != nil {
				return nil, "", err
			}
		}
		out.Rows = append(out.Rows, Row{Cells: row.Cells, Meta: obj.ObjectMeta})
	}
	return out, t.Continue, nil
}

// shape is what kuba asked the server for, so what a plain-JSON answer holds: one object, or a list of them.
type shape string

const (
	// objectShape is a GET by name, answered with the object itself, e.g. kind Pod.
	objectShape shape = "object"
	// listShape is a LIST, answered with a list, e.g. kind PodList.
	listShape shape = "list"
)

// fallbackTable builds the Table the server didn't send, with the columns kubectl falls back to: NAME and AGE, from
// each object's metadata.
//
// The server sent what kuba asked for:
//   - objectShape: the object itself, so one row
//   - listShape: a list, so a row per item
//
// REF: https://github.com/kubernetes/cli-runtime/blob/v0.37.1/pkg/printers/tableprinter.go#L675-L703
func fallbackTable(raw []byte, asked shape) (*Table, error) {
	var objs []metav1.PartialObjectMetadata
	if asked == objectShape {
		var obj metav1.PartialObjectMetadata
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
		objs = []metav1.PartialObjectMetadata{obj}
	} else {
		var list metav1.PartialObjectMetadataList
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		objs = list.Items
	}

	out := &Table{
		Columns: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Age", Type: "string"},
		},
	}
	for _, obj := range objs {
		m := obj.ObjectMeta
		out.Rows = append(out.Rows, Row{Cells: []any{m.Name, age(m.CreationTimestamp)}, Meta: m})
	}
	return out, nil
}

// age renders a creation time as kubectl's AGE column does, e.g. 5m or 3d4h.
func age(created metav1.Time) string {
	if created.IsZero() {
		return "<unknown>"
	}
	return duration.HumanDuration(time.Since(created.Time))
}
