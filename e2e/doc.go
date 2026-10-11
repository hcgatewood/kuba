// Package e2e is kuba's black-box suite: testscript scripts that run against either implementation, Python or Go.
//
// # Running
//
//	make e2e-go                     every script, against the Go kuba
//	make e2e-python RUN=get/fuzzy   scripts matching a path, against the Python kuba
//	make e2e-go UPDATE=1            rewrite cmp goldens from actual output
//
// RUN is go test's -run pattern. make sets KUBA_IMPL to pick the implementation. Without it the suite skips, so plain
// `go test ./...` stays a unit run.
//
// # Scripts
//
// Each script under testdata/script is a txtar file: commands with assertions on their output, then the files they
// use, e.g. objects to apply and golden output.
//
// [impl:go] and [impl:python] limit a line to one implementation, e.g. where Go fixes a Python bug. A comment above
// says what differs.
//
// # Environment
//
//   - Scripts share one envtest API server. Each gets its own namespace ($NS), and a kubeconfig whose apple context
//     points at that server.
//   - A script that sees cluster-wide state, e.g. nodes, CRDs, or -A, starts with `mkcluster apple` to get a server of
//     its own.
//   - PATH is hermetic: the implementation, fakes for the tools kuba runs (fzf, kubecolor, ssh, kubectl plugins, and
//     more), envtest's kubectl, /usr/bin, and /bin. Most fakes just record their calls.
//
// # Script commands
//
// Beyond testscript's builtins, scripts get these. Each is documented on its cmd function.
//
//	kuba ARGS...          run the implementation, normalizing its output
//	ptyexec CMD...        run a command on a pseudo-terminal, for terminal-only output like headers and color
//	tui start CMD...      drive a terminal UI, like the fzf built into the Go kuba
//	kapply FILE...        create the objects in YAML files, status included
//	mkcluster NAME...     give contexts fresh API servers of their own
//	mkcontext NAME...     add contexts sharing apple's server
//	rows ROW...           assert stdout's rows, by first column
//	reqs N                assert each context sent at most N API requests, from kuba's debug log
//	exitcode N            assert the last exit code
//	current CONTEXT       assert kubeconfig's current context
//	calls                 print what the fakes recorded, e.g. the lines fzf was shown
//	shellenv ARGS...      save `kuba shellenv` output, for shell and defines to source
//	shell CMDLINE         run a line in $SHELL with the shell functions loaded
//	history [REGEX]       check the newest history entry shell left
//	defines NAME...       assert the shell functions define each name
//
// # Normalized output
//
// kuba's output is normalized before any check, so goldens hold across runs and implementations:
//   - Padding between columns collapses to two spaces
//   - Ages become <age>, timestamps <time>, resource versions <rv>, and UIDs <uid>
//   - The work dir becomes $WORK, and a server's host:port its first context's name, e.g. <apple>
//
// So goldens can't check column widths; render's unit tests pin those.
package e2e
