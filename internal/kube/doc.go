// Package kube is kuba's read-only access to clusters: kubeconfig, discovery, Table lookups, and fan-out.
//
// # Clusters
//
// To kuba, a cluster is a kubeconfig context. Kubeconfig reads kubeconfig as kubectl does: every file merged, with the
// connection flags, e.g. --as, applied to every context. It hands out one Cluster per context.
//
// A Cluster connects on its first API call. So in a fan-out, each cluster loads its credentials and discovery on its
// own goroutine, and a broken one fails alone.
//
// FanOut runs a lookup on many clusters at once, each with its own timeout. It returns every cluster's result,
// failures included, and the caller decides whether to warn or fail.
//
// # Lookups
//
// A Cluster's lookups:
//   - Mappings resolves a type argument as kubectl does, e.g. po, deployments.apps, pod,svc, or all. Discovery is
//     cached in kubectl's own cache dir, which the two share.
//   - List and Get read the server-side Table API: the columns kubectl get would show, plus each object's metadata,
//     500 rows a page.
//   - GetOrList sends an exact GET and a LIST at once, so an exact name and a partial one both take one round trip.
//   - Object reads a whole object, for output kuba merges itself.
//   - LeaseHolders reads leases, for --leader, and guesses which objects hold them (LeaseHolders.Hold).
//
// # Read-only
//
// Every request passes a guard that refuses anything but GET (ErrNotReadOnly). So changes only ever happen in a
// command kuba hands off, where you can see them.
//
// Under --debug, the guard also logs a line per request. The e2e request budgets count those lines.
//
// # Matching kubectl
//
// Where kube redoes kubectl's work, tests check it against kubectl's own code: type resolution, how connection flags
// shape each client, and the cache dir. Only tests import kubectl, so the binary stays free of it and kustomize.
//
// # Auth plugins
//
// Users that log in with an auth-provider, e.g. oidc, need its plugin registered. As in kubectl, that's the binary's
// job: cmd/kuba imports k8s.io/client-go/plugin/pkg/client/auth. Without it, such a context fails with "no Auth
// Provider found for name".
package kube
