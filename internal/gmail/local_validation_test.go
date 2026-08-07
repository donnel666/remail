package gmail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/emersion/go-imap/v2"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocalGmailValidationTransitionsHealth(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-validation?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &governanceinfra.SystemLogModel{}))
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "validate@gmail.com", Identity: "validate@gmail.com", Password: "login-password",
		BindingEmail:    "binding@example.com",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password",
		CredentialRevision: 1, ValidationGeneration: 1, Status: LocalResourcePending,
	}).Error)

	checkedAt := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	queue := newLocalGmailValidationTestQueue(t)
	service := NewService(db, queue)
	service.now = func() time.Time { return checkedAt }
	require.NoError(t, service.validateLocalResourceWith(context.Background(), root.ID,
		func(_ context.Context, input localGmailValidationInput) localGmailValidationResult {
			require.Equal(t, "validate@gmail.com", input.Email)
			require.Equal(t, "login-password", input.Password)
			require.Equal(t, "binding@example.com", input.BindingEmail)
			require.Equal(t, "JBSWY3DPEHPK3PXP", input.TwoFactorSecret)
			require.Equal(t, "app-password", input.AppPassword)
			return localGmailValidationResult{
				TwoFactorSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ",
				AppPassword:     "abcdefghijklmnop",
			}
		}))
	var stored localResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, LocalResourceIdentifying, stored.Status)
	require.Equal(t, "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", stored.TwoFactorSecret)
	require.Equal(t, "abcdefghijklmnop", stored.AppPassword)
	require.EqualValues(t, 2, stored.CredentialRevision)
	require.Empty(t, stored.LastSafeError)
	require.NotNil(t, stored.LastCheckedAt)
	require.Equal(t, checkedAt, stored.LastCheckedAt.UTC())
	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", root.ID).
		Update("status", localResourceRollbackNormal).Error)
	list, err := service.ListLocalResources(context.Background(), LocalResourceListFilter{Status: LocalResourceNormal})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, LocalResourceNormal, list.Items[0].Status)
	require.Equal(t, "binding@example.com", list.Items[0].BindingEmail)
	require.EqualValues(t, 1, list.Facets.Normal)

	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", root.ID).
		Updates(map[string]any{"status": LocalResourcePending, "validation_failures": 0, "last_safe_error": ""}).Error)
	require.NoError(t, service.validateLocalResourceWith(context.Background(), root.ID,
		func(context.Context, localGmailValidationInput) localGmailValidationResult {
			return localGmailValidationResult{SafeError: "Gmail account password is incorrect.", Err: errors.New("authentication failed")}
		}))
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, LocalResourceAbnormal, stored.Status)
	require.Equal(t, "Gmail account password is incorrect.", stored.LastSafeError)
	require.NotContains(t, stored.LastSafeError, stored.AppPassword)
	require.Equal(t, "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", stored.TwoFactorSecret)
	require.Equal(t, "abcdefghijklmnop", stored.AppPassword)

	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", root.ID).
		Updates(map[string]any{"status": LocalResourcePending, "validation_failures": 0, "last_safe_error": ""}).Error)
	temporaryErr := errors.New("temporary transport failure")
	err = service.validateLocalResourceWith(context.Background(), root.ID,
		func(context.Context, localGmailValidationInput) localGmailValidationResult {
			return localGmailValidationResult{SafeError: "Gmail validation is temporarily unavailable.", Temporary: true, Err: temporaryErr}
		})
	require.NoError(t, err)
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, LocalResourcePending, stored.Status)
	require.EqualValues(t, 1, stored.ValidationFailures)
	require.EqualValues(t, 2, stored.ValidationGeneration)
	require.Equal(t, "Gmail validation is temporarily unavailable.", stored.LastSafeError)

	for wantFailures := 2; wantFailures <= localGmailValidationMaxFailures; wantFailures++ {
		require.NoError(t, service.validateLocalResourceWith(context.Background(), root.ID,
			func(context.Context, localGmailValidationInput) localGmailValidationResult {
				return localGmailValidationResult{SafeError: "Gmail validation is temporarily unavailable.", Temporary: true, Err: temporaryErr}
			}))
		require.NoError(t, db.First(&stored, root.ID).Error)
		require.EqualValues(t, wantFailures, stored.ValidationFailures)
	}
	require.Equal(t, LocalResourceAbnormal, stored.Status)
	require.EqualValues(t, 3, stored.ValidationGeneration)
	require.NoError(t, db.First(&root, root.ID).Error)
	require.EqualValues(t, 6, root.Version)
}

