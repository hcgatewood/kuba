# Kuba: kubectl wizardry

The **magical kubectl companion** with [fzf](https://github.com/junegunn/fzf), [fx](https://github.com/antonmedv/fx), [aliases](https://github.com/ahmetb/kubectl-aliases), new output formats, and more!

<p align="center"><img src="https://raw.githubusercontent.com/hcgatewood/kuba/main/assets/logo_transparent.png" alt="Kuba logo" width="300"/></p>

## Features

<p align="center"><img src="https://raw.githubusercontent.com/hcgatewood/kuba/main/assets/demo.gif" alt="Kuba demo" width="1000"/></p>

Kuba is an **evolution of kubectl** that makes it **more efficient for power users**, especially platform operators and SREs managing multiple clusters, investigating cross-cutting issues, and supporting multiple teams/orgs. It adds a number of key features to kubectl, including:

- ☁️ **Fuzzy arguments** for get, describe, logs, exec
- 🔎 **New output formats** like fx, lineage, events, pod's node, node's pods, and pod's containers
- ✈️ **Cross namespaces and clusters** in one command, no more for-loops
- 🧠 **Guess pod containers** automagically, no more `-c <container-name>`
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

Start by using `kuba get` and `kuba describe` as ~drop-in replacements for `kubectl get` and `kubectl describe`. Then try a few examples from the overview video, or check out the commands below.

### Help pages

```text
$ kuba --help
Usage: kuba [OPTIONS] COMMAND [ARGS]...

  The magical kubectl companion.

Options:
  -h, --help  Show this message and exit.

Commands:
  api       Enhances kubectl api-resources.
  cluster   Combines kubectx + kubens for all-in-one switching between clusters.
  ctx       Enhances kubectx.
  describe  Enhances kubectl describe.
  exec      Enhances kubectl exec.
  get       Enhances kubectl get.
  hostname  Convert between node names and hostnames.
  logs      Enhances kubectl logs.
  ns        Enhances kubens.
  sched     Predict which nodes a pod can be scheduled on.
  shellenv  Generate k-aliases and completions for easy kubectl resource access.
  ssh       SSH into a node.
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

  - kns => kuba ns (suffixes: l=list)
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
      - la(b)els
      - anno(t)ations
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
  --clusters TEXT     List of clusters to consider, formatted as e.g. stag*=k8s-staging-*,prod=k8s-production (use
                      single '*' to match multiple clusters). Can also set via KUBA_CLUSTERS env var.
  --shell [|zsh]      Override shell detection.
  --kubectl TEXT      Name or path of the kubectl binary to use. Can also set via KUBA_KUBECTL env var.
  --no-native         Don't include the default native resource mappings.
  --no-resources      Don't include aliases and completions for resource-level commands.
  --no-containers     Don't include aliases and completions for container-level commands.
  --ssh-bastion TEXT  SSH bastion option to use with kuba ssh. Can also set via KUBA_SSH_BASTION env var.
  --ssh-use-name      Use name option to use with kuba ssh.
  --list              Just list all resource aliases.
  --listq TEXT        Same as --list, but filter for the query.
  --debug             Print debug info to stderr.
  -h, --help          Show this message and exit.
```

## How I use Kuba

I use Kuba every day to manage and debug production Kubernetes clusters.

### PinCompute configs

```shell
source <(kuba shellenv --kubectl kubecolor)
export KUBA_SHELLENV_RESOURCES='a=pinapp,b=pinterestjobset,g=pinteresttrainingjob,h=pinpod,i=pinterestdaemon,t=statefulpinapp,v=pinterestservice,w=pinterestcronjob'
export KUBA_CLUSTERS='ndev=k8s-use1-shared-dev-m001,ndev*=fed-k8s-use1-shared-dev-m001-c00*,nstag=cr-m001system-staging-pcs001-use1,nstag*=cr-m001c00*-staging-pcs001-use1,nprod=cr-m001system-prod-pcp004-use1,nprod*=cr-m001c00*-prod-pcp005-use1,dev=cmp-test,dev*=fed-cmp-test-*,stag=k8s-use1-staging-shared-001,stag*=fed-k8s-use1-staging-shared-001-*,prod=k8s-use1-prod-shared-001,prod*=fed-k8s-use1-prod-shared-001-*,pii=k8s-use1-prod-pii-001'
export KUBA_SSH_BASTION='*m001*|*staging*|*prod*=use1-cmp-sre-bastion-prod-0acc0141,*=devapp'
export KUBA_SSH_USE_NAME=1
```

### Switch to a cluster and poke around

```shell
# Switch to muse namespace in new prod cluster 1
$ knprod1 muse
Switched to context "cr-m001c001-prod-pcp005-use1".
Active namespace is "muse".
Context "cr-m001c001-prod-pcp005-use1" modified.

# What was the shortcut for PinApps again?
$ klst pinapp
a=pinapp
t=statefulpinapp

# Show all PinApps in muse namespace
$ ka
NAME             STATE        REPLICAS   UP-TO-DATE   READY   AGE
pin-polaris-v2   Succeeding   2          2            2       8d
pin-polaris-v3   Succeeding   10         10           10      3d

# Investigate a particular PinApp
$ kaf pin-polaris-v2

# Show all child-resources of the PinApp
$ kac pin-polaris-v2
NAME                                                                     READY   STATUS           AGE
PinApp/pin-polaris-v2                                                    -                        8d
├── PersistentVolumeClaim/manas-gflags-pin-polaris-v2-75454c6987-665qq   -                        8d
│   └── Pod/pin-polaris-v2-75454c6987-665qq                              9/9     Running          8d
│       └── PodDisruptionBudget/pinapp-pin-polaris-v2                    -       SufficientPods   8d
├── PersistentVolumeClaim/manas-gflags-pin-polaris-v2-75454c6987-ets93   -                        8d
│   └── Pod/pin-polaris-v2-75454c6987-ets93                              9/9     Running          8d
│       └── PodDisruptionBudget/pinapp-pin-polaris-v2                    -       SufficientPods   8d
├── Pod/pin-polaris-v2-75454c6987-665qq                                  9/9     Running          8d
├── Pod/pin-polaris-v2-75454c6987-ets93                                  9/9     Running          8d
└── PodDisruptionBudget/pinapp-pin-polaris-v2                            -       SufficientPods   8d

# Investigate a particular child pod
$ kpf pinpolaris665  # fuzzy match
```

## Related

- Kubectl + fzf
    - [kube-fzf](https://github.com/thecasualcoder/kube-fzf)
    - [kubectl-fzf](https://github.com/bonnefoa/kubectl-fzf)
- Kubectl aliases
    - [fubectl](https://github.com/kubermatic/fubectl)
    - [kubectl-aliases](https://github.com/ahmetb/kubectl-aliases)

## See also

- 🐙 [Jdd](https://github.com/hcgatewood/jdd): JSON diff diver, the time machine for your JSON
- ⚡️ [Dotsync](https://github.com/hcgatewood/dotsync): dotfiles everywhere, instantly
- ☁️ [Appa](https://github.com/hcgatewood/appa): Markdown previews with live reload
- 🔮 [PDate](https://github.com/hcgatewood/pdate): human-readable dates and times
- 🔬 [Vis](https://github.com/hcgatewood/vis): visualize fuzzy tabular data
