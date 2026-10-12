package kube

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/hcgatewood/kuba/internal/flagspec"
)

func TestLoadKubeconfigErrors(t *testing.T) {
	apple := `{"apiVersion": "v1", "kind": "Config",
		"clusters": [{"name": "apple", "cluster": {"server": "https://apple.example.com"}}],
		"contexts": [{"name": "apple", "context": {"cluster": "apple", "user": "apple"}}],
		"users": [{"name": "apple", "user": {}}]}`
	tests := []struct {
		name       string
		kubeconfig string
		context    string
		wantErr    string
	}{
		{name: "no contexts", kubeconfig: `{"apiVersion": "v1", "kind": "Config"}`, wantErr: "no kubeconfig contexts found"},
		{name: "no current context", kubeconfig: apple, wantErr: "no current context: pass --context"},
		{name: "unknown context", kubeconfig: apple, context: "banana", wantErr: `context "banana" not found in kubeconfig`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			require.NoError(t, os.WriteFile(path, []byte(tt.kubeconfig), 0o600))
			flags := []flagspec.FlagValue{{Flag: flagspec.Flag{Name: "kubeconfig"}, Value: path}}
			_, err := LoadKubeconfig(flags, tt.context, RateLimit{}, slog.New(slog.DiscardHandler), io.Discard)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestKubeconfigCluster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte(`{"apiVersion": "v1", "kind": "Config", "current-context": "apple",
		"clusters": [{"name": "apple", "cluster": {"server": "https://apple.example.com"}}],
		"contexts": [{"name": "apple", "context": {"cluster": "apple", "user": "apple"}}],
		"users": [{"name": "apple", "user": {}}]}`), 0o600))
	flags := []flagspec.FlagValue{{Flag: flagspec.Flag{Name: "kubeconfig"}, Value: path}}
	k, err := LoadKubeconfig(flags, "", RateLimit{}, slog.New(slog.DiscardHandler), io.Discard)
	require.NoError(t, err)

	first, err := k.Cluster("apple")
	require.NoError(t, err)
	again, err := k.Cluster("apple")
	require.NoError(t, err)
	assert.Same(t, first, again)
	_, err = k.Cluster("banana")
	assert.ErrorContains(t, err, `context "banana" not found in kubeconfig`)
}

// TestKubeconfigContext checks that --context picks the starting cluster but leaves every other context reaching its
// own server, which as a clientcmd override it wouldn't: a fan-out would search one cluster again and again.
func TestKubeconfigContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte(`{"apiVersion": "v1", "kind": "Config", "current-context": "apple",
		"clusters": [{"name": "apple", "cluster": {"server": "https://apple.example.com"}},
			{"name": "banana", "cluster": {"server": "https://banana.example.com"}}],
		"contexts": [{"name": "apple", "context": {"cluster": "apple", "user": "apple"}},
			{"name": "banana", "context": {"cluster": "banana", "user": "apple"}}],
		"users": [{"name": "apple", "user": {}}]}`), 0o600))
	flags := []flagspec.FlagValue{{Flag: flagspec.Flag{Name: "kubeconfig"}, Value: path}}

	tests := []struct {
		name        string
		context     string
		wantCurrent string
	}{
		{name: "current context", wantCurrent: "apple"},
		{name: "--context", context: "banana", wantCurrent: "banana"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := LoadKubeconfig(flags, tt.context, RateLimit{}, slog.New(slog.DiscardHandler), io.Discard)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCurrent, k.Current())
			for _, c := range k.Contexts() {
				cfg, err := k.restConfig(c)
				require.NoError(t, err)
				assert.Equal(t, "https://"+c+".example.com", cfg.Host, c)
			}
		})
	}
}

