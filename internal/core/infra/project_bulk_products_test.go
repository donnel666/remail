package infra

import (
	"context"
	"testing"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProjectRepoBulkUpsertProductsPreservesIDsAndSyncsRandomPrices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:project-bulk-products?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ProjectModel{}, &ProjectProductModel{}, &governanceinfra.OperationLogModel{}))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_test_project_products_project_type ON project_products(project_id, type)").Error)
	require.NoError(t, db.Create([]ProjectModel{
		{ID: 1, Name: "one", TargetPlatform: "one.example", Status: "listed", AccessType: "public", LooseMatch: true},
		{ID: 2, Name: "two", TargetPlatform: "two.example", Status: "listed", AccessType: "public", LooseMatch: true},
	}).Error)
	existing := ProjectProductModel{
		ID: 9, ProjectID: 1, Type: "microsoft", Status: "enabled", CodeEnabled: true,
		CodePrice: "1", PurchasePrice: "0", CodeSupplierPrice: "0.5", PurchaseSupplierPrice: "0",
		CodeWindowMinutes: 10, ActivationWindowMinutes: 60, WarrantyMinutes: 60, MainWeight: 1,
	}
	require.NoError(t, db.Create(&existing).Error)

	products := []domain.Product{
		{Type: domain.ProductTypeMicrosoft, Status: domain.ProductStatusEnabled, CodeEnabled: true, PurchaseEnabled: true, CodePrice: "0.100000", PurchasePrice: "0.200000", CodeSupplierPrice: "0.050000", PurchaseSupplierPrice: "0.100000", CodeWindowMinutes: 10, ActivationWindowMinutes: 60, WarrantyMinutes: 1440, MainWeight: 1},
		{Type: domain.ProductTypeDomain, Status: domain.ProductStatusDisabled, CodeEnabled: true, PurchaseEnabled: true, CodePrice: "0.300000", PurchasePrice: "0.500000", CodeSupplierPrice: "0.005000", PurchaseSupplierPrice: "0", CodeWindowMinutes: 10, ActivationWindowMinutes: 60, WarrantyMinutes: 60},
		{Type: domain.ProductTypeRandom, Status: domain.ProductStatusEnabled, CodeEnabled: true, PurchaseEnabled: true, CodePrice: "9.000000", PurchasePrice: "9.000000", CodeSupplierPrice: "0", PurchaseSupplierPrice: "0", CodeWindowMinutes: 10, ActivationWindowMinutes: 60, WarrantyMinutes: 60, MainWeight: 1, DotWeight: 1, PlusWeight: 1},
	}
	log := &governancedomain.OperationLog{OperatorUserID: 1, OperationType: "test", ResourceType: "project", ResourceID: "bulk", Path: "/test", Result: "success", SafeSummary: "updated", RequestID: "request"}
	affected, err := NewProjectRepo(db).BulkUpsertProductsWithLog(context.Background(), coreapp.ProjectListFilter{Scope: coreapp.ProjectListScopeAll, IsAdmin: true, IDs: []uint{1, 2}}, products, log)

	require.NoError(t, err)
	require.Equal(t, 2, affected)
	var rows []ProjectProductModel
	require.NoError(t, db.Order("project_id, type").Find(&rows).Error)
	require.Len(t, rows, 6)
	var updated ProjectProductModel
	require.NoError(t, db.Where("project_id = ? AND type = ?", 1, "microsoft").First(&updated).Error)
	require.Equal(t, uint(9), updated.ID)
	require.True(t, updated.PurchaseEnabled)
	require.Equal(t, "0.2", updated.PurchasePrice)
	var randomProduct ProjectProductModel
	require.NoError(t, db.Where("project_id = ? AND type = ?", 1, "random").First(&randomProduct).Error)
	require.Equal(t, "0.1", randomProduct.CodePrice)
	require.Equal(t, "0.2", randomProduct.PurchasePrice)

	partialMicrosoft := products[0]
	partialMicrosoft.CodePrice = "0.400000"
	partialMicrosoft.PurchasePrice = "0.600000"
	_, err = NewProjectRepo(db).BulkUpsertProductsWithLog(context.Background(), coreapp.ProjectListFilter{Scope: coreapp.ProjectListScopeAll, IsAdmin: true, IDs: []uint{1, 2}}, []domain.Product{partialMicrosoft}, nil)
	require.NoError(t, err)
	require.NoError(t, db.Where("project_id = ? AND type = ?", 1, "random").First(&randomProduct).Error)
	require.Equal(t, "0.3", randomProduct.CodePrice)
	require.Equal(t, "0.5", randomProduct.PurchasePrice)

	require.NoError(t, db.Create(&ProjectModel{ID: 3, Name: "three", TargetPlatform: "three.example", Status: "listed", AccessType: "public", LooseMatch: true}).Error)
	_, err = NewProjectRepo(db).BulkUpsertProductsWithLog(context.Background(), coreapp.ProjectListFilter{Scope: coreapp.ProjectListScopeAll, IsAdmin: true, IDs: []uint{3}}, []domain.Product{products[2]}, nil)
	require.ErrorIs(t, err, domain.ErrInvalidProduct)
	var invalidRows int64
	require.NoError(t, db.Model(&ProjectProductModel{}).Where("project_id = ?", 3).Count(&invalidRows).Error)
	require.Zero(t, invalidRows)
}
