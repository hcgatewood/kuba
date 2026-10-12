package action

import (
	"bytes"
	"context"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hcgatewood/kuba/internal/exit"
)

func TestString(t *testing.T) {
	tests := []struct {
		name string
		a    Action
		want string
	}{
		{name: "bare words", a: Command("kubectl", "get", "pod", "apple-1", "--namespace=banana", "--context=x", "--field-selector=a=b", "-it", "--"), want: "kubectl get pod apple-1 --namespace=banana --context=x --field-selector=a=b -it --"},
		{name: "spaces quoted", a: Command("kubectl", "get", "pod", "--selector=tier notin (web)"), want: "kubectl get pod '--selector=tier notin (web)'"},
		{name: "empty word", a: Command("fzf", "--query="), want: "fzf --query="},
		{name: "empty arg", a: Command("echo", ""), want: "echo ''"},
		{name: "embedded quote", a: Command("echo", "it's"), want: `echo 'it'"'"'s'`},
		{name: "stern regex", a: Command("kubectl", "stern", "^(apple|banana)$"), want: "kubectl stern '^(apple|banana)$'"},
		{name: "pipeline", a: Pipeline(Command("kubectl", "get", "pod", "-o", "json"), Command("kuba", "ifne", "fx")), want: "kubectl get pod -o json | kuba ifne fx"},
		{name: "sequence", a: Sequence(Command("true"), Command("echo", "a b")), want: "true && echo 'a b'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.a.String())
		})
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name string
		a    Action
		code int
	}{
		{name: "success", a: Command("true")},
		{name: "own code", a: Command("/bin/sh", "-c", "exit 7"), code: 7},
		{name: "signal", a: Command("/bin/sh", "-c", "kill -TERM $$"), code: 128 + 15},
		{name: "pipeline via sh", a: Pipeline(Command("echo", "apple"), Command("grep", "-q", "banana")), code: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _ := exit.Code(tt.a.Run())
			assert.Equal(t, tt.code, code)
		})
	}
}

func TestRunEach(t *testing.T) {
	canceled, cancel := context.WithCancelCause(context.Background())
	cancel(&exit.SignalError{Signal: syscall.SIGTERM})

	tests := []struct {
		name    string
		ctx     context.Context
		code    int
		wantOut string
	}{
		{name: "failures keep going", ctx: context.Background(), code: 3, wantOut: "\n\n"},
		{name: "cancel stops the rest", ctx: canceled, code: 128 + 15, wantOut: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			actions := []Action{Command("/bin/sh", "-c", "exit 3"), Command("/bin/sh", "-c", "exit 4"), Command("true")}
			code, _ := exit.Code(RunEach(tt.ctx, actions, &out))
			assert.Equal(t, tt.code, code)
			assert.Equal(t, tt.wantOut, out.String())
		})
	}
}
