# Kuba

The kuba-amazing kubectl companion with [fzf](https://github.com/junegunn/fzf), [fx](https://github.com/antonmedv/fx), [aliases](https://github.com/ahmetb/kubectl-aliases), and more!

<p align="center"><img src="https://raw.githubusercontent.com/hcgatewood/kuba/main/assets/logo_transparent.png" alt="Kuba logo" width="300"/></p>

## Features

<p align="center"><img src="https://raw.githubusercontent.com/hcgatewood/kuba/main/assets/demo.gif" alt="Kuba demo" width="1000"/></p>

- ☁️ **Fuzzy commands** like get and describe
- 🧠 **Guess pod containers** automagically
- ✈️ **Cross namespaces and clusters** in one command
- ⚡ **Cut down on keystrokes** with an alias language
- 🧪 **Simulate scheduling** without the scheduler
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
source <(kuba shellenv --kubectl kubecolor)  # omit the --kubectl kubecolor if you haven't installed kubecolor
```

## Usage

Start by using `kuba get` and `kuba describe` as a ~drop-in replacements for `kubectl get` and `kubectl describe`.

Then check out the help pages for next steps, including using the alias language.

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
