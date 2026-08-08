package icloud

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireICloudIdempotencyKey(t *testing.T) {
	for _, test := range []struct {
		name   string
		header string
		valid  bool
	}{
		{name: "missing"},
		{name: "blank", header: "   "},
		{name: "too long", header: strings.Repeat("x", 129)},
		{name: "valid", header: "icloud-command-1", valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/icloud/resources/1/validation", nil)
			ctx.Request.Header.Set("Idempotency-Key", test.header)

			if got := requireICloudIdempotencyKey(ctx); got != test.valid {
				t.Fatalf("valid = %v, want %v", got, test.valid)
			}
			if !test.valid && recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}
}
