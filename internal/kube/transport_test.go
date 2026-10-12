package kube

import (
	"bytes"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGuard(t *testing.T) {
	tests := []struct {
		method  string
		path    string
		wantErr error
		wantLog string
	}{
		{method: http.MethodGet, path: "/api/v1/namespaces/apple/pods", wantLog: `msg=request context=banana verb=GET path=/api/v1/namespaces/apple/pods query="limit=500" status=200`},
		{method: http.MethodGet, path: "/apis/apps/v1", wantLog: "discovery=true"},
		{method: http.MethodPost, path: "/api/v1/namespaces/apple/pods", wantErr: ErrNotReadOnly},
		{method: http.MethodDelete, path: "/api/v1/namespaces/apple/pods/cherry", wantErr: ErrNotReadOnly},
		{method: http.MethodPatch, path: "/api/v1/nodes/donut", wantErr: ErrNotReadOnly},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			var logs bytes.Buffer
			var sent atomic.Int32
			next := roundTripFunc(func(*http.Request) (*http.Response, error) {
				sent.Add(1)
				return &http.Response{StatusCode: 200}, nil
			})
			log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			rt := guard("banana", log)(next)
			req, err := http.NewRequest(tt.method, "https://example.com"+tt.path+"?limit=500", nil)
			require.NoError(t, err)

			_, err = rt.RoundTrip(req)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Zero(t, sent.Load())
				assert.Empty(t, logs.String())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, int32(1), sent.Load())
			assert.Contains(t, logs.String(), tt.wantLog)
		})
	}
}

func TestIsDiscovery(t *testing.T) {
	tests := map[string]bool{
		"/api":                                true,
		"/api/v1":                             true,
		"/apis":                               true,
		"/apis/apps":                          true,
		"/apis/apps/v1":                       true,
		"/version":                            true,
		"/openapi/v3":                         true,
		"/api/v1/pods":                        false,
		"/api/v1/namespaces/apple/pods":       false,
		"/apis/apps/v1/deployments":           false,
		"/apis/apps/v1/namespaces/a/pods":     false,
		"/apis/coordination.k8s.io/v1/leases": false,
	}
	for path, want := range tests {
		assert.Equal(t, want, isDiscovery(path), path)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
