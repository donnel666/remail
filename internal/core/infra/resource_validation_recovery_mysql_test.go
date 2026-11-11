package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestValidationRecoveryPreservesLiveTasksAndRecoversRedisLossMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	queueClient := asynq.NewClient(asynq.RedisClientOpt{Addr: server.Addr()})
	t.Cleanup(func() {
		require.NoError(t, queueClient.Close())
		require.NoError(t, client.Close())
	})
	ctx := context.Background()
	now := time.Now().UTC()
	resources := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db, client)
	insertAdminValidationOwner(t, db)
	for index := range 3 {
		root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
		require.NoError(t, resources.CreateMicrosoft(ctx, root, &domain.MicrosoftResource{
			EmailAddress: fmt.Sprintf("recover%d@outlook.com", index), Password: "secret", Status: domain.MicrosoftStatusPending,
		}))
	}
	makeValidationAssignmentsReady(t, db)
	tasks, err := validations.ClaimPendingValidations(ctx, 3, false)
	require.NoError(t, err)
	require.Len(t, tasks, 3)
	for _, task := range tasks {
		_, err := validations.MarkValidationDispatched(ctx, task)
		require.NoError(t, err)
	}
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET updated_at=? WHERE id IN ?", now.Add(-time.Hour), []uint{tasks[0].ResourceID, tasks[1].ResourceID}).Error)
	accepted, err := NewResourceValidationQueue(queueClient, client).EnqueueResourceValidation(ctx, tasks[1])
	require.NoError(t, err)
	require.True(t, accepted)

	require.NoError(t, validations.recoverStaleValidations(ctx, now.Add(31*time.Second), false))
	var orphan, live, fresh MicrosoftResourceModel
	require.NoError(t, db.First(&orphan, tasks[0].ResourceID).Error)
	require.Equal(t, "pending", orphan.Status)
	require.Equal(t, tasks[0].ValidationGeneration+1, orphan.ValidationGeneration)
	require.ErrorIs(t, validations.ApplyMicrosoftResult(ctx, tasks[0], coreapp.MicrosoftValidationResult{Valid: true}, nil), coreapp.ErrValidationResultStale)
	require.NoError(t, db.First(&live, tasks[1].ResourceID).Error)
	require.Equal(t, "validating", live.Status, "a live scheduled task must survive recovery regardless of age")
	require.NoError(t, db.First(&fresh, tasks[2].ResourceID).Error)
	require.Equal(t, "validating", fresh.Status, "recent execution must survive a missing queue record")

	server.FlushAll()
	require.NoError(t, validations.recoverStaleValidations(ctx, now.Add(62*time.Second), false))
	require.NoError(t, db.First(&live, tasks[1].ResourceID).Error)
	require.Equal(t, "pending", live.Status)
	require.Equal(t, tasks[1].ValidationGeneration+1, live.ValidationGeneration)
}

func TestValidationRecoveryCannotReleaseNewExecutionMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminValidationOwner(t, db)
	ctx := context.Background()
	resources := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db)
	root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resources.CreateMicrosoft(ctx, root, &domain.MicrosoftResource{
		EmailAddress: "started@outlook.com", Password: "secret", Status: domain.MicrosoftStatusPending,
	}))
	makeValidationAssignmentsReady(t, db)
	tasks, err := validations.ClaimPendingValidations(ctx, 1, false)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	_, err = validations.MarkValidationDispatched(ctx, tasks[0])
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET updated_at=? WHERE id=?", time.Now().Add(-time.Hour), root.ID).Error)
	before := time.Now().Add(-validationTaskTimeout - time.Minute)

	require.NoError(t, validations.StartValidation(ctx, tasks[0]))
	changed, err := validations.releaseValidation(ctx, tasks[0], before)
	require.NoError(t, err)
	require.False(t, changed, "a worker starting after the recovery scan must refresh its age atomically")
	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, root.ID).Error)
	require.Equal(t, "validating", stored.Status)
	require.Equal(t, tasks[0].ValidationGeneration, stored.ValidationGeneration)
}

func TestValidationRecoveryIncludesDomainAndKeepsEnterpriseSeparateMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx := context.Background()
	now := time.Now().UTC()
	insertAdminValidationOwner(t, db)
	resources := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db, client)
	domainRoot := &domain.EmailResource{Type: domain.ResourceTypeDomain, OwnerUserID: 1}
	require.NoError(t, resources.CreateDomain(ctx, domainRoot, &domain.MailDomainResource{
		Domain: "recovery.example.com", MailServerID: 1, Purpose: domain.PurposeNotSale, Status: domain.DomainStatusValidating,
	}))
	enterpriseRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	require.NoError(t, resources.CreateMicrosoft(ctx, enterpriseRoot, &domain.MicrosoftResource{
		EmailAddress: "recover@tenant.onmicrosoft.com", Password: "secret", Status: domain.MicrosoftStatusValidating,
	}))
	require.NoError(t, db.Exec("UPDATE domain_resources SET updated_at=? WHERE id=?", now.Add(-time.Hour), domainRoot.ID).Error)
	require.NoError(t, db.Exec("UPDATE microsoft_enterprise_resources SET updated_at=? WHERE id=?", now.Add(-time.Hour), enterpriseRoot.ID).Error)
	require.NoError(t, validations.recoverStaleValidations(ctx, now, false))
	var status string
	require.NoError(t, db.Raw("SELECT status FROM domain_resources WHERE id=?", domainRoot.ID).Scan(&status).Error)
	require.Equal(t, "pending", status)
	require.NoError(t, db.Raw("SELECT status FROM microsoft_enterprise_resources WHERE id=?", enterpriseRoot.ID).Scan(&status).Error)
	require.Equal(t, "validating", status)
	require.NoError(t, validations.recoverStaleValidations(ctx, now, true))
	require.NoError(t, db.Raw("SELECT status FROM microsoft_enterprise_resources WHERE id=?", enterpriseRoot.ID).Scan(&status).Error)
	require.Equal(t, "pending", status)
}

func TestValidationRecoveryPagesPastFreshAssignmentsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx := context.Background()
	now := time.Now().UTC()
	insertAdminValidationOwner(t, db)
	resources := NewResourceRepo(db)
	validations := NewResourceValidationRepo(db, client)
	var lastID uint
	for index := range 101 {
		root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
		require.NoError(t, resources.CreateMicrosoft(ctx, root, &domain.MicrosoftResource{
			EmailAddress: fmt.Sprintf("page%d@outlook.com", index), Password: "secret", Status: domain.MicrosoftStatusValidating,
		}))
		lastID = root.ID
	}
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET updated_at=? WHERE id=?", now.Add(-time.Hour), lastID).Error)
	require.NoError(t, validations.recoverStaleValidations(ctx, now, false))
	var last MicrosoftResourceModel
	require.NoError(t, db.First(&last, lastID).Error)
	require.Equal(t, "validating", last.Status, "the first sweep must inspect at most 100 resource rows")
	require.NoError(t, validations.recoverStaleValidations(ctx, now.Add(31*time.Second), false))
	require.NoError(t, db.First(&last, lastID).Error)
	require.Equal(t, "pending", last.Status, "fresh or live earlier rows must not starve later orphans")
}
