package render

import (
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/printers"
)

// List prints objects as one List, with kubectl's json, yaml, or kyaml printer.
//
// kubectl prints several names in one namespace as exactly this List, so kuba's merged output across namespaces and
// clusters reads the same to jq and fx.
func List(w io.Writer, format string, items []map[string]any) error {
	var p printers.ResourcePrinter
	switch format {
	case "json":
		p = &printers.JSONPrinter{}
	case "yaml":
		p = &printers.YAMLPrinter{}
	case "kyaml":
		p = &printers.KYAMLPrinter{}
	default:
		return fmt.Errorf("render: no List format %q", format)
	}

	list := &unstructured.UnstructuredList{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "List",
		"metadata":   map[string]any{"resourceVersion": ""},
	}}
	for _, item := range items {
		list.Items = append(list.Items, unstructured.Unstructured{Object: item})
	}

	return p.PrintObj(list, w)
}
