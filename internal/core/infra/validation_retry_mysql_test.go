package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestValidationMailboxBusyCooldownLetsOtherResourcesRunMySQL(t *testing.T) {
	for _, enterprise := range []bool{false, true} {
		t.Run(fmt.Sprintf("enterprise_%t", enterprise), func(t *testing.T) {
			db := newCoreMySQLTestDB(t)
			insertAdminValidationOwner(t, db)
			ctx := context.Background()
			resources := NewResourceRepo(db)
			validations := NewResourceValidationRepo(db)
			suffix := "outlook.com"
			if enterprise {
				suffix = "tenant.onmicrosoft.com"
			}
			busy := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
			require.NoError(t, resources.CreateMicrosoft(ctx, busy, &domain.MicrosoftResource{
				EmailAddress: "busy@" + suffix, Password: "secret", Status: domain.MicrosoftStatusPending,
			}))
			makeValidationAssignmentsReady(t, db)
			tasks, err := validations.ClaimPendingValidations(ctx, 1, enterprise)
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			require.True(t, mustActivateValidation(t, validations, tasks[0]))
			require.NoError(t, validations.ApplyMicrosoftResult(ctx, tasks[0], coreapp.MicrosoftValidationResult{
				Category: "recovery_mailbox_busy", Retryable: true, SafeMessage: "another flow is waiting for its code",
			}, nil))
			ready := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
			require.NoError(t, resources.CreateMicrosoft(ctx, ready, &domain.MicrosoftResource{
				EmailAddress: "ready@" + suffix, Password: "secret", Status: domain.MicrosoftStatusPending,
			}))
			makeValidationAssignmentsReady(t, db)
			table := microsoftResourceTable(domain.MicrosoftResourceType(enterprise))
			runtimeconfig.Set("recovery_code_lease_minutes", "20")
			t.Cleanup(func() { runtimeconfig.Delete("recovery_code_lease_minutes") })
			require.NoError(t, db.Table(table).Where("id=?", busy.ID).Update("updated_at", time.Now().Add(-19*time.Minute)).Error)
			tasks, err = validations.ClaimPendingValidations(ctx, 4, enterprise)
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			require.Equal(t, ready.ID, tasks[0].ResourceID, "a lower-ID busy mailbox must not monopolize dispatch")
			var stored MicrosoftResourceModel
			require.NoError(t, db.Table(table).First(&stored, busy.ID).Error)
			require.Zero(t, stored.ValidationFailures)
			require.Equal(t, validationRecoveryMailboxBusy, stored.LastSafeError)
			require.NoError(t, db.Table(table).Where("id=?", busy.ID).Update("updated_at", time.Now().Add(-21*time.Minute)).Error)
			tasks, err = validations.ClaimPendingValidations(ctx, 1, enterprise)
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			require.Equal(t, busy.ID, tasks[0].ResourceID, "expired cooldowns must return to normal dispatch")
		})
	}
}

func TestValidationTransientFailuresBackOffWithoutChangingUpdatedAtMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminValidationOwner(t, db)
	ctx := context.Background()
	resources := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resources.CreateMicrosoft(ctx, root, &domain.MicrosoftResource{
		EmailAddress: "retry@outlook.com", Password: "secret", Status: domain.MicrosoftStatusPending,
	}))
	makeValidationAssignmentsReady(t, db)
	for attempt, delay := range []time.Duration{validationRetryDelay, validationRepeatedRetryDelay} {
		tasks, err := validations.ClaimPendingValidations(ctx, 1, false)
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		require.True(t, mustActivateValidation(t, validations, tasks[0]))
		require.NoError(t, validations.ApplyMicrosoftResult(ctx, tasks[0], coreapp.MicrosoftValidationResult{
			Category: "request", Retryable: true, SafeMessage: "temporary request failure",
		}, nil))
		var stored MicrosoftResourceModel
		require.NoError(t, db.First(&stored, root.ID).Error)
		require.Equal(t, attempt+1, stored.ValidationFailures)
		require.Equal(t, "pending", stored.Status)
		require.WithinDuration(t, time.Now(), stored.UpdatedAt, 3*time.Second)
		require.NoError(t, db.Model(&MicrosoftResourceModel{}).Where("id=?", root.ID).Update("updated_at", time.Now().Add(-delay+5*time.Second)).Error)
		tasks, err = validations.ClaimPendingValidations(ctx, 1, false)
		require.NoError(t, err)
		require.Empty(t, tasks, "the same transient failure must not immediately run again")
		require.NoError(t, db.Model(&MicrosoftResourceModel{}).Where("id=?", root.ID).Update("updated_at", time.Now().Add(-delay-time.Second)).Error)
	}
	tasks, err := validations.ClaimPendingValidations(ctx, 1, false)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	zero, err := validations.ClaimPendingValidations(ctx, 0, false)
	require.NoError(t, err)
	require.Empty(t, zero)
}

func TestDomainValidationTransientFailureAlsoBacksOffMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminValidationOwner(t, db)
	ctx := context.Background()
	resources := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	root := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	require.NoError(t, resources.CreateDomain(ctx, root, &domain.MailDomainResource{
		Domain: "retry-domain.test", MailServerID: 1, Purpose: domain.PurposeNotSale, Status: domain.DomainStatusPending,
	}))
	makeValidationAssignmentsReady(t, db)
	tasks, err := validations.ClaimPendingValidations(ctx, 1, false)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.True(t, mustActivateValidation(t, validations, tasks[0]))
	require.NoError(t, validations.ApplyDomainResult(ctx, tasks[0], coreapp.DomainValidationResult{
		Retryable: true, Category: "request", SafeMessage: "DNS temporarily unavailable",
	}, nil))
	require.NoError(t, db.Model(&DomainResourceModel{}).Where("id=?", root.ID).Update("updated_at", time.Now().Add(-30*time.Second)).Error)
	tasks, err = validations.ClaimPendingValidations(ctx, 1, false)
	require.NoError(t, err)
	require.Empty(t, tasks)
	require.NoError(t, db.Model(&DomainResourceModel{}).Where("id=?", root.ID).Update("updated_at", time.Now().Add(-validationRetryDelay-time.Second)).Error)
	tasks, err = validations.ClaimPendingValidations(ctx, 1, false)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, root.ID, tasks[0].ResourceID)
}

func TestValidationSingleFreeSlotDoesNotOverbookForFairnessMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminValidationOwner(t, db)
	ctx := context.Background()
	resources := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resources.CreateMicrosoft(ctx, root, &domain.MicrosoftResource{
		EmailAddress: "single-slot@outlook.com", Password: "secret", Status: domain.MicrosoftStatusPending,
	}))
	domainRoot := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	require.NoError(t, resources.CreateDomain(ctx, domainRoot, &domain.MailDomainResource{
		Domain: "single-slot.test", MailServerID: 1, Purpose: domain.PurposeNotSale, Status: domain.DomainStatusPending,
	}))
	makeValidationAssignmentsReady(t, db)
	seen := make(map[domain.ResourceType]bool)
	for range validationDispatchCycle {
		tasks, err := validations.ClaimPendingValidations(ctx, 1, false)
		require.NoError(t, err)
		require.Len(t, tasks, 1, "fairness must never create a second task without a second slot")
		seen[tasks[0].ResourceType] = true
	}
	require.True(t, seen[domain.ResourceTypeMicrosoft])
	require.True(t, seen[domain.ResourceTypeDomain])
}
