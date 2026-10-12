package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/samber/lo"
	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/disk"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

const (
	// chunkSize is how many rows each LIST page holds, as in kubectl.
	chunkSize = 500
	// discoveryTTL is how long kubectl's discovery cache stays fresh.
	discoveryTTL = 6 * time.Hour
	// tableAccept asks for a server-side Table, falling back to plain JSON for APIs that don't serve Tables.
	//
	//   - application/json: encoded as JSON
	//   - as=Table: converted to a Table, rather than the objects themselves
	//   - g=meta.k8s.io, v=v1: the Table kind's group and version, i.e. meta.k8s.io/v1
	//   - ,application/json: the second choice, the plain objects, for servers that can't convert
	tableAccept = "application/json;as=Table;v=v1;g=meta.k8s.io,application/json"
)

// Cluster is a read-only client for one context, connecting on its first API call.
type Cluster struct {
	// Name is the context's name.
	Name string

	kubeconfig *Kubeconfig

	// Set by connect, on first use.
	once   sync.Once
	err    error
	client *rest.RESTClient
	disc   discovery.CachedDiscoveryInterfaceWithContext
	mapper meta.RESTMapperWithContext
}

// Namespace is the context's default namespace, or "default" if it names none.
func (c *Cluster) Namespace() string {
	return c.kubeconfig.Namespace(c.Name)
}

// ListOptions narrow a LIST server-side.
type ListOptions struct {
	LabelSelector string
	FieldSelector string
}

// List reads a type's Table rows in a namespace, or across all of them for "", in 500-row pages.
func (c *Cluster) List(ctx context.Context, m *meta.RESTMapping, namespace string, opts ListOptions) (*Table, error) {
	if err := c.connect(); err != nil {
		return nil, err
	}
	out := &Table{}
	cont := ""
	for {
		req := c.request(m, namespace).
			SetHeader("Accept", tableAccept).
			Param("includeObject", string(metav1.IncludeMetadata)).
			VersionedParams(&metav1.ListOptions{
				LabelSelector: opts.LabelSelector,
				FieldSelector: opts.FieldSelector,
				Limit:         chunkSize,
				Continue:      cont,
			}, metav1.ParameterCodec) // empty fields stay out of the query, e.g. no labelSelector without -l
		raw, err := do(ctx, req)
		if err != nil {
			return nil, err
		}
		page, next, err := decodeTable(raw, listShape)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", m.Resource.Resource, err)
		}
		if out.Columns == nil {
			out.Columns = page.Columns
		}
		out.Rows = append(out.Rows, page.Rows...)
		if next == "" {
			return out, nil
		}
		cont = next
	}
}

// Get reads one object's Table row, or returns the server's NotFound error.
func (c *Cluster) Get(ctx context.Context, m *meta.RESTMapping, namespace, name string) (*Table, error) {
	if err := c.connect(); err != nil {
		return nil, err
	}
	raw, err := do(ctx, c.request(m, namespace).Name(name).
		SetHeader("Accept", tableAccept).
		Param("includeObject", string(metav1.IncludeMetadata)))
	if err != nil {
		return nil, err
	}
	t, _, err := decodeTable(raw, objectShape)
	if err != nil {
		return nil, fmt.Errorf("get %s %s: %w", m.Resource.Resource, name, err)
	}
	return t, nil
}

// GetOrList GETs name, else LISTs the namespace, sending both at once so a partial name costs no extra round trip.
//
// A GET hit returns its row and cancels the LIST. A miss, e.g. NotFound, returns the LIST's rows for matching.
func (c *Cluster) GetOrList(ctx context.Context, m *meta.RESTMapping, namespace, name string) (*Table, error) {
	if err := c.connect(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		table *Table
		err   error
	}
	lists := make(chan result, 1) // buffered, so a canceled LIST's goroutine still exits
	go func() {
		table, err := c.List(ctx, m, namespace, ListOptions{})
		lists <- result{table: table, err: err}
	}()
	if table, err := c.Get(ctx, m, namespace, name); err == nil && len(table.Rows) == 1 {
		return table, nil
	}
	list := <-lists
	return list.table, list.err
}

