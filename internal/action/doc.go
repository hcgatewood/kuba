// Package action is the commands kuba hands off to, built once and then run or printed.
//
// An Action is one of:
//   - A plain command, which runs as its own words
//   - A pipeline (`a | b`) or sequence (`a && b`), which runs as `/bin/sh -c` with its printed text
//
// One value drives both running and printing, so what runs is exactly what --command prints. Words are quoted as
// Python's shlex.quote quotes them, so a printed command pastes into a shell as is.
//
// # Running
//
// A lone action replaces kuba's process (Exec). It takes over the terminal, signals, and exit code, as if you'd run it
// directly.
//
// Several actions, e.g. one per cluster and namespace, run in turn as kuba's children (RunEach). As with kubectl given
// several names, one failing doesn't stop the rest.
//
// Self names kuba as a command's program. It runs as this very binary, whatever PATH holds.
package action
