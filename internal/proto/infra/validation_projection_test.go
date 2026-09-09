package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProtoValidationFailureProjectionUsesCurrentFactsEverywhere(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	ids := map[string][]uint{}
	physical := map[uint]string{}
	payloads := map[uint][]byte{}
	for i, test := range []struct {
		name, stored, kind, outcome, displayed string
		generation, revision                   uint64
	}{
		{"legacy uncertain", domain.StatusPending, maintenanceKindValidation, maintenanceUncertain, domain.StatusValidationFailed, 2, 2},
		{"failed", domain.StatusPending, maintenanceKindValidation, maintenanceFailed, domain.StatusValidationFailed, 2, 2},
		{"next generation", domain.StatusPending, maintenanceKindValidation, maintenanceFailed, domain.StatusPending, 3, 2},
		{"new credentials", domain.StatusPending, maintenanceKindValidation, maintenanceFailed, domain.StatusPending, 2, 3},
		{"history is independent", domain.StatusPending, maintenanceKindHistory, maintenanceFailed, domain.StatusPending, 2, 2},
		{"queued", domain.StatusPending, maintenanceKindValidation, maintenanceQueued, domain.StatusPending, 2, 2},
		{"running", domain.StatusPending, maintenanceKindValidation, maintenanceRunning, domain.StatusPending, 2, 2},
		{"no validation", domain.StatusPending, "", "", domain.StatusPending, 2, 2},
		{"normal wins", domain.StatusNormal, maintenanceKindValidation, maintenanceFailed, domain.StatusNormal, 2, 2},
		{"permanent failure wins", domain.StatusAbnormal, maintenanceKindValidation, maintenanceFailed, domain.StatusAbnormal, 2, 2},
		{"disabled wins", domain.StatusDisabled, maintenanceKindValidation, maintenanceFailed, domain.StatusDisabled, 2, 2},
		{"deleted wins", domain.StatusDeleted, maintenanceKindValidation, maintenanceFailed, domain.StatusDeleted, 2, 2},
	} {
		email := fmt.Sprintf("projection%d@proton.me", i)
		id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: email, Password: "fixture-password"})
		require.NoError(t, err)
		require.NoError(t, s.DB.Model(&Resource{}).Where("id = ?", id).Updates(map[string]any{
			"status": test.stored, "validation_generation": test.generation, "credential_revision": test.revision, "for_sale": true,
		}).Error)
		if test.kind != "" {
			require.NoError(t, s.DB.Create(&MaintenanceRun{ResourceID: id, ValidationGeneration: 2, CredentialRevision: 2, Kind: test.kind, Status: test.outcome}).Error)
		}
		payload, err := encodeSession(id, test.revision, testPKLSession(email))
		require.NoError(t, err)
		require.NoError(t, s.DB.Create(&sessionRecord{ResourceID: id, CredentialRevision: test.revision, Version: 1, Payload: payload}).Error)
		payloads[id], physical[id] = payload, test.stored
		ids[test.displayed] = append(ids[test.displayed], id)
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err, test.name)
		require.Equal(t, test.displayed, row.Status, test.name)
		require.True(t, row.ForSale, test.name)
	}

	page, err := s.ListResources(ctx, ResourceFilter{Limit: 100})
	require.NoError(t, err)
	require.EqualValues(t, len(physical)-1, page.Total, "default listing still excludes deleted resources")
	for _, row := range page.Items {
		require.Contains(t, ids[row.Status], row.ID)
	}
	require.EqualValues(t, len(ids[domain.StatusValidationFailed]), page.Facets.ValidationFailed)
	require.Equal(t, page.Facets.ValidationFailed, page.Facets.Status.ValidationFailed)
	require.EqualValues(t, len(ids[domain.StatusPending]), page.Facets.Pending)
	require.EqualValues(t, len(ids[domain.StatusAbnormal]), page.Facets.Abnormal)
	encoded, err := json.Marshal(page.Facets)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"validation_failed":2`)
	for _, status := range []string{domain.StatusValidationFailed, domain.StatusPending, domain.StatusNormal, domain.StatusAbnormal, domain.StatusDisabled, domain.StatusDeleted} {
		filtered, err := s.ListResources(ctx, ResourceFilter{Status: status, Limit: 100})
		require.NoError(t, err, status)
		actual := make([]uint, 0, len(filtered.Items))
		for _, row := range filtered.Items {
			require.Equal(t, status, row.Status)
			actual = append(actual, row.ID)
		}
		require.ElementsMatch(t, ids[status], actual, status)
		require.EqualValues(t, len(ids[status]), filtered.Total, status)
		require.EqualValues(t, len(ids[status]), filtered.Facets.ForSale.Yes, status)
	}

	// Read projection does not rewrite physical state or touch stored PKL.
	for id, status := range physical {
		var row Resource
		require.NoError(t, s.DB.First(&row, id).Error)
		require.Equal(t, status, row.Status)
		require.True(t, row.ForSale)
		var session sessionRecord
		require.NoError(t, s.DB.First(&session, id).Error)
		require.Equal(t, payloads[id], session.Payload)
	}
	failedID := ids[domain.StatusValidationFailed][0]
	require.ErrorIs(t, s.SetStatus(ctx, failedID, nil, domain.StatusValidationFailed), domain.ErrInvalidResource)

	// The ordinary status predicate stays an indexed physical-column equality.
	query := resourceFilterQuery(s.DB.Session(&gorm.Session{DryRun: true}), ResourceFilter{Status: domain.StatusNormal}, "").Find(&[]Resource{})
	require.NotContains(t, query.Statement.SQL.String(), "validation_run")
	require.Contains(t, query.Statement.SQL.String(), "status = ?")

	// Exercise the actual bulk-filter path with an in-memory Redis and fake queue;
	// only an explicit validate command starts a new generation.
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s.Redis = rdb
	queue := &protoQueueStub{}
	s.Queue = queue
	bulk, err := s.SubmitBulk(ctx, "validate", BulkSelection{Mode: "filter", Filter: ResourceFilter{Status: domain.StatusValidationFailed}}, 7, nil, "projection-retry", "projection-test")
	require.NoError(t, err)
	require.Equal(t, 2, bulk.Requested)
	require.Len(t, queue.tasks, 1)
	var task BulkTask
	require.NoError(t, json.Unmarshal(queue.tasks[0].Payload(), &task))
	require.NoError(t, s.ProcessBulk(ctx, task))
	for _, id := range ids[domain.StatusValidationFailed] {
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.Equal(t, domain.StatusPending, row.Status)
		require.EqualValues(t, 3, row.ValidationGeneration)
		require.True(t, row.ForSale)
		var session sessionRecord
		require.NoError(t, s.DB.First(&session, id).Error)
		require.Equal(t, payloads[id], session.Payload)
	}
	page, err = s.ListResources(ctx, ResourceFilter{Status: domain.StatusValidationFailed, Limit: 100})
	require.NoError(t, err)
	require.Zero(t, page.Total)
}
