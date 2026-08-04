package gmail

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocalResourcesAPIKeepsCredentialsWriteOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:gmail-local-api-safe-list?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&localResourceModel{}))
	require.NoError(t, db.Create(&localResourceModel{
		Email: "safe@gmail.com", Identity: "safe@gmail.com", Password: "login-password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop", Status: LocalResourceAvailable,
	}).Error)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/gmail/resources", nil)

	(&handler{service: NewService(db, nil)}).localResources(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	for _, secret := range []string{"login-password", "JBSWY3DPEHPK3PXP", "abcdefghijklmnop"} {
		require.NotContains(t, recorder.Body.String(), secret)
	}
	require.Contains(t, recorder.Body.String(), `"passwordConfigured":true`)
}
