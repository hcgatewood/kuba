// Command kuba is the magical kubectl companion: kubectl with fuzzy names, fzf pickers, and lookups across namespaces
// and clusters.
//
// Kuba sits in front of kubectl. It works out which objects you mean, then does one of two things:
//   - Prints them itself, e.g. a listing merged across clusters
//   - Hands kubectl the exact command for them, which --command prints instead of running
//
// Kuba's own API requests only ever read. Any change happens in a kubectl command you can see.
//
// # A command's path
//
// Here's how `kuba get deploy api -o yaml` flows through the packages:
//  1. cli runs get, which parses its arguments with flagspec, while config reads kuba's own options.
//  2. kube loads kubeconfig and looks up deployments on the current cluster. With -m, it searches the current
//     context's siblings too, all at once.
//  3. resolve matches api against the names found. -o yaml acts on objects, so fzf asks you to pick if several match.
//  4. action builds the kubectl get command for your pick and runs it, or prints it under --command.
//  5. exit turns how the run ended, e.g. an error, kubectl's exit code, or a Ctrl-C, into kuba's exit code.
//
// A plain listing like `kuba get pod` skips step 4: render prints it itself, through kubectl's own table printer.
//
// # Packages
//
//	internal/cli           commands: dispatch, help, and each command's run
//	internal/flagspec      each command's flags and argument parsing, every flag with one role
//	internal/kubectlflags  kubectl's flags, generated from the pinned kubectl
//	internal/config        kuba's options, and KUBA_CLUSTERS
//	internal/kube          read-only cluster access: kubeconfig, discovery, Table lookups, and fan-out
//	internal/resolve       matching queries to names, and picking among the matches
//	internal/render        output kuba prints itself, through kubectl's printers and kubecolor
//	internal/action        the commands kuba hands off to, run or printed
//	internal/exit          exit codes and signals
//	e2e                    black-box scripts, run against the Go and Python implementations
//
// # Auth plugins
//
// Kubeconfig users that log in with an auth-provider, e.g. oidc, need its plugin registered. main does that by
// importing client-go's auth plugins, as kubectl does.
package main
