package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminDaumOpenAPISecurityContract(t *testing.T) {
	spec := loadAdminMicrosoftOpenAPI(t)
	mutations := 0
	for path, pathItem := range spec.Paths {
		if !strings.HasPrefix(path, "/v1/admin/daum/resources") {
			continue
		}
		for _, method := range []string{"get", "post", "patch", "put", "delete"} {
			operation, exists := pathItem[method].(map[string]any)
			if !exists {
				continue
			}
			mutation := method != "get"
			if mutation {
				mutations++
			}
			t.Run(method+" "+path, func(t *testing.T) {
				require.True(t, operationUsesCookieAuth(operation), "administrator routes require a session")
				responses, ok := operation["responses"].(map[string]any)
				require.True(t, ok)
				require.Contains(t, responses, "401")
				require.Contains(t, responses, "403")
				parameters := operationParameterRefs(operation)
				if mutation {
					require.Contains(t, parameters, "#/components/parameters/CsrfToken", "CSRF middleware rejects mutation requests without the header")
					require.Contains(t, parameters, "#/components/parameters/IdempotencyKey")
				} else {
					require.NotContains(t, parameters, "#/components/parameters/CsrfToken")
				}
			})
		}
	}
	require.Positive(t, mutations, "administrator Daum mutation contracts must exist")
}
