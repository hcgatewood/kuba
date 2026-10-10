package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// server is one envtest API server (kube-apiserver + etcd) plus an admin client for seeding it.
//
// envtest has no controllers, scheduler, or kubelet: pods stay as written, namespaces never finish deleting, and
// ServiceAccount admission is disabled.
type server struct {
	env    *envtest.Environment
	cfg    *rest.Config
	client client.Client

	stopOnce sync.Once
}

// startServer starts an API server, tracked in running so stopOnInterrupt can stop it.
func startServer() (*server, error) {
	handleInterrupts.Do(stopOnInterrupt)
	done, err := running.beginStart()
	if err != nil {
		return nil, err
	}
	defer done()

	s, err := newServer()
	if err != nil {
		return nil, err
	}
	running.add(s)
	return s, nil
}

// newServer starts envtest and connects an admin client to it.
func newServer() (*server, error) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		return nil, fmt.Errorf("start envtest: %w", err)
	}
	clientCfg := rest.CopyConfig(cfg)
	clientCfg.QPS = -1 // No client-side throttling: envtest serves only this test
	c, err := client.New(clientCfg, client.Options{})
	if err != nil {
		return nil, errors.Join(err, env.Stop())
	}
	return &server{env: env, cfg: cfg, client: c}, nil
}

// stop shuts s down.
func (s *server) stop() {
	if s == nil {
		return
	}
	// Concurrent calls (a script's cleanup racing stopOnInterrupt) all wait for the one stop to finish, since the
	// process exiting mid-stop would orphan whatever wasn't stopped yet.
	s.stopOnce.Do(func() {
		if err := s.env.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: stopping API server %s: %v\n", s.cfg.Host, err)
		}
		running.remove(s)
	})
}

// hostPort is the server's address as it shows up in output, e.g. 127.0.0.1:59395.
func (s *server) hostPort() string {
	return s.env.ControlPlane.APIServer.HostPort()
}

func (s *server) createNamespace(name string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	return s.client.Create(context.Background(), ns)
}

// sharedServer is the API server every script's apple context starts on, booting in the background from the start of
// the run.
type sharedServer struct {
	// boot runs startServer, which get and stop wait for.
	boot errgroup.Group
	srv  *server
}

// startSharedServer starts the shared server booting, and stops it when t ends.
func startSharedServer(t *testing.T) *sharedServer {
	s := &sharedServer{}
	s.boot.Go(func() (err error) {
		s.srv, err = startServer()
		return err
	})
	t.Cleanup(s.stop)
	return s
}

// get waits for the server to boot and returns it.
func (s *sharedServer) get() (*server, error) {
	err := s.boot.Wait()
	return s.srv, err
}

// stop waits for the server to boot, then stops it.
func (s *sharedServer) stop() {
	_ = s.boot.Wait()
	s.srv.stop()
}

// handleInterrupts installs stopOnInterrupt as the first server starts.
var handleInterrupts sync.Once

// stopOnInterrupt stops every server on SIGINT or SIGTERM, then exits; a second signal exits right away.
//
// envtest starts each server in its own process group, which Ctrl-C doesn't reach, so an interrupted run would
// otherwise leave them running, orphaned.
func stopOnInterrupt() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		signal.Stop(sigs)
		running.stopAll()
		os.Exit(1)
	}()
}

// running tracks every server managed by startServer that hasn't been (fully) stopped yet.
var running = &registry{servers: sets.New[*server]()}

// registry tracks servers that are up and not yet stopped, plus in-flight starts.
//
// Tracking this way ensures interrupts gracefully stop every server.
type registry struct {
	mu      sync.Mutex
	servers sets.Set[*server]
	// starting counts in-flight starts, which stopAll waits out, since etcd may already be up.
	starting sync.WaitGroup
	// interrupted refuses new starts once stopAll begins, so none slip past its wait.
	interrupted bool
}

