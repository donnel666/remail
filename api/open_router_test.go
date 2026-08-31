package api

import (
	"encoding/json"
	"fmt"
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
	registerOpenRoutes(r.Group("/v1"), &openapiapi.Module{}, nil, nil, nil, nil, nil, nil, nil)

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
	registerOpenRoutes(r.Group("/v1"), &openapiapi.Module{}, nil, nil, nil, nil, nil, nil, nil)
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
	assertSchemaEnum(t, spec, "ProjectOwner", "role", []string{"user", "supplier", "admin", "super_admin"})
	assertSchemaEnum(t, spec, "ProjectMailRule", "ruleType", []string{"sender", "recipient", "subject", "body"})
	assertSchemaEnum(t, spec, "Order", "allocationType", []string{"microsoft", "domain", "gmail", "icloud"})
	assertSchemaEnum(t, spec, "Order", "clientChannel", []string{"console", "api_key"})
	assertSchemaEnum(t, spec, "Order", "serviceCleanupStatus", []string{"none", "succeeded", "partial_failure"})
	assertSchemaEnum(t, spec, "Order", "failureCode", []string{
		"unknown",
		"insufficient_inventory",
		"insufficient_balance",
		"allocation_failed",
		"service_token_failed",
		"activation_failed",
	})
	assertSchemaEnum(t, spec, "FetchState", "lastStatus", []string{"pending", "processing", "normal", "abnormal"})
	assertSchemaEnum(t, spec, "Transaction", "transactionType", []string{"recharge", "debit", "refund", "freeze", "credit", "withdrawal", "manual_adjustment", "card_redeem", "transfer"})
	assertSchemaEnum(t, spec, "Transaction", "balanceBucket", []string{"consumer", "supplier_available", "supplier_frozen"})
	assertSchemaEnum(t, spec, "Transaction", "direction", []string{"in", "out"})
	assertSchemaEnum(t, spec, "Recharge", "paymentMethod", []string{"alipay", "epusdt_usdt_tron"})
	assertSchemaEnum(t, spec, "Recharge", "status", []string{"paying", "callback", "reconciled", "credited", "failed"})
	assertSchemaEnum(t, spec, "CardKey", "status", []string{"enabled", "disabled"})
	assertSchemaEnum(t, spec, "Resource", "purpose", []string{"not_sale", "sale", "binding"})
	assertSchemaEnum(t, spec, "MicrosoftResourceDetail", "status", []string{"pending", "validating", "identifying", "normal", "abnormal", "disabled", "deleted"})
	assertSchemaEnum(t, spec, "DomainResourceDetail", "status", []string{"pending", "validating", "normal", "abnormal", "disabled", "deleted"})
	assertSchemaEnum(t, spec, "ImportStatusResponse", "status", []string{"processing", "imported", "failed"})
	assertSchemaEnum(t, spec, "MailServer", "status", []string{"online", "offline", "disabled"})
	assertSchemaEnum(t, spec, "Mailbox", "status", []string{"normal", "disabled"})
	assertSchemaEnum(t, spec, "CreateDomainRequest", "purpose", []string{"not_sale"})
}

