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

	mailmatchapi "github.com/donnel666/remail/internal/mailmatch/api"
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
	mailmatchapi.RegisterRoutes(r.Group("/v1"), nil)

	got := make([]string, 0)
	for _, route := range r.Routes() {
		if !isPublicOpenAPIRoute(route.Path) {
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
		if !isPublicOpenAPIRoute(path) {
			continue
		}
		for method := range operations {
			entries = append(entries, strings.ToUpper(method)+" "+path)
		}
	}
	return entries
}

func TestPublicOpenAPISchemaUsesBackendEnums(t *testing.T) {
	spec := publicOpenAPISpec(t)
	assertSchemaEnum(t, spec, "Project", "status", []string{"reviewing", "listed", "delisted"})
	assertSchemaEnum(t, spec, "ProjectMailRule", "ruleType", []string{"sender", "recipient", "subject", "body"})
	assertSchemaEnum(t, spec, "Order", "serviceCleanupStatus", []string{"none", "succeeded", "partial_failure"})
	assertSchemaEnum(t, spec, "Order", "failureCode", []string{
		"unknown",
		"insufficient_inventory",
		"insufficient_balance",
		"allocation_failed",
		"service_token_failed",
		"activation_failed",
	})
}

func publicOpenAPISpec(t *testing.T) map[string]any {
	t.Helper()

	data, err := os.ReadFile("../web/public/openapi.json")
	if err != nil {
		t.Fatalf("read public openapi.json: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("decode public openapi.json: %v", err)
	}
	return spec
}

func assertSchemaEnum(t *testing.T, spec map[string]any, schemaName string, propertyName string, want []string) {
	t.Helper()

	components, ok := spec["components"].(map[string]any)
	if !ok {
		t.Fatalf("public openapi missing components")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatalf("public openapi missing components.schemas")
	}
	schema, ok := schemas[schemaName].(map[string]any)
	if !ok {
		t.Fatalf("public openapi missing schema %s", schemaName)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("public openapi schema %s missing properties", schemaName)
	}
	property, ok := properties[propertyName].(map[string]any)
	if !ok {
		t.Fatalf("public openapi schema %s missing property %s", schemaName, propertyName)
	}
	rawEnum, ok := property["enum"].([]any)
	if !ok {
		t.Fatalf("public openapi schema %s.%s missing enum", schemaName, propertyName)
	}
	got := make([]string, len(rawEnum))
	for i := range rawEnum {
		value, ok := rawEnum[i].(string)
		if !ok {
			t.Fatalf("public openapi schema %s.%s enum has non-string value", schemaName, propertyName)
		}
		got[i] = value
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public openapi schema %s.%s enum mismatch: got %v want %v", schemaName, propertyName, got, want)
	}
}

func isPublicOpenAPIRoute(path string) bool {
	return strings.HasPrefix(path, "/v1/open/") || path == "/v1/pickup" || strings.HasPrefix(path, "/v1/pickup/")
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
