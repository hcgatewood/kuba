package kube

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrNotReadOnly is a request kuba refused to send, since kuba's own requests only ever read.
var ErrNotReadOnly = errors.New("kuba only reads from the API")

// guard wraps a transport so it refuses every request but GET, and debug logs each request it sends.
//
// The log line is the request budget tests' source: one per request, with discovery requests marked, and never a
// header or body, so no credentials.
func guard(contextName string, log *slog.Logger) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		return &guardedTransport{context: contextName, log: log, next: next}
	}
}

// guardedTransport is the transport guard wraps around a context's, so every request kuba sends that context passes
// through it, discovery's included.
type guardedTransport struct {
	// context is the context the requests go to, e.g. apple, so a fan-out's log lines say which cluster each is for.
	context string
	// log gets a line per request sent (see guard), discarded without --debug.
	log *slog.Logger

	// next sends the requests guard lets through on toward the server.
	next http.RoundTripper
}

// RoundTrip sends a GET on through next, logging it, and fails anything else with ErrNotReadOnly, unsent.
func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, ErrNotReadOnly)
	}

	start := time.Now()

	res, err := t.next.RoundTrip(req)
	status := "error"
	switch {
	case err == nil:
		status = strconv.Itoa(res.StatusCode)
	case errors.Is(err, context.Canceled):
		status = "canceled"
	}

	t.log.Debug("request",
		"context", t.context,
		"verb", req.Method,
		"path", req.URL.Path,
		"query", req.URL.RawQuery,
		"status", status,
		"latency", time.Since(start).Round(time.Millisecond),
		"discovery", isDiscovery(req.URL.Path),
	)

	return res, err
}

// isDiscovery reports whether a request path is discovery's rather than a resource's.
//
// Example:
//   - Discovery: /api, /api/v1, /apis, /apis/apps, /apis/apps/v1, /version, and anything under /openapi, e.g.
//     /openapi/v3/apis/apps/v1
//   - Resource: /api/v1/pods, /apis/apps/v1/namespaces/kiwi/deployments
//
// REF: https://kubernetes.io/docs/concepts/overview/kubernetes-api/#discovery-api
// REF: https://github.com/kubernetes/apiserver/blob/v0.37.1/pkg/endpoints/request/requestinfo.go#L119-L159
func isDiscovery(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch parts[0] {
	case "version", "openapi":
		return true
	case "api":
		return len(parts) <= 2
	case "apis":
		return len(parts) <= 3
	default:
		return false
	}
}
