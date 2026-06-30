package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func TestServeEmbeddedFrontendServesRootAssetBeforeSPAFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	serveEmbeddedFrontend(r, fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><div id=\"root\"></div>")},
		"logo.png":   {Data: []byte("png-bytes")},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/logo.png", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if got := w.Body.String(); got != "png-bytes" {
		t.Fatalf("expected logo asset body, got %q", got)
	}
}

func TestServeEmbeddedFrontendFallsBackToIndexForSPARoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	serveEmbeddedFrontend(r, fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><div id=\"root\"></div>")},
		"logo.png":   {Data: []byte("png-bytes")},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if got := w.Body.String(); got != "<!doctype html><div id=\"root\"></div>" {
		t.Fatalf("expected index fallback body, got %q", got)
	}
}

func TestServeEmbeddedFrontendDoesNotFallbackForAPIRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	serveEmbeddedFrontend(r, fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><div id=\"root\"></div>")},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/missing", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}
	if got := w.Body.String(); got == "<!doctype html><div id=\"root\"></div>" {
		t.Fatalf("expected API 404, got SPA fallback body")
	}
}
