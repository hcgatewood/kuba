package kubectlflags

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerb(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "verb first", args: []string{"logs", "apple"}, want: "logs"},
		{name: "values after =", args: []string{"--context=apple", "--namespace=ns", "exec", "-it", "apple"}, want: "exec"},
		{name: "values as next arg", args: []string{"--context", "apple", "-n", "kiwi", "logs", "apple"}, want: "logs"},
		{name: "short flag with attached value", args: []string{"-nkiwi", "logs", "apple"}, want: "logs"},
		{name: "bool flag", args: []string{"--insecure-skip-tls-verify", "logs", "apple"}, want: "logs"},
		{name: "no verb", args: []string{"--context", "apple"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Verb(tt.args))
		})
	}
}
