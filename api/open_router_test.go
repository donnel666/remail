package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	openapiapi "github.com/donnel666/remail/internal/openapi/api"
	"github.com/gin-gonic/gin"
)

func TestOpenRoutesRequireAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	registerOpenRoutes(r.Group("/v1"), &openapiapi.Module{}, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/open/projects", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w.Code)
	}
}

func TestOpenRoutesMatchPublicOpenAPISpec(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	registerOpenRoutes(r.Group("/v1"), &openapiapi.Module{}, nil, nil, nil, nil)

	got := make([]string, 0)
	for _, route := range r.Routes() {
		if !strings.HasPrefix(route.Path, "/v1/open/") {
			continue
		}
		got = append(got, route.Method+" "+normalizeGinOpenAPIPath(route.Path))
	}
	sort.Strings(got)

	want := publicOpenAPIEntries(t)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("open routes and public openapi.json differ\nroutes: %v\nspec:   %v", got, want)
	}
}

func publicOpenAPIEntries(t *testing.T) []string {
	t.Helper()

	data, err := os.ReadFile("../web/public/openapi.json")
	if err != nil {
		t.Fatalf("read public openapi.json: %v", err)
	}

	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("decode public openapi.json: %v", err)
	}

	entries := make([]string, 0)
	for path, operations := range spec.Paths {
		if !strings.HasPrefix(path, "/v1/open/") {
			continue
		}
		for method := range operations {
			entries = append(entries, strings.ToUpper(method)+" "+path)
		}
	}
	return entries
}

func normalizeGinOpenAPIPath(path string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		if strings.HasPrefix(parts[i], ":") {
			parts[i] = "{" + strings.TrimPrefix(parts[i], ":") + "}"
		}
	}
	return strings.Join(parts, "/")
}
