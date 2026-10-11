package e2e

import (
	"flag"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"
)

const scriptRoot = "testdata/script"

var update = flag.Bool("update", false, "rewrite cmp goldens inside scripts from actual output")

func TestMain(m *testing.M) {
	testscript.Main(m, fakeCommands())
}

// TestScript runs every script under testdata/script, all in parallel, each named after its path, e.g. get/fuzzy.
//
// go test's -run pattern decides which scripts run (make's RUN sets it, e.g. RUN=get/fuzzy).
func TestScript(t *testing.T) {
	impl := os.Getenv("KUBA_IMPL")
	if impl == "" {
		t.Skip("KUBA_IMPL unset; run make e2e-python or make e2e-go")
	}
	h := newHarness(t, impl)
	for _, dir := range scriptDirs(t) {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			testscript.Run(t, h.params(filepath.Join(scriptRoot, dir)))
		})
	}
	require.NoError(t, h.implReady()) // fail once here, before all script tests run, if the build failed
}

// scriptDirs lists every directory holding txtar scripts, relative to testdata/script.
func scriptDirs(t *testing.T) []string {
	var dirs []string
	err := fs.WalkDir(os.DirFS(scriptRoot), ".", func(p string, _ fs.DirEntry, err error) error {
		if err == nil && path.Ext(p) == ".txtar" {
			dirs = append(dirs, path.Dir(p))
		}
		return err
	})
	require.NoError(t, err)
	slices.Sort(dirs)
	return slices.Compact(dirs)
}
