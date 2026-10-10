# Kuba Go port: design

Status (2026-10-11): Phases 0 and 1 are done; Phase 2, the Go vertical slice, is ready for its checkpoint review.

This file is the plan of record: design, decisions, and working notes, rewritten whole at each plan revision. Three untracked companions track execution:

- [`issues.md`](issues.md): the granular running list of what to do and remember, including every bug fixed and behavior changed. It points into this doc, never the reverse, so this doc changes only when the design does.
- [`decisions.md`](decisions.md): append-only log of decisions made during execution, folded in here at each plan revision.
- [`status.md`](status.md): the phase checklist and notes about this machine, rewritten freely.

## 0. Summary

Kuba becomes a single Go binary (cobra + client-go), built in this repo and released as 1.0.0, announced as a port with breaking changes. Daily use stays the same (commands, flags, env vars, shell aliases, and the `--command` contract), apart from the deliberate changes in section 4.

Why, and how we'll know it worked:

- **Speed.** Today each command runs one to five kubectl processes in sequence. Each costs 80-400ms before its first API call, plus a credential fetch per process on clusters with exec auth plugins. Go makes a few direct API calls, usually in one round trip, and fetches credentials once per context. Tests gate request counts (4.4), and you measure wall time on your clusters while dogfooding.
- **Robustness.** No more parsing kubectl's human-readable tables.
- **Distribution.** One prebuilt binary from your Homebrew tap, with no Python or venv.
- **Confidence.** A black-box suite records today's behavior before any Go exists. It then gates the port and outlives it.

The shape: Kuba resolves natively, and kubectl acts.

- Lookups, matching, and selection use the Kubernetes API directly. The final step is still a real kubectl (or stern, lineage, ssh) command that Kuba runs, or prints with `--command`.
- kubectl flags keep their kubectl meaning. The ones that decide which objects you mean (namespace, context, selectors, credentials) steer Kuba's lookup too.

New in 1.0, beyond the port:

- tiered name matching, with `--match` to pick another mode (4.5)
- exit 1 when a query matches nothing, with the closest names suggested (4.5)
- kubectl flags that steer lookups instead of being ignored or clobbered (4.3)
- `--clusters`, to pick clusters for a single command, and `-m`, short for `--multi-cluster` (4.6)
- per-shell kubeconfig isolation, built from your `knew` (4.12)
- a config file and richer `KUBA_CLUSTERS` patterns (4.13)
- viewer formats as one data table: jdd, klock, and your own (4.10)
- ksched filter flags and a resource-fit check (4.14)
- `kuba debug`, `kuba alias`, and `kuba doctor` (4.15, 4.11, 4.16)
- fzf picking and kubecolor's coloring built in, handoffs included, so neither needs installing (4.5, 4.9)

All of it ships in 1.0, ahead of the r/kubernetes ksched announcement, so the announcement can show it off.

The order:

1. a test harness (done)
2. a characterization suite recording today's Python, which stays frozen as the reference
3. a Go vertical slice, after which you start dogfooding
4. command parity
5. shell integration
6. the 1.0 features
7. hardening with a release-candidate soak
8. release prep and cutover

Each phase ends at a checkpoint where I stop for your review. Apart from backup pushes of the work branches, nothing leaves this machine without your sign-off.

## 1. Principles and scope

These settle most small decisions during execution, and the design applies them.

Product and UX:

1. **Kuba is a resolver in front of kubectl.** It works out what you mean, then runs or prints the real command. You should never have to wonder what Kuba did: `--command` prints it, the shell functions put it in history, and `KUBA_DEBUG` shows every API request.
2. **Kuba's own code only reads.** Every API request it makes is a GET. Every change, including kubeconfig context switches, happens in the visible command it hands off to. A transport guard in the kube layer enforces this.
3. **Fast is the feature.** Most lookups take one API round trip, tests gate request counts, and latency is benchmarked at every checkpoint.
4. **Predictable matching.** An exact name beats a contiguous match, which beats a scattered one, across everything the command looks at. `--match` picks another mode. One match acts. Several matches prompt through fzf unless the select mode says otherwise.
5. **kubectl flags mean what they mean in kubectl.** Flags that decide which objects you mean shape Kuba's lookup too. Everything else passes through untouched. Kuba never silently drops a flag or lets one silently override another.
6. **Errors say where Kuba looked and what to try,** for example `no pod matches "appel" in namespace banana (context cherry); 14 pods checked; closest: apple-1. Try -A.`
7. **Degrade, don't block.** A failing cluster produces a warning, tab completion never hangs, and an auth failure doesn't stop a context switch.

Engineering:

1. **Few knobs.** A new flag or env var needs a concrete user story, and defaults should be right without it.
2. **One source of truth per concept.**
   - One flag spec per command drives parsing, help, completion, and what Kuba may emit.
   - kubectl's flags come from tables generated from the pinned kubectl, never typed by hand.
   - One `Action` value drives both printing and running a command, so the two can't drift apart.
3. **Pure core, thin edges.** Flag parsing, matching, the alias grammar, sched, quoting, templates, and rendering are pure functions with table-driven tests. I/O stays in the kube and action layers.
4. **Test the contract, not the incidentals.** Tests assert printed commands, recorded calls, kubeconfig contents, and normalized output, not column padding or colors.
5. **Measure before optimizing.** Build the simple version, and optimize only what a measurement flags.
6. **Boring dependencies over hand-rolled code.** The standard library first, then a well-known library, then our own code. A library earns its place by deleting code we'd otherwise maintain.

Scope:

- **Not in 1.0:** Windows, bash alias completion, a merged watch across several namespaces or clusters, krew, and alias grammar changes.
- **Only if measurements call for it:** a short-lived completion cache, decided during hardening.
- **Deliberately never:**
  - Commands whose purpose is changing cluster objects (delete, apply, edit, scale), since fuzzy matching plus mutation is how you hit the wrong thing. Interactive sessions (`exec`, `debug`) are different: they're handed off as visible kubectl commands and require picking exactly one pod.
  - A full-screen terminal UI (k9s covers that).
  - A background watcher process for completion.
  - Telemetry, update checks, or any network traffic besides the Kubernetes API.

## 2. Guardrails

