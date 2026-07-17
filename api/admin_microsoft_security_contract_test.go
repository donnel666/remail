package api

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type adminMicrosoftOperationExpectation struct {
	id       string
	method   string
	path     string
	mutation bool
	limit    bool
	search   bool
}

func TestAdminMicrosoftOpenAPISecurityContractMatrix(t *testing.T) {
	spec := loadAdminMicrosoftOpenAPI(t)
	operations := []adminMicrosoftOperationExpectation{
		{id: "API-AMR-Q01", method: "get", path: "/v1/admin/resources", limit: true, search: true},
		{id: "API-AMR-Q02", method: "get", path: "/v1/admin/resources/{resourceId}"},
		{id: "API-AMR-Q03", method: "get", path: "/v1/admin/users", limit: true, search: true},
		{id: "API-AMR-Q04", method: "get", path: "/v1/admin/resources/imports/{importId}"},
		{id: "API-AMR-Q05", method: "get", path: "/v1/admin/allocations", limit: true},
		{id: "API-AMR-Q06", method: "get", path: "/v1/admin/resources/{resourceId}/aliases", limit: true},
		{id: "API-AMR-Q07", method: "get", path: "/v1/admin/tasks", limit: true},
		{id: "API-AMR-Q08", method: "get", path: "/v1/admin/tasks/{taskId}"},
		{id: "API-AMR-Q09", method: "get", path: "/v1/admin/messages", limit: true, search: true},
		{id: "API-AMR-Q10", method: "get", path: "/v1/admin/messages/{messageId}"},
		{id: "API-AMR-Q11", method: "get", path: "/v1/admin/bindings", limit: true, search: true},
		{id: "API-AMR-Q12", method: "get", path: "/v1/admin/bindings/messages/{messageId}"},
		{id: "API-AMR-C01", method: "post", path: "/v1/admin/resources/imports", mutation: true},
		{id: "API-AMR-C02", method: "patch", path: "/v1/admin/resources/{resourceId}", mutation: true},
		{id: "API-AMR-C03", method: "put", path: "/v1/admin/resources/{resourceId}/credentials", mutation: true},
		{id: "API-AMR-C04", method: "post", path: "/v1/admin/resources/{resourceId}/validate", mutation: true},
		{id: "API-AMR-C05", method: "post", path: "/v1/admin/resources/{resourceId}/enable", mutation: true},
		{id: "API-AMR-C06", method: "post", path: "/v1/admin/resources/{resourceId}/disable", mutation: true},
		{id: "API-AMR-C07", method: "post", path: "/v1/admin/resources/{resourceId}/publish", mutation: true},
		{id: "API-AMR-C08", method: "post", path: "/v1/admin/resources/{resourceId}/unpublish", mutation: true},
		{id: "API-AMR-C09", method: "delete", path: "/v1/admin/resources/{resourceId}", mutation: true},
		{id: "API-AMR-C10", method: "post", path: "/v1/admin/resources/{resourceId}/recover", mutation: true},
		{id: "API-AMR-C11", method: "post", path: "/v1/admin/resources/{resourceId}/token/refresh", mutation: true},
		{id: "API-AMR-C12", method: "post", path: "/v1/admin/resources/{resourceId}/aliases", mutation: true},
		{id: "API-AMR-C13", method: "post", path: "/v1/admin/resources/{resourceId}/messages/fetch", mutation: true},
		{id: "API-AMR-C14", method: "post", path: "/v1/admin/resources/validations", mutation: true},
		{id: "API-AMR-C15", method: "post", path: "/v1/admin/resources/disable", mutation: true},
		{id: "API-AMR-C16", method: "post", path: "/v1/admin/resources/publish", mutation: true},
		{id: "API-AMR-C17", method: "post", path: "/v1/admin/resources/unpublish", mutation: true},
		{id: "API-AMR-C18", method: "post", path: "/v1/admin/resources/delete", mutation: true},
		{id: "API-AMR-C19", method: "post", path: "/v1/admin/resources/{resourceId}/projects/scan", mutation: true},
		{id: "API-AMR-C20", method: "post", path: "/v1/admin/resources/maintenance", mutation: true},
	}

	for _, expected := range operations {
		t.Run(expected.id, func(t *testing.T) {
			pathItem, ok := spec.Paths[expected.path]
			require.True(t, ok, "formal OpenAPI path is missing")
			operation, ok := pathItem[expected.method].(map[string]any)
			require.True(t, ok, "formal OpenAPI method is missing")
			require.NotEmpty(t, operation["operationId"])
			require.True(t, operationUsesCookieAuth(operation), "Session cookie authentication must be explicit")
			responses, ok := operation["responses"].(map[string]any)
			require.True(t, ok)
			for _, status := range []string{"401", "403"} {
				require.Contains(t, responses, status, "security response %s must be documented", status)
			}
			require.NotContains(t, responses, "429", "administrator Microsoft APIs do not depend on an application-level Redis limiter")
			parameterRefs := operationParameterRefs(operation)
			if expected.mutation {
				require.Contains(t, parameterRefs, "#/components/parameters/CsrfToken")
				require.Contains(t, parameterRefs, "#/components/parameters/AdminCommandIdempotencyKey")
			} else {
				require.NotContains(t, parameterRefs, "#/components/parameters/CsrfToken")
			}
			if expected.limit {
				require.EqualValues(t, 100, operationParameterMaximum(t, operation, "limit"))
			}
			if expected.search {
				require.EqualValues(t, 120, operationParameterMaximum(t, operation, "search"))
			}
		})
	}
}

