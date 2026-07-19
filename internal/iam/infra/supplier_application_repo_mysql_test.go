package infra

import (
	"context"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

func TestSupplierApplicationRepoRejectsConcurrentReviewingApplicationsMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewSupplierApplicationRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		1,
		"user@test.local",
		"hash",
		"user",
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

func TestSupplierApplicationApprovalRejectsDeletedUserMySQL(t *testing.T) {
	db := newMySQLTestDB(t)
	repo := NewSupplierApplicationRepo(db)
	ctx := context.Background()

	user := &domain.User{
		Email:        "deleted-approval@test.local",
		PasswordHash: "hash",
		Status:       domain.UserStatusActive,
		Role:         domain.RoleUser,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, user))
	application := &domain.SupplierApplication{
		ApplicantUserID: user.ID,
		Reason:          "approval race",
		Status:          domain.SupplierApplicationReviewing,
	}
	require.NoError(t, repo.CreateSupplierApplicationReviewing(ctx, application))

	require.NoError(t, db.Model(&UserModel{}).Where("id = ?", user.ID).Update("status", domain.UserStatusDeleted).Error)
	now := time.Now().UTC()
	application.Status = domain.SupplierApplicationApproved
	application.ReviewedAt = &now
	user.Role = domain.RoleSupplier
	err := repo.ApproveSupplierApplicationWithUserAndLog(ctx, application, user, &governancedomain.OperationLog{})
	require.ErrorIs(t, err, domain.ErrUserNotFound)

	stored, err := repo.FindSupplierApplicationByID(ctx, application.ID)
	require.NoError(t, err)
	require.Equal(t, domain.SupplierApplicationReviewing, stored.Status)
	var storedUser UserModel
	require.NoError(t, db.First(&storedUser, user.ID).Error)
	require.Equal(t, domain.RoleUser.String(), storedUser.Role)
	require.Equal(t, domain.UserStatusDeleted.String(), storedUser.Status)
}
