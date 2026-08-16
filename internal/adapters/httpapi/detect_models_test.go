package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-proxy-agent-harness/internal/core/openai"
)

// upstreamServer responde /v1/models con una lista fija, verificando la API key.
func upstreamServer(t *testing.T, wantKey string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if wantKey != "" && r.Header.Get("Authorization") != "Bearer "+wantKey {
			t.Errorf("expected Authorization header, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"liquid/lfm2-1.2b","object":"model","owned_by":"lmstudio"},{"id":"qwen/qwen3-1.7b","object":"model","owned_by":"lmstudio"}]}`))
	}))
}

func TestDetectModelsReachable(t *testing.T) {
	handler, _ := newTestServer(t, nil, true)
	server := upstreamServer(t, "")
	defer server.Close()

	rec := doJSON(t, handler, http.MethodPost, "/api/detect-models",
		`{"url":"`+server.URL+`/v1","apiKey":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload := decodeResponse[struct {
		Reachable bool                     `json:"reachable"`
		Models    []openai.ModelDescriptor `json:"models"`
	}](t, rec)
	if !payload.Reachable {
		t.Fatalf("expected reachable=true, got %s", rec.Body.String())
	}
	if len(payload.Models) != 2 {
		t.Fatalf("expected 2 detected models, got %d", len(payload.Models))
	}
	if payload.Models[0].ID != "liquid/lfm2-1.2b" || payload.Models[1].ID != "qwen/qwen3-1.7b" {
		t.Errorf("unexpected detected models: %v", payload.Models)
	}
}

func TestDetectModelsSendsAPIKey(t *testing.T) {
	handler, _ := newTestServer(t, nil, true)
	server := upstreamServer(t, "sk-test")
	defer server.Close()

	rec := doJSON(t, handler, http.MethodPost, "/api/detect-models",
		`{"url":"`+server.URL+`/v1","apiKey":"sk-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload := decodeResponse[struct {
		Reachable bool `json:"reachable"`
	}](t, rec)
	if !payload.Reachable {
		t.Fatalf("expected reachable=true, got %s", rec.Body.String())
	}
}

func TestDetectModelsUnreachable(t *testing.T) {
	handler, _ := newTestServer(t, nil, true)

	// Puerto 1: conexión rechazada casi seguro en cualquier máquina.
	rec := doJSON(t, handler, http.MethodPost, "/api/detect-models",
		`{"url":"http://127.0.0.1:1/v1","apiKey":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with structured result, got %d", rec.Code)
	}
	payload := decodeResponse[struct {
		Reachable bool   `json:"reachable"`
		Error     string `json:"error"`
	}](t, rec)
	if payload.Reachable {
		t.Fatalf("expected reachable=false for a dead server")
	}
	if payload.Error == "" {
		t.Errorf("expected a non-empty error message for the UI")
	}
}

func TestDetectModelsRejectsInvalidURL(t *testing.T) {
	handler, _ := newTestServer(t, nil, true)
	rec := doJSON(t, handler, http.MethodPost, "/api/detect-models",
		`{"url":"not-a-url","apiKey":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid URL, got %d", rec.Code)
	}
}

func TestDetectModelsRejectsEmptyBody(t *testing.T) {
	handler, _ := newTestServer(t, nil, true)
	rec := doJSON(t, handler, http.MethodPost, "/api/detect-models", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing URL, got %d", rec.Code)
	}
}
