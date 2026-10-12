package kube

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeTable(t *testing.T) {
	table := `{
		"kind": "Table", "apiVersion": "meta.k8s.io/v1", "metadata": {"continue": "next"},
		"columnDefinitions": [{"name": "Name", "type": "string", "format": "name", "priority": 0}, {"name": "Restarts", "type": "integer", "priority": 0}, {"name": "IP", "type": "string", "priority": 1}],
		"rows": [{"cells": ["apple-1", 3, null], "object": {"kind": "PartialObjectMetadata", "metadata": {"name": "apple-1", "namespace": "banana", "labels": {"app": "apple"}}}}]
	}`
	got, cont, err := decodeTable([]byte(table), listShape)
	require.NoError(t, err)
	assert.Equal(t, "next", cont)
	require.Len(t, got.Columns, 3)
	assert.Equal(t, int32(1), got.Columns[2].Priority)
	require.Len(t, got.Rows, 1)
	assert.Equal(t, []any{"apple-1", json.Number("3"), nil}, got.Rows[0].Cells)
	assert.Equal(t, "banana", got.Rows[0].Meta.Namespace)
	assert.Equal(t, map[string]string{"app": "apple"}, got.Rows[0].Meta.Labels)
}

func TestDecodeTableFallback(t *testing.T) {
	created := time.Now().Add(-90 * time.Minute).UTC().Format(time.RFC3339)
	tests := []struct {
		name     string
		raw      string
		asked    shape
		wantRows [][]any
		wantCont string
	}{
		{
			name:     "object",
			raw:      `{"kind": "Widget", "metadata": {"name": "donut"}}`,
			asked:    objectShape,
			wantRows: [][]any{{"donut", "<unknown>"}},
		},
		{
			name:     "object whose kind ends in List",
			raw:      `{"kind": "AllowList", "metadata": {"name": "egg"}}`,
			asked:    objectShape,
			wantRows: [][]any{{"egg", "<unknown>"}},
		},
		{
			name:     "list",
			raw:      `{"kind": "WidgetList", "metadata": {"continue": "next"}, "items": [{"metadata": {"name": "cherry", "creationTimestamp": "` + created + `"}}]}`,
			asked:    listShape,
			wantRows: [][]any{{"cherry", "90m"}},
			wantCont: "next",
		},
		{
			name:     "generic List",
			raw:      `{"kind": "List", "metadata": {}, "items": [{"metadata": {"name": "fig"}}, {"metadata": {"name": "grape"}}]}`,
			asked:    listShape,
			wantRows: [][]any{{"fig", "<unknown>"}, {"grape", "<unknown>"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cont, err := decodeTable([]byte(tt.raw), tt.asked)
			require.NoError(t, err)
			assert.Equal(t, []string{"Name", "Age"}, []string{got.Columns[0].Name, got.Columns[1].Name})
			assert.Equal(t, tt.wantRows, lo.Map(got.Rows, func(r Row, _ int) []any { return r.Cells }))
			assert.Equal(t, tt.wantCont, cont)
		})
	}
}
