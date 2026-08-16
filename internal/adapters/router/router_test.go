package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-proxy-agent-harness/internal/config"
)

// newProbeServer returns a httptest server that answers /v1/models (used by
// Probe and ListModels) with an empty list, enough to prove connectivity.
func newProbeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
}

// TestRouterAllModelsSingleUpstream verifies a single upstream exposes its models.
func TestRouterAllModelsSingleUpstream(t *testing.T) {
	r := New([]config.Upstream{{
		BaseURL: "http://localhost:11434/v1",
		Models:  []string{"qwen2.5:7b", "llama3.2:3b"},
	}}, time.Second)

	got := r.AllModels()
	if len(got) != 2 {
		t.Fatalf("expected 2 models, got %v", got)
	}
}

// TestRouterClientForFallback verifies that an unknown model falls back to the
// first upstream (legacy passthrough behavior).
func TestRouterClientForFallback(t *testing.T) {
	r := New([]config.Upstream{{
		BaseURL: "http://localhost:11434/v1",
		Models:  []string{"qwen2.5:7b"},
	}}, time.Second)

	c, err := r.ClientFor("unknown-model")
	if err != nil {
		t.Fatalf("ClientFor(unknown) error: %v", err)
	}
	if c == nil {
		t.Fatal("expected a fallback client")
	}
}

// TestRouterMultiUpstreamAllModels verifies two upstreams expose all models.
func TestRouterMultiUpstreamAllModels(t *testing.T) {
	r := New([]config.Upstream{
		{BaseURL: "http://localhost:11434/v1", Models: []string{"qwen2.5:7b", "llama3.2:3b"}},
		{BaseURL: "https://api.openai.com/v1", Models: []string{"gpt-4o-mini"}},
	}, time.Second)

	got := r.AllModels()
	if len(got) != 3 {
		t.Fatalf("expected 3 models, got %v", got)
	}
	want := map[string]bool{"qwen2.5:7b": false, "llama3.2:3b": false, "gpt-4o-mini": false}
	for _, m := range got {
		if _, ok := want[m]; ok {
			want[m] = true
		}
	}
	for m, found := range want {
		if !found {
			t.Errorf("model %q missing from AllModels()", m)
		}
	}
}

// TestRouterProbeAllUpstreams verifies that Probe succeeds when all upstreams
// are reachable.
func TestRouterProbeAllUpstreams(t *testing.T) {
	srv1 := newProbeServer(t)
	srv2 := newProbeServer(t)
	defer srv1.Close()
	defer srv2.Close()

	r := New([]config.Upstream{
		{BaseURL: srv1.URL + "/v1", Models: []string{"m1"}},
		{BaseURL: srv2.URL + "/v1", Models: []string{"m2"}},
	}, time.Second)

	if err := r.Probe(context.Background()); err != nil {
		t.Fatalf("Probe() expected success, got: %v", err)
	}
}

// TestRouterProbeReportsFailure verifies that Probe reports a failure when an
// upstream is unreachable.
func TestRouterProbeReportsFailure(t *testing.T) {
	srv := newProbeServer(t)
	defer srv.Close()

	r := New([]config.Upstream{
		{BaseURL: "http://127.0.0.1:1/v1", Models: []string{"bad"}},
		{BaseURL: srv.URL + "/v1", Models: []string{"m2"}},
	}, time.Second)

	if err := r.Probe(context.Background()); err == nil {
		t.Fatal("expected Probe() to fail for an unreachable upstream")
	}
}

// TestRouterListModelsAggregates verifies ListModels aggregates across upstreams.
func TestRouterListModelsAggregates(t *testing.T) {
	srv1 := newProbeServer(t)
	srv2 := newProbeServer(t)
	defer srv1.Close()
	defer srv2.Close()

	r := New([]config.Upstream{
		{BaseURL: srv1.URL + "/v1", Models: []string{"m1"}},
		{BaseURL: srv2.URL + "/v1", Models: []string{"m2"}},
	}, time.Second)

	models, err := r.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected 0 advertised models (empty test servers), got %d", len(models))
	}
}
