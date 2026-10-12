package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/resource"

	"github.com/hcgatewood/kuba/internal/flagspec"
)

func TestLeaseHolders(t *testing.T) {
	pages := map[string]string{
		"":   `{"metadata": {"continue": "c1"}, "items": [{"metadata": {"name": "apple", "namespace": "kiwi"}, "spec": {"holderIdentity": "apple-1_1"}}]}`,
		"c1": `{"metadata": {"continue": "c2"}, "items": [{"metadata": {"name": "banana", "namespace": "kiwi"}, "spec": {}}, {"metadata": {"name": "cherry", "namespace": "lemon"}, "spec": {"holderIdentity": "cherry-1_1"}}]}`,
		"c2": `{"metadata": {}, "items": [{"metadata": {"name": "donut", "namespace": "lemon"}, "spec": {"holderIdentity": "donut-1_1"}}]}`,
	}
	var continues []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A real server reads only the first of several continue values, so a client sending more loops; fail fast.
		cont := r.URL.Query()["continue"]
		if len(cont) > 1 || r.URL.Path != "/apis/coordination.k8s.io/v1/leases" {
			http.Error(w, fmt.Sprintf("unexpected request %s", r.URL), http.StatusBadRequest)
			return
		}
		continues = append(continues, r.URL.Query().Get("continue"))
		_, _ = io.WriteString(w, pages[r.URL.Query().Get("continue")])
	}))
	defer srv.Close()

	k, err := LoadKubeconfig([]flagspec.FlagValue{
		{Flag: flagspec.Flag{Name: "kubeconfig"}, Value: writeKubeconfig(t, srv.URL)},
		{Flag: flagspec.Flag{Name: "cache-dir"}, Value: t.TempDir()},
	}, "", RateLimit{}, slog.New(slog.DiscardHandler), io.Discard)
	require.NoError(t, err)
	c, err := k.Cluster("apple")
	require.NoError(t, err)

	got, err := c.LeaseHolders(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, LeaseHolders{{"kiwi", "apple-1_1"}, {"lemon", "cherry-1_1"}, {"lemon", "donut-1_1"}}, got)
	assert.Equal(t, []string{"", "c1", "c2"}, continues)
}

func TestLeaseHoldersHold(t *testing.T) {
	holders := LeaseHolders{{Namespace: "kiwi", Holder: "apple-db-leader_1a2b"}}
	tests := []struct {
		name      string
		namespace string
		object    string
		want      bool
	}{
		{name: "holder's own name", namespace: "kiwi", object: "apple-db-leader", want: true},
		{name: "name starting the holder's", namespace: "kiwi", object: "apple-db", want: true},
		{name: "other namespace", namespace: "lemon", object: "apple-db-leader"},
		{name: "other name", namespace: "kiwi", object: "banana"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, holders.Hold(tt.namespace, tt.object))
		})
	}
}

