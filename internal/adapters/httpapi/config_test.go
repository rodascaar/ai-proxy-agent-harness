package httpapi_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"ai-proxy-agent-harness/internal/testutil/fakellm"
)

func TestGetConfig(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := doJSON(t, handler, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload := decodeResponse[struct {
		Config    map[string]string `json:"config"`
		APIKeySet bool              `json:"apiKeySet"`
	}](t, rec)
	if payload.Config["UPSTREAM_MODEL"] != "test-model" {
		t.Errorf("expected test-model, got %q", payload.Config["UPSTREAM_MODEL"])
	}
	if _, leaked := payload.Config["UPSTREAM_API_KEY"]; leaked {
		t.Errorf("config must not leak the API key")
	}
	if payload.APIKeySet {
		t.Errorf("apiKeySet should be false in the test server")
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
