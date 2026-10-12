package flagspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSpecs checks that every Kuba-role flag has help text.
func TestSpecs(t *testing.T) {
	specs := map[string]Spec{"root": Root, "get": Get}
	for name, s := range specs {
		t.Run(name, func(t *testing.T) {
			for _, f := range s {
				if f.Role == Kuba {
					assert.NotEmpty(t, f.Usage, "--%s", f.Name)
				}
			}
		})
	}
}
