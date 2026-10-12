package flagspec

import (
	"fmt"

	"github.com/samber/lo"

	"github.com/hcgatewood/kuba/internal/kubectlflags"
)

// Kubectl lists a wrapped kubectl command's flags with their roles, then the global flags it doesn't shadow.
//
// Global flags default to Connection, and a command's own flags to Passthrough; kubectlRoles lists the exceptions.
func Kubectl(command string) []Flag {
	if _, ok := kubectlflags.Commands[command]; !ok || command == "options" {
		panic(fmt.Sprintf("flagspec: no kubectl flag table for %q", command))
	}

	flags := append(specFlags(command, Passthrough), KubectlGlobals()...) // own flags first, then globals
	return lo.UniqBy(flags, func(f Flag) string { return f.Name })        // keeps first occurrences
}

// KubectlGlobals lists kubectl's global flags, the ones every kubectl command takes, with their roles.
func KubectlGlobals() []Flag {
	return specFlags("options", Connection)
}

// kubectlRoles marks the kubectl flags that change what kuba does, e.g. -n, -l, and -o, keyed by command, then flag.
//
// Every other flag gets a default role, and kuba just passes it on: Connection for a global flag, which reaches every
// request, and Passthrough for a command's own, which reaches the final command.
var kubectlRoles = map[string]map[string]Role{
	"options": {
		"context":   Target,
		"namespace": Target,
	},
	"get": {
		"all-namespaces":      Target,
		"field-selector":      Filter,
		"label-columns":       Output,
		"no-headers":          Output,
		"output":              Output,
		"selector":            Filter,
		"show-kind":           Output,
		"show-labels":         Output,
		"show-managed-fields": Output,
		"sort-by":             Output,
		"watch":               Output,
		"watch-only":          Output,
	},
}

// specFlags lists a kubectl command's flags, each with its role: the one kubectlRoles gives it, else defaultRole.
//
// kubectlflags knows a flag's names and whether it takes a value; the role is what kuba adds, i.e. what it does with
// the flag.
func specFlags(command string, defaultRole Role) []Flag {
	var flags []Flag
	for _, kf := range kubectlflags.Commands[command] {
		role, ok := kubectlRoles[command][kf.Long]
		if !ok {
			role = defaultRole
		}
		flags = append(flags, Flag{Name: kf.Long, Short: kf.Short, Role: role, Bool: kf.Bool})
	}
	return flags
}
