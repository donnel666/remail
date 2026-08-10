package icloud

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type iCloudAdminTestUser struct {
	ID          uint   `gorm:"column:id;primaryKey"`
	Email       string `gorm:"column:email"`
	Nickname    string `gorm:"column:nickname"`
	Status      string `gorm:"column:status"`
	Role        string `gorm:"column:role"`
	UserGroupID uint   `gorm:"column:user_group_id"`
}

func (iCloudAdminTestUser) TableName() string { return "users" }

type iCloudAdminTestGroup struct {
	ID   uint   `gorm:"column:id;primaryKey"`
	Name string `gorm:"column:name"`
}

func (iCloudAdminTestGroup) TableName() string { return "user_groups" }

func TestListAdminICloudResourcesReturnsOnlySafeOperationalFacts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-admin-resources?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudAdminTestGroup{}, &iCloudAdminTestUser{}, &iCloudRootModel{},
		&iCloudResourceModel{}, &iCloudAliasModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	now := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	probeStartedAt := now.Add(-time.Minute)
	models := []any{
		&iCloudAdminTestGroup{ID: 3, Name: "Suppliers"},
		&iCloudAdminTestUser{ID: 7, Email: "owner@example.com", Nickname: "Owner", Status: "active", Role: "supplier", UserGroupID: 3},
		&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 4, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now},
		&iCloudRootModel{ID: 2, Type: "icloud", OwnerUserID: 7, Version: 2, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
		&iCloudResourceModel{
			ID: 1, ResourceType: "icloud", PrimaryEmail: "main@icloud.com", Host: "www.icloud.com",
			DSID: "secret-dsid", ClientID: "secret-client", ClientBuildNumber: "build", ClientMasteringNumber: "master",
			Cookie: "secret-cookie", SelectedForwardTo: "icloud@aishop6.com",
			ExpireAt: now.Add(30 * 24 * time.Hour), Status: iCloudResourceNormal, SessionStatus: iCloudSessionValid,
			AliasCount: iCloudMaxAliases - 1, AliasProvisionCandidate: "candidate@icloud.com",
			CredentialRevision: 3, ValidationGeneration: 4, ValidationFailures: 2, SessionFailures: 1,
			DeliveryProbeStartedAt: &probeStartedAt,
			CredentialUpdatedAt:    now, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now,
		},
		&iCloudResourceModel{
			ID: 2, ResourceType: "icloud", PrimaryEmail: "pending@me.com", Host: "www.icloud.com",
			DSID: "secret-dsid-2", ClientID: "secret-client-2", ClientBuildNumber: "build", ClientMasteringNumber: "master",
			Cookie: "secret-cookie-2", SelectedForwardTo: "icloud-backup@aishop6.com", ExpireAt: now.Add(30 * 24 * time.Hour),
			ForSale: true, Status: iCloudResourcePending, SessionStatus: iCloudSessionUnchecked,
			AliasCount: 12, CredentialRevision: 1, ValidationGeneration: 1,
			CredentialUpdatedAt: now, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		},
		&iCloudAliasModel{ResourceID: 1, AnonymousID: "alias-1", Email: "alias@icloud.com", Status: "normal", CreatedAt: now, UpdatedAt: now},
	}
	for _, model := range models {
		if err := db.Create(model).Error; err != nil {
			t.Fatalf("create %T: %v", model, err)
		}
	}

	service := NewService(db, nil, nil)
	result, err := service.ListAdminICloudResources(context.Background(), AdminICloudResourceListFilter{
		Search: "alias@icloud.com", Suffix: "icloud.com", Status: iCloudResourceNormal,
		ForSale: iCloudBoolPointer(false), SessionStatus: iCloudSessionValid, Limit: 20,
	})
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("result size = total %d items %d", result.Total, len(result.Items))
	}
	item := result.Items[0]
	if item.PrimaryEmail != "main@icloud.com" || item.SelectedForwardTo != "icloud@aishop6.com" || item.AliasCount != iCloudMaxAliases-1 {
		t.Fatalf("unexpected safe item: %#v", item)
	}
	if item.Owner.ID != 7 || item.Owner.GroupName != "Suppliers" || !item.Owner.Enabled {
		t.Fatalf("unexpected owner: %#v", item.Owner)
	}
	if result.AliasLimit != iCloudMaxAliases || result.Facets.Status.Normal != 1 || result.Facets.ForSale.No != 1 {
		t.Fatalf("unexpected list metadata: %#v", result)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal safe response: %v", err)
	}
	for _, secret := range []string{"secret-cookie", "secret-dsid", "secret-client"} {
		if bytes.Contains(payload, []byte(secret)) {
			t.Fatalf("safe response exposed %q: %s", secret, payload)
		}
	}
	detail, err := service.GetAdminICloudResource(context.Background(), 1)
	if err != nil {
		t.Fatalf("get resource detail: %v", err)
	}
	if detail.Version != 4 || detail.AliasLimit != iCloudMaxAliases || detail.AliasRemaining != 1 ||
		!detail.AliasProvisioning || detail.CredentialRevision != 3 || detail.ValidationGeneration != 4 ||
		detail.ValidationFailures != 2 || detail.SessionFailures != 1 || detail.DeliveryProbeStartedAt == nil {
		t.Fatalf("unexpected safe detail: %#v", detail)
	}
	detailPayload, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal safe detail: %v", err)
	}
	for _, secret := range []string{"secret-cookie", "secret-dsid", "secret-client"} {
		if bytes.Contains(detailPayload, []byte(secret)) {
			t.Fatalf("safe detail exposed %q: %s", secret, detailPayload)
		}
	}

	falseValue := false
	fast, err := service.ListAdminICloudResources(context.Background(), AdminICloudResourceListFilter{
		Limit: 20, IncludeTotal: &falseValue, IncludeFacets: &falseValue,
	})
	if err != nil {
		t.Fatalf("list page without aggregates: %v", err)
	}
	if fast.Total != 0 || fast.Facets.Status.All != 0 || fast.Facets.Suffixes == nil || len(fast.Facets.Suffixes) != 0 || len(fast.Items) != 2 {
		t.Fatalf("aggregate-free page returned unexpected metadata: %#v", fast)
	}
}

func TestListAdminICloudAliasesReturnsSafePagedFields(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-admin-aliases?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudAliasModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudAliasModel{
		ResourceID: 1, AnonymousID: "anonymous", RecipientMailID: "recipient-mail-id", Email: "alias@icloud.com", ForwardToEmail: "icloud@aishop6.com",
		Status: iCloudResourceNormal, Origin: "hme", ProviderDomain: "icloud.com", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	service := NewService(db, nil, nil)
	result, err := service.ListAdminICloudAliases(context.Background(), 1, 0, 20)
	if err != nil {
		t.Fatalf("list aliases: %v", err)
	}
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].Email != "alias@icloud.com" ||
		result.Items[0].AnonymousID != "anonymous" || result.Items[0].RecipientMailID != "recipient-mail-id" {
		t.Fatalf("unexpected alias page: %#v", result)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal alias page: %v", err)
	}
	for _, secret := range []string{"cookie", "dsid", "clientBuildNumber"} {
		if bytes.Contains(payload, []byte(secret)) {
			t.Fatalf("alias response exposed %q: %s", secret, payload)
		}
	}
}
