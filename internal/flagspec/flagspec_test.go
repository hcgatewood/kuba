package flagspec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hcgatewood/kuba/internal/exit"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		argv      string
		args      []string
		flags     []string
		dashIndex int
	}{
		{name: "flag among positionals", argv: "pod -n banana apple", args: []string{"pod", "apple"}, flags: []string{"--namespace=banana"}},
		{name: "short attached", argv: "pod -owide", args: []string{"pod"}, flags: []string{"--output=wide"}},
		{name: "short equals", argv: "pod -o=wide", args: []string{"pod"}, flags: []string{"--output=wide"}},
		{name: "long spaced", argv: "pod --output wide", args: []string{"pod"}, flags: []string{"--output=wide"}},
		{name: "long equals", argv: "pod --output=wide", args: []string{"pod"}, flags: []string{"--output=wide"}},
		{name: "combined shorts", argv: "pod -Ao yaml", args: []string{"pod"}, flags: []string{"--all-namespaces", "--output=yaml"}},
		{name: "combined kuba and kubectl shorts", argv: "pod -mn banana", args: []string{"pod"}, flags: []string{"--multi-cluster", "--namespace=banana"}},
		{name: "bool false", argv: "pod --select=false", args: []string{"pod"}, flags: []string{"--select=false"}},
		{name: "short bool false", argv: "pod -A=false", args: []string{"pod"}, flags: []string{"--all-namespaces=false"}},
		{name: "passthrough before query", argv: "pod --ignore-not-found aple", args: []string{"pod", "aple"}, flags: []string{"--ignore-not-found"}},
		{name: "passthrough value", argv: "pod --chunk-size 10 bnana", args: []string{"pod", "bnana"}, flags: []string{"--chunk-size=10"}},
		{name: "order kept", argv: "pod -L app -n x -L tier", args: []string{"pod"}, flags: []string{"--label-columns=app", "--namespace=x", "--label-columns=tier"}},
		{name: "empty value", argv: "pod --selector=", args: []string{"pod"}, flags: []string{"--selector="}},
		{name: "flag-like value", argv: "pod -n --newflag", args: []string{"pod"}, flags: []string{"--namespace=--newflag"}},
		{name: "dash", argv: "pod apple -- -n x", args: []string{"pod", "apple", "-n", "x"}, dashIndex: 2},
		{name: "lone dash is positional", argv: "pod -", args: []string{"pod", "-"}},
		{name: "help", argv: "pod -h", args: []string{"pod"}, flags: []string{"--help"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmdline, err := Get.Parse(strings.Fields(tt.argv))
			require.NoError(t, err)
			var flags []string
			for _, g := range cmdline.Flags {
				flags = append(flags, g.String())
			}
			if tt.dashIndex == 0 {
				tt.dashIndex = -1
			}
			assert.Equal(t, tt.args, cmdline.Args)
			assert.Equal(t, tt.flags, flags)
			assert.Equal(t, tt.dashIndex, cmdline.DashIndex)
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		argv string
		want string
	}{
		{name: "missing value", argv: "pod -n", want: "flag needs an argument"},
		{name: "missing long value", argv: "pod --output", want: "flag needs an argument"},
		{name: "bad bool", argv: "pod --select=maybe", want: "invalid argument"},
		{name: "unknown long", argv: "pod bnana --newflag value", want: "unknown flag: --newflag"},
		{name: "unknown short", argv: "pod -z apple", want: "unknown shorthand flag: 'z' in -z"},
		{name: "unknown letter in combined shorts", argv: "pod -Az", want: "unknown shorthand flag: 'z' in -z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Get.Parse(strings.Fields(tt.argv))
			var usage *exit.UsageError
			require.ErrorAs(t, err, &usage)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestCommandLine(t *testing.T) {
	cmdline, err := Get.Parse(strings.Fields("pod -L app --select -n x -L tier --ignore-not-found --select=false"))
	require.NoError(t, err)

	assert.True(t, cmdline.Has("select"))
	assert.False(t, cmdline.Bool("select"))
	assert.False(t, cmdline.Has("context"))
	assert.Equal(t, "", cmdline.Value("context"))
	assert.Equal(t, "tier", cmdline.Value("label-columns"))
	assert.Equal(t, []string{"app", "tier"}, cmdline.Values("label-columns"))

	v, ok := cmdline.Lookup("label-columns")
	assert.True(t, ok)
	assert.Equal(t, "tier", v)
	_, ok = cmdline.Lookup("context")
	assert.False(t, ok, "declared but not given")
	_, ok = cmdline.Lookup("nmespace")
	assert.False(t, ok, "not declared")

	var names []string
	for _, g := range cmdline.ByRole(Target, Passthrough) {
		names = append(names, g.Name)
	}
	assert.Equal(t, []string{"namespace", "ignore-not-found"}, names)

	assert.PanicsWithValue(t, "flagspec: no flag --nmespace in spec", func() { cmdline.Value("nmespace") })
}

func TestEmit(t *testing.T) {
	tests := []struct {
		name  string
		flag  string
		value string
		want  string
		panic string
	}{
		{name: "target", flag: "namespace", value: "banana", want: "--namespace=banana"},
		{name: "output", flag: "output", value: "yaml", want: "--output=yaml"},
		{name: "true bool", flag: "no-headers", value: "true", want: "--no-headers"},
		{name: "false bool", flag: "no-headers", value: "false", want: "--no-headers=false"},
		{name: "connection", flag: "as", value: "nobody", want: "--as=nobody"},
		{name: "set-based selector", flag: "selector", value: "tier notin (web)", want: "--selector=tier notin (web)"},
		{name: "undeclared", flag: "since", panic: "flagspec: emit of undeclared --since"},
		{name: "kuba", flag: "select", panic: "flagspec: emit of kuba's own --select"},
		{name: "passthrough", flag: "ignore-not-found", panic: "flagspec: emit of passthrough --ignore-not-found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.panic != "" {
				assert.PanicsWithValue(t, tt.panic, func() { Get.Emit(tt.flag, tt.value) })
				return
			}
			assert.Equal(t, tt.want, Get.Emit(tt.flag, tt.value))
		})
	}
}

func TestNewPanicsOnClash(t *testing.T) {
	tests := []struct {
		name  string
		group []Flag
		want  string
	}{
		{name: "long", group: []Flag{{Name: "selector", Role: Kuba}}, want: "flagspec: --selector declared twice"},
		{name: "short", group: []Flag{{Name: "apple", Short: "l", Role: Kuba}}, want: "flagspec: -l declared twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.PanicsWithValue(t, tt.want, func() { New(tt.group, Kubectl("get")) })
		})
	}
}
