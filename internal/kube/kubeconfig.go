package kube

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/samber/lo"
	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/homedir"

	"github.com/hcgatewood/kuba/internal/flagspec"
)

// Kubeconfig is kubeconfig as kubectl reads it: every file merged, plus flags like --as and --token on every context.
type Kubeconfig struct {
	// rules pick the files raw is read from:
	//   - With --kubeconfig cherry.yaml: cherry.yaml alone, even if $KUBECONFIG is set
	//   - Else, with KUBECONFIG=apple.yaml:banana.yaml: both, merged
	//   - Else: ~/.kube/config
	//
	// They outlive the read, to save refreshed login tokens. E.g. if banana.yaml's user logs in with auth-provider
	// oidc:
	//   - Its id-token expires, so the oidc plugin gets a new one with its refresh-token
	//   - clientcmd, given rules by restConfig, finds banana.yaml as the file that defines the user
	//   - It saves the new id-token there, so the next kuba or kubectl run needn't refresh again
	rules *clientcmd.ClientConfigLoadingRules
	// raw is what rules' files say, read once and merged as kubectl merges them, e.g. the first file to define a
	// context wins. It's without the connection flags, which restConfig adds to each context's client.
	raw clientcmdapi.Config

	// overrides hold the connection flags, e.g. --as, --token, or --request-timeout. Each beats kubeconfig's own
	// setting in every context, e.g. --token replaces each context's token.
	overrides clientcmd.ConfigOverrides
	// context is the context --context picked, or "" for kubeconfig's current one. It's kept out of overrides, whose
	// context beats the one each Cluster asks for, so a fan-out's clients would all reach the same cluster.
	context string

	// cacheDir holds kubectl's discovery and HTTP caches; shared with kubectl.
	cacheDir string
	// limit throttles each cluster's requests, if at all.
	limit RateLimit

	// log is the debug log, e.g. a line per request (see guard).
	log *slog.Logger
	// warnings is where warnings print: discovery's directly, and servers' through serverWarnings.
	warnings io.Writer
	// serverWarnings prints server warnings, e.g. on a deprecated API, dropping repeats, even ones from other clusters.
	serverWarnings rest.WarningHandler

	// mu guards clusters, so Cluster is safe to call from several goroutines at once, e.g. a fan-out's.
	mu sync.Mutex
	// clusters are the Clusters handed out so far, by context, so a second fan-out over a context reuses its clients
	// rather than building them again, e.g. rerunning an exec plugin like aws eks get-token.
	clusters map[string]*Cluster
}

// LoadKubeconfig reads kubeconfig the way kubectl does, given kubectl's connection flags and --context.
//
// flags are the connection flags, e.g. --kubeconfig, --as, or --v=6, and only those: clientcmd also reads
// --context and --namespace, which as overrides would apply to every context. context is --context's value, or ""
// for kubeconfig's current one. It's passed apart as it picks the starting cluster, not how kuba reaches each one
// (see Kubeconfig.context).
//
// It fails when:
//   - A connection flag's value doesn't parse, e.g. --insecure-skip-tls-verify=maybe
//   - A kubeconfig file doesn't parse, or --kubeconfig names one that doesn't exist
//   - Kubeconfig has no contexts at all, in which case the error names the files kuba looked in
//   - There's no starting context, i.e. no --context and no current-context, e.g. after kubectl config unset
//     current-context
//   - The starting context isn't one of the contexts, e.g. a --context typo, or a current-context naming a deleted
//     context, or one defined in a file that isn't loaded
func LoadKubeconfig(flags []flagspec.FlagValue, context string, limit RateLimit, log *slog.Logger, warnings io.Writer) (*Kubeconfig, error) {
	k := &Kubeconfig{
		rules:          clientcmd.NewDefaultClientConfigLoadingRules(),
		context:        context,
		cacheDir:       defaultCacheDir(),
		limit:          limit,
		log:            log,
		warnings:       warnings,
		serverWarnings: rest.NewWarningWriter(warnings, rest.WarningWriterOptions{Deduplicate: true}),
		clusters:       map[string]*Cluster{},
	}

	// fs maps clientcmd's override flags to overrides' fields: kuba already parsed argv, so fs just parses each value
	// as kubectl does and sets its field, e.g. --as apple -> overrides.AuthInfo.Impersonate
	fs := pflag.NewFlagSet("overrides", pflag.ContinueOnError)
	clientcmd.BindOverrideFlags(&k.overrides, fs, clientcmd.RecommendedConfigOverrideFlags(""))

	// Most connection flags are overrides fs can set; kuba handles the rest itself, or skips them
	userExtra := map[string][]string{}
	for _, f := range flags {
		switch f.Name {
		case "kubeconfig":
			// --kubeconfig reads its file alone, ignoring $KUBECONFIG and ~/.kube/config. E.g. with
			// KUBECONFIG=apple.yaml:banana.yaml, --kubeconfig cherry.yaml means:
			//   - Only cherry.yaml's contexts exist: a fan-out leaves out apple.yaml's and banana.yaml's, and --context
			//     can't pick them
			//   - Without --context, kuba starts at cherry.yaml's current-context
			//   - A missing cherry.yaml fails, whereas a missing apple.yaml would just be skipped
			k.rules.ExplicitPath = f.Value
		case "cache-dir":
			// --cache-dir moves the discovery and HTTP caches kuba shares with kubectl, from $KUBECACHEDIR or
			// ~/.kube/cache (see defaultCacheDir)
			k.cacheDir = f.Value
		case "as-user-extra":
			// --as-user-extra is an override like --as, but clientcmd has no flag for it, so it's parsed here as
			// kubectl parses it: key=value, each repeat adding a value. E.g. --as-user-extra reason=debug
			// --as-user-extra reason=oncall -> reason: [debug oncall]. Like kubectl, it skips a value without =
			//
			// REF: https://github.com/kubernetes/cli-runtime/blob/v0.37.1/pkg/genericclioptions/config_flags.go#L190-L202
			if key, value, ok := strings.Cut(f.Value, "="); ok {
				userExtra[key] = append(userExtra[key], value)
			}
		default:
			// Any other flag is an override if clientcmd defines it, e.g. --as apple, --insecure-skip-tls-verify, or
			// --request-timeout 5s. If not, e.g. --v=6, kuba's own requests ignore it, but a handoff still passes it to
			// kubectl: kuba get pod apple --v=6 runs kubectl get pod apple --v=6
			if fs.Lookup(f.Name) == nil {
				continue
			}
			if err := fs.Set(f.Name, f.Value); err != nil {
				return nil, fmt.Errorf("--%s: %w", f.Name, err)
			}
		}
	}
	if len(userExtra) > 0 {
		k.overrides.AuthInfo.ImpersonateUserExtra = userExtra
	}

	raw, err := k.rules.Load()
	if err != nil {
		return nil, err
	}
	k.raw = *raw
	if len(k.raw.Contexts) == 0 {
		return nil, fmt.Errorf("no kubeconfig contexts found (looked in %s)", strings.Join(k.rules.GetLoadingPrecedence(), ", "))
	}

	switch c := k.Current(); {
	case c == "":
		return nil, errors.New("no current context: pass --context, or set one with kubectl config use-context")
	case k.raw.Contexts[c] == nil:
		return nil, fmt.Errorf("context %q not found in kubeconfig", c)
	}

	return k, nil
}

