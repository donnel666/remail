package infra

import (
	"context"
	"strings"
	"testing"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftEnterpriseImportFormat(t *testing.T) {
	parser := NewTXTParser()
	valid := "user@tenant.onmicrosoft.com---- password ----client-id----" + strings.Repeat("r", 1800)
	invalid := []string{
		"user@outlook.com----password----client----rt",
		"user@tenant.onmicrosoft.com.evil.test----password----client----rt",
		"user@tenant.onmicrosoft.com----password",
		"user@tenant.onmicrosoft.com----password----client----rt----recovery@example.com",
	}
	lines, failures, err := parser.ParseMicrosoftImport(strings.Join(append([]string{valid}, invalid...), "\n"), domain.ImportErrorStrategySkip, true)
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Len(t, failures, len(invalid))
	require.Equal(t, " password ", lines[0].Password)
	require.Len(t, lines[0].RefreshToken, 1800)
	require.Empty(t, lines[0].BindingAddress)
	_, _, err = parser.ParseMicrosoftImport(valid, domain.ImportErrorStrategyAbort, false)
	require.ErrorIs(t, err, domain.ErrInvalidImportFormat)
	_, _, err = parser.ParseMicrosoftImport(valid+"\n"+invalid[0], domain.ImportErrorStrategyAbort, true)
	require.ErrorIs(t, err, domain.ErrInvalidImportFormat)
}

func TestMicrosoftEnterpriseResourcesAndFiltersMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.NoError(t, db.Exec("INSERT INTO users(id,email,password_hash,role,status) VALUES(1,'enterprise-owner@test.local','hash','supplier','active')").Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources(id,type,owner_user_id) VALUES(101,'microsoft_enterprise',1),(102,'microsoft',1)").Error)
	require.NoError(t, db.Exec("INSERT INTO microsoft_enterprise_resources(id,email_address,email_domain,password,client_id,refresh_token,status) VALUES(101,'user@tenant.onmicrosoft.com','tenant.onmicrosoft.com','password','client',?,'normal')", strings.Repeat("r", 1800)).Error)
	require.NoError(t, db.Exec("INSERT INTO microsoft_resources(id,email_address,email_domain,password,client_id,refresh_token,status) VALUES(102,'user@outlook.com','outlook.com','password','client','rt','normal')").Error)
	for _, enterprise := range []bool{false, true} {
		filter := coreapp.ResourceListFilter{ResourceType: domain.ResourceTypeMicrosoft, Enterprise: enterprise}
		var ids []uint
		require.NoError(t, NewResourceRepo(db).listQuery(context.Background(), 1, filter).Pluck("email_resources.id", &ids).Error)
		want := uint(102)
		if enterprise {
			want = 101
		}
		require.Equal(t, []uint{want}, ids)
		ids = nil
		require.NoError(t, applyMicrosoftBulkFilter(db, 1, filter).Pluck("er.id", &ids).Error)
		require.Equal(t, []uint{want}, ids)
	}
	item := &domain.ResourceImport{OwnerUserID: 1, ResourceType: domain.ResourceTypeMicrosoft, Enterprise: true, SourceObjectKey: "test-enterprise", Status: domain.ResourceImportProcessing}
	repo := NewResourceImportRepo(db)
	require.NoError(t, repo.Create(context.Background(), item))
	stored, err := repo.FindByID(context.Background(), item.ID)
	require.NoError(t, err)
	require.True(t, stored.Enterprise)
}
