package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hcgatewood/kuba/internal/exit"
	"github.com/hcgatewood/kuba/internal/flagspec"
	"github.com/hcgatewood/kuba/internal/resolve"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name string
		argv string
		env  map[string]string
		want Options
	}{
		{
			name: "defaults",
			want: Options{
				Kubectl:           Setting[string]{Value: "kuba kubectl", Source: Default},
				Fzf:               Setting[string]{Source: Default},
				QPS:               Setting[int]{Source: Default},
				Burst:             Setting[int]{Source: Default},
				Debug:             Setting[bool]{Source: Default},
				Match:             Setting[resolve.Mode]{Value: resolve.Tiered, Source: Default},
				StrictClusters:    Setting[bool]{Source: Default},
				MaxParallel:       Setting[int]{Value: 8, Source: Default},
				ClusterTimeout:    Setting[time.Duration]{Value: 15 * time.Second, Source: Default},
				ProgressDelay:     Setting[time.Duration]{Value: 2 * time.Second, Source: Default},
				Clusters:          Setting[string]{Source: Default},
				EscapeAnnotations: Setting[bool]{Source: Default},
			},
		},
		{
			name: "env",
			env: map[string]string{
				"KUBA_KUBECTL":            "kubecolor",
				"KUBA_FZF":                "/opt/fzf",
				"KUBA_QPS":                "20",
				"KUBA_BURST":              "40",
				"KUBA_DEBUG":              "YES",
				"KUBA_MATCH":              "fuzzy",
				"KUBA_STRICT_CLUSTERS":    "nope",
				"KUBA_MAX_PARALLEL":       "4",
				"KUBA_CLUSTER_TIMEOUT":    "1m",
				"KUBA_PROGRESS_DELAY":     "5s",
				"KUBA_CLUSTERS":           "k*=*",
				"KUBA_ESCAPE_ANNOTATIONS": "yes",
			},
			want: Options{
				Kubectl:           Setting[string]{Value: "kubecolor", Source: Env},
				Fzf:               Setting[string]{Value: "/opt/fzf", Source: Env},
				QPS:               Setting[int]{Value: 20, Source: Env},
				Burst:             Setting[int]{Value: 40, Source: Env},
				Debug:             Setting[bool]{Value: true, Source: Env},
				Match:             Setting[resolve.Mode]{Value: resolve.Fuzzy, Source: Env},
				StrictClusters:    Setting[bool]{Source: Env},
				MaxParallel:       Setting[int]{Value: 4, Source: Env},
				ClusterTimeout:    Setting[time.Duration]{Value: time.Minute, Source: Env},
				ProgressDelay:     Setting[time.Duration]{Value: 5 * time.Second, Source: Env},
				Clusters:          Setting[string]{Value: "k*=*", Source: Env},
				EscapeAnnotations: Setting[bool]{Value: true, Source: Env},
			},
		},
		{
			name: "flags beat env",
			argv: "pod --kubectl=kubectl-1.36 --fzf /opt/fzf --qps 5 --burst 10 --debug=false --match exact --strict-clusters --max-parallel 0 --cluster-timeout 30s --progress-delay 500ms --cluster-groups c*=cherry-* --escape-annotations",
			env: map[string]string{
				"KUBA_MAX_PARALLEL":       "4",
				"KUBA_CLUSTER_TIMEOUT":    "1m",
				"KUBA_PROGRESS_DELAY":     "5s",
				"KUBA_KUBECTL":            "kubecolor",
				"KUBA_FZF":                "fzf",
				"KUBA_QPS":                "20",
				"KUBA_BURST":              "40",
				"KUBA_DEBUG":              "1",
				"KUBA_MATCH":              "fuzzy",
				"KUBA_CLUSTERS":           "k*=*",
				"KUBA_ESCAPE_ANNOTATIONS": "0",
			},
			want: Options{
				Kubectl:           Setting[string]{Value: "kubectl-1.36", Source: Flag},
				Fzf:               Setting[string]{Value: "/opt/fzf", Source: Flag},
				QPS:               Setting[int]{Value: 5, Source: Flag},
				Burst:             Setting[int]{Value: 10, Source: Flag},
				Debug:             Setting[bool]{Source: Flag},
				Match:             Setting[resolve.Mode]{Value: resolve.Exact, Source: Flag},
				StrictClusters:    Setting[bool]{Value: true, Source: Flag},
				MaxParallel:       Setting[int]{Source: Flag},
				ClusterTimeout:    Setting[time.Duration]{Value: 30 * time.Second, Source: Flag},
				ProgressDelay:     Setting[time.Duration]{Value: 500 * time.Millisecond, Source: Flag},
				Clusters:          Setting[string]{Value: "c*=cherry-*", Source: Flag},
				EscapeAnnotations: Setting[bool]{Value: true, Source: Flag},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmdline, err := flagspec.Get.Parse(strings.Fields(tt.argv))
			require.NoError(t, err)
			got, err := Load(cmdline, func(k string) string { return tt.env[k] })
			require.NoError(t, err)
			assert.Equal(t, tt.want, *got)
		})
	}
}

