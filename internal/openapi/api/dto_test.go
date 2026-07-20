package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConcurrencyLimitNullability(t *testing.T) {
	var omitted KeyCreateRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.Nil(t, omitted.ConcurrencyLimit)

	var zero KeyCreateRequest
	require.NoError(t, json.Unmarshal([]byte(`{"concurrencyLimit":0}`), &zero))
	require.NotNil(t, zero.ConcurrencyLimit)
	require.Zero(t, *zero.ConcurrencyLimit)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"concurrencyLimit":null}`))
	patch, ok := decodeKeyPatchRequest(ctx)
	require.True(t, ok)
	require.True(t, patch.ConcurrencySet)
	require.Nil(t, patch.ConcurrencyLimit)
}