// Object reads one full object, as the server sends it, managedFields included.
func (c *Cluster) Object(ctx context.Context, m *meta.RESTMapping, namespace, name string) (map[string]any, error) {
	if err := c.connect(); err != nil {
		return nil, err
	}
	raw, err := do(ctx, c.request(m, namespace).Name(name).SetHeader("Accept", "application/json"))
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("get %s %s: %w", m.Resource.Resource, name, err)
	}
	return obj, nil
}

// LeaseHolder is a lease's namespace and holder identity.
type LeaseHolder struct {
	Namespace string
	Holder    string
}

// LeaseHolders are the holders of a cluster's leases (see Cluster.LeaseHolders).
type LeaseHolders []LeaseHolder

// Hold reports whether an object likely holds a lease: whether its name starts a holder's identity in its namespace.
//
// A guess, since leader election names holders NAME_ID, e.g. apple-db-leader_1a2b. So it also counts objects whose
// names start a holder's, e.g. deploy apple-db for holder apple-db-5d8f-x2x9z_1, and apple-1 for apple-10_1.
func (h LeaseHolders) Hold(namespace, name string) bool {
	return slices.ContainsFunc(h, func(l LeaseHolder) bool {
		return l.Namespace == namespace && strings.HasPrefix(l.Holder, name)
	})
}

// LeaseHolders lists the holders of the leases in a namespace, or across all of them for "", skipping leases with
// no holder.
func (c *Cluster) LeaseHolders(ctx context.Context, namespace string) (LeaseHolders, error) {
	if err := c.connect(); err != nil {
		return nil, err
	}
	var holders LeaseHolders
	cont := ""
	for {
		raw, err := do(ctx, c.client.Get().
			AbsPath("/apis", coordinationv1.SchemeGroupVersion.String()).
			Namespace(namespace).
			Resource("leases").
			VersionedParams(&metav1.ListOptions{Limit: chunkSize, Continue: cont}, metav1.ParameterCodec))
		if err != nil {
			return nil, err
		}
		var list coordinationv1.LeaseList
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("list leases: %w", err)
		}
		for _, lease := range list.Items {
			if holder := lo.FromPtr(lease.Spec.HolderIdentity); holder != "" {
				holders = append(holders, LeaseHolder{Namespace: lease.Namespace, Holder: holder})
			}
		}
		if list.Continue == "" {
			return holders, nil
		}
		cont = list.Continue
	}
}

// Mappings resolves a type argument to each type's mapping, i.e. its resource, kind, and scope, as kubectl does.
//
// Examples:
//   - `pod`, `po`, `Pod`, `deployments.apps`, or a CRD's short name: one type, by any of its names (see mappingFor)
//   - `pod,deploy`: several types, in turn
//   - `all`: a category, i.e. every type discovery lists in it, e.g. pods, services, deployments.apps, ...
//   - `pod,all`: each type once, where it first appears
//   - `pod,`: an error, as the empty part names no type
//
// REF: https://github.com/kubernetes/cli-runtime/blob/v0.37.1/pkg/resource/builder.go#L646-L672
// REF: https://github.com/kubernetes/cli-runtime/blob/v0.37.1/pkg/resource/builder.go#L827-L847
func (c *Cluster) Mappings(ctx context.Context, arg string) ([]*meta.RESTMapping, error) {
	if err := c.connect(); err != nil {
		return nil, err
	}
	expander := restmapper.NewDiscoveryCategoryExpanderWithContext(c.disc)
	var out []*meta.RESTMapping
	for _, part := range strings.Split(arg, ",") {
		names := []string{part}
		// A category expands to its types, e.g. all -> {pods, services, deployments.apps, ...}
		if groupResources, ok := expander.ExpandWithContext(ctx, part); ok {
			names = lo.Map(groupResources, func(gr schema.GroupResource, _ int) string { return gr.String() })
		}
		for _, name := range names {
			m, err := c.mappingFor(ctx, name) // ex: po -> v1 pods; deployments.apps -> apps/v1 deployments
			if err != nil {
				return nil, err
			}
			out = append(out, m)
		}
	}
	// Each type once, where it first appears, since types can repeat:
	//   - Aliases, e.g. pod,po
	//   - A category and a type in it, e.g. all,pod
	//   - A category alone, listing a type once per version served, e.g. horizontalpodautoscalers in autoscaling/v1 and v2
	return lo.UniqBy(out, func(m *meta.RESTMapping) schema.GroupVersionResource { return m.Resource }), nil
}