func TestAdminMicrosoftOpenAPIResponseSchemasDoNotPublishSecretFields(t *testing.T) {
	spec := loadAdminMicrosoftOpenAPI(t)
	forbidden := map[string]struct{}{
		"password": {}, "passwordhash": {}, "clientid": {}, "refreshtoken": {}, "accesstoken": {},
		"objectkey": {}, "sourceobjectkey": {}, "claimtoken": {}, "dispatchtoken": {},
		"rawbody": {}, "rawenvelope": {}, "providerpayload": {},
	}
	for _, schemaName := range []string{
		"AdminMicrosoftResourceListResponse",
		"AdminMicrosoftResourceDetail",
		"AdminMicrosoftMutationResponse",
		"AdminMicrosoftImportResponse",
		"AdminMicrosoftAliasListResponse",
		"AdminAllocationListResponse",
		"AdminTaskListResponse",
		"AdminTaskView",
		"AdminTaskAcceptedResponse",
		"AdminMessageListResponse",
		"AdminMessageDetail",
		"AdminBindingMessageListResponse",
		"AdminAuxiliaryMessageDetail",
		"AdminUserListResponse",
	} {
		t.Run(schemaName, func(t *testing.T) {
			schema, ok := spec.Schemas[schemaName]
			require.True(t, ok, "response schema is missing")
			assertAdminMicrosoftSchemaHasNoSecretProperties(t, spec.Schemas, schema, forbidden, map[string]bool{})
		})
	}

	credentialSchema := spec.Schemas["AdminMicrosoftCredentialsInput"]
	properties, ok := credentialSchema["properties"].(map[string]any)
	require.True(t, ok)
	for _, field := range []string{"password", "clientId", "refreshToken"} {
		property, ok := properties[field].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, property["writeOnly"], "%s must remain write-only", field)
	}
}

