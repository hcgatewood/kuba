package kube

import (
	"cmp"
	"context"
	"time"

	"golang.org/x/sync/errgroup"
)

// Result is fn's Value, or Err, for one cluster. A failing cluster doesn't stop the rest, so the caller decides what
// its Err means, e.g. a warning, or under --strict-clusters, a failure.
type Result[T any] struct {
	Cluster *Cluster
	Value   T
	Err     error
}

// FanOutOptions tune a fan-out.
type FanOutOptions struct {
	// Timeout bounds each cluster's lookups; zero means no bound beyond the request timeout.
	Timeout time.Duration
	// MaxParallel caps clusters looked up at once, so exec auth plugins (e.g. browser logins) don't all fire together;
	// zero means no cap.
	MaxParallel int
	// OnDone, if set, hears of each cluster as it finishes, e.g. to update a progress line. It's called from that
	// cluster's goroutine, so it must be safe to call concurrently.
	OnDone func(*Cluster)
}

// FanOut calls fn on each cluster, at most MaxParallel at a time, returning results in cluster order.
func FanOut[T any](ctx context.Context, clusters []*Cluster, opts FanOutOptions, fn func(context.Context, *Cluster) (T, error)) []Result[T] {
	results := make([]Result[T], len(clusters))
	var g errgroup.Group
	g.SetLimit(cmp.Or(opts.MaxParallel, -1)) // errgroup reads 0 as no goroutines at all, and -1 as no cap
	for i, c := range clusters {
		g.Go(func() error {
			clusterCtx := ctx
			if opts.Timeout > 0 {
				var cancel context.CancelFunc
				clusterCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
				defer cancel()
			}
			value, err := fn(clusterCtx, c)
			results[i] = Result[T]{Cluster: c, Value: value, Err: err}
			if opts.OnDone != nil {
				opts.OnDone(c)
			}
			return nil // an error is the cluster's own, in its Result
		})
	}
	_ = g.Wait()
	return results
}