func TestPublicOpenAPISDKContract(t *testing.T) {
	spec := publicOpenAPISpec(t)
	if got := spec["openapi"]; got != "3.0.3" {
		t.Fatalf("public OpenAPI version = %v, want 3.0.3", got)
	}
	servers := spec["servers"].([]any)
	if len(servers) != 1 || servers[0].(map[string]any)["url"] != "https://remail.aishop6.com" {
		t.Fatalf("public OpenAPI servers = %v", servers)
	}

	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	errorProperties := schemas["ErrorResponse"].(map[string]any)["properties"].(map[string]any)
	if _, ok := errorProperties["code"]; !ok {
		t.Fatal("ErrorResponse must expose the optional business code")
	}
	fields := errorProperties["fields"].(map[string]any)
	additional, ok := fields["additionalProperties"].(map[string]any)
	if fields["type"] != "object" || !ok || additional["type"] != "string" {
		t.Fatalf("ErrorResponse.fields must be a string map: %v", fields)
	}
	pickup := schemas["PickupMailResponse"].(map[string]any)
	pickupProperties := pickup["properties"].(map[string]any)
	if len(pickupProperties) != 2 || pickupProperties["items"] == nil || pickupProperties["fetch"] == nil {
		t.Fatalf("PickupMailResponse must expose only items/fetch: %v", pickupProperties)
	}
	batchItems := schemas["PickupBatchRequest"].(map[string]any)["properties"].(map[string]any)["items"].(map[string]any)
	if batchItems["maxItems"] != float64(100) {
		t.Fatalf("pickup batch maxItems = %v, want 100", batchItems["maxItems"])
	}
	assertSchemaEnum(t, spec, "OrderBatchItemErrorResponse", "code", []string{
		"insufficient_balance", "insufficient_inventory", "upstream_price_protected",
		"upstream_unavailable", "idempotency_conflict", "temporarily_unavailable",
	})
	if requiredField(schemas["CreateOrderBatchItemResponse"].(map[string]any), "order") {
		t.Fatal("CreateOrderBatchItemResponse.order must be optional")
	}
	if requiredField(schemas["MailMessage"].(map[string]any), "id") {
		t.Fatal("MailMessage.id must be optional for synthesized upstream code items")
	}
	createDomainProperties := schemas["CreateDomainRequest"].(map[string]any)["properties"].(map[string]any)
	if _, ok := createDomainProperties["allowNewBindings"]; ok {
		t.Fatal("public CreateDomainRequest cannot expose admin-only allowNewBindings")
	}
	resourceDetail := schemas["ResourceDetail"].(map[string]any)
	if resourceDetail["type"] != "object" || !requiredField(resourceDetail, "type") {
		t.Fatalf("ResourceDetail must be a flat typed object: %v", resourceDetail)
	}

	for name, rawSchema := range schemas {
		schema, ok := rawSchema.(map[string]any)
		if !ok || schema["type"] != "object" {
			continue
		}
		properties, _ := schema["properties"].(map[string]any)
		assertPublicPropertyFormats(t, name, properties)
	}
	for _, name := range []string{
		"ProjectProductSummary", "ProjectMailRule", "ProjectFacets", "FetchState",
		"Transaction", "Recharge", "CardKey", "Resource", "MicrosoftResourceDetail",
		"DomainResourceDetail", "MailServer", "Mailbox",
	} {
		if required, ok := schemas[name].(map[string]any)["required"].([]any); !ok || len(required) == 0 {
			t.Fatalf("public response schema %s must declare required fields", name)
		}
	}

	paths := spec["paths"].(map[string]any)
	for path, rawPath := range paths {
		for method, rawOperation := range rawPath.(map[string]any) {
			operation := rawOperation.(map[string]any)
			if body, ok := operation["requestBody"].(map[string]any); ok && body["required"] != true {
				t.Fatalf("%s %s requestBody must be required", method, path)
			}
			for _, rawParameter := range operationParameters(operation) {
				parameter := rawParameter.(map[string]any)
				name, _ := parameter["name"].(string)
				schema, _ := parameter["schema"].(map[string]any)
				if strings.HasSuffix(name, "Id") && schema["type"] == "integer" && schema["format"] != "int64" {
					t.Fatalf("%s %s parameter %s must use int64", method, path, name)
				}
			}
		}
	}
	assertStableEnumNames(t, "$", spec)
}

func assertStableEnumNames(t *testing.T, path string, value any) {
	t.Helper()
	switch current := value.(type) {
	case map[string]any:
		if values, ok := current["enum"].([]any); ok {
			names, ok := current["x-enum-varnames"].([]any)
			if !ok || len(names) != len(values) {
				t.Fatalf("enum %s must define one stable x-enum-varnames entry per value", path)
			}
			seen := make(map[string]bool, len(names))
			for _, rawName := range names {
				name, ok := rawName.(string)
				if !ok || name == "" || seen[name] {
					t.Fatalf("enum %s has an invalid or duplicate stable name %v", path, rawName)
				}
				seen[name] = true
			}
		}
		for key, child := range current {
			assertStableEnumNames(t, path+"."+key, child)
		}
	case []any:
		for index, child := range current {
			assertStableEnumNames(t, fmt.Sprintf("%s[%d]", path, index), child)
		}
	}
}