func TestAdminMicrosoftOpenAPIAcceptedResponsesPublishFlatDurableMetadata(t *testing.T) {
	spec := loadAdminMicrosoftOpenAPI(t)
	tests := []struct {
		name     string
		required []string
	}{
		{
			name: "AdminTaskAcceptedResponse",
			required: []string{
				"taskId", "requestId", "status", "accepted", "task", "reused",
			},
		},
		{
			name: "AdminMicrosoftImportResponse",
			required: []string{
				"importId", "taskId", "requestId", "status", "accepted", "imported",
				"skipped", "lastSafeError", "task", "reused", "createdAt", "updatedAt",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema, ok := spec.Schemas[test.name]
			require.True(t, ok)
			properties, ok := schema["properties"].(map[string]any)
			require.True(t, ok)
			required, ok := schema["required"].([]any)
			require.True(t, ok)
			for _, field := range test.required {
				require.Contains(t, properties, field)
				require.Contains(t, required, field)
			}
		})
	}
}

type adminMicrosoftOpenAPISpec struct {
	Paths   map[string]map[string]any `yaml:"paths"`
	Schemas map[string]map[string]any
}

func loadAdminMicrosoftOpenAPI(t *testing.T) adminMicrosoftOpenAPISpec {
	t.Helper()
	content, err := os.ReadFile("openapi.yaml")
	require.NoError(t, err)
	var document struct {
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	require.NoError(t, yaml.Unmarshal(content, &document))
	return adminMicrosoftOpenAPISpec{Paths: document.Paths, Schemas: document.Components.Schemas}
}

func operationUsesCookieAuth(operation map[string]any) bool {
	security, ok := operation["security"].([]any)
	if !ok {
		return false
	}
	for _, item := range security {
		requirement, ok := item.(map[string]any)
		if ok {
			if _, exists := requirement["cookieAuth"]; exists {
				return true
			}
		}
	}
	return false
}

func operationParameterRefs(operation map[string]any) []string {
	parameters, _ := operation["parameters"].([]any)
	result := make([]string, 0, len(parameters))
	for _, item := range parameters {
		parameter, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if reference, ok := parameter["$ref"].(string); ok {
			result = append(result, reference)
		}
	}
	return result
}

func operationParameterMaximum(t *testing.T, operation map[string]any, name string) any {
	t.Helper()
	parameters, ok := operation["parameters"].([]any)
	require.True(t, ok)
	for _, item := range parameters {
		parameter, ok := item.(map[string]any)
		if !ok || parameter["name"] != name {
			continue
		}
		schema, ok := parameter["schema"].(map[string]any)
		require.True(t, ok)
		if name == "search" {
			return schema["maxLength"]
		}
		return schema["maximum"]
	}
	require.FailNow(t, fmt.Sprintf("parameter %s is missing", name))
	return nil
}

func assertAdminMicrosoftSchemaHasNoSecretProperties(
	t *testing.T,
	schemas map[string]map[string]any,
	node any,
	forbidden map[string]struct{},
	seen map[string]bool,
) {
	t.Helper()
	switch value := node.(type) {
	case map[string]any:
		if reference, ok := value["$ref"].(string); ok {
			const prefix = "#/components/schemas/"
			if strings.HasPrefix(reference, prefix) {
				name := strings.TrimPrefix(reference, prefix)
				if !seen[name] {
					seen[name] = true
					assertAdminMicrosoftSchemaHasNoSecretProperties(t, schemas, schemas[name], forbidden, seen)
				}
			}
		}
		if properties, ok := value["properties"].(map[string]any); ok {
			for name, property := range properties {
				_, blocked := forbidden[strings.ToLower(name)]
				require.False(t, blocked, "response schema publishes forbidden property %s", name)
				assertAdminMicrosoftSchemaHasNoSecretProperties(t, schemas, property, forbidden, seen)
			}
		}
		for _, keyword := range []string{"allOf", "anyOf", "oneOf", "items"} {
			if child, exists := value[keyword]; exists {
				assertAdminMicrosoftSchemaHasNoSecretProperties(t, schemas, child, forbidden, seen)
			}
		}
	case []any:
		for _, child := range value {
			assertAdminMicrosoftSchemaHasNoSecretProperties(t, schemas, child, forbidden, seen)
		}
	}
}
