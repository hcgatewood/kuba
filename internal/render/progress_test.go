package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

func TestProgress(t *testing.T) {
	var out bytes.Buffer
	p := StartProgress(&out, 0, []string{"apple", "banana"})
	p.Done("apple")
	time.Sleep(50 * time.Millisecond) // well past the zero delay, so the line shows banana pending
	p.Stop()
	got := out.String()
	assert.Regexp(t, `\x1b\[2m. waiting on banana \(1/2, 0s\)\x1b\[m`, got)
	assert.True(t, strings.HasSuffix(got, "\r\x1b[2K"), "progress line erased: %q", got)

	var none *Progress
	none.Done("apple")
	none.Stop()
}

func TestProgressLine(t *testing.T) {
	five := strings.Fields("apple banana cherry donut egg")
	tests := []struct {
		name    string
		pending []string
		width   int
		want    string
	}{
		{name: "all fit", pending: five, width: 80, want: "⠴ waiting on apple, banana, cherry, donut, egg (5/10, 4s)"},
		{name: "some fit", pending: five, width: 46, want: "⠴ waiting on apple, banana, +3 more (5/10, 4s)"},
		{name: "one fits", pending: five, width: 45, want: "⠴ waiting on apple, +4 more (5/10, 4s)"},
		{name: "last name where +1 more wouldn't fit", pending: strings.Fields("apple banana fig"), width: 42, want: "⠴ waiting on apple, banana, fig (7/10, 4s)"},
		{name: "none fit", pending: []string{"apple-with-a-long-name"}, width: 20, want: "⠴ waiting on apple-…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := progressLine(tt.pending, 10, 4500*time.Millisecond, tt.width)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, ansi.StringWidth(got), tt.width)
		})
	}
}
