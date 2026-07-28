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

func TestServeEmbeddedFrontendServesWellKnownAssetBeforeSPAFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	serveEmbeddedFrontend(r, fstest.MapFS{
		"index.html":                  {Data: []byte("<!doctype html><div id=\"root\"></div>")},
		".well-known/bimi/logo.svg":   {Data: []byte("<svg></svg>")},
		".well-known/bimi/readme.txt": {Data: []byte("well-known")},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/bimi/logo.svg", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if got := w.Body.String(); got != "<svg></svg>" {
		t.Fatalf("expected BIMI asset body, got %q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("expected SVG content type, got %q", got)
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

func TestTrustedProxyClientIPHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.RemoteIPHeaders = []string{gin.PlatformCloudflare, "X-Forwarded-For", "X-Real-IP"}
	if err := r.SetTrustedProxies([]string{"127.0.0.1", "::1"}); err != nil {
		t.Fatalf("configure trusted proxy: %v", err)
	}
	r.GET("/ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })

	tests := []struct {
		name           string
		remoteAddr     string
		cfConnectingIP string
		xForwardedFor  string
		want           string
	}{
		{name: "cloudflare header wins from trusted proxy", remoteAddr: "127.0.0.1:1234", cfConnectingIP: "203.0.113.9", xForwardedFor: "192.0.2.10", want: "203.0.113.9"},
		{name: "xff remains a fallback", remoteAddr: "127.0.0.1:1234", xForwardedFor: "203.0.113.9", want: "203.0.113.9"},
		{name: "untrusted client cannot forge proxy headers", remoteAddr: "198.51.100.8:1234", cfConnectingIP: "203.0.113.9", xForwardedFor: "203.0.113.9", want: "198.51.100.8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/ip", nil)
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set(gin.PlatformCloudflare, tt.cfConnectingIP)
			req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			r.ServeHTTP(w, req)
			if got := w.Body.String(); got != tt.want {
				t.Fatalf("expected client IP %q, got %q", tt.want, got)
			}
		})
	}
}
