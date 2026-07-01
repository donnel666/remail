package infra

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

func TestSupplierApplicationRepoRejectsConcurrentReviewingApplicationsMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewSupplierApplicationRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role_level) VALUES (?, ?, ?, ?)",
		1,
		"user@test.local",
		"hash",
		10,
	).Error)

	first := &domain.SupplierApplication{
		ApplicantUserID: 1,
		Reason:          "first",
		Status:          domain.SupplierApplicationReviewing,
	}
	require.NoError(t, repo.CreateSupplierApplicationReviewing(context.Background(), first))

	second := &domain.SupplierApplication{
		ApplicantUserID: 1,
		Reason:          "second",
		Status:          domain.SupplierApplicationReviewing,
	}
	err := repo.CreateSupplierApplicationReviewing(context.Background(), second)
	require.ErrorIs(t, err, domain.ErrSupplierApplicationAlreadyReviewing)
}
