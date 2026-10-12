// Package render prints kuba's own output: listings, label and annotation maps, merged Lists, and the fan-out progress
// line.
//
// What kuba prints should look like kubectl printed it, so render uses kubectl's own printers:
//   - A Listing goes through kubectl's table printer, plus a CLUSTER column on a fan-out. So kuba's listings match
//     handed-off ones byte for byte.
//   - List goes through kubectl's json, yaml, or kyaml printer. It's for objects across namespaces or clusters, which
//     no single kubectl command could print as one List.
//   - Printer colors a table the way kubecolor colors kubectl's. It colors after padding, so color never skews column
//     widths.
//
// The rest is kuba's own:
//   - MetadataMaps prints -o labels and -o annotations
//   - Progress shows, on a terminal, which clusters a slow fan-out is still waiting on
package render
