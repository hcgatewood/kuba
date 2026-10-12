package render

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/samber/lo"
)

// Metadata is one object's labels or annotations, and where it lives.
type Metadata struct {
	// Map is the object's labels or annotations, whichever the command asked for.
	Map map[string]string

	// Cluster is the object's cluster.
	Cluster string
	// Namespace is the object's namespace; or empty for a cluster-scoped object.
	Namespace string
	// Name is the object's name as kubectl takes it, e.g. donut-web, or pod/donut-web when the command spans types.
	Name string
}

// MetadataMaps renders objects' labels or annotations as blocks of key-value lines, e.g. `app   donut`.
//
//   - Entries sort by key, with keys padded so values line up across every block
//   - With several objects, each block opens with a heading of the parts that tell its object apart, e.g.
//     banana/cherry/donut-web across clusters and namespaces, or just donut-web within one; a blank line separates
//     blocks
//   - An object without entries gets no block, but still counts toward headings, so they don't hinge on which had entries
//   - A multi-line value's later lines are indented to the value column, unless escapeNewlines
//   - Non-printing characters are always escaped (see escapeValue)
func MetadataMaps(objs []Metadata, escapeNewlines bool) string {
	layout := newMetadataLayout(objs, escapeNewlines)
	var blocks []string
	for _, obj := range objs {
		if len(obj.Map) > 0 {
			blocks = append(blocks, layout.block(obj))
		}
	}
	return strings.Join(blocks, "\n")
}

// metadataLayout is how MetadataMaps lays out every block, settled across all objects so the blocks agree.
type metadataLayout struct {
	// headed is whether blocks get headings, i.e. there are several objects.
	headed bool
	// withCluster is whether headings name objects' clusters, i.e. objects span several.
	withCluster bool
	// withNamespace is whether headings name objects' namespaces, i.e. namespaced objects span several. A
	// cluster-scoped object doesn't count, as its type already tells it apart, e.g. node/fig beside pod/apple-1.
	withNamespace bool

	// indent is the spaces before the value column: the longest key, then 3 more, as kubectl pads table columns.
	indent string
	// escapeNewlines is whether values' newlines (and tabs) are escaped, putting each value on one line (see
	// escapeValue).
	escapeNewlines bool
}

func newMetadataLayout(objs []Metadata, escapeNewlines bool) metadataLayout {
	keys := lo.FlatMap(objs, func(o Metadata, _ int) []string { return lo.Keys(o.Map) })
	keyWidth := lo.Max(lo.Map(keys, func(k string, _ int) int { return len(k) }))
	namespaces := lo.Compact(lo.Map(objs, func(o Metadata, _ int) string { return o.Namespace }))
	return metadataLayout{
		headed:         len(objs) > 1,
		withCluster:    len(lo.UniqBy(objs, func(o Metadata) string { return o.Cluster })) > 1,
		withNamespace:  len(lo.Uniq(namespaces)) > 1,
		indent:         strings.Repeat(" ", keyWidth+3),
		escapeNewlines: escapeNewlines,
	}
}

// block is an object's heading, if blocks get one, then a line per entry, sorted by key.
func (l metadataLayout) block(obj Metadata) string {
	var b strings.Builder
	if l.headed {
		b.WriteString(l.heading(obj) + "\n")
	}
	for _, k := range slices.Sorted(maps.Keys(obj.Map)) {
		b.WriteString(l.entry(k, obj.Map[k]) + "\n")
	}
	return b.String()
}

// heading names an object by the parts that tell it apart, e.g. banana/cherry/donut-web, with - for a cluster-scoped
// object's namespace.
func (l metadataLayout) heading(obj Metadata) string {
	var parts []string
	if l.withCluster {
		parts = append(parts, obj.Cluster)
	}
	if l.withNamespace {
		parts = append(parts, cmp.Or(obj.Namespace, "-"))
	}
	return strings.Join(append(parts, obj.Name), "/")
}

// entry is a key and its value, which starts at the value column, as does each later line of a multi-line value.
//
// Example of two entries:
//
//	anno-key-1   anno-val-a-line-1
//	             anno-val-a-line-2
//	             anno-val-a-line-3
//	anno-key-2   anno-val-b-line-1
//	             anno-val-b-line-2
func (l metadataLayout) entry(key, value string) string {
	valLines := strings.Split(escapeValue(value, l.escapeNewlines), "\n")
	for i, valLine := range valLines {
		pad := l.indent
		if i == 0 {
			pad = l.indent[len(key):] // the key already fills that much of the first line's indent
		}
		if valLine != "" {
			valLines[i] = pad + valLine
		}
	}
	return key + strings.Join(valLines, "\n")
}

// escapeValue returns s with its invisible characters escaped, e.g. \x1b or \u202e, so no terminal sequence or bidi
// override reaches the terminal. Visible characters and spaces stay as they are, accented or CJK alike.
//
// Newlines, tabs, and backslashes stay too, for readability. escapeNewlines escapes them instead, as \n, \t, and \\,
// putting the value on one line; doubling backslashes keeps a literal \n in the value apart from an escaped newline.
func escapeValue(s string, escapeNewlines bool) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsGraphic(r) && r != '\\' || !escapeNewlines && strings.ContainsRune("\n\t\\", r) {
			b.WriteRune(r)
		} else {
			b.WriteString(strings.Trim(strconv.QuoteRune(r), "'"))
		}
	}
	return b.String()
}
