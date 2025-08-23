# Kuba

The magical kubectl companion with [fzf](https://github.com/junegunn/fzf), [fx](https://github.com/antonmedv/fx), [aliases](https://github.com/ahmetb/kubectl-aliases), and more!

<p align="center"><img src="https://raw.githubusercontent.com/hcgatewood/kuba/main/assets/logo_transparent.png" alt="Kuba logo" width="300"/></p>

## Features

<p align="center"><img src="https://raw.githubusercontent.com/hcgatewood/kuba/main/assets/demo.gif" alt="Kuba demo" width="1000"/></p>

- ☁️ **Fuzzy commands** like get, describe, logs, exec
- 🧠 **Guess pod containers** automagically, no more typing `-c <container-name>`
- ✈️ **Cross namespaces and clusters** in one command, no more for loops
- ⚡ **Cut down on keystrokes** with an extensible alias language, e.g. `kpf` to `kuba get pods -o json | fx`
- 🧪 **Simulate scheduling** without the scheduler, try it with `kuba sched`
- 🔁 **And lots more**!

## Install

Quick-install:

```bash
pip install kuba
```

Kuba makes use of the following tools you'll likely want to install as well: [fzf](https://github.com/junegunn/fzf#installation), [fx](https://fx.wtf/install), [kubecolor](https://kubecolor.github.io/setup/install), and [krew](https://krew.sigs.k8s.io/docs/user-guide/setup/install) for [stern](https://github.com/stern/stern) and [lineage](https://github.com/tohjustin/kube-lineage). On macOS, you can install these tools with [Homebrew](https://brew.sh/):

```bash
brew install fzf fx kubecolor krew && kubectl krew install stern lineage
```

To use the aliases, add this to one of your dotfiles:

```bash
source <(kuba shellenv --kubectl kubecolor)  # omit --kubectl kubecolor if you haven't installed kubecolor
```

## Usage

Start by using `kuba get` and `kuba describe` as a ~drop-in replacements for `kubectl get` and `kubectl describe`. Then try a few examples from the overview video, or check out the commands below.

### Help pages

```text
$ kuba --help
Usage: kuba [OPTIONS] COMMAND [ARGS]...

  The kuba-mazing kubectl companion.

Options:
  -h, --help  Show this message and exit.

Commands:
  api       Shadow of `kubectl api-resources` but with optional fzf for resource selection.
  cluster   Shadow of `kubectx` and `kubens` for all-in-one switching between logical clusters.
  ctx       Shadow of `kubectx` for context switching.
  describe  Shadow of `kubectl describe` but with optional fzf for resource selection.
  exec      Shadow of `kubectl exec` but with optional fzf for pod and container selection.
  get       Shadow of `kubectl get` but with optional fzf for resource selection.
  logs      Shadow of `kubectl logs` but with optional fzf for pod and container selection.
  ns        Shadow of `kubens` for namespace switching.
  sched     Predict which nodes a pod can be scheduled on.
  shellenv  Generate k-aliases and completions for easy kubectl resource access.
  ssh       Shadow of `ssh` for connecting to a node, with optional fzf for node selection.
```

### Alias language

```text
$ kuba shellenv --help
Usage: kuba shellenv [OPTIONS]

  Generate k-aliases and completions for easy kubectl resource access.

  E.g. kp for kubectl get pod.

  Generated aliases all forward to kuba, which eventually forwards to kubectl, including any kubectl-specific
  parameters. Passing kubectl arguments and shell completion both have some rough edges.

  GENERAL

  - kns => kuba ns
  - kctx => kuba ctx (suffixes: l=list, n=ns)
  - kclus => kuba cluster
  - kssh => kuba ssh (suffixes: a=any, p=pods)
  - kapi => kuba api (suffixes: n=name, z=select)
  - ksys => kuba ns kube-system

  RESOURCE-LEVEL

  Default native resource mappings:
  - c=configmap
  - d=deployment
  - j=job
  - m=daemonset
  - n=node
  - o=cronjob
  - p=pod
  - r=secret
  - s=service
  - x=lease

  Alias modifiers, optional and in this order:
  - Search in (a)ll namespaces or (k)ube-system namespace
  - Search across (m)ultiple sibling clusters
  - Restrict to objects e(x)clusively holding a lease
  - Force selection using f(z)f or force (p)icking all
  - Choose an alternative output type:
      - (d)escribe
      - (n)ame
      - (w)ide
      - (y)aml
      - (j)son
      - (f)x
      - (e)vents
      - lo(g)s
      - follow (l)ogs
      - lineage downward i.e. (c)hildren
      - lineage (u)pward
      - p(o)ds (only for nodes, shows pods on the node)
      - n(o)de (only for pods, shows node the pod is on)
      - containe(r)s (only for pods, shows containers in the pod)

  Example alias usage:
  - kp -> kuba get pod
  - kpw -> kuba get pod -o wide
  - kpz  -> kuba get pod (fzf multi-select over all pods)
  - kpzf myapp -> kuba get pod --select -o json | fx (fzf multi-select over all pods fuzzy matching 'myapp')
  - kxd -> kuba describe lease
  - kny -> kuba get node -o yaml
  - kdj -> kuba get deployment -o json
  - ksz -> kuba get service (fzf multi-select over all services)
  - kjf -> kuba get job -o json | fx
  - kcaj -l app=myapp -> kuba get configmap --all-namespaces -o json -l app=myapp

  CONTAINER-LEVEL

  Default kuba aliases:
  - l=logs
  - e=exec

  Alias modifiers, optional and in this order:
  - Search in (a)ll namespaces or (k)ube-system namespace
  - Search across (m)ultiple sibling clusters
  - Restrict to objects e(x)clusively holding a lease (via convenience heuristic)
  - Choose (c)ontainers, or automatically pick a(l)l containers
  - Command-specific
      - logs: (f)ollow logs

  Example alias usage:
  - kl -> kuba logs --guess
  - ke -> kuba exec --guess
  - klx -> kuba logs --leader --guess
  - kexc -> kuba exec --leader
  - klamlf -> kuba logs --all-namespaces --multi-cluster --all --follow

  Usage: source <(kuba shellenv [OPTIONS])

Options:
  --resources TEXT    Add or override resource mappings, formatted as e.g. p=pod,n=node. Can also set via
                      KUBA_SHELLENV_RESOURCES env var.
  --clusters TEXT     List of clusters to consider, formatted as e.g. k1=v1,k2=v2 (use '*' to match multiple
                      clusters). Can also set via KUBA_CLUSTERS env var.
  --shell [|zsh]      Override shell detection.
  --kubectl TEXT      Name or path of the kubectl binary to use. Can also set via KUBA_KUBECTL env var.
  --no-native         Don't include the default native resource mappings.
  --no-resources      Don't include aliases and completions for resource-level commands.
  --no-containers     Don't include aliases and completions for container-level commands.
  --ssh-bastion TEXT  SSH bastion option to use with kuba ssh.
  --ssh-use-name      Use name option to use with kuba ssh.
  --list              Just list all resource aliases.
  --listq TEXT        Same as --list, but filter for the query.
  --debug             Print debug info to stderr.
  -h, --help          Show this message and exit.
```

## Related

- Kubectl + fzf
    - [kube-fzf](https://github.com/thecasualcoder/kube-fzf)
    - [kubectl-fzf](https://github.com/bonnefoa/kubectl-fzf)
- Kubectl aliases
    - [fubectl](https://github.com/kubermatic/fubectl)
    - [kubectl-aliases](https://github.com/ahmetb/kubectl-aliases)
