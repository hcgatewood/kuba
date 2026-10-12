package flagspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKubectlRoles(t *testing.T) {
	tests := []struct {
		command string
		flag    string
		role    Role
		bool    bool
	}{
		{command: "get", flag: "namespace", role: Target},
		{command: "get", flag: "context", role: Target},
		{command: "get", flag: "all-namespaces", role: Target, bool: true},
		{command: "get", flag: "selector", role: Filter},
		{command: "get", flag: "field-selector", role: Filter},
		{command: "get", flag: "output", role: Output},
		{command: "get", flag: "no-headers", role: Output, bool: true},
		{command: "get", flag: "kubeconfig", role: Connection},
		{command: "get", flag: "insecure-skip-tls-verify", role: Connection, bool: true},
		{command: "get", flag: "show-labels", role: Output, bool: true},
		{command: "get", flag: "ignore-not-found", role: Passthrough, bool: true},
		{command: "get", flag: "chunk-size", role: Passthrough},
		{command: "debug", flag: "profile", role: Passthrough},
	}
	for _, tt := range tests {
		t.Run(tt.command+"/"+tt.flag, func(t *testing.T) {
			var got []Flag
			for _, f := range Kubectl(tt.command) {
				if f.Name == tt.flag {
					got = append(got, f)
				}
			}
			require.Len(t, got, 1)
			assert.Equal(t, tt.role, got[0].Role)
			assert.Equal(t, tt.bool, got[0].Bool)
		})
	}
}