func requiredField(schema map[string]any, name string) bool {
	for _, raw := range schema["required"].([]any) {
		if raw == name {
			return true
		}
	}
	return false
}

func assertPublicPropertyFormats(t *testing.T, schemaName string, properties map[string]any) {
	t.Helper()
	for name, rawProperty := range properties {
		property, ok := rawProperty.(map[string]any)
		if !ok {
			continue
		}
		if (name == "id" || strings.HasSuffix(name, "Id")) && property["type"] == "integer" && property["format"] != "int64" {
			t.Fatalf("public schema %s.%s must use int64", schemaName, name)
		}
		if (strings.HasSuffix(name, "At") || strings.HasSuffix(name, "Until")) && property["type"] == "string" && property["format"] != "date-time" {
			t.Fatalf("public schema %s.%s must use date-time", schemaName, name)
		}
		if items, ok := property["items"].(map[string]any); ok && strings.HasSuffix(name, "Ids") && items["format"] != "int64" {
			t.Fatalf("public schema %s.%s items must use int64", schemaName, name)
		}
	}
}

func operationParameters(operation map[string]any) []any {
	parameters, _ := operation["parameters"].([]any)
	return parameters
}

func TestPublicOpenAPIDoesNotExposeSystemKeySurface(t *testing.T) {
	spec := publicOpenAPISpec(t)
	paths := spec["paths"].(map[string]any)
	for path := range paths {
		if strings.HasPrefix(path, "/v1/open/icloud/") || strings.HasPrefix(path, "/v1/bot/") {
			t.Fatalf("public openapi exposes system-key route %s", path)
		}
	}

	components := spec["components"].(map[string]any)
	securitySchemes := components["securitySchemes"].(map[string]any)
	for _, name := range []string{"remailSystemKey", "systemKeyAuth"} {
		if _, ok := securitySchemes[name]; ok {
			t.Fatal("public openapi exposes the system-key security scheme")
		}
	}
	for _, rawTag := range spec["tags"].([]any) {
		if name := rawTag.(map[string]any)["name"]; name == "iCloud" || name == "Bot" {
			t.Fatalf("public openapi exposes internal tag %s", name)
		}
	}
}

func TestPublicOpenAPIDoesNotExposeTicketsGroup(t *testing.T) {
	spec := publicOpenAPISpec(t)
	for _, rawTag := range spec["tags"].([]any) {
		if rawTag.(map[string]any)["name"] == "Tickets" {
			t.Fatal("public openapi exposes the unused Tickets group")
		}
	}
}

func TestPublicOpenAPIDoesNotExposeInternalProductIDs(t *testing.T) {
	spec := publicOpenAPISpec(t)
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)

	for _, schemaName := range []string{"CreateOrderRequest", "CreateOrderBatchRequest"} {
		schema := schemas[schemaName].(map[string]any)
		properties := schema["properties"].(map[string]any)
		if _, ok := properties["emailSuffix"]; !ok {
			t.Fatalf("public openapi schema %s is missing emailSuffix", schemaName)
		}
		if _, ok := properties["productId"]; ok {
			t.Fatalf("public openapi schema %s exposes productId", schemaName)
		}
	}

	productSummary := schemas["ProjectProductSummary"].(map[string]any)
	properties := productSummary["properties"].(map[string]any)
	if _, ok := properties["id"]; ok {
		t.Fatal("public openapi schema ProjectProductSummary exposes its internal id")
	}

	order := schemas["Order"].(map[string]any)
	orderProperties := order["properties"].(map[string]any)
	if _, ok := orderProperties["projectProductId"]; ok {
		t.Fatal("public openapi schema Order exposes projectProductId")
	}
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
	return (strings.HasPrefix(path, "/v1/open/") && !strings.HasPrefix(path, "/v1/open/icloud/")) || path == "/v1/pickup" || strings.HasPrefix(path, "/v1/pickup/")
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
