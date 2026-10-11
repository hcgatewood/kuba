package e2e

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// harness holds what every script shares: the implementation under test, PATH, and the shared API server.
type harness struct {
	// impl is the kuba implementation under test (python or go).
	//
	// Guards which [impl:...] conditions check against.
	impl string
	// implReady waits for the implementation under test to be in place, since the Go kuba builds in the background.
	implReady func() error
	// path is the hermetic PATH every script runs with.
	path string
	// coverDir is where a coverage build of the Go kuba writes its counters (GOCOVERDIR), or empty for a plain build.
	coverDir string

	// shared is the API server every script's apple context starts on, booting while the Go kuba builds.
	shared *sharedServer
}

// newHarness readies the implementation under test and the PATH every script sees.
//
// PATH is hermetic: the implementation, the fakes (testscript.Main prepends their directory to this process's PATH),
// envtest's kubectl, and the base system directories. Nothing from the developer's PATH (real fzf, kubecolor, kubectl
// plugins) leaks in.
func newHarness(t *testing.T, impl string) *harness {
	implDir := t.TempDir()
	var implReady func() error
	switch impl {
	case "python":
		linkPython(t, implDir)
		implReady = func() error { return nil }
	case "go":
		implReady = buildGo(implDir, os.Getenv(coverDirEnv) != "", os.Getenv(ldflagsEnv))
	default:
		t.Fatalf("KUBA_IMPL=%q; want python or go", impl)
	}

	fakesDir := filepath.SplitList(os.Getenv("PATH"))[0] // testscript.Main prepends fakes to PATH

	assets := os.Getenv("KUBEBUILDER_ASSETS") // envtest's kubectl, kube-apiserver, etcd, etc
	require.NotEmpty(t, assets, "KUBEBUILDER_ASSETS unset; run make e2e-python or make e2e-go")

	path := strings.Join([]string{implDir, fakesDir, assets, "/usr/bin", "/bin"}, string(filepath.ListSeparator))

	return &harness{
		impl:      impl,
		implReady: implReady,
		path:      path,
		coverDir:  os.Getenv(coverDirEnv),
		shared:    startSharedServer(t),
	}
}

func (h *harness) params(dir string) testscript.Params {
	return testscript.Params{
		Dir:                 dir,
		UpdateScripts:       *update,
		RequireUniqueNames:  true,
		RequireExplicitExec: true,
		Setup:               h.setup,
		Condition:           h.condition,
		Cmds:                scriptCommands(),
	}
}

// setup gives each script its own HOME, KUBECONFIG, and call log, plus a kubeconfig context named apple.
//
// apple points at the shared API server and defaults to a namespace just for this script (named after it, also in
// $NS), which keeps scripts' namespaced objects apart. Scripts that need a server to themselves, or more clusters, use
// `mkcluster`, whose contexts default to $NS too.
func (h *harness) setup(e *testscript.Env) error {
	if h.implReady() != nil {
		return errors.New("kuba isn't ready; see TestScript's failure")
	}

	home := filepath.Join(e.WorkDir, ".home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}

	st := &scriptState{work: e.WorkDir, kubeconfig: filepath.Join(home, ".kube", "config")}
	e.Values[stateKey{}] = st

	for k, v := range map[string]string{
		"HOME":       home,
		"KUBECONFIG": st.kubeconfig,
		"PATH":       h.path,
		callsEnv:     filepath.Join(e.WorkDir, ".calls"),
		"LANG":       "en_US.UTF-8",
		"TZ":         "UTC",
		// A fixed theme keeps kubecolor's printer from asking ptyexec's terminal for its background color, which
		// nothing answers, so each colored table would wait out the query's timeout.
		"KUBECOLOR_PRESET": "dark",
		// The fzf fake picks per FZF_PICK; scripts about the fzf built into the Go kuba unset this and drive it with tui.
		"KUBA_FZF": "fzf",
		// Handoffs run plain kubectl, as Python's do, so printed commands match; scripts about the Go kuba's default,
		// `kuba kubectl`, unset this.
		"KUBA_KUBECTL": "kubectl",
	} {
		e.Setenv(k, v)
	}

	if h.coverDir != "" {
		e.Setenv("GOCOVERDIR", h.coverDir)
	}

	srv, err := h.shared.get()
	if err != nil {
		return err
	}
	ns := namespaceFor(e)
	if err := srv.createNamespace(ns); err != nil {
		return err
	}
	e.Setenv("NS", ns)
	err = st.setContext("apple", srv, ns)
	if err != nil {
		return err
	}

	return nil
}

// condition answers whether a condition is true for this harness.
//
// Specifically, answers whether the implementation under test matches is Python [impl:python] or Go [impl:go].
func (h *harness) condition(cond string) (bool, error) {
	if impl, ok := strings.CutPrefix(cond, "impl:"); ok {
		return impl == h.impl, nil
	}
	return false, fmt.Errorf("unknown condition %q", cond)
}

// linkPython symlinks the Python kuba from `make venv` into dir.
func linkPython(t *testing.T, dir string) {
	venvKuba, err := filepath.Abs(filepath.Join("..", ".venv", "bin", "kuba"))
	require.NoError(t, err)
	require.FileExists(t, venvKuba, "python kuba missing (run make venv)")
	require.NoError(t, os.Symlink(venvKuba, filepath.Join(dir, "kuba")))
}

// coverDirEnv names the directory for Go e2e coverage (make cover). With it set, the harness builds the Go kuba with
// -cover, and every script's kuba writes its counters there.
const coverDirEnv = "E2E_GOCOVERDIR"

// ldflagsEnv names the -ldflags the harness builds the Go kuba with: make passes the ones `make build` uses.
const ldflagsEnv = "E2E_LDFLAGS"

// buildGo starts building the Go kuba from ./cmd/kuba into dir, instrumented for coverage if cover and linked with
// ldflags if set, and returns a func that waits for the build.
func buildGo(dir string, cover bool, ldflags string) func() error {
	args := []string{"build", "-o", filepath.Join(dir, "kuba")}
	if cover {
		args = append(args, "-cover")
	}
	if ldflags != "" {
		args = append(args, "-ldflags="+ldflags)
	}
	cmd := exec.Command("go", append(args, "./cmd/kuba")...)
	cmd.Dir = ".."
	var build errgroup.Group
	build.Go(func() error {
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go build ./cmd/kuba: %w\n%s", err, out)
		}
		return nil
	})
	return build.Wait
}

