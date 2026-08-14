package icloud

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestLoadICloudIMAPScopeUsesAccountCredentialsAndAllActiveAliases(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-imap-scope?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudAliasModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "app-password",
		IMAPUIDValidity: "9", IMAPLastUID: 42, CredentialRevision: 3,
		Status: iCloudResourceNormal, ExpireAt: now.Add(-time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	aliases := []iCloudAliasModel{
		{ResourceID: 1, AnonymousID: "one", Email: "One@icloud.com", Status: iCloudResourceNormal, CreatedAt: now, UpdatedAt: now},
		{ResourceID: 1, AnonymousID: "two", Email: "two@icloud.com", Status: iCloudResourceNormal, CreatedAt: now, UpdatedAt: now},
		{ResourceID: 1, AnonymousID: "released", Email: "released@icloud.com", Status: "released", CreatedAt: now, UpdatedAt: now},
		{ResourceID: 2, AnonymousID: "other", Email: "other@icloud.com", Status: iCloudResourceNormal, CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&aliases).Error; err != nil {
		t.Fatalf("create aliases: %v", err)
	}

	scope, gotAliases, err := NewService(db, nil, nil).loadICloudIMAPScope(context.Background(), 1)
	if err != nil {
		t.Fatalf("load IMAP scope: %v", err)
	}
	slices.Sort(gotAliases)
	if scope.PrimaryEmail != "owner@icloud.com" || scope.AppPassword != "app-password" ||
		scope.UIDValidity != "9" || scope.LastUID != 42 || scope.CredentialRevision != 3 {
		t.Fatalf("unexpected scope: %#v", scope)
	}
	if !slices.Equal(gotAliases, []string{"one@icloud.com", "two@icloud.com"}) {
		t.Fatalf("unexpected aliases: %#v", gotAliases)
	}
}

func TestICloudIMAPAuthenticationFailureOwnsResourceHealthOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-imap-auth-health?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudResourceChannelModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 3, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "revoked",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 2,
		NextProvisionAt: iCloudTimePointer(now), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com", Cookie: "still-valid",
		SessionStatus: iCloudSessionValid, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.markICloudIMAPAuthenticationFailure(context.Background(), iCloudIMAPResourceScope{ID: 1, CredentialRevision: 2}); err != nil {
		t.Fatalf("mark authentication failure: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourceAbnormal || resource.NextValidationAt == nil || resource.NextProvisionAt != nil {
		t.Fatalf("unexpected IMAP health state: %#v", resource)
	}
	var channel iCloudResourceChannelModel
	if err := db.Where("resource_id = ?", 1).First(&channel).Error; err != nil {
		t.Fatalf("read channel: %v", err)
	}
	if channel.SessionStatus != iCloudSessionValid || channel.Cookie != "still-valid" {
		t.Fatalf("IMAP failure changed provisioning channel: %#v", channel)
	}
	var root iCloudRootModel
	if err := db.First(&root, 1).Error; err != nil || root.Version != 4 {
		t.Fatalf("health mutation version: root=%#v err=%v", root, err)
	}
}
