package icloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

func TestICloudProvisionFallsBackFromRateLimitedNewChannelToLegacyReconcile(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-fallback?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudAliasModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "app-password",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1,
		AliasCount: 0, NextProvisionAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	channels := []iCloudResourceChannelModel{
		{
			ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
			Cookie: "myacinfo=secret", Scnt: "scnt", SessionStatus: iCloudSessionUnchecked,
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com",
			Cookie: testICloudOldCookie, DSID: "123", ClientID: "client",
			ClientBuildNumber: "build", ClientMasteringNumber: "master",
			SessionStatus: iCloudSessionUnchecked, CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := db.Create(&channels).Error; err != nil {
		t.Fatalf("create channels: %v", err)
	}

	legacyPaths := make([]string, 0, 4)
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader(`{"code":-41015}`))}, nil
	})})
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		legacyPaths = append(legacyPaths, request.URL.Path)
		body := `{"success":true,"result":{"total":0,"hasMore":false,"hmeEmails":[]}}`
		switch request.URL.Path {
		case "/v1/hme/generate":
			body = `{"success":true,"result":{"hme":"candidate@icloud.com"}}`
		case "/v1/hme/reserve":
			body = `{"success":true,"result":{"hme":{"hme":"candidate@icloud.com","anonymousId":"candidate-id","isActive":true}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})

	if err := service.ProcessICloudProvision(context.Background(), iCloudProvisionTask{ResourceID: 1}); err != nil {
		t.Fatalf("first provision round: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read first-round resource: %v", err)
	}
	if resource.Status != iCloudResourceNormal || resource.AliasProvisionCandidate != "candidate@icloud.com" ||
		resource.NextProvisionAt == nil {
		t.Fatalf("new-channel limit blocked legacy generate: %#v", resource)
	}
	var newChannel iCloudResourceChannelModel
	if err := db.Where("resource_id = ? AND kind = ?", 1, iCloudChannelAppleAccount).First(&newChannel).Error; err != nil {
		t.Fatalf("read new channel: %v", err)
	}
	if newChannel.SessionStatus != iCloudSessionValid || newChannel.CooldownUntil == nil ||
		!newChannel.CooldownUntil.Equal(now.Add(30*time.Minute)) || newChannel.CooldownStage != 1 {
		t.Fatalf("unexpected new-channel cooldown: %#v", newChannel)
	}

	now = now.Add(2 * time.Second)
	if err := service.ProcessICloudProvision(context.Background(), iCloudProvisionTask{ResourceID: 1}); err != nil {
		t.Fatalf("second provision round: %v", err)
	}
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read second-round resource: %v", err)
	}
	if resource.Status != iCloudResourceNormal || resource.AliasCount != 1 || resource.AliasProvisionCandidate != "" {
		t.Fatalf("legacy reserve did not complete: %#v", resource)
	}
	var alias iCloudAliasModel
	if err := db.Where("resource_id = ?", 1).First(&alias).Error; err != nil {
		t.Fatalf("read created alias: %v", err)
	}
	if alias.Email != "candidate@icloud.com" || alias.AnonymousID != "candidate-id" || alias.Status != iCloudResourceNormal {
		t.Fatalf("unexpected created alias: %#v", alias)
	}
	wantPaths := []string{"/v2/hme/list", "/v1/hme/generate", "/v2/hme/list", "/v1/hme/reserve"}
	if strings.Join(legacyPaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("legacy provision sequence = %#v, want %#v", legacyPaths, wantPaths)
	}
	if iCloudChannelHourlyLimit(iCloudChannelAppleAccount) != 20 || iCloudChannelHourlyLimit(iCloudChannelWeb) != 5 {
		t.Fatalf("unexpected hourly limits: new=%d old=%d", iCloudChannelHourlyLimit(iCloudChannelAppleAccount), iCloudChannelHourlyLimit(iCloudChannelWeb))
	}
}

func TestICloudProvisionFailedAppleRefreshKeepsStoredCredentials(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-refresh-failure?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC)
	resource := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "app-password",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	channel := iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
		Cookie: "myacinfo=original", Scnt: "original-scnt", APIKey: "original-api-key",
		SessionStatus: iCloudSessionValid, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	service := NewService(db, nil, nil)
	service.apple = NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/account/manage/gs/ws/token" {
			header := make(http.Header)
			header.Set("Set-Cookie", "myacinfo=rotated; Path=/")
			header.Set("scnt", "rotated-scnt")
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"timeOutInterval":15}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})})

	created, err := service.provisionICloudAppleAccount(context.Background(), resource, channel, true, now)
	if created || err == nil {
		t.Fatalf("failed refresh result: created=%v err=%v", created, err)
	}
	var stored iCloudResourceChannelModel
	if err := db.First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("read channel: %v", err)
	}
	if stored.Cookie != channel.Cookie || stored.Scnt != channel.Scnt || stored.APIKey != channel.APIKey ||
		stored.SessionFailures != 1 || stored.SessionStatus != iCloudSessionValid {
		t.Fatalf("failed refresh overwrote stored channel: %#v", stored)
	}
	if err := db.First(&resource, resource.ID).Error; err != nil || resource.Status != iCloudResourceNormal {
		t.Fatalf("failed refresh changed resource health: resource=%#v err=%v", resource, err)
	}
}

func TestICloudProvisionDispatcherDrainsAllInvalidChannels(t *testing.T) {
	redisServer := miniredis.RunT(t)
	queue := asynq.NewClient(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		_ = inspector.Close()
		_ = queue.Close()
	})
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-invalid?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 11, 30, 0, 0, time.UTC)
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@icloud.com", IMAPAppPassword: "app-password",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1,
		NextProvisionAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com", Cookie: "expired",
		SessionStatus: iCloudSessionInvalid, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	service := NewService(db, queue, nil)
	service.now = func() time.Time { return now }
	if err := service.DispatchICloudProvisions(context.Background(), 10); err != nil {
		t.Fatalf("dispatch provisioning: %v", err)
	}
	tasks, err := inspector.ListPendingTasks(platform.QueueBackgroundICloudValidation)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("pending provision tasks=%d err=%v", len(tasks), err)
	}
	var task iCloudProvisionTask
	if err := json.Unmarshal(tasks[0].Payload, &task); err != nil {
		t.Fatalf("decode provision task: %v", err)
	}
	if err := service.ProcessICloudProvision(context.Background(), task); err != nil {
		t.Fatalf("drain invalid provisioning: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil || resource.NextProvisionAt != nil {
		t.Fatalf("invalid channels left provisioning scheduled: resource=%#v err=%v", resource, err)
	}
}