func TestLocalGmailValidationResultCannotOverwriteAdminDisable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-validation-fence?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &governanceinfra.SystemLogModel{}))
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "fenced@gmail.com", Identity: "fenced@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password",
		CredentialRevision: 1, ValidationGeneration: 1, Status: LocalResourcePending,
	}).Error)

	service := NewService(db, newLocalGmailValidationTestQueue(t))
	require.NoError(t, service.validateLocalResourceWith(context.Background(), root.ID,
		func(context.Context, localGmailValidationInput) localGmailValidationResult {
			require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", root.ID).
				Update("status", LocalResourceDisabled).Error)
			return localGmailValidationResult{
				TwoFactorSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", AppPassword: "abcdefghijklmnop",
			}
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

func TestLocalGmailValidationTaskFencesGenerationRevisionAndOwner(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-validation-task-fences?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}))
	root := resourceRootModel{Type: "gmail", OwnerUserID: 7, Version: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 7,
		Email: "stale@gmail.com", Identity: "stale@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password",
		CredentialRevision: 2, ValidationGeneration: 2, Status: LocalResourceValidating,
	}).Error)
	service := NewService(db, nil)
	validationCalls := 0
	validate := func(context.Context, localGmailValidationInput) localGmailValidationResult {
		validationCalls++
		return localGmailValidationResult{
			TwoFactorSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", AppPassword: "abcdefghijklmnop",
		}
	}
	for _, task := range []localResourceValidationTask{
		{ResourceID: root.ID, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 2},
		{ResourceID: root.ID, OwnerUserID: 7, ValidationGeneration: 2, ExpectedCredentialRevision: 1},
		{ResourceID: root.ID, OwnerUserID: 8, ValidationGeneration: 2, ExpectedCredentialRevision: 2},
	} {
		require.NoError(t, service.processLocalResourceValidationWith(context.Background(), task, validate))
	}
	require.Zero(t, validationCalls)
	var stored localResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, LocalResourceValidating, stored.Status)
	require.EqualValues(t, 2, stored.ValidationGeneration)
	require.EqualValues(t, 2, stored.CredentialRevision)
}

func TestLocalGmailValidationPersistsRotatedCredentialsBeforeHistoryEnqueue(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-validation-history-dependency?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}))
	root := resourceRootModel{Type: "gmail", OwnerUserID: 7, Version: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 7,
		Email: "dependency@gmail.com", Identity: "dependency@gmail.com", AppPassword: "app-password",
		CredentialRevision: 2, ValidationGeneration: 3, Status: LocalResourceValidating,
	}).Error)
	service := NewService(db, nil)
	err = service.processLocalResourceValidationWith(context.Background(), localResourceValidationTask{
		ResourceID: root.ID, OwnerUserID: 7, ValidationGeneration: 3, ExpectedCredentialRevision: 2,
	}, func(context.Context, localGmailValidationInput) localGmailValidationResult {
		return localGmailValidationResult{
			TwoFactorSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", AppPassword: "abcdefghijklmnop",
		}
	})
	require.ErrorIs(t, err, ErrLocalValidationDependency)

	var stored localResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, LocalResourceIdentifying, stored.Status)
	require.Equal(t, "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", stored.TwoFactorSecret)
	require.Equal(t, "abcdefghijklmnop", stored.AppPassword)
	require.EqualValues(t, 3, stored.CredentialRevision)
}

func TestLocalGmailValidationPersistsAuthoritativePartialRotationForRetry(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-validation-partial?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &governanceinfra.SystemLogModel{}))
	root := resourceRootModel{Type: "gmail", OwnerUserID: 7, Version: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 7,
		Email: "partial@gmail.com", Identity: "partial@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "old-app-password",
		CredentialRevision: 1, ValidationGeneration: 1, Status: LocalResourcePending,
	}).Error)
	service := NewService(db, nil)
	require.NoError(t, service.validateLocalResourceWith(context.Background(), root.ID,
		func(context.Context, localGmailValidationInput) localGmailValidationResult {
			return localGmailValidationResult{
				TwoFactorSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", TwoFactorAuthoritative: true,
				AppPasswordRevoked: true,
				SafeError:          "Gmail App Password creation is temporarily unavailable.", Temporary: true,
				Err: errors.New("app password page unavailable"),
			}
		}))

	var stored localResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, LocalResourcePending, stored.Status)
	require.Equal(t, "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", stored.TwoFactorSecret)
	require.Empty(t, stored.AppPassword)
	require.EqualValues(t, 2, stored.CredentialRevision)
	require.EqualValues(t, 2, stored.ValidationGeneration)
}

func newLocalGmailValidationTestQueue(t *testing.T) *asynq.Client {
	t.Helper()
	server := miniredis.RunT(t)
	queue := asynq.NewClient(asynq.RedisClientOpt{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, queue.Close()) })
	return queue
}