// RateLimit caps each cluster's API requests on the client. The zero value caps nothing, kuba's default.
type RateLimit struct {
	// QPS is requests a second, or 0 for no cap.
	QPS int
	// Burst is how many requests can go at once before the cap applies, or 0 for QPS's worth. Unused without QPS.
	Burst int
}

// apply sets cfg's client-side throttling to match l, whose zero QPS means no cap.
//
// Example:
//
//	RateLimit{}                     -> QPS -1, i.e. no cap
//	RateLimit{QPS: 20}              -> QPS 20, Burst 20
//	RateLimit{QPS: 20, Burst: 50}   -> QPS 20, Burst 50
func (l RateLimit) apply(cfg *rest.Config) {
	cfg.QPS = -1 // any negative turns client-go's throttling off
	if l.QPS > 0 {
		cfg.QPS, cfg.Burst = float32(l.QPS), cmp.Or(l.Burst, l.QPS)
	}
}

// Contexts lists every context's name, sorted.
func (k *Kubeconfig) Contexts() []string {
	return slices.Sorted(maps.Keys(k.raw.Contexts))
}

// Current is the starting context: --context, else kubeconfig's current context.
func (k *Kubeconfig) Current() string {
	return cmp.Or(k.context, k.raw.CurrentContext)
}

// Namespace is a context's default namespace, or "default" if it names none.
func (k *Kubeconfig) Namespace(context string) string {
	return cmp.Or(lo.FromPtr(k.raw.Contexts[context]).Namespace, "default")
}

// Cluster is a context's Cluster, the same one on every call, so its clients are built once, on its first API call
// (see connect).
func (k *Kubeconfig) Cluster(context string) (*Cluster, error) {
	if k.raw.Contexts[context] == nil {
		return nil, fmt.Errorf("context %q not found in kubeconfig", context)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	c, ok := k.clusters[context]
	if !ok {
		c = &Cluster{Name: context, kubeconfig: k}
		k.clusters[context] = c
	}
	return c, nil
}

// restConfig is how kuba reaches a context: its credentials and the connection flags, plus kuba's throttling, warning
// handler, and read-only guard, which every client built from it shares.
func (k *Kubeconfig) restConfig(context string) (*rest.Config, error) {
	cfg, err := clientcmd.NewNonInteractiveClientConfig(k.raw, context, new(k.overrides), k.rules).ClientConfig()
	if err != nil {
		return nil, err
	}

	k.limit.apply(cfg)
	cfg.WarningHandler = k.serverWarnings
	cfg.Wrap(guard(context, k.log)) // kuba's direct requests are ~read-only

	return cfg, nil
}

// defaultCacheDir is kubectl's cache directory: $KUBECACHEDIR, else ~/.kube/cache.
//
// REF: https://github.com/kubernetes/cli-runtime/blob/v0.37.1/pkg/genericclioptions/config_flags.go#L326-L332
func defaultCacheDir() string {
	return cmp.Or(os.Getenv("KUBECACHEDIR"), filepath.Join(homedir.HomeDir(), ".kube", "cache"))
}
