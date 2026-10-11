package cli

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/hcgatewood/kuba/internal/config"
	"github.com/hcgatewood/kuba/internal/flagspec"
	"github.com/hcgatewood/kuba/internal/kube"
	"github.com/hcgatewood/kuba/internal/render"
)

// TestFanOutOptions checks that --request-timeout drops the default cluster timeout, but not one the user set.
func TestFanOutOptions(t *testing.T) {
	tests := []struct {
		name        string
		argv        string
		env         map[string]string
		wantTimeout time.Duration
	}{
		{name: "default", argv: "pod", wantTimeout: 15 * time.Second},
		{name: "request timeout drops the default", argv: "pod --request-timeout=1m"},
		{name: "flag beats request timeout", argv: "pod --request-timeout=1m --cluster-timeout=30s", wantTimeout: 30 * time.Second},
		{name: "env beats request timeout", argv: "pod --request-timeout=1m", env: map[string]string{"KUBA_CLUSTER_TIMEOUT": "30s"}, wantTimeout: 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmdline, err := flagspec.Get.Parse(strings.Fields(tt.argv))
			require.NoError(t, err)
			opts, err := config.Load(cmdline, func(k string) string { return tt.env[k] })
			require.NoError(t, err)
			g := &getRun{app: &app{}, cmdline: cmdline, opts: opts}
			assert.Equal(t, tt.wantTimeout, g.fanOutOptions().Timeout)
		})
	}
}

// TestGather checks which rows share a section: a type's rows from every cluster, unless a cluster's columns differ.
func TestGather(t *testing.T) {
	pods := &meta.RESTMapping{
		Resource:         schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		GroupVersionKind: schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
		Scope:            meta.RESTScopeNamespace,
	}
	svcs := &meta.RESTMapping{
		Resource:         schema.GroupVersionResource{Version: "v1", Resource: "services"},
		GroupVersionKind: schema.GroupVersionKind{Version: "v1", Kind: "Service"},
		Scope:            meta.RESTScopeNamespace,
	}
	podColumns := []metav1.TableColumnDefinition{{Name: "Name", Type: "string", Format: "name"}, {Name: "Ready", Type: "string"}}
	reworded := []metav1.TableColumnDefinition{{Name: "Name", Type: "string", Format: "name", Description: "Name must be unique within a namespace."}, {Name: "Ready", Type: "string"}}
	fallback := []metav1.TableColumnDefinition{{Name: "Name", Type: "string", Format: "name"}, {Name: "Age", Type: "string"}}
	type lister struct {
		mapping *meta.RESTMapping
		columns []metav1.TableColumnDefinition
		name    string
	}
	tests := []struct {
		name             string
		clusters         map[string][]lister
		wantSections     [][]string
		wantSeveralTypes bool
	}{
		{
			name:         "clusters share a type's section",
			clusters:     map[string][]lister{"apple": {{pods, podColumns, "cherry-1"}}, "banana": {{pods, podColumns, "cherry-2"}}},
			wantSections: [][]string{{"apple/cherry-1", "banana/cherry-2"}},
		},
		{
			name:         "reworded descriptions still share",
			clusters:     map[string][]lister{"apple": {{pods, podColumns, "cherry-1"}}, "banana": {{pods, reworded, "cherry-2"}}},
			wantSections: [][]string{{"apple/cherry-1", "banana/cherry-2"}},
		},
		{
			name:         "other columns split a type",
			clusters:     map[string][]lister{"apple": {{pods, podColumns, "cherry-1"}}, "banana": {{pods, fallback, "cherry-2"}}},
			wantSections: [][]string{{"apple/cherry-1"}, {"banana/cherry-2"}},
		},
		{
			name:             "types split",
			clusters:         map[string][]lister{"apple": {{pods, podColumns, "cherry-1"}, {svcs, fallback, "donut"}}},
			wantSections:     [][]string{{"apple/cherry-1"}, {"apple/donut"}},
			wantSeveralTypes: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmdline, err := flagspec.Get.Parse([]string{"pod"})
			require.NoError(t, err)
			g := &getRun{app: &app{}, cmdline: cmdline}
			var results []kube.Result[*clusterRows]
			for _, cluster := range slices.Sorted(maps.Keys(tt.clusters)) {
				rows := &clusterRows{}
				for _, li := range tt.clusters[cluster] {
					rows.mappings = append(rows.mappings, li.mapping)
					rows.tables = append(rows.tables, &kube.Table{Columns: li.columns, Rows: []kube.Row{{Meta: metav1.ObjectMeta{Name: li.name}}}})
				}
				results = append(results, kube.Result[*clusterRows]{Cluster: &kube.Cluster{Name: cluster}, Value: rows})
			}

			l := g.gather(results)
			got := lo.Map(l.sections, func(s *render.Section, _ int) []string {
				return lo.Map(s.Rows, func(r render.Row, _ int) string { return r.Cluster + "/" + r.Meta.Name })
			})
			assert.Equal(t, tt.wantSections, got)
			assert.Equal(t, tt.wantSeveralTypes, l.severalTypes())
		})
	}
}
