package cli

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFindCommand checks that cobra finds the command name past any root flag, and hands the command every other word.
func TestFindCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCmd  string
		wantArgs []string
	}{
		{name: "kuba bool", args: []string{"--debug", "get", "pod"}, wantCmd: "get", wantArgs: []string{"--debug", "pod"}},
		{name: "kuba value", args: []string{"--kubectl", "kubecolor", "get", "pod"}, wantCmd: "get", wantArgs: []string{"--kubectl", "kubecolor", "pod"}},
		{name: "kubectl bool", args: []string{"--insecure-skip-tls-verify", "get", "pod"}, wantCmd: "get", wantArgs: []string{"--insecure-skip-tls-verify", "pod"}},
		{name: "kubectl value", args: []string{"-n", "kiwi", "get", "pod"}, wantCmd: "get", wantArgs: []string{"-n", "kiwi", "pod"}},
		{name: "kubectl log level", args: []string{"--v", "6", "get", "pod"}, wantCmd: "get", wantArgs: []string{"--v", "6", "pod"}},
		{name: "kubectl log level, short", args: []string{"-v=6", "get", "pod"}, wantCmd: "get", wantArgs: []string{"-v=6", "pod"}},
		{name: "kubectl log level, short spaced", args: []string{"-v", "6", "get", "pod"}, wantCmd: "get", wantArgs: []string{"-v", "6", "pod"}},
		{name: "before kuba kubectl", args: []string{"--warnings-as-errors", "kubectl", "version"}, wantCmd: "kubectl", wantArgs: []string{"--warnings-as-errors", "version"}},
	}
	root := (&app{version: "v1.2.3", stdout: io.Discard, stderr: io.Discard}).root(context.Background())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, args, err := root.Find(tt.args)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCmd, cmd.Name())
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}
