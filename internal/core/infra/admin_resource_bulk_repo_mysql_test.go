package infra

import (
	"context"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
)

func TestAdminResourceBulkRepoOnlyPagesBusinessCandidatesMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminValidationOwner(t, db)
	resources := NewResourceRepo(db)
	var normalIDs []uint
	for index, status := range []domain.MicrosoftResourceStatus{
		domain.MicrosoftStatusNormal,
		domain.MicrosoftStatusAbnormal,
		domain.MicrosoftStatusNormal,
	} {
		root := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
		require.NoError(t, resources.CreateMicrosoft(context.Background(), root, &domain.MicrosoftResource{
			EmailAddress: "bulk-page-" + string(rune('a'+index)) + "@outlook.com",
			Password:     "secret", Status: status,
		}))
		if status == domain.MicrosoftStatusNormal {
			normalIDs = append(normalIDs, root.ID)
		}
	}

	repo := NewAdminResourceBulkRepo(db)
	filter := coreapp.AdminResourceBulkFilterValue{Status: domain.MicrosoftStatusNormal}
	throughID, err := repo.MaxCandidateID(context.Background(), filter, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, normalIDs[len(normalIDs)-1], throughID)
	ids, err := repo.ListCandidateIDs(context.Background(), filter, 0, throughID, 100, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, normalIDs, ids)
}
