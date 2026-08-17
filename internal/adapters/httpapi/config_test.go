package httpapi_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-proxy-agent-harness/internal/adapters/dbstore"
	"ai-proxy-agent-harness/internal/adapters/httpapi"
	"ai-proxy-agent-harness/internal/application/service"
	"ai-proxy-agent-harness/internal/config"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/testutil/fakellm"
)

func TestGetConfig(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := doJSON(t, handler, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload := decodeResponse[struct {
		Config       map[string]string `json:"config"`
		APIKeySet    map[string]bool   `json:"apiKeySet"`
		DefaultModel string            `json:"defaultModel"`
	}](t, rec)
	if payload.Config["UPSTREAM_MODEL"] != "test-model" {
		t.Errorf("expected test-model, got %q", payload.Config["UPSTREAM_MODEL"])
	}
	if _, leaked := payload.Config["UPSTREAM_API_KEY"]; leaked {
		t.Errorf("config must not leak the API key")
	}
	if len(payload.APIKeySet) != 0 {
		t.Errorf("apiKeySet should be empty in the test server, got %v", payload.APIKeySet)
	}
	if payload.DefaultModel != "test-model" {
		t.Errorf("expected defaultModel test-model, got %q", payload.DefaultModel)
	}
}

func TestGetConfigAPIKeysSetPerUpstream(t *testing.T) {
	llm := fakellm.New()
	store, err := dbstore.Open(filepath.Join(t.TempDir(), "proxy.db"), time.Minute, 100, noopLogger())
	if err != nil {
		t.Fatalf("dbstore.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := service.New(llm, store, "test-model", 3, 25, noopLogger())
	cfg := &config.Config{
		Upstreams: []config.Upstream{
			{BaseURL: "http://a/v1", APIKey: "k1", Models: []string{"a"}},
			{BaseURL: "http://b/v1", Models: []string{"b"}},
			{BaseURL: "http://c/v1", APIKey: "k3", Models: []string{"c"}},
		},
		RequestTimeout: time.Minute,
		DBPath:         "data/proxy.db",
		MaxFileBytes:   20 << 20,
	}
	handler := httpapi.New(svc, cfg, nil, noopLogger())
	rec := doJSON(t, handler, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	payload := decodeResponse[struct {
		APIKeySet map[string]bool `json:"apiKeySet"`
	}](t, rec)
	if !payload.APIKeySet["UPSTREAM_1"] {
		t.Errorf("UPSTREAM_1 should report key set")
	}
	if payload.APIKeySet["UPSTREAM_2"] {
		t.Errorf("UPSTREAM_2 should not report key set")
	}
	if !payload.APIKeySet["UPSTREAM_3"] {
		t.Errorf("UPSTREAM_3 should report key set")
	}
}

func TestPutConfigInvalid(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := doJSON(t, handler, http.MethodPut, "/api/config", `{"config":{"PROXY_PORT":"nope"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutConfigMissingBody(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := doJSON(t, handler, http.MethodPut, "/api/config", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty config, got %d", rec.Code)
	}
}

func TestPutConfigWritesEnvFile(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)

	// El PUT escribe .env en el directorio de trabajo del test; se limpia al
	// terminar para no dejar basura.
	t.Cleanup(func() { _ = os.Remove(".env") })

	rec := doJSON(t, handler, http.MethodPut, "/api/config", `{"config":{"UPSTREAM_MODEL":"otro-modelo"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload := decodeResponse[struct {
		Status string `json:"status"`
	}](t, rec)
	if payload.Status != "saved" {
		t.Errorf("expected status saved, got %q", payload.Status)
	}

	data, err := os.ReadFile(".env")
	if err != nil {
		t.Fatalf("expected .env to be written: %v", err)
	}
	if !strings.Contains(string(data), "UPSTREAM_MODEL=otro-modelo") {
		t.Errorf("expected UPSTREAM_MODEL in .env, got %q", string(data))
	}
}

func TestWebUIServedAtRoot(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := doJSON(t, handler, http.MethodGet, "/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 serving index.html, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected html content type, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "AI Proxy") {
		t.Errorf("expected index.html body, got %q", rec.Body.String())
	}
}

// fakeLister implementa ports.ModelLister para tests.
type fakeLister struct {
	models []openai.ModelDescriptor
	err    error
	calls  int
}

func (f *fakeLister) ListModels(ctx context.Context) ([]openai.ModelDescriptor, error) {
	f.calls++
	return f.models, f.err
}

func TestModelsPassthrough(t *testing.T) {
	llm := fakellm.New().
		Completion(`{"atomic": true, "subtasks": []}`).
		StreamResponse([]string{"final"}, nil)
	store, err := dbstore.Open(filepath.Join(t.TempDir(), "proxy.db"), time.Minute, 100, noopLogger())
	if err != nil {
		t.Fatalf("dbstore.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := service.New(llm, store, "test-model", 3, 25, noopLogger())
	cfg := &config.Config{
		UpstreamBaseURL: "http://localhost:11434/v1",
		UpstreamModel:   "test-model",
		RequestTimeout:  time.Minute,
		DBPath:          "data/proxy.db",
		MaxFileBytes:    20 << 20,
	}
	lister := &fakeLister{models: []openai.ModelDescriptor{
		{ID: "qwen2.5:7b", Object: openai.ObjectModel, OwnedBy: "ollama"},
		{ID: "mistral", Object: openai.ObjectModel, OwnedBy: "ollama"},
	}}
	handler := httpapi.New(svc, cfg, lister, noopLogger())

	rec := doJSON(t, handler, http.MethodGet, "/v1/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload := decodeResponse[struct {
		Data []openai.ModelDescriptor `json:"data"`
	}](t, rec)
	ids := map[string]bool{}
	for _, m := range payload.Data {
		ids[m.ID] = true
	}
	if len(payload.Data) != 3 {
		t.Errorf("expected 2 upstream + 1 default = 3 models, got %d", len(payload.Data))
	}
	if !ids["qwen2.5:7b"] || !ids["mistral"] {
		t.Errorf("expected upstream models in list, got %v", ids)
	}
	if !ids["test-model"] {
		t.Errorf("expected default model present, got %v", ids)
	}
	if lister.calls != 1 {
		t.Errorf("expected one upstream call (cached on repeat), got %d", lister.calls)
	}

	// Segunda llamada: cache (no vuelve a consultar al upstream).
	rec2 := doJSON(t, handler, http.MethodGet, "/v1/models", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on cached call, got %d", rec2.Code)
	}
	if lister.calls != 1 {
		t.Errorf("cached call should not query upstream again, calls=%d", lister.calls)
	}
}