func TestAuthProviderRefresh(t *testing.T) {
	jwt := func(exp time.Time) string {
		enc := base64.RawURLEncoding
		return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(fmt.Appendf(nil, `{"exp":%d}`, exp.Unix())) + ".sig"
	}
	stale, fresh := jwt(time.Now().Add(-time.Hour)), jwt(time.Now().Add(time.Hour))

	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_, _ = fmt.Fprintf(w, `{"token_endpoint": %q}`, issuer.URL+"/token")
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"access_token": "x", "token_type": "Bearer", "id_token": %q, "refresh_token": "r2"}`, fresh)
		}
	}))
	defer issuer.Close()
	var gotAuth string
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer api.Close()

	// apple.yaml has the context, and banana.yaml its user, so the new token must find its way to banana.yaml
	dir := t.TempDir()
	apple, banana := filepath.Join(dir, "apple.yaml"), filepath.Join(dir, "banana.yaml")
	require.NoError(t, os.WriteFile(apple, fmt.Appendf(nil, `{"apiVersion": "v1", "kind": "Config", "current-context": "apple",
		"clusters": [{"name": "apple", "cluster": {"server": %q, "insecure-skip-tls-verify": true}}],
		"contexts": [{"name": "apple", "context": {"cluster": "apple", "user": "banana"}}]}`, api.URL), 0o600))
	require.NoError(t, os.WriteFile(banana, fmt.Appendf(nil, `{"apiVersion": "v1", "kind": "Config",
		"users": [{"name": "banana", "user": {"auth-provider": {"name": "oidc", "config": {
			"idp-issuer-url": %q, "client-id": "cherry", "id-token": %q, "refresh-token": "r1"}}}}]}`, issuer.URL, stale), 0o600))
	t.Setenv("KUBECONFIG", apple+string(os.PathListSeparator)+banana)

	k, err := LoadKubeconfig([]flagspec.FlagValue{{Flag: flagspec.Flag{Name: "cache-dir"}, Value: t.TempDir()}}, "",
		RateLimit{}, slog.New(slog.DiscardHandler), io.Discard)
	require.NoError(t, err)
	c, err := k.Cluster("apple")
	require.NoError(t, err)
	require.NoError(t, c.connect())
	_, err = do(context.Background(), c.client.Get().AbsPath("/version"))
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+fresh, gotAuth)

	saved, err := clientcmd.LoadFromFile(banana)
	require.NoError(t, err)
	assert.Equal(t, fresh, saved.AuthInfos["banana"].AuthProvider.Config["id-token"])
	assert.Equal(t, "r2", saved.AuthInfos["banana"].AuthProvider.Config["refresh-token"])
	untouched, err := clientcmd.LoadFromFile(apple)
	require.NoError(t, err)
	assert.Empty(t, untouched.AuthInfos)
}

func TestRateLimitApply(t *testing.T) {
	tests := []struct {
		name      string
		limit     RateLimit
		wantQPS   float32
		wantBurst int
	}{
		{name: "off", limit: RateLimit{}, wantQPS: -1},
		{name: "burst alone", limit: RateLimit{Burst: 20}, wantQPS: -1},
		{name: "qps", limit: RateLimit{QPS: 5}, wantQPS: 5, wantBurst: 5},
		{name: "qps and burst", limit: RateLimit{QPS: 5, Burst: 20}, wantQPS: 5, wantBurst: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &rest.Config{}
			tt.limit.apply(cfg)
			assert.Equal(t, tt.wantQPS, cfg.QPS)
			assert.Equal(t, tt.wantBurst, cfg.Burst)
		})
	}
}

// TestRESTConfigMatchesKubectl checks that kuba reaches a context as kubectl would, given the same connection flags.
//
// kuba binds clientcmd's override flags, and kubectl its own, so a flag one side binds and the other doesn't would
// silently send kuba's lookups somewhere, or as someone, kubectl's command doesn't. kuba's own additions, i.e. its
// throttling, warning printer, and read-only guard, are cleared before comparing.
func TestRESTConfigMatchesKubectl(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ca.crt", "client.crt", "client.key"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600))
	}
	path := filepath.Join(dir, "config")
	require.NoError(t, os.WriteFile(path, []byte(`{"apiVersion": "v1", "kind": "Config", "current-context": "apple",
		"clusters": [
			{"name": "apple", "cluster": {"server": "https://apple.example.com"}},
			{"name": "banana", "cluster": {"server": "https://banana.example.com", "insecure-skip-tls-verify": true}}],
		"contexts": [{"name": "apple", "context": {"cluster": "apple", "user": "apple", "namespace": "kiwi"}}],
		"users": [
			{"name": "apple", "user": {"token": "t0"}},
			{"name": "banana", "user": {"username": "u0", "password": "p0"}}]}`), 0o600))

	tests := []struct {
		name  string
		flags []string
	}{
		{name: "none"},
		{name: "impersonation", flags: []string{"as=apple", "as-uid=1", "as-group=banana", "as-group=cherry"}},
		{name: "impersonation extras", flags: []string{"as=apple", "as-user-extra=reason=debug", "as-user-extra=reason=oncall", "as-user-extra=scope=view", "as-user-extra=malformed"}},
		{name: "token", flags: []string{"token=t1"}},
		{name: "basic auth", flags: []string{"user=banana", "username=u1", "password=p1"}},
		{name: "client certificate", flags: []string{"client-certificate=" + filepath.Join(dir, "client.crt"), "client-key=" + filepath.Join(dir, "client.key")}},
		{name: "server", flags: []string{"server=https://cherry.example.com", "tls-server-name=cherry", "certificate-authority=" + filepath.Join(dir, "ca.crt")}},
		{name: "cluster", flags: []string{"cluster=banana"}},
		{name: "insecure", flags: []string{"insecure-skip-tls-verify=true"}},
		{name: "transport", flags: []string{"request-timeout=5s", "disable-compression=true", "proxy-url=http://proxy.example.com:3128"}},
		{name: "unknown user", flags: []string{"user=donut"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubaFlags := []flagspec.FlagValue{{Flag: flagspec.Flag{Name: "kubeconfig"}, Value: path}}
			kubectlArgs := []string{"--kubeconfig=" + path}
			for _, f := range tt.flags {
				name, value, _ := strings.Cut(f, "=")
				kubaFlags = append(kubaFlags, flagspec.FlagValue{Flag: flagspec.Flag{Name: name}, Value: value})
				kubectlArgs = append(kubectlArgs, "--"+f)
			}

			configFlags := kubectlConfigFlags()
			fs := pflag.NewFlagSet("kubectl", pflag.ContinueOnError)
			configFlags.AddFlags(fs)
			require.NoError(t, fs.Parse(kubectlArgs))
			want, wantErr := configFlags.ToRESTConfig()

			k, err := LoadKubeconfig(kubaFlags, "", RateLimit{}, slog.New(slog.DiscardHandler), io.Discard)
			require.NoError(t, err)
			got, err := k.restConfig("apple")
			if got != nil {
				got.QPS, got.WarningHandler, got.WrapTransport = 0, nil, nil
			}

			assert.Equal(t, comparableConfig(t, want, wantErr), comparableConfig(t, got, err))
		})
	}
}

// TestDefaultCacheDirMatchesKubectl checks that kuba finds kubectl's cache directory, to share its discovery cache.
func TestDefaultCacheDirMatchesKubectl(t *testing.T) {
	tests := []struct {
		name         string
		kubecachedir string
	}{
		{name: "under home"},
		{name: "KUBECACHEDIR", kubecachedir: "/apple/cache"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("KUBECACHEDIR", tt.kubecachedir)
			assert.Equal(t, *kubectlConfigFlags().CacheDir, defaultCacheDir())
		})
	}
}

// writeKubeconfig writes a kubeconfig whose one context, apple, reaches server, returning its path.
func writeKubeconfig(t *testing.T, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	kubeconfig := fmt.Sprintf(`{"apiVersion": "v1", "kind": "Config", "current-context": "apple",
		"clusters": [{"name": "apple", "cluster": {"server": %q}}],
		"contexts": [{"name": "apple", "context": {"cluster": "apple", "user": "apple"}}],
		"users": [{"name": "apple", "user": {}}]}`, server)
	require.NoError(t, os.WriteFile(path, []byte(kubeconfig), 0o600))
	return path
}

// kubectlConfigFlags are the connection flags as kubectl's root command builds them, e.g. with --username and
// --password, which ConfigFlags leaves out by default.
//
// REF: https://github.com/kubernetes/kubectl/blob/v0.37.1/pkg/cmd/cmd.go#L91-L93
func kubectlConfigFlags() *genericclioptions.ConfigFlags {
	return genericclioptions.NewConfigFlags(true).WithDeprecatedPasswordFlag().WithDiscoveryBurst(300).WithDiscoveryQPS(50.0)
}

// comparableConfig is a REST config as assert.Equal can compare it, its proxy func swapped for the proxy it picks, or
// the error building the config failed with.
func comparableConfig(t *testing.T, cfg *rest.Config, err error) any {
	t.Helper()
	if err != nil {
		return err.Error()
	}
	cfg = rest.CopyConfig(cfg)
	var proxy string
	if cfg.Proxy != nil {
		u, err := cfg.Proxy(httptest.NewRequest(http.MethodGet, cfg.Host, nil))
		require.NoError(t, err)
		proxy, cfg.Proxy = u.String(), nil
	}
	return struct {
		Config   *rest.Config
		ProxyURL string
	}{cfg, proxy}
}
