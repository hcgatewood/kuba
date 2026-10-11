package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVersion checks that --version prints just what `kuba version` does, and that -v is kubectl's log level instead.
func TestVersion(t *testing.T) {
	tests := []struct {
		args    []string
		want    string
		wantErr string
	}{
		{args: []string{"version"}, want: "kuba v1.2.3\n"},
		{args: []string{"--version"}, want: "kuba v1.2.3\n"},
		{args: []string{"-v"}, wantErr: "flag needs an argument"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var stdout bytes.Buffer
			a := &app{version: "v1.2.3", stdout: &stdout, stderr: io.Discard}
			err := a.run(context.Background(), tt.args)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, stdout.String())
		})
	}
}