// TestMappingsMatchKubectl resolves type arguments with kuba and with kubectl's resource builder, against one server.
//
// Mappings ports the builder's resolution, so the two must agree on every type, its order, and every error. want pins
// what kubectl resolves, so the fixture can't drift into agreeing on nothing. The two share one cache directory, as
// kuba and kubectl do, and must keep the server's discovery in the same place in it.
func TestMappingsMatchKubectl(t *testing.T) {
	srv := discoveryServer(t, fixtureResources)
	path, cacheDir := writeKubeconfig(t, srv.URL), t.TempDir()

	k, err := LoadKubeconfig([]flagspec.FlagValue{
		{Flag: flagspec.Flag{Name: "kubeconfig"}, Value: path},
		{Flag: flagspec.Flag{Name: "cache-dir"}, Value: cacheDir},
	}, "", RateLimit{}, slog.New(slog.DiscardHandler), io.Discard)
	require.NoError(t, err)
	c, err := k.Cluster("apple")
	require.NoError(t, err)

	flags := kubectlConfigFlags()
	flags.KubeConfig, flags.CacheDir = &path, &cacheDir

	all := "v1/pods, v1/services, apps/v1/deployments, apps/v1/replicasets, autoscaling/v2/horizontalpodautoscalers"
	tests := []struct {
		arg  string
		want string
	}{
		{arg: "pods", want: "v1/pods"},
		{arg: "po", want: "v1/pods"},
		{arg: "Pod", want: "v1/pods"},
		{arg: "deploy", want: "apps/v1/deployments"},
		{arg: "deployments.apps", want: "apps/v1/deployments"},
		{arg: "deployments.v1.apps", want: "apps/v1/deployments"},
		{arg: "certificates.cert-manager.io", want: "cert-manager.io/v1/certificates"},
		{arg: "cert", want: "cert-manager.io/v1/certificates"},
		{arg: "banana", want: "example.com/v1/bananas"},
		{arg: "Cherry", want: `the server doesn't have a resource type "Cherry"`},
		{arg: "Cherry.example.com", want: "example.com/v1/bananas"},
		{arg: "Cherry.v1.example.com", want: "example.com/v1/bananas"},
		{arg: "events", want: "v1/events"},
		{arg: "events.events.k8s.io", want: "events.k8s.io/v1/events"},
		{arg: "hpa", want: "autoscaling/v2/horizontalpodautoscalers"},
		{arg: "horizontalpodautoscalers.v1.autoscaling", want: "autoscaling/v1/horizontalpodautoscalers"},
		{arg: "all", want: all},
		{arg: "pod,all", want: all},
		{arg: "svc,all", want: "v1/services, v1/pods, apps/v1/deployments, apps/v1/replicasets, autoscaling/v2/horizontalpodautoscalers"},
		{arg: "deploy,pod,po,pods", want: "apps/v1/deployments, v1/pods"},
		{arg: "cert-manager", want: "cert-manager.io/v1/certificates, cert-manager.io/v1/issuers"},
		{arg: "nope", want: `the server doesn't have a resource type "nope"`},
		{arg: "pod,nope", want: `the server doesn't have a resource type "nope"`},
		{arg: "deployments.nope", want: `the server doesn't have a resource type "deployments" in group "nope"`},
		{arg: "deployments.v9.apps", want: `the server doesn't have a resource type "deployments" in group "v9.apps"`},
		{arg: "pod,", want: `the server doesn't have a resource type ""`},
		{arg: ",", want: `the server doesn't have a resource type ""`},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			var want []*meta.RESTMapping
			wantErr := resource.NewBuilder(flags).
				Unstructured().
				NamespaceParam("kiwi").
				ResourceTypeOrNameArgs(true, tt.arg).
				Do().
				Visit(func(info *resource.Info, err error) error { // once per type, as each lists nothing
					want = append(want, info.Mapping)
					return err
				})
			require.Equal(t, tt.want, resolved(want, wantErr), "kubectl")

			got, err := c.Mappings(context.Background(), tt.arg)
			assert.Equal(t, resolved(want, wantErr), resolved(got, err))
			assert.Equal(t, want, got)
		})
	}

	servers, err := os.ReadDir(filepath.Join(cacheDir, "discovery"))
	require.NoError(t, err)
	assert.Len(t, servers, 1, "kuba's discovery cache should be kubectl's")
}

func TestDiscoveryCacheDir(t *testing.T) {
	assert.Equal(t, "/c/discovery/127.0.0.1_6443", discoveryCacheDir("/c/discovery", "https://127.0.0.1:6443"))
	assert.Equal(t, "/c/discovery/example.com/k8s/clusters/c_1", discoveryCacheDir("/c/discovery", "https://example.com/k8s/clusters/c-1"))
	assert.Equal(t, "/c/discovery/example.com/k8s/(c_1)", discoveryCacheDir("/c/discovery", "https://example.com/k8s/(c-1)"))
}

