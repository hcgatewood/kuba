Require kuba

Set Shell zsh
Set FontSize 20
Set Height 800
Set Width 1600
Set Framerate 30

Type `# Welcome to Kuba!` Sleep 3s Ctrl+c Enter

Type `# Get pods like normal` Sleep 1s Ctrl+c Type `kuba get pods` Enter Sleep 6s Enter
Type `# With fuzzy search` Sleep 2s Ctrl+c Type `kuba get pods cathy` Enter Sleep 6s Enter
Type `# And fuzzy select` Sleep 2s Ctrl+c Type `kuba get pods cathy --select` Sleep 2s Enter Sleep 3s Up Up Sleep 1s Enter Sleep 3s Enter
Type `# Search by label` Sleep 2s Ctrl+c Type `kuba get pods pinterest.com/crd_name=cathy-pa` Enter Sleep 6s Enter

Type `# Guess a pod's main container` Sleep 2s Ctrl+c Type `kuba logs hcg-test --guess --follow` Sleep 2s Enter Sleep 6s Ctrl+c Sleep 4s Enter
Type `# Detect a pod's shell too` Sleep 2s Ctrl+c Type `kuba exec hcg-test --guess` Enter Sleep 6s Type `echo in a container!` Enter Sleep 2s Type `exit` Enter Enter Sleep 2s Enter
Type `# Logs from multiple pods at the same time` Sleep 2s Ctrl+c Type `kuba logs test --follow` Sleep 2s Enter Sleep 3s Shift+Tab Shift+Tab Sleep 1s Enter Sleep 6s Ctrl+c Sleep 2 Enter

Type `# Cross namespaces and clusters` Sleep 2s Ctrl+c Type `kuba get pods cathy --all-namespaces --multi-cluster` Sleep 2s Enter Sleep 10s Enter

Type `# Find pods on a node` Sleep 2s Ctrl+c Type `kuba get nodes ip-10-3-38-221.ec2.internal --output pods` Sleep 2s Enter Sleep 8s Enter
Type `# Filter by lease holders` Sleep 2s Ctrl+c Type `kuba get pods --namespace pincompute-system --leader` Sleep 2s Enter Sleep 8s Enter
Type `# Simulate scheduling` Sleep 2s Ctrl+c Type `kuba sched hcg-test` Sleep 2s Enter Sleep 8s Enter

Type `# And aliases for everything 🔥` Ctrl+c
Type `# kp => kuba get pods` Ctrl+c Sleep 2s
Type `# knd => kuba describe nodes` Ctrl+c Sleep 2s
Type `# kdf => kuba get deployments -o json | fx` Ctrl+c Sleep 2s
Type `# kpkx => kuba get pods --namespace kube-system --leader` Ctrl+c Sleep 2s
Type `# knm => kuba get nodes --multi-cluster` Ctrl+c Sleep 2s
Enter Type `# And many more!` Sleep 1s Type ` See kuba shellenv --help for the full language` Ctrl+c Sleep 3s Enter

Type `# Featuring:` Sleep 1s Ctrl+c
Type `#   Shell completion` Sleep 1s Ctrl+c
Type `#   Kubectl passthrough` Sleep 1s Ctrl+c
Type `#   Footgun-free (no edit, apply, etc)` Sleep 1s Ctrl+c
Type `#   Extensible aliasing` Sleep 1s Ctrl+c
Type `#   And much more!` Sleep 3s Ctrl+c

Enter
Type `# Get started with:` Ctrl+c
Type `#   pip install kuba` Sleep 8s Ctrl+c
