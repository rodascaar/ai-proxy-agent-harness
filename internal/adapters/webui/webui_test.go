package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesAssetsWithCacheHeaders(t *testing.T) {
	h := Handler()

	t.Run("index served with etag and no-cache", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("Cache-Control = %q, want no-cache", got)
		}
		if got := rec.Header().Get("ETag"); got == "" {
			t.Fatalf("ETag header is empty")
		}
		if !strings.Contains(rec.Body.String(), "<html") {
			t.Fatalf("index body does not contain <html")
		}
	})

	t.Run("asset etag and 304", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/style.css", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		etag := rec.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("ETag header is empty")
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("Cache-Control = %q, want no-cache", got)
		}

		rec2 := httptest.NewRecorder()
		req2 := httptest.NewRequest(http.MethodGet, "/style.css", nil)
		req2.Header.Set("If-None-Match", etag)
		h.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusNotModified {
			t.Fatalf("status with If-None-Match = %d, want 304", rec2.Code)
		}
		if rec2.Body.Len() != 0 {
			t.Fatalf("304 response has body")
		}
	})

	t.Run("head request returns headers without body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodHead, "/app.js", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD response has body")
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("Cache-Control = %q, want no-cache", got)
		}
	})

	t.Run("unknown path returns 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/foo/bar", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}