func TestKubectlArgv(t *testing.T) {
	tests := []struct {
		kubectl string
		want    []string
	}{
		{kubectl: "kuba kubectl", want: []string{"kuba", "kubectl"}},
		{kubectl: "kubecolor", want: []string{"kubecolor"}},
		{kubectl: " /opt/bin/kubectl  --kubeconfig  /tmp/apple ", want: []string{"/opt/bin/kubectl", "--kubeconfig", "/tmp/apple"}},
	}
	for _, tt := range tests {
		t.Run(tt.kubectl, func(t *testing.T) {
			o := Options{Kubectl: Setting[string]{Value: tt.kubectl}}
			assert.Equal(t, tt.want, o.KubectlArgv())
		})
	}
}

func TestLoadBadValue(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		env  map[string]string
		// wantUsage reports whether the error is a usage error, which only a bad flag is.
		wantUsage bool
		wantErr   string
	}{
		{name: "match flag", argv: []string{"pod", "--match=closest"}, wantUsage: true, wantErr: "--match"},
		{name: "match env", argv: []string{"pod"}, env: map[string]string{"KUBA_MATCH": "closest"}, wantErr: "KUBA_MATCH"},
		{name: "qps flag", argv: []string{"pod", "--qps=lots"}, wantUsage: true, wantErr: `--qps: want a whole number, got "lots"`},
		{name: "qps env", argv: []string{"pod"}, env: map[string]string{"KUBA_QPS": "1.5"}, wantErr: "KUBA_QPS"},
		{name: "negative burst", argv: []string{"pod", "--burst=-1"}, wantUsage: true, wantErr: `--burst: want a whole number, got "-1"`},
		{name: "unitless timeout", argv: []string{"pod", "--cluster-timeout=15"}, wantUsage: true, wantErr: `--cluster-timeout: want a duration, e.g. 30s, got "15"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmdline, err := flagspec.Get.Parse(tt.argv)
			require.NoError(t, err)
			_, err = Load(cmdline, func(k string) string { return tt.env[k] })
			assert.ErrorContains(t, err, tt.wantErr)
			_, usage := errors.AsType[*exit.UsageError](err)
			assert.Equal(t, tt.wantUsage, usage)
		})
	}
}

// TestOptionTags checks that every option names an env var, and a flag that a spec declares.
func TestOptionTags(t *testing.T) {
	for _, opt := range (&Options{}).all() {
		assert.NotEmpty(t, opt.env, opt.name)
		assert.NotNil(t, flagspec.Get.Lookup(opt.flag), "%s: no spec declares --%s", opt.name, opt.flag)
	}
}

// TestLoadRootSpec checks that flags a command lacks fall back to env and defaults rather than panic.
func TestLoadRootSpec(t *testing.T) {
	cmdline, err := flagspec.Root.Parse([]string{"--debug"})
	require.NoError(t, err)
	got, err := Load(cmdline, func(k string) string { return map[string]string{"KUBA_MATCH": "exact"}[k] })
	require.NoError(t, err)
	assert.Equal(t, Setting[bool]{Value: true, Source: Flag}, got.Debug)
	assert.Equal(t, Setting[resolve.Mode]{Value: resolve.Exact, Source: Env}, got.Match)
}