// mappingFor resolves one type name to its mapping, trying it as a resource, then as a kind, as kubectl does.
//
// A mapping pairs the resource kuba requests, e.g. deployments in /apis/apps/v1/namespaces/kiwi/deployments, with the
// kind that comes back, e.g. Deployment, and whether it's namespaced. The mapper, built from discovery, links the two:
// KindFor takes a resource to its kind, then RESTMapping takes the kind to its mapping. So a resource name takes both
// steps, and a kind name just the second.
//
// Nearly every name is a resource, in any form: plural, singular, short name, any case. The kind fallback only rescues
// a kind whose lowercase isn't its resource's singular name, and only with its group, as a kind alone matches only
// core kinds. Dots add a group and version, but groups have dots of their own, so a name with two or more is read as
// name.version.group, then again as name.group.
//
// Examples:
//
//	po                            -> v1 pods                          short name
//	Pod                           -> v1 pods                          singular, any case
//	deployments.apps              -> apps/v1 deployments              resource.group
//	deployments.v1.apps           -> apps/v1 deployments              resource.version.group
//	certificates.cert-manager.io  -> cert-manager.io/v1 certificates  resource.group, on the second read
//	Cherry.example.com            -> example.com/v1 bananas           kind.group, of a CRD whose singular isn't cherry
//	Cherry                        -> the server doesn't have a resource type "Cherry"
//	deployments.nope              -> the server doesn't have a resource type "deployments" in group "nope"
//
// REF: https://github.com/kubernetes/cli-runtime/blob/v0.37.1/pkg/resource/builder.go#L772-L825
func (c *Cluster) mappingFor(ctx context.Context, arg string) (*meta.RESTMapping, error) {
	// First as a resource, which covers nearly every input
	parsedGVR, gr := schema.ParseResourceArg(arg)
	gvk := schema.GroupVersionKind{}
	if parsedGVR != nil { // two or more dots, read as resource.version.group, e.g. deployments.v1.apps
		gvk, _ = c.mapper.KindForWithContext(ctx, *parsedGVR)
	}
	if gvk.Empty() { // else resource[.group], e.g. po, deployments.apps, or certificates.cert-manager.io, misread above
		gvk, _ = c.mapper.KindForWithContext(ctx, gr.WithVersion(""))
	}
	if !gvk.Empty() {
		return c.mapper.RESTMappingWithContext(ctx, gvk.GroupKind(), gvk.Version)
	}

	// Then as a kind, which only matters for one whose lowercase isn't its resource's singular name
	parsedGVK, gk := schema.ParseKindArg(arg) // same forms, e.g. Cherry.v1.example.com, Cherry.example.com, or Cherry
	if parsedGVK == nil {
		parsedGVK = new(gk.WithVersion(""))
	}
	if !parsedGVK.Empty() {
		if m, err := c.mapper.RESTMappingWithContext(ctx, parsedGVK.GroupKind(), parsedGVK.Version); err == nil {
			return m, nil
		}
	}
	m, err := c.mapper.RESTMappingWithContext(ctx, gk, gvk.Version) // again as kind[.group], now keeping the error
	if meta.IsNoMatchError(err) {
		if gr.Group != "" {
			return nil, fmt.Errorf("the server doesn't have a resource type %q in group %q", gr.Resource, gr.Group)
		}
		return nil, fmt.Errorf("the server doesn't have a resource type %q", gr.Resource)
	}
	return m, err
}