// beginStart counts an in-flight start, or fails if interrupted. Call done after adding the started server.
func (r *registry) beginStart() (done func(), err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.interrupted {
		return nil, errors.New("interrupted")
	}
	r.starting.Add(1)
	return r.starting.Done, nil
}

func (r *registry) add(s *server) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.servers.Insert(s)
}

func (r *registry) remove(s *server) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.servers.Delete(s)
}

// stopAll refuses new starts, waits out those in flight, then stops every server.
func (r *registry) stopAll() {
	r.mu.Lock()
	r.interrupted = true
	r.mu.Unlock()

	r.starting.Wait() // drain in-flight starts

	r.mu.Lock()
	servers := r.servers.UnsortedList()
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Go(s.stop)
	}
	wg.Wait()
}

// apply creates obj, waits for a CRD to be established, then writes any status through the status subresource.
func (k *kubeContext) apply(obj *unstructured.Unstructured) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, hasStatus := obj.Object["status"]

	// A custom resource's kind can be unknown for a moment after its CRD is applied, as the client discovers it async
	noMatchBackoff := wait.Backoff{Duration: 100 * time.Millisecond, Steps: 300}
	err := retry.OnError(noMatchBackoff, meta.IsNoMatchError, func() error {
		if err := k.defaultNamespaces(obj); err != nil {
			return err
		}
		return k.srv.client.Create(ctx, obj)
	})
	if err != nil {
		return fmt.Errorf("create %s %s: %w", obj.GetKind(), obj.GetName(), err)
	}

	if obj.GetKind() == "CustomResourceDefinition" {
		if err := waitEstablished(ctx, k.srv.client, obj.GetName()); err != nil {
			return err
		}
	}

	if hasStatus {
		obj.Object["status"] = status
		if err := k.srv.client.Status().Update(ctx, obj); err != nil {
			return fmt.Errorf("set status of %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}

	return nil
}

// defaultNamespaces fills in the namespaces a fixture leaves out.
//
// Defaults:
//   - a namespaced object: the context's namespace
//   - an Event's involvedObject: the Event's namespace
func (k *kubeContext) defaultNamespaces(obj *unstructured.Unstructured) error {
	namespaced, err := k.srv.client.IsObjectNamespaced(obj)
	if err != nil {
		return err
	}
	if namespaced && obj.GetNamespace() == "" {
		obj.SetNamespace(k.namespace)
	}

	if obj.GetKind() != "Event" {
		return nil
	}
	if ns, _, _ := unstructured.NestedString(obj.Object, "involvedObject", "namespace"); ns != "" {
		return nil
	}
	return unstructured.SetNestedField(obj.Object, obj.GetNamespace(), "involvedObject", "namespace")
}

// waitEstablished waits until the server accepts objects of the CRD's kind (its Established condition).
func waitEstablished(ctx context.Context, c client.Client, name string) error {
	crd := &unstructured.Unstructured{}
	crd.SetAPIVersion("apiextensions.k8s.io/v1")
	crd.SetKind("CustomResourceDefinition")
	err := wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, client.ObjectKey{Name: name}, crd); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		conds, _, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
		return slices.ContainsFunc(conds, func(cond any) bool {
			m, ok := cond.(map[string]any)
			return ok && m["type"] == "Established" && m["status"] == "True"
		}), nil
	})
	if err != nil {
		return fmt.Errorf("CRD %s not established: %w", name, err)
	}
	return nil
}

// decodeObjects splits multi-document YAML (or JSON) into objects, also expanding any List kinds into their items.
func decodeObjects(data string) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(data), 4096)
	var objs []*unstructured.Unstructured
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			return objs, nil
		}
		if err != nil {
			return nil, err
		}
		if len(m) == 0 {
			continue
		}

		obj := &unstructured.Unstructured{Object: m}
		if !obj.IsList() {
			objs = append(objs, obj)
			continue
		}
		err = obj.EachListItem(func(item runtime.Object) error {
			objs = append(objs, item.(*unstructured.Unstructured))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
}
