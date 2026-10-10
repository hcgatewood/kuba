package kubectlflags

//go:generate go test -run ^TestTables$ -count=1 -update .

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
)

// Flag is one of a kubectl command's flags.
type Flag struct {
	// Long is the long name, without dashes.
	Long string
	// Short is the one-letter name, if any.
	Short string
	// Bool flags take no value, e.g. --all-namespaces, though they accept one after =, e.g. --all-namespaces=false.
	Bool bool
}

// Verb is the kubectl command a kubectl argv runs: its first argument that's neither a flag nor a global flag's value,
// e.g. logs in both `--context=apple logs apple` and `--context apple logs apple`.
func Verb(args []string) string {
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case !strings.HasPrefix(arg, "-"):
			return arg
		case !strings.Contains(arg, "=") && globalValueFlags.Has(arg):
			i++
		}
	}
	return ""
}

// globalValueFlags are kubectl's global flags that take a value, by long and short name, e.g. --context and -n but
// not --insecure-skip-tls-verify.
var globalValueFlags = func() sets.Set[string] {
	flags := sets.New[string]()
	for _, f := range Commands["options"] {
		if f.Bool {
			continue
		}
		flags.Insert("--" + f.Long)
		if f.Short != "" {
			flags.Insert("-" + f.Short)
		}
	}
	return flags
}()