// fixtureResources is a cluster's discovery, with types that test each way of naming one:
//   - Short names and categories, e.g. po and all
//   - A resource in two groups, events, which the core group wins unqualified
//   - A resource in two versions, horizontalpodautoscalers, whose first, v2, is preferred
//   - A dotted group, cert-manager.io, which a resource.group argument with two dots misreads as version.group
//   - A kind whose lowercase isn't its resource's singular name, Cherry, which names bananas, but only with its group
var fixtureResources = []metav1.APIResourceList{
	{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "pods", SingularName: "pod", Namespaced: true, Kind: "Pod", ShortNames: []string{"po"}, Categories: []string{"all"}},
		{Name: "pods/log", Namespaced: true, Kind: "Pod"},
		{Name: "services", SingularName: "service", Namespaced: true, Kind: "Service", ShortNames: []string{"svc"}, Categories: []string{"all"}},
		{Name: "nodes", SingularName: "node", Kind: "Node", ShortNames: []string{"no"}},
		{Name: "events", SingularName: "event", Namespaced: true, Kind: "Event", ShortNames: []string{"ev"}},
	}},
	{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
		{Name: "deployments", SingularName: "deployment", Namespaced: true, Kind: "Deployment", ShortNames: []string{"deploy"}, Categories: []string{"all"}},
		{Name: "replicasets", SingularName: "replicaset", Namespaced: true, Kind: "ReplicaSet", ShortNames: []string{"rs"}, Categories: []string{"all"}},
	}},
	{GroupVersion: "autoscaling/v2", APIResources: []metav1.APIResource{
		{Name: "horizontalpodautoscalers", SingularName: "horizontalpodautoscaler", Namespaced: true, Kind: "HorizontalPodAutoscaler", ShortNames: []string{"hpa"}, Categories: []string{"all"}},
	}},
	{GroupVersion: "autoscaling/v1", APIResources: []metav1.APIResource{
		{Name: "horizontalpodautoscalers", SingularName: "horizontalpodautoscaler", Namespaced: true, Kind: "HorizontalPodAutoscaler", ShortNames: []string{"hpa"}, Categories: []string{"all"}},
	}},
	{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{
		{Name: "events", SingularName: "event", Namespaced: true, Kind: "Event"},
	}},
	{GroupVersion: "cert-manager.io/v1", APIResources: []metav1.APIResource{
		{Name: "certificates", SingularName: "certificate", Namespaced: true, Kind: "Certificate", ShortNames: []string{"cert", "certs"}, Categories: []string{"cert-manager"}},
		{Name: "issuers", SingularName: "issuer", Namespaced: true, Kind: "Issuer", Categories: []string{"cert-manager"}},
	}},
	{GroupVersion: "example.com/v1", APIResources: []metav1.APIResource{
		{Name: "bananas", SingularName: "banana", Namespaced: true, Kind: "Cherry"},
	}},
}

// discoveryServer serves resources as a cluster's discovery does, without aggregation: groups in order, each
// preferring its first version. Every other GET gets an empty list, e.g. the builder's for each type it resolves.
func discoveryServer(t *testing.T, resources []metav1.APIResourceList) *httptest.Server {
	t.Helper()
	paths := map[string]any{"/api": metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions"}, Versions: []string{"v1"}}}
	groups := metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}}
	for _, list := range resources {
		list.TypeMeta = metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		require.NoError(t, err)
		if gv.Group == "" {
			paths["/api/"+gv.Version] = list
			continue
		}
		paths["/apis/"+list.GroupVersion] = list
		version := metav1.GroupVersionForDiscovery{GroupVersion: list.GroupVersion, Version: gv.Version}
		if i := slices.IndexFunc(groups.Groups, func(g metav1.APIGroup) bool { return g.Name == gv.Group }); i >= 0 {
			groups.Groups[i].Versions = append(groups.Groups[i].Versions, version)
		} else {
			groups.Groups = append(groups.Groups, metav1.APIGroup{Name: gv.Group, Versions: []metav1.GroupVersionForDiscovery{version}, PreferredVersion: version})
		}
	}
	paths["/apis"] = groups

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := paths[r.URL.Path]
		if !ok {
			body = map[string]any{"apiVersion": "v1", "kind": "List", "metadata": map[string]any{}, "items": []any{}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// resolved is what a type argument resolved to, e.g. "v1/pods, apps/v1/deployments", or the error it failed with.
func resolved(mappings []*meta.RESTMapping, err error) string {
	if err != nil {
		return err.Error()
	}
	return strings.Join(lo.Map(mappings, func(m *meta.RESTMapping, _ int) string {
		return m.Resource.GroupVersion().String() + "/" + m.Resource.Resource
	}), ", ")
}