- **No external side effects beyond backup pushes.** No PRs, tags, GitHub releases, PyPI/Homebrew/krew publishing, or filed issues, and no running Kuba against real clusters. Network use is limited to backup pushes and build and test downloads: Go modules and toolchain, envtest binaries, Python test deps, and goreleaser.
- **No Docker.** It isn't allowed on your work machine, so nothing here needs a container runtime. That rules out kind (and podman or colima): live-pod behavior is covered by `--command`, fakes, and your dogfooding.
- **Branches pushed as backup, never as PRs** (section 8). The repo is public, so pushed branches are visible, just unannounced.
- **Hermetic tests.** Every script gets its own `HOME` and `KUBECONFIG` in a temp dir. The real `~/.kube` is never read or written.
- **Checkpoints.** I stop for review at the end of every phase.
- **Go conventions:**
  - files ordered top-down: public and important first, then what they call
  - table-driven tests where reasonable, asserting with testify
  - metasyntactic names in order (apple, banana, cherry, donut, egg, fig, grape, ...)
  - comments wrapped at ~120 chars, ASCII only, with a one-line tl;dr and a blank `//` line before longer doc comments
  - non-ASCII output symbols (sched's) written as `\u` escapes
  - formatting via `make fmt`; tests and builds run in bulk; no Bazel
- **Code style:**
  - Doc comments open with a topic sentence, then lists (`FZF_PICK modes:`), rather than packing items into the sentence. They explain the idea, not the code.
  - Known shortcuts carry a `HACK:` note saying what breaks and why it's acceptable.
  - Guard clauses and early returns; no silent error swallows (a discarded error says why).
  - One struct field per line; names that say what happens (`exitWithCannedReply`, not `cannedReply`); single-letter method receivers, enforced by lint.
  - Nothing public cites private planning. Code, comments, scripts, commit messages, and the README never mention the plan files or anything in them (section numbers, log or issue IDs); a comment says what it means in plain words. Planning notes may point at code and scripts, never the reverse.
- **Markdown is never hard-wrapped.** Each paragraph or list item is one line, since your editor soft-wraps. This covers these plan files, the README, and any other `.md`.

## 3. Where Kuba is today

- **Code:** `kuba/cli.py` is 3,972 lines of click, with no tests or CI. Deps: click, pyyaml, hcgatewood_coda. It needs Python 3.12+ (PEP 695 generics).
- **Commands:** `get`, `describe`, `logs`, `exec`, `ns`, `ctx`, `cluster`, `api`, `hostname`, `ssh`, `sched`, `shellenv`, and the hidden `ifne`.
- **Flow:** every command resolves, then acts.
  - **Resolve:** one or more `kubectl get` calls, parsing table text, subsequence matching, sometimes fzf.
  - **Act:** build a kubectl/stern/lineage/ssh command, then run it or print it with `--command`.
  - About 30 subprocess call sites in total.
- **Lookups:** per cluster, `kubectl get TYPE QUERY` first, falling back to listing the whole type. Clusters are looked up one after another.
  - An exact name beats subsequence matches, but only within each cluster, because each cluster's lookup is matched separately. Within a cluster, it applies across namespaces.
  - Lookups see only the flags Kuba parses itself: `-n`, `-A`, `-l`, and `-o wide`, which widens the picker's columns. Nodes also get the instance-type column and newest-first sort.
- **Final steps:**
  - **Native printing:** `get` prints matched rows itself only for default, `wide`, and `name` output with no extra kubectl flags, `-L`, or `--sort-by`. Color comes from kubecolor (`--force-colors`) when it's the configured kubectl; plain kubectl gives none.
  - **Listing shortcut:** with no query, no picker, no `--leader`, and a single cluster, Kuba skips the lookup and hands kubectl the bare type plus all your flags. Examples: `kuba get pod --show-labels`, or any format with `--no-select`. Formats like yaml and describe open the picker by default, so they take this shortcut only with `--no-select`.
  - Everything else hands off to kubectl with the resolved names, one command per (cluster, namespace). Kuba drops `-A` there.
  - Your other kubectl flags are appended after Kuba's own. When there's no final command, as with `api` and a query, they reach nothing.
  - `exec` first probes the container with a separate `kubectl exec ... -- which bash`, then hands off `-- bash` or `-- sh`.
- **Printed commands** are space-joined and unquoted. The one exception is the stern pod regex, hand-quoted so shell functions can eval it.
- **Headers:** table headers print only when stdout is a terminal. Otherwise handed-off and printed commands get `--no-headers`, even for `-o yaml`.
- **History functions:** `kns`, `kctx`, `k<cluster>`, `kssh`, the container functions (`kl...`, `ke...`), and the `...f` fx functions eval the output of `kuba ... --command` or `--command-multi` and push it into shell history. When `ns` or `ctx` picks from several candidates, `--command-multi` prints the deterministic `kns NAME` form.
- **Context switches** run `kubectl config use-context` / `set-context`, against the shared kubeconfig.
- **Env vars:** `KUBA_KUBECTL`, `KUBA_CLUSTERS`, `KUBA_SHELLENV_RESOURCES`, `KUBA_SSH_BASTION`, `KUBA_SSH_USE_NAME`, `KUBA_DEBUG`, `KUBA_TRACE`, `KUBA_ESCAPE_ANNOTATIONS`.
  - A boolean is true iff its lowercased value is `true`, `1`, or `yes`.
  - Pattern lists are order-sensitive; for example, the first matching bastion pattern wins.
- **shellenv output:** 12,268 lines (~1 MB): ~9,000 aliases, ~1,600 functions, and ~790 `compdef`s. Python takes ~0.2s to generate it; zsh takes ~0.05s to source it.
- **Latency:**
  - `kuba --help` takes 70-130ms.
  - Each kubectl process takes 80-400ms before any API call.
  - `kuba logs foo` makes 3-5 sequential kubectl calls.
  - With `KUBA_CLUSTERS` set, every command first runs `kubectl config get-contexts`.
  - `sched --pods` fetches every pod in the cluster as full JSON.
- **Daily install:** click 8.3.3, pyyaml 6.0.3, hcgatewood-coda 1.0.1. A fresh install resolves a newer click, whose help, errors, and completion differ, so the test env pins these (5.2).

## 4. Design

### 4.1 Architecture, packages, and libraries

```text
argv
  -> cobra root    dispatch, help, completion protocol
  -> flagspec      parse every command; give each flag one role (4.3)
  -> resolve       kube: per-context clients, static types or discovery, server-side Tables, parallel fan-out, leader filter
                   match: tiers across all candidates, inline label queries
                   select: Select modes, the built-in fzf
                   (skipped entirely for a listing handoff)
  -> act           render natively, or hand off as an Action (a listing, or resolved names),
                   then print it (--command) or run it (exec, or sh -c) (4.7, 4.8)
```

Packages. This is the starting layout; any package that stays thin gets merged.

| Package | Job |
|---|---|
| `cmd/kuba` | `main`; version via ldflags |
| `internal/cli` | cobra commands, help groups, wiring |
| `internal/config` | options (flag, env, config file, default) and where each came from; `KUBA_CLUSTERS` and bastion patterns; `--clusters` selection |
| `internal/flagspec` | per-command specs and roles; parsing; the emit guard; help and completion metadata |
| `internal/kubectlflags` | kubectl's flag tables, generated from the pinned kubectl: each flag's names and whether it takes a value, no roles. Also finds a kubectl command line's verb, which the e2e harness's kubectl recorder uses. |
| `internal/kube` | per-context clients, static types, RESTMapper, Table and object reads, lease holders for `--leader`, fan-out, read-only guard, request log |
| `internal/resolve` | queries, matching, `Select` modes, the fzf picker (built in, or `KUBA_FZF`) |
| `internal/action` | `Action` (command, pipeline, sequence), quoting, print and run, `ifne`, viewer formats, tool registry |
| `internal/render` | tables, colored by kubecolor's printer; label/annotation maps; merged `List`; the fan-out progress line |
| `internal/sched` | scheduling filters and explanations |
| `internal/shellenv` | alias grammar, shellenv generation, alias expansion (completion, `kuba alias`), `isolate` shell code |
| `e2e` | black-box suite: harness, fakes, scripts |

The Go module is `github.com/hcgatewood/kuba` at the repo root. The Python code stays where it is (`kuba/`, `pyproject.toml`), untouched, until it's deleted at cutover with your sign-off.

Libraries, pinned to the newest stable release when adopted:

- cobra v1.10.2, with pflag underneath (4.3)
- client-go, apimachinery, `k8s.io/api`, component-helpers, all v0.37.1:
  - `clientcmd` for kubeconfig loading, overrides, and exec auth plugins, exactly as kubectl does it
  - dynamic and metadata clients, the disk-cached discovery client, and the deferred RESTMapper
  - `k8s.io/api` structs only for the types Kuba decodes (Pod, Node, Namespace, Lease, Event)
  - component-helpers for sched (4.14)
- cli-runtime v0.37.1, only its `printers` package: kubectl's own table, json, and yaml printers, so Kuba's output is kubectl's byte for byte (4.9). It adds 6 packages, where the rest of cli-runtime pulls in kustomize.
- `x/sync/errgroup`, `x/term`, `go.yaml.in/yaml/v3` (config file, for its ordered nodes; it's the maintained fork of the archived `gopkg.in/yaml.v3`)
- `al.essio.dev/pkg/shellescape` v1.6.1, for all quoting (4.8)
- kubecolor v0.8.0: its printer colors Kuba's own tables, and its `Run` is `kuba kubectl`, which handoffs run by default (4.9)
- fzf v0.74.5 (`github.com/junegunn/fzf/src`), the picker built into Kuba (4.5). Upstream calls library use experimental, so a bump means rerunning the picker scripts.
- samber/lo v1.53.0, only where the standard library has no clean equivalent (`Find`, `GroupBy`, `KeyBy`)
- Test-only:
  - go-internal v1.16.0 (testscript), controller-runtime v0.25.2 (envtest), stretchr/testify v1.12.1
  - creack/pty v1.1.24, lithammer/fuzzysearch v1.1.8 (the fzf fake's smart-case matching; the better-known sahilm/fuzzy always ignores case), charmbracelet/x/ansi v0.11.9 (a real ANSI parser for stripping codes), charmbracelet/x/vt v0.1.0 (the terminal emulator behind `tui`; maintained, built on x/ansi's parser, and it answers fzf's queries as a real terminal would)
  - `k8s.io/kubectl`, component-base, and the rest of cli-runtime, all v0.37.1: kubectl's command tree, for the flag tables (4.3), and kubectl's own `ConfigFlags` and resource builder, which tests check Kuba's connection flags and type resolution against
- Not used in the binary:
  - the rest of cli-runtime, e.g. `ConfigFlags`, which pulls in kustomize (74 packages, 577 in all) to provide flags that `clientcmd` binds directly
  - the full typed clientset (`k8s.io/client-go/kubernetes`), to keep the binary smaller and startup fast
  - viper for Kuba's own settings (heavy and global); it comes in only inside kubecolor's printer (4.9)
  - `k8s.io/kubectl` and `k8s.io/kubernetes` (huge, and not meant as libraries)

Kubernetes versions move together: client libraries v0.37.x, envtest 1.37.0 (kube-apiserver, etcd 3.7.0, kubectl), setup-envtest v0.25.2, and the generated flag tables (4.3). Bumping the minor means bumping all four. Go is `go 1.27.0`, the newest language version (kubecolor v0.8.0 needs 1.26.6 and client-go 1.37 needs 1.26.0), with `toolchain go1.27.2`, which the local Go 1.25 downloads automatically.

### 4.2 CLI surface and compatibility

Commands in 1.0, grouped in help:

| Group | Commands |
|---|---|
| Resources | `get`, `describe`, `logs`, `exec`, `debug`, `kubectl` |
| Contexts | `ns`, `ctx`, `cluster`, `isolate` |
| Nodes | `ssh`, `hostname`, `sched` |
| Setup | `shellenv`, `alias`, `config`, `doctor`, `completion`, `version` |

Hidden: `ifne` and `__alias` (plumbing for shell functions), plus cobra's own `__complete`.

Stays the same, except for the deliberate changes described across section 4:

- command names, flags (long and short, same semantics), and positional args
- env var names and boolean parsing
- alias names and grammar
- `--command`, `--command-multi`, and `--loud` behavior, since shell functions eval their output
- `-o name` output (bare names), and headers only on a terminal
- kubectl flags keep their kubectl meaning (4.3)
- `source <(kuba shellenv ...)`
- exit codes: 1 for errors (now including a query that matches nothing), 2 for usage errors, 130 for Ctrl-C (including canceling fzf), otherwise the final command's code
  - A usage error is a command line that can't work whatever the clusters hold: a flag the command doesn't take, a missing argument, flags that conflict (`--guess` with `--all`, two label selectors), or a format the type can't take (`-o pods` on a pod). Today most of those exit 1.
  - An error that depends on what Kuba finds or reads exits 1: a query matching several objects where one is needed (`--sort-by`, `-o events`), a watch spanning namespaces, an unreachable cluster, a missing `-f` file, or a malformed `KUBA_CLUSTERS`, `KUBA_SSH_BASTION`, or `KUBA_SHELLENV_RESOURCES`. Today a missing file and a malformed env var exit 2.
  - A failing final command's code passes through, e.g. ssh's 255, or a remote command's code through `exec`. Today any failing final command exits 1.
  - A signal ends Kuba by that same signal, whether Kuba caught it (Ctrl-C, SIGTERM, SIGHUP) or it killed the command Kuba ran. Shells still report 128+n, so 130 for Ctrl-C, but the difference matters to them: a bash or sh loop over Kuba stops on Ctrl-C only when Kuba died of SIGINT, rather than exiting 130. Signals Go can't re-raise quietly, like SIGPIPE, exit 128+n instead. Today Ctrl-C prints `Aborted!` and exits 1.
  - A command that outlives the signal decides instead, as it would had Kuba exec'd it: `kubectl logs -f`, `get -w`, `wait`, and the like catch Ctrl-C and exit 1, and `port-forward` exits 0, so `kuba kubectl` does too.

Not part of the contract: help text, error wording, table padding and colors, debug output, and completion internals.

Intentional changes ship as clean breaks: no deprecation warnings, no compatibility shims for removed forms, and no transition release. The migration guide, `docs/migration/v1.md` (Phase 7), lists them. Aliases that are simply part of the design, like sched's `--nodename` and `--pods`, stay.

### 4.3 Flags: one spec per command, one role per flag

Every Kuba command parses its argv with `flagspec`. Cobra still dispatches commands and provides `help`, `completion`, and `__complete`, but each command sets `DisableFlagParsing`.

- **Why one parser:** `get`, `describe`, `logs`, `exec`, `debug`, and `api` accept arbitrary kubectl flags, which cobra can't parse: pflag's unknown-flag mode silently drops unknown flags and eats the argument after them. With one parser everywhere, shared groups (`--debug`, `--kubectl`, connection flags) are defined once and work everywhere.
- **Specs:** each command declares one spec, composed from shared groups (select, `--match`, multi-cluster, leader, `--command`, connection, ...). That replaces click's repeated option decorators. A spec lists Kuba's own flags plus every flag of the matching kubectl command.
- **kubectl's flags are generated.** `make gen` builds kubectl's command tree from the `k8s.io/kubectl` version go.mod pins (a test-only dependency, so the binary doesn't link it), and writes a Go table of each wrapped command's flags (`get`, `describe`, `logs`, `exec`, `debug`, `api-resources`) plus the global ones: each flag's names, and whether it takes a value, i.e. its type is bool. A flag that takes an optional value, e.g. `--dry-run`, fails generation, since the table can't express it. `TestTables` regenerates the tables and fails on any diff, so a kubectl bump can't leave them stale.

Every flag has exactly one role. The role alone decides whether the flag shapes Kuba's lookups and how it reaches the final command:

| Role | Examples | Effect on lookups | In the final command |
|---|---|---|---|
| Kuba | `--select`, `--match`, `--leader`, `--guess`, `--multi-cluster` (`-m`), `--clusters`, `--command` | Kuba's own options | never |
| Target | `-n`, `-A`, `--context`; `-c` (logs, exec); `--target` (debug) | where Kuba looks, and what it picks | each target's own context, namespace, and container; `-A` only in a listing handoff |
| Filter | `-l`, `--field-selector`, an inline `key=value` query; `--api-group`, `--namespaced`, `--verbs` (api) | applied server-side (for `api`, to discovery data) | only in a listing handoff |
| Connection | the rest of kubectl's global flags: `--kubeconfig`, `--as`, `--request-timeout`, `--user`, `--token`, ... | applied to every request | verbatim |
| Output | `-o`, `-L`, `--sort-by`, `--no-headers`, `-w`; `-f`, `--since` (logs); flags Kuba sets itself, like exec's `-it` and debug's `--image` | none; they choose the final step and defaults | once, in canonical form, where the final command needs it |
| Passthrough | every other kubectl flag | none | verbatim, in order |

Why it's shaped this way:

- **Flags that decide which objects you mean shape the lookup.** Otherwise the lookup and the final command disagree. Today, lookups ignore `--context`, and the picker ignores `--field-selector`.
- **Names already encode targets and filters.** Once Kuba has resolved names, it emits each target's own context, namespace, and container, and drops `-A` and selectors. Context and namespace are emitted only where kubectl wouldn't already land there: a namespace after `-n`, `-A`, or a fan-out, and a context after `--context` or a fan-out. So `kuba get pod bnana -o yaml --command` prints `kubectl get pod banana-1 --output=yaml`, which runs against your current context and namespace, as the lookup did. kubectl rejects names combined with either (`name cannot be provided when a selector is specified`). A listing handoff has no names, so it forwards them, and kubectl does the filtering (4.7).
- **Kuba never emits a flag twice.** Action builders emit kubectl flags only through the command's spec, which rejects any flag that's undeclared or passthrough. A unit test drives every builder. So when you pass a flag Kuba also emits, your value feeds Kuba's decision: `-c` picks the container, `--context` the starting cluster, `--since` replaces the default. Today yours is appended after Kuba's and silently overrides it.
- **Nothing is silently dropped.** When Kuba renders output itself and can't honor a passthrough flag, it hands off to kubectl. If no kubectl command can produce that output, it errors and names the flag.

Connection flags:

- They come from the generated `kubectl options` table and bind to `clientcmd`'s overrides, so they're exactly kubectl's set. `--as-user-extra`, which `clientcmd` lacks, is parsed as kubectl parses it. A test checks that each gives the REST config kubectl's `ConfigFlags` gives. Help shows them as one line pointing at `kubectl options`.
- Every command that reads the API accepts them, plus `--context`. That includes `ssh`, `hostname`, and `sched`.
  - `ns --context X` sets X's namespace, chosen from X's namespaces, without switching to X.
  - `ctx` and `cluster` pick the context themselves, so `--context` with them is a usage error.
- Flags that pick one cluster entry, server, or credential (`--cluster`, `--user`, `--server`, `--token`, the certificate flags) are a usage error with `--multi-cluster` or `--clusters`, since they'd override every context's own. The rest apply to every context (4.6).

Parsing:

- pflag does the tokenizing, so every form works: `-o wide`, `-owide`, `-o=wide`, `--output wide`, `--output=wide`, combined short booleans, and `--flag=false`. Its `ParseAll` keeps passthrough flags in their original order.
- Kuba's flags and every flag in the spec can go anywhere among the positionals, so `kuba get pod -n banana apple` works.
- **A flag missing from the spec is a usage error,** e.g. a typo like `--namspace`. Kuba can't tell whether an unknown flag takes a value, so it couldn't read anything after one: `-n kiwi` after it would reach kubectl but not Kuba's lookup. Today every kubectl flag ends parsing that way ("kubectl flags go last"), so in `kuba get pod --show-labels aple`, `aple` reaches kubectl as a name.
- **Version skew** follows from that. A flag your older kubectl lacks reaches it, and kubectl rejects it with its own error. A flag newer than 1.37 is a usage error until the tables are regenerated; `kuba kubectl` runs such a command as is.
- `-h/--help` is handled by flagspec. Parsing returns a `CommandLine` with each flag's role. Usage errors exit 2.

The same spec registers the flags on the cobra command for help text, and drives flag completion. A unit test fails if a Kuba-role flag shares a name with a kubectl flag on the same command.

Flags that exist only for the shell functions (`--command-multi`, `--must-single-command`, `--loud`) are hidden from help but still work.

### 4.4 Kubernetes access

- **Config and auth:** `clientcmd`'s loading rules plus overrides from your connection flags, per context. That gives kubectl's `KUBECONFIG` merging, exec auth plugins, and contexts without a namespace meaning `default`. Credentials are fetched once per context per invocation.
- **No kubeconfig:** with no contexts at all, Kuba says there's no kubeconfig, naming where it looked. Today kubectl falls back to `localhost:8080`, so the error is a refused connection there.
- **Types:**
  - Commands with fixed types (`logs`, `exec`, `debug`, `ns`, `ctx`, `cluster`, `ssh`, `hostname`, `sched`, and leader lookups) use static API types (pods, nodes, namespaces, leases, events) and never touch discovery.
  - `get`, `describe`, and `api` use a RESTMapper with shortcut expansion (`po`, `deploy`, CRDs like `pinapp`), over the discovery cache kubectl shares at `~/.kube/cache`. On a miss, Kuba invalidates discovery and retries once, to pick up new CRDs. Kuba handles `type/name` prefixes itself.
  - Several types at once (`pod,deploy`) and `all` work as in kubectl, with or without a query. Each type is resolved and looked up on its own, `all` expands through discovery's `all` category, and rows and `-o name` carry the `type/name` prefix, as kubectl prints them. Today they work only with a query, and crash without one.
  - No `resource.Builder`: Kuba resolves its types itself, and the builder's visitor machinery buys nothing.
- **Lookups:**
  - Matching reads server-side Tables with `includeObject=Metadata`, so names, namespaces, and labels come from metadata rather than parsed cells.
  - **One round trip.** With one query, one namespace, and no filter, Kuba sends the exact GET and the LIST together. An exact hit wins and cancels the LIST; otherwise the LIST gets matched. With a filter, `-A`, several queries, or fan-out, only the LIST goes out, since a GET can't honor those. Python does the two in sequence, so under tiered matching, where partial queries are the norm, sending them together saves a round trip on most lookups.
  - When Kuba needs a full object (pods for logs, exec, and debug), the exact GET fetches it, so an exact name needs nothing more. After a LIST, Kuba GETs only the chosen pod. It never lists full objects just to match names.
  - Lists are chunked at 500, like kubectl.
  - Filters go to the server as list parameters. If the server doesn't support a field selector for a type, its error comes through as-is, as in kubectl.
  - **No lookup at all** for a listing handoff (4.7), since kubectl does the work.
- **Bulk reads** (sched's all-pods and all-nodes lists) use protobuf encoding and chunking, several times cheaper than JSON on large clusters.
- **Read-only guard:** a transport wrapper rejects every request that isn't a GET. Unit-tested.
- **Server warnings** (deprecated APIs and the like) print once each on stderr, as kubectl does.
- **Rate limits:** none on the client by default, as Kuba's requests are few and servers throttle fairly themselves (API Priority and Fairness). `--qps` / `KUBA_QPS` caps each cluster's requests a second, with a burst of the same unless `--burst` / `KUBA_BURST` sets it.
- **Startup:** clients are created lazily. `version`, `shellenv`, `alias`, help, and flag completion never contact the API. `shellenv` and `alias` read kubeconfig only for cluster aliases.
- **Ctrl-C** cancels in-flight requests, then ends Kuba by SIGINT (4.2).

Request budgets: hard gates, asserted by tests in Go mode. Requests are maxima per cluster, excluding discovery. Round trips are the design's critical path; they're benchmarked, not gated.

| Invocation | Requests (max) | Round trips |
|---|---|---|
| `get pod` (native listing) | 1 per 500-item page | 1 per page |
| `get pod --sort-by=.metadata.name` (listing handoff) | 0 | 0 |
| `get pod apple` (exact) or `get pod app` (fuzzy) | 2 | 1 |
| `get pod app -l tier=web` (filtered) | 1 | 1 |
| `logs apple --command` (exact) | 2 | 1 |
| `logs app --command` (fuzzy) | 3 | 2 (Python: 3-5 kubectl processes) |
| `logs app --leader --command` | 4 | 2 |
| `exec app --command` | 3 | 2 |
| `ns ban`, `cluster x` | 1 | 1 |
| a completion | 1 | 1 |

Discovery: fixed-type commands and listing handoffs make zero discovery requests, even with a cold cache. Every command makes zero with a warm cache.

Local benchmarks, tracked at every checkpoint but not gated, since they depend on the machine:

| Measure | Target |
|---|---|
| `kuba version`, flag completion | ~25ms |
| `kuba shellenv` generation | ~50ms (Python today: ~200ms) |

One exception is a hard gate: tab completion that needs the network gives up after 2s with an empty result, and a test asserts it.

### 4.5 Matching and selection

Today any query whose letters appear in order counts as a match, and plain listing shows every match without asking. So `kp api` also lists `apple-pie-indexer`, big namespaces get noisy, and the fzf picker for yaml or describe opens with more candidates than it needs.

- **Match modes,** set with `--match` (env `KUBA_MATCH`, config `match`):
  - `tiered` (default): an exact name wins. Otherwise, names containing the query as a contiguous substring. Otherwise, names containing the query's letters in order. Kuba uses the first tier with any hits.
  - `exact`: exact names only.
  - `substring`: an exact name, otherwise contiguous matches only.
  - `fuzzy`: today's matching. An exact name, otherwise any in-order match.
- **Example:** with pods `api-server`, `frontend-api-2`, and `apple-pie-indexer`, `kp api` shows the first two under `tiered`, and all three under `fuzzy`.
- **One tier per query, across every candidate.** A query picks its tier once, over everything the command looked at: every cluster, namespace, and type. They're all just groupings, so they follow one rule.
  - **Why not per cluster:** a cluster missing the best match would fall back to worse ones, which brings back noise and can swap in a different object.
  - **Example:** sibling clusters `apple` and `banana` have deployment `donut`, and `cherry` has only `donut-canary`. `kuba get deploy donut -m` shows `donut` from apple and banana, and nothing from cherry. That absence is the signal.
  - **Trade-off:** when sibling names drift (`api` in one cluster, `api-v2` in another), an exact match elsewhere hides `api-v2`. A shorter query with no exact match, or a label selector, brings it back.
  - With several queries, each one picks its own tier, and the results are combined. If each query lands on exactly one object, picker formats use them without asking. Today they open fzf whenever more than one object matches, so `kuba get pod apple-1 banana-1 -o yaml` makes you pick from the two pods you named.
- **Scope:** every query uses the same matcher: resources, pods, containers, namespaces, contexts, and API types. kubectl's own way of naming a container is exact: `-c NAME` on logs and exec, `--target NAME` on debug. It's the same as a CONTAINER query under `--match exact`, and giving both forms is a usage error.
- **Select modes:** `YES`/`UNSET`/`ONE`/`ANY`/`ALL` and their per-format defaults are ported as is.
  - `ANY` (`ssh --any`) takes the newest ready worker node without asking: one whose Ready condition is True, with neither the control-plane nor the master role, and `-l` narrows that set. Today it skips only the master role, reads readiness from whichever condition is listed last, ignoring its status, and `-l` replaces the role filter.
- **Container ranking:** container pickers list the likeliest main container first, and `--guess` takes it without asking. Containers sort by these keys, in order:
  1. The container named by the pod's `kubectl.kubernetes.io/default-container` annotation, which kubectl itself defaults to for logs and exec, and which meshes like Istio set. Today it's ignored.
  2. Regular containers before init containers, native sidecars included. Today init containers rank last only when their name contains `init`, so one named like its pod tops the picker.
  3. Containers whose name appears in the pod's name (`api` in `api-7d9f-x2k`), so a workload's own container wins even when it sounds like a helper (`node-exporter`).
  4. Containers with no helper word (`sidecar`, `proxy`, `exporter`, `agent`, `metrics`, `log`, ..., today's list) as a whole dash-separated part of the name. Today any substring counts, so `catalog` is a helper.
  5. The pod's own container order, kubectl's default when there's no annotation.
  - Today's score, a difflib similarity ratio times per-word penalties, isn't ported. The keys usually agree with it on the top pick, and each one is a rule you can state and test. Helpers can come out in a different order.
  - `--guess` never takes an init container, as today. When exec's guess isn't running, that's an error naming its state (4.8), never a fallback to another container.
- **fzf:** fzf is built into Kuba as a library, so picking needs no install. `KUBA_FZF` names an fzf command to run instead, e.g. a newer one. Either way fzf gets the same flags as today, including `--query`, and reads `FZF_DEFAULT_OPTS`, so your options apply.
  - The built-in one differs from the binary in two ways: it ranks lines as plain text (`--scheme=default`), where the binary guesses file paths whenever stdin is a terminal, and its popups (`--tmux`) are off, since they'd rerun Kuba's own command line as fzf's.
  - Canceling fzf (its exit code 130) makes Kuba exit 130 too, as Ctrl-C does anywhere else. Today it exits 1 with "no X selected", which Go keeps for accepting with nothing picked.
- **No match:** a query that matches nothing is an error (exit 1). Like kubectl with names it can't find, Kuba still runs on what the other queries matched, then exits 1.
  - The error says where Kuba looked, and suggests the names nearest by edit distance, up to three when tied. That catches typos like `appel`, which no in-order match can.
  - Today an unmatched query is dropped silently as long as another query matches, and the error names no candidates.
  - A listing with no query and no results stays exit 0 and prints kubectl's `No resources found in X namespace.` So does a picker with nothing to pick, without opening fzf.

### 4.6 Multi-cluster

- **A cluster, to Kuba, is a kubeconfig context:** a name for a cluster entry (the server's address and CA), a user, and a default namespace. `KUBA_CLUSTERS`, `--clusters`, and the CLUSTER column all use context names, and a fan-out runs once per context. kubectl's `--cluster` flag is the exception: it names a cluster entry (4.3).
  - Sibling groups come only from `KUBA_CLUSTERS` patterns matching context names, never from which server or cluster entry a context points at.
  - Several contexts can point at one server, e.g. with different users or namespaces. A fan-out over two of them searches that server once through each, so an object both can see is listed twice, once per context. Kuba doesn't merge the rows, since each context may see different objects.
- `KUBA_CLUSTERS` is parsed only when `--multi-cluster`, `--clusters`, `ctx --nss`, or shellenv needs it, and contexts are read from kubeconfig directly. That removes today's extra `kubectl config get-contexts` on every command. `--kubeconfig` (or `KUBECONFIG`) decides which contexts exist, so patterns match against that file.
- **Starting cluster:** `--context`, or else the current context.
  - `--multi-cluster` (`-m`) and the alias letter `m` use its sibling group: every context that one `KUBA_CLUSTERS` entry matches along with it. Its namespace applies to them all (`default` if it names none), unless `-n` picks one.
  - A starting cluster in no group is an error (exit 1) whose message names it and says to add a pattern or drop `--multi-cluster`. Searching it alone would look like a complete search, and a missing cluster is the signal people read (4.5). A group of one is fine: it's complete.
  - `--clusters` names its clusters outright, so `--context` or `--multi-cluster` with it is a usage error.
  - Connection flags that pick one cluster entry, server, or credential are a usage error with any fan-out (4.3).
- Lookups run in parallel, at most 8 clusters at a time (`--max-parallel` / `KUBA_MAX_PARALLEL`, 0 for no cap), so exec auth plugins (e.g. browser logins) don't all fire at once.
- Each cluster's lookups time out after 15s (`--cluster-timeout` / `KUBA_CLUSTER_TIMEOUT`, 0 for never), so one unreachable cluster can't stall the rest. The default gives way to `--request-timeout`, which bounds each request instead; a timeout you set still applies.
- **Progress:** on a terminal, if lookups run past 2s (`--progress-delay` / `KUBA_PROGRESS_DELAY`), one faint stderr line spins, names the pending clusters that fit the terminal's width, and counts how many are done and for how long (`waiting on banana, cherry, +3 more (5/10, 4s)`), redrawn in place and cleared before any output. A slow login or an unreachable cluster is then visible instead of a silent hang. Never shown in completion or when stderr isn't a terminal.
  - Kept deliberately simple: one plain line, rewritten in place with `\r` and erased before output. No spinner, counts, or colors, and no flag or env var to tune it.
- **Partial failure:** by default, Kuba warns about each failed cluster on stderr and continues with the rest. It errors only if every cluster fails.
- **`--strict-clusters`** (env `KUBA_STRICT_CLUSTERS`, config `strictClusters`) makes any failure fatal. Kuba then lists the failed clusters, exits 1, and acts on nothing.
- **Picking clusters ad hoc.** `--multi-cluster` only reaches the starting cluster's sibling group, so today there's no way to say "this pinapp across all prod clusters" or "nstag and nprod". And `--clusters`, despite its name, takes the alias pattern map.
  - `KUBA_CLUSTERS` (or the config file) names your clusters. `--clusters` picks among them for one command: comma-separated aliases or globs, matched against aliases first, then context names. Examples: `--clusters 'nprod*'`, `--clusters nstag,nprod`.
  - Kuba fans out over exactly those clusters, with the same parallelism, timeouts, and partial-failure rules as `--multi-cluster`.
  - shellenv drops its own `--clusters`, which today takes the pattern map for that run, so the flag means one thing everywhere. Cluster aliases come only from `KUBA_CLUSTERS` or the config file.

### 4.7 Final steps: native or handed off

Rule: anything `--command` can print stays a real external command. Kuba renders natively only output it fully understands, and only when it understands every flag given (4.3). That output is the set Python already prints itself today, plus metadata maps, the merged `List`, and its own setup commands.

`get` and `describe` end in one of three final steps, checked in this order:

1. **Native:** default, `wide`, or `name` output, with no passthrough flags and no `--sort-by` or `-w`. Kuba looks up the rows and prints them, with target, filter, and connection flags applied server-side, and `-L`, `--show-labels`, and `--show-kind` drawn by kubectl's own table printer (4.9). Python renders natively only when there are no extra kubectl flags at all.
2. **Listing handoff:** no query, a select mode that won't prompt, no `--leader`, and a single cluster.
   - Kuba makes no lookup. It runs one `kubectl get|describe TYPE` with your target, filter, connection, output, and passthrough flags. kubectl does all the filtering, which is why every kubectl flag works for listings.
   - **Single cluster only, as in Python.** Fan-out needs Kuba's parallel lookups, per-cluster timeouts, and partial-failure warnings (4.6). A sequence of bare kubectl listings would hang on an unreachable cluster.
3. **Named handoff:** everything else, i.e. a query, a picker, `--leader`, or several clusters. Kuba resolves names, then runs one command per (cluster, namespace) group. Each gets the resolved names, the group's `--context` and `-n`, and your connection, output, and passthrough flags. No `-A` and no filters: the names already encode them.

Rules for every handoff:

- `-o json`, `-o yaml`, or `-o kyaml` output that would take more than one kubectl command renders natively as one `List` instead (4.9). A passthrough flag there is an error naming the flag, since no single kubectl command produces that output.
- **Watching:** `-w` passes through, so `kuba get pod foo -o json -w` prints the full object on every change, as today. A watch that would take more than one command is an error. Python runs those one after another, so it hangs on the first. A merged multi-target watch is post-1.0.
- `--command` that would print more than one command is an error, as today.
- `--sort-by` with more than one name is an error naming the flag, as today. kubectl sorts only what it lists; given names, it fails with an error that doesn't mention the flag.

Kuba's own formats that need names (`events`, `pods`, `containers`, `labels`, `annotations`) always take the named path. Each viewer format declares which paths it allows (4.10).

| Invocation | Lookup | Final step |
|---|---|---|
| `get`: native listing | Table API | Kuba prints rows |
| `get`, `describe`: listing handoff | none | `kubectl get/describe TYPE` |
| `get`, `describe`: named handoff | Table API | `kubectl get/describe NAMES`, one per (cluster, namespace) |
| `get -o json/yaml/kyaml` that would take more than one command | Table API, then objects | Kuba prints one `List` |
| `get -o labels/annotations` | object metadata | Kuba prints |
| viewer formats: `fx`, `children`, `parents`, `jdd`, `klock`, your own | per the path taken | the row's command (4.10) |
| `logs`, `get -o logs/logs-follow` | pod lookup, then the chosen pod | `kubectl logs` or `kubectl stern` |
| `exec` | pod lookup, then the chosen pod | `kubectl exec` (4.8) |
| `debug` | pod or node lookup | `kubectl debug` (4.15) |
| `ns`, `ctx`, `cluster` | kubeconfig, namespace list | `kubectl config use-context` / `set-context` |
| `isolate` | none | shell code that runs `kubectl config view --flatten --raw` (4.12) |
| `ssh` | node or pod | `ssh [-J bastion]` |
| `api`, `hostname`, `sched` | API | Kuba prints |
| `alias`, `config show`, `doctor` | local (`doctor --network` pings clusters) | Kuba prints |

So Go reimplements no kubectl output flag. It hands off `--sort-by` and the rest, as Python does. `-L`, `--show-labels`, and `--show-kind` render natively only because kubectl's own table printer draws them (4.9).

kubectl's own output formats (`-o jsonpath=...`, `custom-columns=...`, `go-template=...`, and the rest) are handed off the same way. Like every format but the default, `wide`, and `name`, they open the picker by default. `-o name` stays Kuba's bare names. Today `-o` takes only Kuba's fixed list, so these are usage errors.

Context switches stay handoffs to `kubectl config`, as in Python. That keeps kubectl's exact multi-file `KUBECONFIG` behavior and messages (`Switched to context "x".`) for free, and keeps Kuba's own code read-only. The cost is one kubectl process with no API calls (~50-100ms). If dogfooding says that matters, `clientcmd.ModifyConfig` is the fallback.

When the target cluster rejects namespace listing (expired credentials, say), `ns`, `ctx`, and `cluster` still switch, and warn. With no list to check names against, Kuba keeps the namespace (`--ns`, `--nss`) without checking that the new cluster has it, and takes a namespace query as the exact name. A partial query or typo then lands in a namespace that doesn't exist, so the next command fails loudly, rather than leaving you in a real namespace you didn't ask for.

### 4.8 Running commands

One `Action` type covers a command, a pipeline (`a | b`), or a sequence (`a && b`). It has two methods:

- `String()` renders POSIX shell text, quoted by shellescape, a port of Python's `shlex.quote`. Words made only of `[A-Za-z0-9_@%+=:,./-]` stay bare, so Kuba's usual args (`--context=x`, `--field-selector=a=b`, `-it`, `--`) print exactly as Python prints them. Anything else is single-quoted, with an embedded `'` written as `'"'"'`. The stern pod regex comes out exactly as Python's hand-quoting does (`'^(apple|banana)$'`), so it needs no special case.
- `Run()` executes it. A plain command runs via `syscall.Exec`, so kubectl takes over the terminal, signals, and exit code. Pipelines and sequences run as `/bin/sh -c` with exactly the `String()` text.
  - A command whose program is `kuba`, like the default `kuba kubectl`, runs the running binary (`os.Executable`), so a handoff reaches this Kuba whatever else `PATH` holds.

Either way, what runs is exactly what `--command` prints. One e2e check asserts this with the recording kubectl (`kubectl-rec`, 5.3).

- **Several actions** (several namespaces or clusters): sequential child processes, with a blank line between outputs. As with kubectl given several names, a failure doesn't stop the rest: Kuba runs every action, then exits with the first failure's code, or dies by the signal that ended it (4.2). Ctrl-C or SIGTERM stops them all. Today the first failure stops the rest, and Kuba exits 1 whatever its code.
- **`--command`:**
  - prints `String()`
  - is still an error when there's more than one action
  - drops tty-dependent flags that Kuba added (`--no-headers`, stern's `--color=always`), because the printed command runs later in a terminal. Flags you passed stay.
- **`--must-single-command`:** unchanged.
- **`ifne`:** unchanged, with three fixes:
  - Everything after `ifne` reaches the command untouched, apart from a leading `--`. Today `-h` anywhere shows ifne's own help, and any `--` disappears.
  - The command's exit code survives it exiting before reading all of stdin.
  - No command is a usage error.
- **stern's pod query** is always an anchored regex, `'^(banana-1)$'` even for one pod, and so is its container query. stern reads both as unanchored regexes, so today a lone pod's bare name also tails `banana-10`.
- **exec's shell:** with no command given, Kuba hands off `kubectl exec -it POD ... -- sh -c 'command -v bash >/dev/null && exec bash || exec sh'`: bash where the image has it, sh otherwise, in one exec. Python probes first with a separate `kubectl exec ... which bash`. Dropping the probe saves a round trip and an extra exec event in the cluster's audit log, and keeps Kuba's own requests GET-only (1). The cost is a longer line in history than Python's `-- bash`; if that grates during dogfooding, revisit. `kuba exec --help` points at `kuba debug` for images with no shell at all.
- **exec's container picker** offers only containers running now. Today it offers every container, so picking an init container that has exited, or a regular one still waiting on init, only errors.
  - exec never lands in an init container on its own. With no regular container running, it errors as `--guess` does, rather than taking the one running container: a native sidecar (an init container with `restartPolicy: Always`) when the app is down, or the init container a pod is stuck on.
  - The error names the container's state (`CrashLoopBackOff, restarted 5x`). While the pod is initializing, it also names the classic init container running then, the one the pod is waiting on: `init-db is running: kuba exec fig-1 init-db`. Classic init containers run one at a time, so there's only ever one to name. Native sidecars run beside the app for the pod's whole life, so they say nothing about why it's down, and the error leaves them out.
  - Init containers stay reachable by name, and running native sidecars appear in the picker beside a running app, ranked last (4.5).

### 4.9 Output and color

- **Tables:** Kuba fetches server-side Table rows itself, and prints them with kubectl's own table printer (cli-runtime's), so its listings match kubectl's byte for byte.
  - Columns follow kubectl's rule: priority 0 by default, all of them with `-o wide`. Kuba adds the CLUSTER column (first, as today) and NAMESPACE.
  - `-L`, `--show-labels`, and `--show-kind` are the printer's own options, so Kuba honors them natively, including in a table merged across clusters. Several types prefix each name with its kind, as kubectl does.
  - The printer pads columns (3-space gutters), cuts multi-line cells with `...`, and escapes terminal control characters, e.g. ESC as `^[`, so a cell can't restyle or rewrite the terminal. Color comes after padding, so it never skews widths.
  - An exact-bytes unit test pins the renderer on fixed rows, since e2e goldens collapse padding.
  - Merging output from several clusters becomes a data operation, so Python's `rectify`/`justify` go away (and with them its padding of the last column).
  - Headers print only on a terminal, as today, and `--no-headers` is honored. Off a terminal, a handed-off command that prints a table (default, `wide`, `custom-columns`) gets `--no-headers` too, so its rows read like Kuba's own; other formats have no header row, so they don't. Today every handoff off a terminal gets it, even `-o yaml`.
  - APIs that don't serve Tables (some aggregated APIs) fall back to NAME and AGE columns built from metadata, as in kubectl.
- **Nodes:** listings keep today's defaults: an instance-type column, hostname for `-o wide`, newest first.
- **Color:** Kuba colors its own tables with kubecolor's printer, imported as a library, whether or not kubecolor is installed. They look like kubecolor's tables and follow your kubecolor theme. Today Kuba colors them only when kubecolor is the configured kubectl.
  - Color follows kubecolor's own rules and settings:
    - on by default on a terminal, and off elsewhere
    - off with `NO_COLOR` or `KUBECOLOR_FORCE_COLORS=none`
    - on even off a terminal with `FORCE_COLOR`, or `KUBECOLOR_FORCE_COLORS` naming a level
    - themed by `~/.kube/color.yaml` and `KUBECOLOR_*` env vars
  - Settings that don't parse print a warning, and the table plain.
  - Kuba gives the printer the arguments that shape the table, as if kubectl had printed it: `get TYPE`, `-o wide` or `-o name`, and `--no-headers` whenever it printed no header row, since kubecolor would bold a first row it took for one.
  - **Handoffs are colored too.** By default they run `kuba kubectl`, which is kubecolor's `Run` built in, so `-o yaml` and describe come out colored with no kubecolor install. Printed commands read `kuba kubectl get ...`. Today they're colored only when kubecolor is the configured kubectl.
  - `--kubectl` or `KUBA_KUBECTL` picks another command, split into words: `kubecolor` for an installed one, or `kubectl` for no color on handoffs. Python takes it as one name or path, which still works.
  - Kuba has no color settings of its own: `NO_COLOR` or `KUBECOLOR_FORCE_COLORS=none` turns color off everywhere, Kuba's tables and handoffs alike. No palette, and no `--color` flag, which would shadow stern's.
  - **Cost:**
    - kubecolor brings viper and about 19 other modules: 50 more packages and about 1.7 MB (+5%) of stripped binary.
    - kubecolor exports no single call to color text, so Kuba rebuilds the middle of its `Run`: settings, color level, theme cache, then the printer. Each kubecolor upgrade means re-checking that code against `Run`.
    - The color level lives in gookit/color's process-wide state, and kubecolor's printer can panic on input it doesn't expect, so Kuba recovers and prints plain.
    - A `kuba kubectl` handoff starts Kuba a second time, which runs kubectl as a child, as the kubecolor binary does.
- **json/yaml across several namespaces or clusters (the `kaaj` bug).** Today Kuba runs one kubectl per (cluster, namespace) group. kubectl prints a bare object for a group with one name and a `List` for a group with several. Select one pinapp in namespace A and two in namespace B, and you get a bare PinApp followed by a `List`, which isn't valid input for `jq` or `fx`.
  - More than one selected resource always yields exactly one `List` (`apiVersion: v1`, `kind: List`, `items`). That's what kubectl does for several names in one namespace. A single selection stays a bare object.
  - When one kubectl command can produce the output, Kuba still hands off (4.7). When it would take more than one, Kuba GETs the selected objects concurrently and prints them as kubectl does: `managedFields` omitted unless `--show-managed-fields`, each item carrying its `apiVersion` and `kind`, through kubectl's own json, yaml, and kyaml printers.
  - A passthrough flag on that native path (e.g. `--subresource`) is an error naming the flag. Today it reaches each per-group command, which is what produces the mixed output.
  - Upgrade path, only if dogfooding shows a need: run `kubectl get ... -o json` per group with your flags, then merge the results into one `List`. That honors every flag, at the cost of one kubectl process per group and a second code path.
  - Viewer formats that read JSON (like `fx`) get the same single `List`.

### 4.10 Viewer formats

Formats that end in another tool become rows in one table rather than special cases. Each row holds:

- the format name and alias letter
- a command template, built as an `Action` (4.8)
- the final-step paths it allows (4.7): listing, named, or both, plus limits such as "one resource only"
- the input it needs (`-o json`, or a `-o json --watch` stream)
- the tool it requires, which feeds install hints and `kuba doctor`

Built-in rows:

| Format | Command | Paths and notes |
|---|---|---|
| `fx` | `kubectl get ... -o json \| kuba ifne fx` | listing or named; works directly now |
| `children`, `parents` | `kubectl lineage [--dependencies] TYPE NAME` | named, one resource |
| `jdd` | `kubectl get TYPE NAME -o json --watch \| kuba ifne jdd` | named, one resource; a navigable history of its changes |
| `klock` | `kubectl klock TYPE [-l SEL] [-n NS \| -A]` | listing only, so filters like `-l` or `app=x` pass straight through |

klock is a watch, so it takes the listing path even when the select mode would prompt, and it errors with fan-out. A query naming resources is an error that links the upstream klock issue, since klock doesn't support named resources yet.

**Your own formats.** A `formats:` section in the config file adds rows, e.g. for `kubectl tree`, `kubectl neat`, or `dyff`:

```yaml
formats:
  tree:
    command: "{kubectl} tree {type} {name} -n {namespace} --context {context}"
    max: 1
    requires: kubectl-tree
    install: kubectl krew install tree
  neat:
    letter: q
    command: "{kubectl} get {type} {names} -n {namespace} --context {context} -o yaml | kubectl neat"
```

- `kuba get pinapp foo -o tree` uses the row. With a `letter`, shellenv also generates aliases (`kaq`, `kaaq`, ...).
- **Templates:**
  - Variables: `{kubectl}`, `{type}`, `{name}` (requires `max: 1`), `{names}`, `{namespace}`, `{context}`, and `{args}` (your connection and passthrough flags).
  - Kuba quotes every substituted value with shellescape, as in 4.8.
  - The template is a shell string, so it runs via `sh -c`, and `--command` prints exactly what runs.
- **Runs:** your formats take the named path. Kuba builds one command per (cluster, namespace) group, as it does for built-in formats. `max` caps how many resources a run can select, and the default select mode prompts with fzf when several match.
- **Names:** a user row with a built-in viewer's name replaces it, e.g. pointing `fx` at `jless`. A name matching a kubectl output format (`json`, `yaml`, `wide`, `jsonpath=...`, ...) or a Kuba core format is a config error.
- **Alias letters** are scarce: a suffix can't reuse any built-in suffix or modifier letter (`a k m x z p`). So `letter` is optional, and shellenv rejects a collision with a clear error. Every format works through `-o NAME` either way. The letters for jdd and klock are picked during implementation, under the same uniqueness test (e.g. `k` is taken, since `kpk` means "pods in kube-system").

### 4.11 Shell integration

- **Generator:** moves to Go. `internal/shellenv` owns the alias grammar and can enumerate every alias name with its expansion.
- **Uniqueness:** unit tests check that every generated name is unique. That includes the fixed functions (`kns`, `kctx`, `knew`, `knewr`, ...), the cluster functions, and user format letters. A cluster alias named `new` makes shellenv exit 1 with a clear error instead of silently shadowing `knew`, or being shadowed, as today's `ns` would be by `kns`.
- **Output:** keeps today's shape, with fully expanded bodies, so `which kpamzf` stays informative. Go makes generation nearly free, and sourcing stays at ~50ms. shellenv exports `KUBA_SHELLENV_VERSION`, so `doctor` can spot a stale shell after an upgrade.
- **Completion:**
  - cobra's zsh completion replaces click's.
  - Plain aliases already complete through zsh's alias expansion.
  - The ~790 per-function `compdef`s collapse into one `_kuba_alias` function plus one `compdef` line. It calls the hidden `kuba __alias NAME WORDS...`, which expands the function name and completes the result.
  - Completion follows the flag roles (4.3): target, filter, and connection flags already on the line shape its lookup. So `kuba logs --context x <TAB>` completes pods from `x`.
- **`kuba alias NAME`** explains an alias, using the same expansion code:

  ```text
  $ kuba alias kpamzf
  kpamzf: kuba get --select --all-namespaces --multi-cluster --output=json pod | kuba ifne fx
    p  pods
    a  all namespaces
    m  sibling clusters
    z  pick with fzf
    f  open in fx
  ```

- **Shells:** zsh in full, plus today's POSIX fallback, made POSIX for real: today's uses bash's `function` keyword and `[[`, which dash rejects, and leaves `--ssh-bastion` unquoted. Completion for the `kuba` command itself in bash and fish comes free from cobra (`kuba completion bash|fish`). Alias completion for bash is deferred past 1.0.

### 4.12 Per-shell kubeconfig isolation

Today `kns`, `kctx`, and `k<cluster>` rewrite the shared kubeconfig, so switching to prod in one terminal silently switches every other terminal. Your `knew` script already solves this by copying the kubeconfig into a temp file and pointing `KUBECONFIG` at it. Kuba builds that in and fixes the script's gaps.

- **Commands.** A child process can't change its parent shell's environment, so `kuba isolate` prints shell code to eval, like `kuba shellenv` does. shellenv defines:
  - `knew`: `eval "$(kuba isolate)"`. Snapshots whatever this shell currently sees into a private kubeconfig and points `KUBECONFIG` at it.
  - `knewr`: `eval "$(kuba isolate --reset)"`. Restores the previous `KUBECONFIG` exactly (or unsets it, if it was unset) and deletes the private file.
- **The snapshot** is `kubectl config view --flatten --raw`, written to a `mktemp` file (mode 0600) in `$TMPDIR`. Kuba's own code still writes nothing; the shell runs the visible kubectl command. Compared with the script, this:
  - merges every file when `KUBECONFIG` lists several (the script's `cat "$src"` fails on `a:b`)
  - keeps certificate paths working, since `--flatten` inlines files that relative paths would otherwise break
  - always uses plain `kubectl`, never the configured kubecolor, so no ANSI codes end up in the file
  - saves the previous value in `KUBA_KUBECONFIG_ORIG`, so `knewr` restores it exactly
  - removes the file when a zsh shell exits (a `zshexit` hook). In POSIX shells, the file stays in `$TMPDIR` until the OS clears it.
- **Prompt:** `KUBA_ISOLATED` is set to the private file's path. Your p10k segment can switch from `HCG_P9K_KNEW` to it, which is a one-line change in your dotfiles.
- **Everything else just works:** `kns`, `kctx`, `k<cluster>`, plain kubectl, and kubens all write to the private file.
- **Snapshot semantics,** same as your script: contexts or credentials added to the main kubeconfig later don't appear until you run `knewr && knew`. Exec-plugin token caches (EKS, GKE, kubelogin) live outside kubeconfig, so auth keeps working.
- **Auto mode (opt-in):** `kuba shellenv --isolate` (config `isolate: true`) runs `knew` when the shell starts, so every terminal gets its own context and namespace. A nested shell snapshots its parent's current view. The default stays shared.

### 4.13 Config file and cluster patterns

- **Location:** `${XDG_CONFIG_HOME:-~/.config}/kuba/config.yaml`, overridable with `KUBA_CONFIG`.
- **Precedence:** flag, then env var, then config file, then default. Every env var keeps working. Phase 2 builds the option loader with this precedence (env vars only at first), so the file plugs in later without touching commands.
- **Format:** structured YAML. Order-sensitive maps (clusters, bastions) are decoded in file order, matching today's comma-list order:

  ```yaml
  kubectl: kubecolor
  match: tiered
  isolate: true
  resources:
    a: pinapp
    t: statefulpinapp
  clusters:
    nprod: cr-m001system-prod-pcp004-use1
    nprod*: cr-m001c00*-prod-pcp00%-use1
  ssh:
    bastions:
      "*m001*|*staging*|*prod*": use1-cmp-sre-bastion-prod-0acc0141
      "*": devapp
    useName: true
  debug:
    image: busybox
  formats: {}  # your own viewer formats (4.10)
  ```

- **`kuba config show`** prints the effective settings and where each one came from.
- **`KUBA_CLUSTERS` v2 syntax:**
  - In the context pattern (the value), `*` matches and captures, while a second wildcard matches without capturing, so it isn't part of the alias name. That wildcard is a character rare in context names, picked when this is built. It isn't `@`, which kubeadm's default context `kubernetes-admin@kubernetes` uses. `%` stands in for it below.
  - In the alias (the key), a single `*` receives all captures concatenated (today's behavior). N `*`s receive the N captures in order. Any other count is an error.
  - Everything but the wildcards matches itself, since a context name can hold any character, backslash included. A pattern matches the whole name.
  - A backslash before punctuation makes it literal, as in Go's regexp. So `\*`, `\%`, `\,`, `\=`, and `\\` let a pattern spell out any name, and regex escapes like `\.` in today's patterns keep working. A backslash before a letter or digit is an error, which catches regex classes like `\d` and a backslash someone forgot to double.
  - Example: `nprod*=cr-m001c00*-prod-pcp00%-use1` maps `cr-m001c001-prod-pcp005-use1` to `nprod1` and `cr-m001c002-prod-pcp006-use1` to `nprod2`, both in one sibling group.
  - Matching stays greedy. A golden test runs your current `KUBA_CLUSTERS` against synthetic contexts to prove today's patterns produce the same aliases.
  - The same syntax applies in the config file.
- **Bastion patterns** (`KUBA_SSH_BASTION`, `--bastion`, `ssh.bastions`) follow the same rules as the context patterns above, plus `|` between alternatives. The first pattern matching the name of the context ssh looks in (`--context`, or the current one) wins. Today they're regexes matched anywhere in the name, so `prod` alone matches `banana-prod`; patterns with `*` at both ends, like the example above, match the same contexts either way.

### 4.14 sched

- Same output format and symbols.
- Pass/fail checks use upstream helpers, so edge cases match the real scheduler:
  - component-helpers' `nodeaffinity` for nodeSelector and required node affinity
  - `Toleration.ToleratesTaint` for taints
  - `metav1.LabelSelectorAsSelector` for label selectors
- Kuba's pod affinity/anti-affinity logic and explanation rendering are ported as is, with two fixes:
  - A missing label value prints as `<none>`, and a taint with no value as `key:effect`, as kubectl prints them. Today both print Python's `None`.
  - After a `∅` (a topology domain with no candidate pods), a term's fields join with `∧`. Today its labelSelector and namespaces join with `∨`, as if negated.
- Elsewhere, edge cases follow the upstream matchers even where today's differ: an empty nodeSelectorTerm matches nothing, `Gt` or `Lt` on a missing or non-numeric label doesn't match (today the command fails), a keyless `Exists` toleration tolerates every taint, and a pod affinity term with no labelSelector matches no pods (today every pod).
- A pod file (`-f`) without a namespace takes the one from `-n` or the context, as kubectl does. Today it's an error.
- **Filter flags:** the evaluator becomes a list of named filters, each with a `--<filter>/--no-<filter>` flag. Defaults match today: `node-name` off; `node-selector`, `node-affinity`, and `taints` on; `pod-affinity` and `pod-anti-affinity` off. `--nodename` and `--pods` stay as aliases.
- **`resources` filter (default off):** compares the pod's requests against each node's allocatable minus the requests of non-terminated pods already on it.
  - It accounts for the init-container max rule, pod overhead, pod count, and extended resources like `nvidia.com/gpu`, using component-helpers' request helpers.
  - Failures read like `cpu 3.6/4, need 1`.

### 4.15 `kuba debug`

Minimal "distroless" images have no shell, so `kuba exec` fails on them more and more often. `kubectl debug` solves this with an ephemeral container that shares the target container's process namespace. Kuba adds what it's good at: finding the pod and guessing the container.

- **Pods:** `kuba debug [POD] [CONTAINER]` resolves the pod and container exactly like `exec`: fuzzy queries, `--guess`, `-n`/`-A`/`-l`, `--leader`, `--multi-cluster`, and `--clusters`. It requires picking exactly one pod, then hands off to `kubectl debug -it POD -n NS --context CTX --image=IMAGE --target=CONTAINER`.
- **Nodes:** `kuba debug --node [NODE]` resolves a node the way `ssh` does, including `--any`, and hands off to `kubectl debug node/NAME -it --image=IMAGE`. It's the in-cluster alternative to `kssh` when SSH isn't available.
- **Image:** `--image` (env `KUBA_DEBUG_IMAGE`, config `debug.image`), defaulting to `busybox`. Clusters that can't pull from Docker Hub set their own mirror once in config.
- **Flags:**
  - Other kubectl debug flags pass through (`--profile`, `--copy-to`, `--share-processes`, ...). Whether Kuba sets a default `--profile` gets decided against the pinned kubectl's behavior and logged.
  - `--target NAME` names the target container exactly (4.5).
  - `--container` keeps kubectl's meaning (the name of the new debug container) and passes through.
- **History:** a shellenv history function puts the resolved `kubectl debug ...` command in history, as `ke` does for exec. Its alias letter is picked during implementation under the uniqueness test (`d` is taken: `kd` means deployments).

### 4.16 Diagnostics

- **Debug logs:** `--debug` / `KUBA_DEBUG` writes structured logs to stderr: the parsed invocation with each flag's role, every API request (verb, URL, status, latency), and the final action. They never include credentials or request bodies. Budget tests count requests from this log. `KUBA_TRACE` adds client-go's verbose logs.
- **Errors** print as `Error: <message>` on stderr (red on a terminal), naming the context and namespace searched.
- **Hints:**
  - A missing optional tool fails with an install hint, e.g. `fx not found; install it with: brew install fx`. The hints come from a tool registry shared with the viewer formats and `doctor`.
  - When `logs` picks a crash-looping container, it adds `container app restarted 12x; try -p for the previous run's logs`. Kuba already has the pod object, so this costs no extra requests.
- **Help:** every command gets two to four real examples, replacing one-liners like "Enhances kubectl get."
- **`kuba doctor`** checks the setup and exits 1 if anything fails:
  - kubectl on `PATH`, with its version, plus the optional tools (fx, stern, lineage, klock, jdd, and any user format's `requires`; fzf and kubecolor only when `KUBA_FZF` or `--kubectl` names them), with versions and install hints
  - another `kuba` earlier on `PATH`, such as a leftover pip install shadowing the new binary
  - what each `KUBA_CLUSTERS` pattern matches, warning about patterns that match no contexts (today they silently produce no aliases)
  - which contexts each bastion pattern routes to
  - whether the config file parses, and any unknown keys
  - whether shellenv is loaded and matches the binary's version
  - whether the shell is isolated, and whether the private file still exists
  - aliases that shadow commands on `PATH`

  `doctor` is offline by default. `--network` adds a 2s reachability check per context.

### 4.17 Distribution

Everything here is prepared locally; publishing needs your sign-off.

- **Version:** release builds pass `-X main.version`. Otherwise `kuba version` reports Go's VCS stamp (`debug.ReadBuildInfo`: a tag or pseudo-version), then `dev`. `make build` sets no version.
- **Homebrew, with prebuilt binaries:**
  - goreleaser builds stripped, `-trimpath` archives for darwin and linux on amd64 and arm64, and generates the formula locally with `skip_upload`, for your review.
  - The formula goes into `hcgatewood/homebrew-tap` only with your sign-off. Otherwise it keeps `jdd.rb`'s style, minus `depends_on "fzf"`, since fzf is built in: caveats list kubectl and the optional tools (fx, stern, lineage, klock, jdd).
  - kubectl isn't a formula dependency, since most people install it some other way (gcloud, Docker Desktop, asdf). A dependency would install a second one.
  - It installs completions for `kuba` itself via `generate_completions_from_executable`.
  - Why not build from source: every user would download a Go toolchain and compile client-go, which takes minutes.
- **PyPI:** no further releases. The 1.0 announcement covers the move to brew, including `pip uninstall kuba`.
- krew comes later. Windows is out of scope.

## 5. Testing

### 5.1 Shape

One black-box suite, written once, runs against either implementation.

- **Python is the frozen reference.** No Python edits during the port. Its recorded behavior, bugs included, is the spec, and every deliberate difference is a paired expectation. There's no further Python release, so Python fixes would never ship. A frozen reference also keeps the goldens honest.
- **Characterization first.** Scripts hold command lines plus expected output.
  - The expected output is generated by running the scripts against Python in update mode, then reviewed by eye and committed. The reviewed goldens are the spec.
  - Known bugs get a comment where they appear, saying what's wrong, e.g. `# Python bug: -l is forwarded alongside resolved names, which kubectl rejects.`
  - This is much cheaper than hand-writing every expectation, and it gives Go an exact target.
- **Then the gate.** The same scripts run against Go, with every difference spelled out as `[impl:go]` lines.
- **Go-only scripts** cover what Python doesn't have: completion, `isolate`, `alias`, `doctor`, `debug`, user formats, and request budgets.
- **Unit tests** cover the pure packages (flagspec, resolve matching, action quoting and templates, render, sched, shellenv grammar, config and cluster patterns), table-driven, asserting with testify (`assert`, or `require` where a test can't go on).

### 5.2 Harness

Built in Phase 0.

- **Framework:** go-internal testscript in `e2e/`, with scripts in `e2e/testdata/script/<area>/*.txtar`. Every script runs as a parallel subtest of `TestScript` named after its directory and file, so `make e2e-python RUN=get` selects an area and `RUN=get/fuzzy` one script.
- **Implementation under test:**
  - `KUBA_IMPL=python` symlinks `.venv/bin/kuba`: the working-tree Python, installed by `make venv` (uv) with deps pinned to your daily install (section 3). Bump the pins only alongside a golden refresh.
  - `KUBA_IMPL=go` builds `./cmd/kuba` into a temp dir per run, never using a stale `bin/kuba`. The build runs while the shared server boots. On macOS it uses the system linker, via the same `BUILD_LDFLAGS` as `make build`: on some Macs (seen with endpoint security software installed), big binaries from Go's own linker launch one at a time at about 60ms each, which cost the Go scripts a third of their time.
  - `make cover` builds it with `-cover` instead, runs the Go scripts, and merges their coverage with the unit tests' into `.cache/cover/cover.html`. Kuba flushes its counters before an exec, which would otherwise lose them.
  - Without `KUBA_IMPL`, `TestScript` skips, so `go test ./...` stays a unit run.
- **Per script:** its own `HOME`, `KUBECONFIG`, and call log under `$WORK`; `LANG=en_US.UTF-8`, `TZ=UTC`, and `KUBECOLOR_PRESET=dark`, so kubecolor's printer never asks the pseudo-terminal for its background color, which nothing answers; `KUBA_FZF=fzf`, so pickers run the fzf fake, and `KUBA_KUBECTL=kubectl`, so Go's handoffs print and run plain kubectl as Python's do (scripts about the built-in picker and `kuba kubectl` unset them); and a hermetic `PATH` of the implementation, the fakes, envtest's kubectl, `/usr/bin`, and `/bin`. Nothing from your `PATH` (real fzf, kubecolor, kubectl plugins) leaks in.
- **API servers:** envtest (kube-apiserver + etcd) 1.37.0, fetched by setup-envtest into `.cache/envtest`, with kubectl from the same bundle. A server starts in about 3s.
  - One shared server starts booting as the harness starts, alongside the Go build.
  - `mkcluster` boots its servers when it runs, in parallel.
  - No server is reused, since envtest can't delete namespaces.
- **envtest realities:** no controllers, scheduler, or kubelet.
  - ServiceAccount admission is already disabled, so pods need no default ServiceAccount.
  - The harness also disables TaintNodesByCondition admission, since only a controller would lift the not-ready taint it puts on every new node.
  - Pods keep whatever status `kapply` writes through the status subresource.
  - Namespaces never finish deleting, so state can't be cleaned up by deleting namespaces.
  - logs, exec, debug, and port-forward can't work, so those commands are asserted via `--command` and fakes.
- **Isolation:**
  - Every script gets a namespace named after its path (`get/fuzzy` becomes `get-fuzzy`), exported as `$NS`. Its `apple` context points at the shared server and defaults to `$NS`, which keeps scripts' namespaced objects apart.
  - Scripts that see cluster-wide state (anything with `-A`, nodes, namespaces, CRDs, sched, `ns`/`ctx`/`cluster`, `hostname`, or `ssh`) start with `mkcluster apple`, which moves `apple` to a fresh server of its own. On a shared server, their output would depend on whichever other scripts happen to be running.
  - Multi-cluster scripts add sibling contexts with `mkcluster banana cherry`.
  - Every context defaults to `$NS`, whichever server it's on, so namespaces line up across clusters as they do for Python's `--multi-cluster`. A script testing "context has no namespace" clears it itself.
- **Cleanup:** envtest puts each server in its own process group, which Ctrl-C doesn't reach, so the harness stops every server on SIGINT or SIGTERM, including ones mid-start. SIGKILL or a `go test -timeout` panic still orphans them; clean up with `pkill -f "$PWD/.cache/envtest"`.

### 5.3 Fakes and script commands

The fakes are the test binary itself: `testscript.Main` installs it under each fake's name, first on `PATH`. Every fake appends its call (name, argv, stdin) as one JSON line to `$E2E_CALLS`. A fake that can't record, or gets bad input, fails loudly with `NAME (fake): ...` and exit 2, so `! calls` can't pass by mistake.

- **`fzf`:** picks per `FZF_PICK`: `first` (the default), `all`, `index:N`, `match:REGEX`, or `cancel` (exit 130). Honors `--query` (a smart-case subsequence match per term, like fzf), `--ansi`, and `--multi`. Nothing to pick exits 1.
- **The recorder,** installed as `fx`, `jdd`, `kubectl-klock`, `kubectl-lineage`, `kubectl-stern`, and `ssh`: prints `$E2E_OUT_<NAME>` and exits `$E2E_EXIT_<NAME>` (default 0), with `NAME` upper-cased and dashes as underscores.
- **`kubecolor`,** used via `KUBA_KUBECTL`: records argv, drops the kubecolor flags Kuba passes (`--plain`, `--force-colors`), then execs the real kubectl. It never colors, so output reads as plain kubectl's. Python's sched works only through it.
- **`kubectl-rec`,** used via `KUBA_KUBECTL`: records argv, then execs the real kubectl. Verbs that need a kubelet (`attach`, `cp`, `debug`, `exec`, `logs`, `port-forward`) get the canned reply instead (`E2E_OUT_KUBECTL`). It finds the verb with `kubectlflags.Verb`, which skips flags, and the values of global flags that take one, per the generated table.
- **`e2e-pty`:** the pseudo-terminal runner behind `ptyexec` (50 rows, 200 columns).

Script commands:

- **`[!] kuba ARGS`:** runs the implementation under test and normalizes stdout and stderr, so goldens hold across runs and implementations:
  - ANSI codes stripped; `$WORK` for the script's work dir; `<apple>` for a server's address, named after the first context on it
  - `<time>` for RFC 3339 and RFC 1123 times, `<uid>` for UIDs, `<rv>` for resourceVersions
  - `<age>` for table cells that are ages; padding (2+ spaces) collapsed to two; trailing whitespace and CRLF removed
  - Blind spot: a lone CPU quantity like `100m` reads as an age, so fixtures use whole CPUs.

  `exec kuba` stays available for raw output.
- **`[!] ptyexec CMD`:** runs on a pseudo-terminal, for terminal-only behavior such as headers and color. Output is normalized with colors written as markup (`{bold}NAME{/}`, `{fg:208}`, `{on-blue}`), and stderr merges into stdout.
- **`tui start CMD` / `wait REGEX` / `type TEXT` / `key KEY...` / `screen` / `[!] exit`:** drives a terminal UI such as the built-in picker (Go only). CMD runs on a 100x16 pseudo-terminal whose screen charmbracelet/x/vt keeps; keys go by fzf's names (`enter`, `btab`, `ctrl-c`); `screen` prints the normalized text once it holds still, and `exit` waits for CMD and prints the screen it left.
- **`shellenv ARGS` / `[!] shell CMDLINE` / `[!] history [REGEX]` / `[!] defines NAME...`:** generate shellenv's output into `$WORK`, then run a command line in a bare shell that sourced it, normalized like `kuba`; check the newest history entry a zsh run left; or check which names it defines as aliases or functions. As for a user, `SHELL` (zsh, bash, or dash) decides both what Kuba writes and which shell sources it.
- **`exitcode N`:** asserts the last `kuba`, `ptyexec`, `shell`, or `tui exit` exit code, since testscript's `!` only checks non-zero.
- **`rows ROW...`:** asserts stdout's rows, in order, by each row's first column: object names mostly, but also hostnames, API resources, or shellenv's letters (`d=deployment`). A ROW with slashes spans that many more columns (`kiwi/apple-web` for `-A`, `banana/apple-web` across clusters), and every ROW must have the same number. Blank and header lines are skipped, and a mismatch prints the line that would pass. For no rows, use `! stdout .`.
- **`current CONTEXT [NAMESPACE]`:** asserts the current context and its namespace, merging every file in `KUBECONFIG`.
- **`kapply [-c CONTEXT] [--stagger] FILE...`:** creates objects, status included. It waits for CRDs to be established, puts namespaced objects without one in `$NS`, and defaults an Event's involvedObject namespace to its own, since envtest rejects a mismatch. `--stagger` creates each file in a later second than the one before, so creation times sort in argument order; the API server sets them itself, to the second.
- **`mkcluster NAME...`:** gives each named context a fresh server of its own, with `$NS`. Naming `apple` moves it off the shared server, so that comes first.
- **`mkcontext [-c CONTEXT] NAME...`:** adds contexts on CONTEXT's server (default `apple`), each with cluster and user entries of its own name, and `$NS`. No server starts, so scripts about which contexts Kuba reaches (`KUBA_CLUSTERS` groups) stay fast, and a fan-out lists each object once per context.
- **`[!] calls [NAME...]`:** prints recorded calls, oldest first, each command line quoted by shellescape (the same style as Kuba's printed commands), then any stdin with each line prefixed `| `. With `!`, fails if any named fake was called.
- **`reqs [--discovery] N`:** asserts that during the last `kuba`, each context sent at most N API requests, discovery excluded, or with `--discovery`, discovery alone. It counts the request lines in Kuba's debug log, so the run needs `KUBA_DEBUG=1`, and a run with no debug log fails rather than pass as zero. Maxima, not exact counts, since a GET-or-LIST race's canceled half may or may not have been sent.
- **Conditions:** `[impl:python]` and `[impl:go]`.

Gotcha: testscript's `stdout -count=N` counts regex matches, not lines; use `'^.+$'` to count lines.

### 5.4 Conventions

- **Pairs** are conditional lines under a comment saying what changed, e.g. `[impl:go] cmp stdout want.go.txt` under `# Go prints one List, however many namespaces the selection spans.`
  - A script that diverges wholesale gets a sibling `foo.go.txtar`, with a skip guard in each.
  - A Go expectation not yet written is `[impl:go] skip 'TODO(go): ...'`.
  - **Go lines start light.** Until a feature lands, its Go lines pin intent rather than detail: which objects come back, success or failure with the exit code, and a key substring. Goldens and exact forms wait until there's Go output to review.
  - **They're deepened when the feature lands:** regenerate with `UPDATE=1` against Go, review, and strengthen the Go lines until each pair's Go side asserts as much as its Python side. Where the Python lines use `cmp` or `rows`, the Go lines do too, and the feature's `TODO(go)` skips are gone. That's checked in review, pair by pair, since deleting the Python lines at cutover would otherwise leave a thin suite.
- **What to assert:** which objects come back via `rows`, inline, so a pair reads side by side; actions via `--command` output and recorded calls; formatting via normalized goldens, only where it's the point (columns, `-o` formats, terminal output, merged `List`s); errors via substrings and `exitcode`, since wording isn't part of the contract but exit codes are.
- **Names:** test data uses the metasyntactic names (apple, banana, cherry, donut, ...), taken in order within each script and skipping any it already uses, so a deployment beside contexts `apple`, `banana`, and `cherry` is `donut`. Namespaces (`kiwi`, `lemon`) and nodes (`fig`, `grape`, `honey`) keep their own names everywhere, so a row like `apple/kiwi/donut` reads at a glance. Where a script is about attributes, names spell them out (`apple-web`, `apple-pending`, `apple-db-leader`). Otherwise pods are numbered, like StatefulSet pods (`apple-1`, `apple-2`, `banana-1`). A lease holder is its pod's name plus `_1` (`apple-db-leader_1`), since holders match by prefix.
- **Iterating:** `make e2e-python RUN=get/fuzzy`, then `UPDATE=1` to regenerate goldens, then review the diff by eye. `rows` lines are hand-written; a mismatch prints the line to paste.
- **After cutover:** the Python mode and every `[impl:python]` line get deleted, and the suite stays. By then every pair's Go side is as strong as its Python side.

### 5.5 Budgets and gates

- Key scripts assert request counts with `reqs`, per the table in 4.4. These are hard gates.
- A Go benchmark tracks startup and shellenv generation. It's reported at checkpoints, not gated.
- `hack/compare.sh` runs a fixed command list (all `--command` or read-only) against both implementations and prints a wall-time table. You run it against your clusters; I don't.
- **`make check`** is the fast gate before every commit: golangci-lint (with gofmt and vet), the import-order check, and unit tests. e2e scripts run in bulk for each change, and the full suite runs at every checkpoint.

### 5.6 Coverage checklist (Phase 1, plus Go-only scripts as features land)

- **get**
  - Type forms: singular, plural, short name, CRD (including one created after discovery was cached), `type/name`, case; several types at once, and `all`.
  - Queries: none, exact, fuzzy, several, and an exact name beating fuzzy matches.
  - Match tiers and every `--match` mode, using a namespace shaped like the 4.5 example.
  - Tiers across clusters: the `donut`/`donut-canary` shape from 4.5 with `--multi-cluster`, and the same shape across namespaces with `-A`.
  - No match: one query and several queries, with exit codes and the closest names.
  - Inline label queries and their conflict with `-l`; `-n`, `-A`, `-l`.
  - Every `-o` value, with its per-type and per-count restrictions, and kubectl's own formats.
  - Select flags and the default select mode per format.
  - `--leader`, including a lease with no holder.
  - `--multi-cluster`, including a current context with no namespace, `-A`, the picker, `-o yaml`, and an unreachable sibling (with and without `--strict-clusters`); `--clusters`: today's map form on Python, the selector form on Go; the progress line under `ptyexec` (Go only).
  - `KUBA_CLUSTERS` groups: whole-name matching, `*` matching nothing, separate entries, a context in no group (an error) or in several, characters that look like regex, backslash escapes, a malformed value, siblings with namespaces of their own, and contexts sharing a server (siblings only by pattern, one row per context).
  - `-A` with `-n`, with `-l`, with the picker, and on a cluster-scoped type.
  - A listing longer than one page (500 objects).
  - `--command`, `--must-single-command`.
  - `-L`, `--show-labels`, and `--show-kind` (native, including merged across clusters) and `--sort-by` (handed off); passthrough flags, including value flags; flags before and after positionals; a flag missing from the spec.
  - Node defaults; terminal vs. non-terminal output (headers, including the NAMESPACE, CLUSTER, wide, and node columns; `--no-headers`); `KUBA_ESCAPE_ANNOTATIONS`.
  - `-w`, including `-o json -w`, and across several namespaces.
  - `-o json`/`-o yaml` with 0 selections, 1, several in one namespace, and the `kaaj` shape; `-o kyaml` in the `kaaj` shape.
  - Flag roles and final steps (4.3, 4.7):
    - A listing handoff with passthrough flags (e.g. `--chunk-size`, `--no-select -o yaml`), making zero requests in Go. The same invocation with `--multi-cluster` takes the named path.
    - A native listing with `--context`, `-l`, and `--field-selector`.
    - `-l`, the inline label form, and `--field-selector`, each with a query and with the picker.
    - `--context` alone, and with `--multi-cluster`, where it sets the starting cluster; a `--context` naming no context.
    - Connection flags (`--kubeconfig`, `--as`) applied to the lookup.
    - A set-based selector (`-l 'tier notin (web)'`), run and printed.
    - Errors: `--context` or `--multi-cluster` with `--clusters`; a single-cluster connection flag with fan-out; a passthrough flag on the native `List` path.
- **describe:** resolution, default select mode, passthrough, listing handoff with `--no-select`.
- **logs**
  - Pod and container queries.
  - Container ranking: the default-container annotation, init containers (including one named like its pod), helper names, and pod order; `--all`.
  - Multi-pod and multi-container go to stern.
  - `--since` defaults, `-f`/`--follow`, `--leader`.
  - Errors: containers not ready, pods spanning namespaces or clusters.
  - The crash-loop hint (Go only).
  - `-c NAME`, alone and with a CONTAINER query; `-l` and `--field-selector` on the pod lookup.
  - `--command`.
- **exec:** pod and container queries, `-c NAME`, `--guess`, no regular container running (during init, and beside a native sidecar), with and without `--guess`, the shell choice (Python's probe via a fake `which` reply; Go's `sh -c`), container not running, `--command`, `--loud`, `-it` given explicitly, and `-n`, `--context`, and `--multi-cluster` on the pod lookup.
- **ns / ctx / cluster**
  - `--list`; exact, fuzzy, and fzf selection.
  - `--command-multi` with one vs. several candidates (the `kns NAME` form).
  - `--nss`/`--ns`, and `-` as the namespace.
  - No match.
  - Bad credentials in the target context: the switch still happens, keeping the namespace or taking a namespace query as typed.
  - `--context`: `ns` sets that context's namespace without switching; `ctx` and `cluster` reject it.
  - Kubeconfig contents afterwards, including a multi-file `KUBECONFIG`.
- **api:** list, query, no match, `--select`, `-o name`, `--one` errors, `--command`.
  - A query with `--context`, `--api-group`, `--namespaced`, and `--verbs`.
  - A query with an unsupported flag.
- **hostname:** node name to hostname and back, no match; `--context` (Go only).
- **ssh**
  - Node query, the picker, no match; `--pod`, including a pod with no node; `--use-name` and `KUBA_SSH_USE_NAME`.
  - Address priority.
  - `--any`: the newest ready worker, without asking; the control-plane and master roles; readiness from the Ready condition's status; `-l` narrowing.
  - Bastion patterns from `KUBA_SSH_BASTION` and `--bastion`: order, `*`, `|`, whole-name matching, no match, a malformed value.
  - `--command`, `--loud`; ssh's own exit code; `--context` (Go only), including the bastion it picks.
- **sched**
  - nodeName, nodeSelector, node affinity (every operator, matchFields, an empty term), and `Gt`/`Lt` on a missing or non-numeric label.
  - Taints and tolerations, including PreferNoSchedule, effects, and wildcard tolerations.
  - Pod affinity and anti-affinity, including `namespaces`, `namespaceSelector` (set, empty, and absent), a term with no labelSelector, and the `∅` rendering.
  - Missing label and taint values in explanations.
  - `--pods`, `--nodename`, `--pass`/`--fail`/`--names`.
  - The pod lookup: a query, the picker, `-n`, `-A`, `-l`, no match; plain kubectl and kubecolor.
  - `-f`, including a missing file, invalid YAML, a non-Pod, and a pod without a namespace.
  - Filter flags and the `resources` filter (Go only); `--context` (Go only).
- **shellenv:** scripts source the output and check what its names do, never the generated text, so the output's shape stays free to change.
  - Sourced in zsh: a plain alias, an fx function, a container function, `kssh` with the ssh options, a cluster function, and `kns`, plus what each puts in history.
  - Sourced in bash: the same names without history, and ssh options holding `|`. Sourced in dash (Go only).
  - SHELL detection, `--kubectl` exported as `KUBA_KUBECTL`, and `KUBA_SHELLENV_VERSION` (Go only).
  - Every option and env var: which names each defines, and resource letters added or overridden; `--list`/`--listq` (including an empty query) and `klst`; malformed values; `--clusters`, dropped in Go.
  - Cluster aliases from `KUBA_CLUSTERS`, including `*`, and the v2 patterns (Go only).
  - A collision between a cluster alias and a fixed function.
- **ifne:** empty stdin; stdin, output, and arguments passed through, including `-h` and `--`; exit codes, including a signal and a command that exits before reading all of stdin; a missing command; no command.
- **errors:** no kubeconfig, unreachable cluster, unknown type, fzf cancel, failed-kubectl message, one of several commands failing.
- **Go only:**
  - **completion,** through a `complete` script command added with Phase 4: resource names, types, contexts, namespaces per context, pods, containers, and nodes (for ssh and hostname). `--context`, `-n`, and `-l` already on the line shape the results. Time stays bounded with an unreachable cluster.
  - **isolate:**
    - `knew` creates a 0600 private file and exports `KUBECONFIG`, `KUBA_KUBECONFIG_ORIG`, and `KUBA_ISOLATED`.
    - `kns`, `kctx`, and plain kubectl write only to the private file; the original stays byte-for-byte unchanged.
    - `knewr` restores the previous value, including "unset", and deletes the file.
    - Merges a multi-file `KUBECONFIG`.
    - Keeps relative certificate paths working.
    - No ANSI codes in the snapshot with `KUBA_KUBECTL=kubecolor`.
    - `shellenv --isolate` isolates at startup, and the zsh exit hook removes the file.
  - **viewer formats:** the built-in rows, including klock with a named query and with fan-out; for user rows:
    - template expansion with awkward values
    - `max: 1` with several selections
    - one command per group
    - overriding a built-in
    - rejected names and colliding letters
    - generated aliases
  - **debug:** `--command` output for a guessed target, a CONTAINER query, and `--target NAME`; `--image` from flag, env, and config; `--node` and `--any`; passthrough flags, including `--container`; the several-pods error.
  - **alias and doctor:**
    - `kuba alias` for resource, container, cluster, and fixed functions, plus unknown names.
    - `kuba doctor` with missing tools, a shadowing `kuba` on `PATH`, a pattern matching no contexts, a stale shellenv, and a bad config file.
  - **config:** precedence (flag, env, file, default), `config show` sources, file-order maps.
  - **Self-checks:**
    - the generated kubectl flag tables match the pinned kubectl (4.3)
    - the emit guard: no action builder emits a flag that's undeclared or passthrough (unit test)
    - the read-only guard rejects every non-GET request (unit test)
    - "runs what it prints"

## 6. Phases

Each phase ends at a checkpoint where I stop, summarize what landed, the test status, the decisions logged, and any questions that need you, then wait for your review.

**Dogfooding during the port.** You start using the Go binary right after Phase 2, for what it has: `get` first, then each command as it lands. It hands nothing back to Python, so until shell integration (Phase 4) you run `bin/kuba` directly, beside your Python install rather than in its place.

### Phase 0: Harness spike (done)

Landed on `port/0-harness`, stacked on `port/plan`:

- `go.mod`, the Makefile entrypoint (8), `.golangci.yml`, and a stub `cmd/kuba`.
- The harness, fakes, and script commands of 5.2 and 5.3, with unit tests for the fakes and the normalizer.
- Seven harness self-test scripts and three sample scripts against Python: a namespaced fuzzy `get`, an `-A` `get` on its own server, and a multi-cluster `get`.
- Folded in after the checkpoint: the shell commands, color markup for `ptyexec`, kubectl-rec's verb parsing from the generated kubectl flag tables (which moved down from Phase 2 for it, with `make gen` and their staleness check), and `make cover`. The `tui` command came later, with the Go picker it exists to drive.

Verified: envtest startup (~3s, so no pool); ServiceAccount admission already off; pod status via the status subresource; CRD printer columns in Tables; leases, Events, node labels and taints; update mode with the normalizing `kuba` command; `poetry build` output unchanged by the Go files.

Exit met: `make e2e-python` is green and hermetic. Checkpoint: you review the harness design and the script format.

### Phase 1: Characterization suite against Python

Goal: the checklist's cross-implementation part is covered and green on Python.

- Write scripts area by area, starting with `get`, with goldens generated from Python and reviewed. `get` gets a branch of its own, and every other area shares a second.
- Comment Python bugs where they show up, and write Go lines wherever the new behavior is already clear. They start light (5.4).
- Pin the `kaaj` repro, paired with Go's single `List`.
- Add the shellenv scripts, which source the output rather than golden it.

Exit: everything is green on Python, and the full suite runs in under 5 minutes. Checkpoint: you skim the goldens and the pairs.

### Phase 2: Go vertical slice

Goal: the foundation, proven on `get`.

- cobra root and help groups; flagspec with roles over the generated kubectl flag tables (Phase 0), the emit guard, and unit tests; the option loader (flag, env, default, with the config seam); `KUBA_CLUSTERS` groups, the `--clusters` selector, and `--strict-clusters`, which `get/clusters` needs.
- Kube layer: clientcmd config, static types, RESTMapper, Tables, one-round-trip lookups, chunking, fan-out (its progress line in render), leader filter, read-only guard, request log.
- Resolve: tiered matching and `--match`, closest-name suggestions, `Select`, the built-in fzf picker and `KUBA_FZF`, with the `tui` script command to drive it.
- Render: the table renderer with its exact-bytes unit test, kubecolor's coloring, headers only on a terminal; `kuba kubectl` as the default kubectl.
- Action: shellescape quoting, printing, exec, `sh -c`.
- Errors, exit codes, Ctrl-C, root-level `--debug`/`--kubectl`, `version` (4.17), and the `reqs` script command.
- The slice covers all three final steps on `get`, single- and multi-cluster:
  - native listings, with `--context` and filters
  - a listing handoff
  - a named `-o yaml` handoff with `--command`

  That proves the flag-role model end to end before anything else builds on it.

Exit: the slice's scripts are green on Go, with Go lines as strong as their Python side (5.4), unit tests pass, and `get` meets its request budgets. Checkpoint: code review of the foundation before widening. **Dogfood checkpoint A:** `bin/kuba get` takes over from `kuba get` in daily use, run directly beside your Python install.

### Phase 3: Command parity

Order:

1. The remaining `get -o` formats (with the viewer-format table holding `fx` and lineage), and `describe`.
2. `ns`, `ctx`, `cluster`.
3. `logs` (with the crash-loop hint), `exec`.
4. `api`, `hostname`, `ssh`.
5. `sched`.
6. `ifne`.

For each command: implement it, unit-test the pure logic, get its scripts green on Go, deepen their Go lines (5.4), and write its help examples. You pick each one up as it lands.

Nothing here runs live pods. logs, exec, follow, and lineage are covered by `--command` and fakes in the suite, and by your dogfooding against real clusters.

Exit: every cross-implementation script except shellenv is green on both implementations, with every pair's Go side as strong as its Python side. Checkpoint.

### Phase 4: Shell integration

- The shellenv package and generator, green on the shellenv scripts.
- Completion: cobra, `_kuba_alias`, `__alias`, and the 2s cap.
- `kuba alias NAME`, on the same expansion code.

Exit: the full cross-implementation suite is green on Go. **Dogfood checkpoint B:** `source <(bin/kuba shellenv ...)` becomes your daily setup, and the Python install retires. Checkpoint.

### Phase 5: 1.0 features

Go-only. Each feature lands on its own branch with unit tests and scripts, in order of daily value:

1. Config file and `kuba config show` (4.13).
2. Per-shell isolation: `kuba isolate`, `knew`/`knewr`, and `shellenv --isolate` (4.12).
3. `KUBA_CLUSTERS` v2 patterns (4.13). The `--clusters` selector (4.6) landed in Phase 2.
4. Viewer formats `jdd` and `klock`, and user-defined formats (4.10).
5. `kuba debug` for pods and nodes (4.15).
6. ksched filter flags and the `resources` filter (4.14).
7. `kuba doctor` (4.16). It goes last because it checks everything above.

You pick up each feature in daily use as it lands. Exit: the features are green, and help text is updated. Checkpoint.

### Phase 6: Hardening and soak

Hardening comes after the features, so it covers what actually ships.

- Request budgets asserted across key scripts; benchmarks reviewed.
- The `hack/compare.sh` table from your clusters.
- The completion-cache decision, made from measurements.
- Fixes for whatever dogfooding turned up.
- **Release-candidate soak:** you use an RC build as your daily driver for about a week, config file and isolation included. Only fixes land during the soak.

Exit: budgets met, nothing due before release left in `issues.md`, and a clean soak. Checkpoint.

### Phase 7: Release prep and cutover

Local only; every external step needs your sign-off.

- README rewrite:
  - install via brew (no more pip or venv advice), and a short quickstart: install, add shellenv, run `kuba doctor`
  - one vhs demo gif per feature, fubectl style: recorded by me against envtest where no live pods are needed, otherwise by you
  - a "pairs well with jdd" note
  - a link to the migration guide, up top, for anyone coming from the Python Kuba
  - an expanded "See also"
- A migration guide at `docs/migration/v1.md`, for Python Kuba users moving to 1.0:
  - switching installs: `pip uninstall kuba`, `brew install`, re-source shellenv, then `kuba doctor`
  - every deliberate change (the V entries in `issues.md`) and every fixed bug a user could notice (the B entries), grouped by command, each with a before-and-after command where a form changed, and what to do, if anything
  - exit code changes, and env vars or flags that moved or went away
- A goreleaser snapshot build and the generated formula (4.17).
- A CI workflow file (envtest, uv, zsh; lint tools come from the Makefile's pins), left uncommitted until you sign off, since a pushed branch would run it.
- A draft 1.0 announcement for you to edit and post: a port with breaking changes, linking the migration guide.
- Optional: mascot refresh candidates via image generation, for you to pick from.
- After sign-off: delete the Python code, the harness's Python mode, and the `py-*` make targets, then set the version to 1.0.0.

Exit: you approve, then either do the external steps yourself or tell me to.

## 7. Risks and mitigations

| Risk | Mitigation |
|---|---|
| envtest can't run pods (logs, exec, debug, lineage) | `--command` and fakes; your dogfooding on real clusters |
| Goldens churn on incidental formatting | The normalizing `kuba` command; substring asserts for errors |
| Goldens enshrine bugs | Every golden reviewed in Phase 1; bugs commented where they show up; a Go expectation for every deliberate difference |
| Parsing edge cases | pflag does the tokenizing; table-driven tests; flag tables generated from the pinned kubectl |
| A kubectl flag gets the wrong role, so it's dropped, duplicated, or ignored by lookups | Roles declared once per command; the emit guard; nothing silently dropped; Phase 1 goldens; dogfooding |
| Your kubectl differs from the pinned 1.37 | Older kubectl rejects flags it lacks with its own error; newer flags pass through (4.3) |
| Kuba's table renderer drifts from kubectl's look | kubectl's column rule and padding; an exact-bytes unit test; Phase 1 goldens |
| client-go dependencies slow startup | Startup benchmark at checkpoints; lazy clients; no cli-runtime or typed clientset |
| Auth plugin storms during fan-out | At most 8 clusters at a time, or `--max-parallel`; the progress line shows who's pending |
| Interactive auth prompts collide during fan-out | Rare, since tokens are cached; if dogfooding shows it, fetch credentials one cluster at a time before fanning out |
| An unreachable cluster stalls everything | Per-cluster timeout plus partial-failure warnings |
| Behaviors no test covers | Dogfooding from Phase 2, per-feature dogfooding, the RC soak, `hack/compare.sh` |
| Phase 5 grows large | Ordered by daily value; one branch and review per feature; anything that slips moves to 1.1 with your OK |
| Tiered matching fights muscle memory | `--match fuzzy` (flag, env, or config) restores today's matching |
| An isolated snapshot goes stale | Documented; `knewr && knew` refreshes; `kuba doctor` reports isolation state |
| Private kubeconfig files pile up | Mode 0600 in `$TMPDIR`; the zsh exit hook removes them; the OS clears tmp |
| Few free alias letters for new formats and `debug` | Letters are optional for user formats (`-o NAME` always works); the uniqueness test rejects collisions |
| A leftover pip install shadows the new binary | The announcement says to `pip uninstall kuba`; `kuba doctor` detects it |
| Binary size (~40-60 MB) | Acceptable; stripped release builds; no cli-runtime |
| Interrupted test runs orphan envtest servers | The harness stops them on SIGINT/SIGTERM; `pkill` for the rest (5.2) |

## 8. Process

- **Plan files** live in `plans/port/`. `design.md` is committed. The three companions stay untracked via `.git/info/exclude`, so their frequent updates don't force restacks.
  - `issues.md` is edited in place, following the conventions in its own header. This doc never cites its entries.
  - `decisions.md` is append-only: context, decision, alternatives, consequences, each with the next free number. Its entries get folded into `design.md` at each plan revision, which then cites sections rather than log numbers.
  - `status.md` is rewritten freely. I update it after every phase, bug fix, or feature.
  - After every context compaction I re-read status and issues, checked against `git spice log short` (git wins).
  - A plan revision rewrites `design.md` whole, then updates any section numbers `issues.md` cites.
- **Branches:** a git-spice stack on top of `main`, always invoked as `git spice` (`gs` is your `git status` alias).
  - The bottom branch, `port/plan`, holds only `plans/port/design.md`. Each phase gets one or two branches above it, splitting off only a part hefty enough to review alone, like `get`: `port/0-harness`, then `port/1-get` and `port/1-rest`, then `port/2-foundation` and `port/2-get`.
  - One commit per branch: changes are amended (`git spice commit amend`), never added as a second commit. Amending a lower branch restacks the ones above it.
- **Pushes,** as backup only: `git spice branch submit --no-publish`, or `git spice upstack submit --no-publish` from the lowest changed branch after a restack. Never `--force`, never `main`, and never your `p`/`push` shorthands, which map to plain `branch submit` and would open PRs.
- **Review rounds:** fixes stay uncommitted until you say amend. Then `design.md` changes go onto `port/plan` and the rest onto its phase branch, followed by a restack and push.
- **Before each commit:** `make fmt`, then `make check` and the affected e2e scripts, in bulk. The full suite runs at checkpoints.
- **Make is the single entrypoint** (it's allowlisted):
  - Go: `build`, `fmt`, `lint`, `test`, `check`, `tidy`, `clean`; e2e: `venv`, `e2e-python`, `e2e-go`, `e2e`, taking `RUN=<area or script>`, `UPDATE=1`, and `V=1`; `gen` arrives in Phase 2.
  - Python: `py-build`, `py-install`, `py-uninstall`, `py-publish`, renamed so the Go `build` could take its name, and deleted at cutover.
- **Lint:** `make lint` runs golangci-lint, configured in `.golangci.yml`: the `standard` linters (errcheck, govet, ineffassign, staticcheck, unused), revive with its defaults plus `import-shadowing` and single-letter receivers (`receiver-naming` with `max-length: 1`), and the gofmt formatter, which covers generated files too, so a generator whose output drifts shows up as a diff. Imports are ordered by goimports-reviser: standard library, third-party, then this module. golangci-lint has no goimports-reviser formatter, and its `gci` would only mimic the order. So `make fmt` runs goimports-reviser, then `golangci-lint fmt`; `make lint` also fails on misordered imports; and `make check` is just `lint` and `test`. The Makefile pins both tools' versions and builds them with `go install` on first use (`make tools` does it up front) into `.cache/tools/<go version>/`, so nothing needs installing beyond Go. The Go version is in the path because golangci-lint rejects code newer than the Go it was built with. golangci-lint's docs prefer its release binaries over source builds; a source build at a pinned tag is what keeps it in step with the module's Go, and nothing has broken so far. Writes to testscript's in-memory stdout and stderr discard their errors explicitly (`_, _ =`).
- **Checkpoints:** as described at the top of section 6.