// request starts a GET for a type, in a namespace if it's namespaced and one is given.
func (c *Cluster) request(m *meta.RESTMapping, namespace string) *rest.Request {
	gv := m.Resource.GroupVersion()
	return c.client.Get().
		AbsPath(lo.Ternary(gv.Group == "", "/api", "/apis"), gv.String()). // e.g. /api/v1, or /apis/apps/v1
		NamespaceIfScoped(namespace, m.Scope.Name() == meta.RESTScopeNameNamespace).
		Resource(m.Resource.Resource)
}

// do sends a request, returning the body, or the server's own error from its Status body, e.g. who was forbidden what.
func do(ctx context.Context, req *rest.Request) ([]byte, error) {
	res := req.Do(ctx)
	raw, err := res.Raw()
	if err != nil {
		return nil, res.Error()
	}
	return raw, nil
}

// connect builds the context's clients on first use: credentials, the read-only guard, and kubectl's discovery cache.
func (c *Cluster) connect() error {
	c.once.Do(func() { c.err = c.build() })
	return c.err
}

func (c *Cluster) build() error {
	kc := c.kubeconfig
	cfg, err := kc.restConfig(c.Name)
	if err != nil {
		return fmt.Errorf("context %s: %w", c.Name, err)
	}

	disc, err := disk.NewCachedDiscoveryClientForConfig(
		rest.CopyConfig(cfg),
		discoveryCacheDir(filepath.Join(kc.cacheDir, "discovery"), cfg.Host),
		filepath.Join(kc.cacheDir, "http"),
		discoveryTTL,
	)
	if err != nil {
		return fmt.Errorf("context %s: %w", c.Name, err)
	}

	rc := rest.CopyConfig(cfg)
	rc.NegotiatedSerializer = codecs.WithoutConversion()
	client, err := rest.UnversionedRESTClientFor(rc)
	if err != nil {
		return fmt.Errorf("context %s: %w", c.Name, err)
	}

	c.client = client
	c.disc = disc
	c.mapper = restmapper.NewShortcutExpanderWithContext(
		restmapper.NewDeferredDiscoveryRESTMapperWithContext(disc),
		disc,
		func(warning string) { _, _ = fmt.Fprintf(kc.warnings, "Warning: %s\n", warning) },
	)

	return nil
}

// codecs decode the server's Status errors, keeping its message, e.g. who was forbidden what.
var codecs = func() serializer.CodecFactory {
	scheme := runtime.NewScheme()
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Version: "v1"})
	return serializer.NewCodecFactory(scheme)
}()

// unsafeHostChars matches the characters kubectl swaps for _ in a server's discovery cache directory name.
//
// That's anything but a letter, digit, _, ., /, or parenthesis. The ( ) are literal inside [...], likely a kubectl slip,
// but copied as-is, since any difference names another directory, and splits the cache kuba shares with kubectl.
//
// Ex:
//
//	127.0.0.1:6443                -> 127.0.0.1_6443
//	example.com/k8s/clusters/c-1  -> example.com/k8s/clusters/c_1  (/ stays, so a server path nests directories)
//	example.com/k8s/(c-1)         -> example.com/k8s/(c_1)         (parentheses stay, from the slip)
//
// REF: https://github.com/kubernetes/cli-runtime/blob/v0.37.1/pkg/genericclioptions/config_flags.go#L513-L514
var unsafeHostChars = regexp.MustCompile(`[^(\w/.)]`)

// discoveryCacheDir is the server's discovery cache directory, shared with kubectl.
//
// REF: https://github.com/kubernetes/cli-runtime/blob/v0.37.1/pkg/genericclioptions/config_flags.go#L517-L523
func discoveryCacheDir(parent, host string) string {
	host = strings.Replace(host, "https://", "", 1)
	host = strings.Replace(host, "http://", "", 1)
	host = unsafeHostChars.ReplaceAllString(host, "_")
	return filepath.Join(parent, host)
}
