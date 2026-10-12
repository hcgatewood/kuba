package resolve

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hcgatewood/kuba/internal/exit"
)

func TestFzfArgs(t *testing.T) {
	tests := []struct {
		name   string
		picker Picker
		multi  bool
		want   []string
	}{
		{name: "multi", picker: Picker{Type: "pod", Query: "aple", Header: "NAME"}, multi: true, want: []string{"--multi", "--query=aple", "--prompt=Choose pod: ", "--header=NAME", "--ansi"}},
		{name: "single without header", picker: Picker{Type: "pod"}, want: []string{"--bind=shift-tab:up,tab:down", "--query=", "--prompt=Choose pod: ", "--ansi"}},
		{name: "query lowered", picker: Picker{Type: "pod", Query: "APLE"}, multi: true, want: []string{"--multi", "--query=aple", "--prompt=Choose pod: ", "--ansi"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.picker.fzfArgs(tt.multi))
		})
	}
}

func TestFzfResult(t *testing.T) {
	tests := []struct {
		name     string
		printed  []string
		code     int
		err      error
		want     []string
		wantCode int
		wantErr  string
	}{
		{name: "picked", printed: []string{"apple"}, want: []string{"apple"}},
		{name: "no match picks nothing", code: 1},
		{name: "cancel", code: 130, wantCode: exit.Interrupted, wantErr: "exit status 130"},
		{name: "bad option", code: 2, err: errors.New("unknown option: --banana"), wantCode: 1, wantErr: "fzf: unknown option: --banana"},
		{name: "external failure", code: 2, wantCode: 1, wantErr: "fzf exited 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fzfResult(tt.printed, tt.code, tt.err)
			assert.Equal(t, tt.want, got)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
			code, _ := exit.Code(err)
			assert.Equal(t, tt.wantCode, code)
		})
	}
}

func TestPickedIndices(t *testing.T) {
	lines := []string{"apple-1", "banana-1", "apple-1", "cherry-1"}
	tests := []struct {
		name    string
		printed []string
		want    []int
	}{
		{name: "sorted", printed: []string{"cherry-1", "banana-1"}, want: []int{1, 3}},
		{name: "duplicates pick each copy once", printed: []string{"apple-1", "apple-1", "apple-1"}, want: []int{0, 2}},
		{name: "unknown lines skipped", printed: []string{"aple", "banana-1"}, want: []int{1}},
		{name: "nothing", printed: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pickedIndices(lines, tt.printed))
		})
	}
}
