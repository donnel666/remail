package gmail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocalGmailValidationTransitionsHealth(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-validation?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}))
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "validate@gmail.com", Identity: "validate@gmail.com", Password: "login-password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password", Status: LocalResourcePending,
	}).Error)

	checkedAt := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	service := NewService(db, nil)
	service.now = func() time.Time { return checkedAt }
	require.NoError(t, service.validateLocalResourceWith(context.Background(), root.ID,
		func(_ context.Context, email, appPassword string) localIMAPValidationResult {
			require.Equal(t, "validate@gmail.com", email)
			require.Equal(t, "app-password", appPassword)
			return localIMAPValidationResult{}
		}))
	var stored localResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, LocalResourceAvailable, stored.Status)
	require.Empty(t, stored.LastSafeError)
	require.NotNil(t, stored.LastCheckedAt)
	require.Equal(t, checkedAt, stored.LastCheckedAt.UTC())
	list, err := service.ListLocalResources(context.Background(), LocalResourceListFilter{Status: LocalResourceAvailable})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, LocalResourceAvailable, list.Items[0].Status)
	require.EqualValues(t, 1, list.Facets.Available)

	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", root.ID).
		Updates(map[string]any{"status": LocalResourcePending, "last_safe_error": ""}).Error)
	require.NoError(t, service.validateLocalResourceWith(context.Background(), root.ID,
		func(context.Context, string, string) localIMAPValidationResult {
			return localIMAPValidationResult{SafeError: "Gmail IMAP authentication failed. Check the app password.", Err: errors.New("authentication failed")}
		}))
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, LocalResourceAbnormal, stored.Status)
	require.Equal(t, "Gmail IMAP authentication failed. Check the app password.", stored.LastSafeError)
	require.NotContains(t, stored.LastSafeError, stored.AppPassword)

	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", root.ID).
		Updates(map[string]any{"status": LocalResourcePending, "last_safe_error": ""}).Error)
	temporaryErr := errors.New("temporary transport failure")
	err = service.validateLocalResourceWith(context.Background(), root.ID,
		func(context.Context, string, string) localIMAPValidationResult {
			return temporaryLocalIMAPValidation(temporaryErr)
		})
	require.ErrorIs(t, err, temporaryErr)
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, LocalResourcePending, stored.Status)
	require.Equal(t, "Gmail IMAP is temporarily unavailable.", stored.LastSafeError)
}

func TestLocalGmailValidationResultCannotOverwriteAdminDisable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-validation-fence?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}))
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "fenced@gmail.com", Identity: "fenced@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password", Status: LocalResourcePending,
	}).Error)

	service := NewService(db, nil)
	require.NoError(t, service.validateLocalResourceWith(context.Background(), root.ID,
		func(context.Context, string, string) localIMAPValidationResult {
			require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", root.ID).
				Update("status", LocalResourceDisabled).Error)
			return localIMAPValidationResult{}
		}))
	var stored localResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, LocalResourceDisabled, stored.Status)
}

func TestDefinitiveLocalGmailAuthenticationFailure(t *testing.T) {
	require.True(t, isDefinitiveLocalGmailAuthFailure(&imap.Error{
		Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed,
	}))
	require.False(t, isDefinitiveLocalGmailAuthFailure(errors.New("network timeout")))
}
