package kube

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFanOut(t *testing.T) {
	var clusters []*Cluster
	for _, n := range strings.Fields("apple banana cherry donut egg fig grape honey ice jam") {
		clusters = append(clusters, &Cluster{Name: n})
	}
	var running, peak atomic.Int32
	var mu sync.Mutex
	var finished []*Cluster
	opts := FanOutOptions{MaxParallel: 3, OnDone: func(c *Cluster) {
		mu.Lock()
		defer mu.Unlock()
		finished = append(finished, c)
	}}
	results := FanOut(context.Background(), clusters, opts, func(_ context.Context, c *Cluster) (string, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		running.Add(-1)
		if c.Name == "cherry" {
			return "", errors.New("unreachable")
		}
		return strings.ToUpper(c.Name), nil
	})
	require.Len(t, results, len(clusters))
	for i, r := range results {
		assert.Equal(t, clusters[i], r.Cluster)
		if r.Cluster.Name == "cherry" {
			assert.EqualError(t, r.Err, "unreachable")
			continue
		}
		assert.Equal(t, strings.ToUpper(r.Cluster.Name), r.Value)
	}
	assert.LessOrEqual(t, peak.Load(), int32(3))
	assert.ElementsMatch(t, clusters, finished)
}

// TestFanOutNoCap checks that MaxParallel 0 runs every cluster at once: each waits for all to start, which a cap would
// stall until the wait times out.
func TestFanOutNoCap(t *testing.T) {
	var clusters []*Cluster
	for _, n := range strings.Fields("apple banana cherry donut egg fig grape honey ice jam") {
		clusters = append(clusters, &Cluster{Name: n})
	}
	var started sync.WaitGroup
	started.Add(len(clusters))
	all := make(chan struct{})
	go func() {
		started.Wait()
		close(all)
	}()
	results := FanOut(context.Background(), clusters, FanOutOptions{}, func(_ context.Context, _ *Cluster) (int, error) {
		started.Done()
		select {
		case <-all:
			return 0, nil
		case <-time.After(time.Second):
			return 0, errors.New("not every cluster ran at once")
		}
	})
	for _, r := range results {
		assert.NoError(t, r.Err, r.Cluster.Name)
	}
}

func TestFanOutTimeout(t *testing.T) {
	results := FanOut(context.Background(), []*Cluster{{Name: "apple"}}, FanOutOptions{Timeout: 10 * time.Millisecond},
		func(ctx context.Context, _ *Cluster) (int, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		})
	assert.ErrorIs(t, results[0].Err, context.DeadlineExceeded)
}