var illegalNameCharsRe = regexp.MustCompile(`[^a-z0-9]+`)

// namespaceFor names a script's namespace after its path.
//
// Ex: TestScript/get/fuzzy -> get-fuzzy, TestScript/commands/exec -> commands-exec.
func namespaceFor(e *testscript.Env) string {
	testName := strings.TrimPrefix(e.T().(interface{ Name() string }).Name(), "TestScript/")
	withLegalChars := illegalNameCharsRe.ReplaceAllString(strings.ToLower(testName), "-")
	withLegalLength := strings.Trim(withLegalChars[:min(len(withLegalChars), validation.DNS1123LabelMaxLength)], "-")
	return withLegalLength
}

func state(ts *testscript.TestScript) *scriptState {
	return ts.Value(stateKey{}).(*scriptState)
}

type stateKey struct{}

type scriptState struct {
	work       string
	kubeconfig string
	contexts   []*kubeContext
	lastExit   int
	lastStderr string
	// tui is the command `tui start` started, until `tui exit`.
	tui *tuiSession
}

// kubeContext is a context in the script's kubeconfig, plus the API server behind it.
//
// Its cluster and user entries share its name, so no two contexts share entries, though several can share a server
// (mkcontext).
type kubeContext struct {
	// name is the context's name in the kubeconfig, and how harness commands pick it (kapply -c banana).
	name string
	// namespace is the context's default namespace, where kapply puts objects that don't name one.
	namespace string
	// srv is the API server the context's cluster entry points at, and where kapply creates objects.
	srv *server
}

// setContext points context name at srv in the script's kubeconfig, adding it or replacing the one of that name; the
// first one added becomes current.
func (s *scriptState) setContext(name string, srv *server, namespace string) error {
	cfg, err := clientcmd.LoadFromFile(s.kubeconfig)
	if errors.Is(err, fs.ErrNotExist) {
		cfg, err = clientcmdapi.NewConfig(), nil
	}
	if err != nil {
		return err
	}
	cfg.Clusters[name] = &clientcmdapi.Cluster{Server: srv.cfg.Host, CertificateAuthorityData: srv.cfg.CAData}
	cfg.AuthInfos[name] = &clientcmdapi.AuthInfo{
		ClientCertificateData: srv.cfg.CertData,
		ClientKeyData:         srv.cfg.KeyData,
		Token:                 srv.cfg.BearerToken,
	}
	cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name, Namespace: namespace}
	if cfg.CurrentContext == "" {
		cfg.CurrentContext = name
	}
	if err := clientcmd.WriteToFile(*cfg, s.kubeconfig); err != nil {
		return err
	}

	kc := &kubeContext{name: name, namespace: namespace, srv: srv}
	// Replace in place, so a remade apple stays first
	if i := s.contextIndex(name); i >= 0 {
		s.contexts[i] = kc
	} else {
		s.contexts = append(s.contexts, kc)
	}

	return nil
}

// context finds a context by name.
//
// Empty name means the first context (apple).
func (s *scriptState) context(name string) (*kubeContext, error) {
	if name == "" {
		return s.contexts[0], nil
	}
	i := s.contextIndex(name)
	if i < 0 {
		return nil, fmt.Errorf("no context %q", name)
	}
	return s.contexts[i], nil
}

// contextIndex is the position of the context called name in s.contexts, or -1.
func (s *scriptState) contextIndex(name string) int {
	return slices.IndexFunc(s.contexts, func(kc *kubeContext) bool { return kc.name == name })
}

func (s *scriptState) normalizer(markColor bool) normalizer {
	servers := map[string]string{}
	for _, kc := range s.contexts {
		// Contexts can share a server (mkcontext), which keeps the name of the first one on it.
		if _, ok := servers[kc.srv.hostPort()]; !ok {
			servers[kc.srv.hostPort()] = kc.name
		}
	}
	return normalizer{work: s.work, servers: servers, markColor: markColor}
}
