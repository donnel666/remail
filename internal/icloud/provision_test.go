package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

func TestAppleAccountRefreshBootstrapsScntFromTokenResponse(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	tokenScnt := strings.Repeat("t", iCloudAppleAccountValueMaxLength)
	manageScnt := strings.Repeat("m", iCloudAppleAccountValueMaxLength)
	apiKey := strings.Repeat("k", iCloudAppleAccountValueMaxLength)
	sessionID := strings.Repeat("i", iCloudAppleAccountValueMaxLength)
	dataAccessToken := strings.Repeat("d", iCloudAppleAccountValueMaxLength)
	client := NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("X-Apple-I-FD-Client-Info"); got != testICloudFDClientInfo {
			t.Fatalf("FD client info = %q, want imported value", got)
		}
		header := make(http.Header)
		switch request.URL.Path {
		case "/account/manage/gs/ws/token":
			if scnt := request.Header.Get("scnt"); scnt != "" {
				t.Fatalf("initial token scnt = %q, want empty", scnt)
			}
			header.Set("scnt", tokenScnt)
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"timeOutInterval":15}`))}, nil
		case "/account/manage":
			if scnt := request.Header.Get("scnt"); scnt != tokenScnt {
				t.Fatal("manage request did not preserve the token scnt")
			}
			header.Set("scnt", manageScnt)
			header.Set("X-Apple-ID-Session-Id", sessionID)
			header.Set("X-Apple-I-DA-Token", dataAccessToken)
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"apiKey":"` + apiKey + `"}`))}, nil
		default:
			t.Fatalf("unexpected Apple Account path %q", request.URL.Path)
			return nil, nil
		}
	})})

	refreshed, err := client.refresh(context.Background(), iCloudResourceChannelModel{
		Host: "appleid.apple.com", Cookie: "myacinfo=secret", FDClientInfo: testICloudFDClientInfo,
	}, now)
	if err != nil {
		t.Fatalf("refresh Apple Account state: %v", err)
	}
	if refreshed.Scnt != manageScnt || refreshed.APIKey != apiKey || refreshed.SessionID != sessionID ||
		refreshed.DataAccessToken != dataAccessToken || refreshed.ManageExpiresAt == nil ||
		!refreshed.ManageExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatal("Apple Account refresh did not preserve opaque values up to 1000 characters")
	}
}

func TestAppleAccountRefreshAcceptsTokenChallengeScnt(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 5, 0, 0, time.UTC)
	client := NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		header := make(http.Header)
		switch request.URL.Path {
		case appleAccountTokenPath:
			header.Set("scnt", "challenge-scnt")
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: header, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		case "/account/manage":
			if got := request.Header.Get("scnt"); got != "challenge-scnt" {
				t.Fatalf("manage scnt = %q, want challenge-scnt", got)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"apiKey":"api-key"}`))}, nil
		default:
			t.Fatalf("unexpected Apple Account path %q", request.URL.Path)
			return nil, nil
		}
	})})

	refreshed, err := client.refresh(context.Background(), iCloudResourceChannelModel{
		Host: "appleid.apple.com", Cookie: "myacinfo=secret",
	}, now)
	if err != nil || refreshed.Scnt != "challenge-scnt" || refreshed.APIKey != "api-key" {
		t.Fatalf("challenge refresh: state=%#v err=%v", refreshed, err)
	}
}

func TestAppleAccountRefreshWarmsPortalBeforeRetryingMissingScnt(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 10, 0, 0, time.UTC)
	paths := make([]string, 0, 5)
	tokenCalls := 0
	client := NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Host+request.URL.Path)
		header := make(http.Header)
		switch request.URL.Path {
		case appleAccountTokenPath:
			tokenCalls++
			if got := request.Header.Get("scnt"); got != "" {
				t.Fatalf("bootstrap token scnt = %q, want empty", got)
			}
			if tokenCalls == 2 {
				header.Set("scnt", "retry-scnt")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"timeOutInterval":15}`))}, nil
		case "/account/manage/section/privacy":
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`<html></html>`))}, nil
		case "/bootstrap/portal":
			if got := request.Header.Get("X-Apple-I-FD-Client-Info"); got != testICloudFDClientInfo {
				t.Fatalf("bootstrap FD client info = %q", got)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		case "/account/manage":
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"apiKey":"api-key"}`))}, nil
		default:
			t.Fatalf("unexpected Apple Account path %q", request.URL.Path)
			return nil, nil
		}
	})})

	refreshed, err := client.refresh(context.Background(), iCloudResourceChannelModel{
		Host: "appleid.apple.com.cn", Cookie: "myacinfo=secret", FDClientInfo: testICloudFDClientInfo,
	}, now)
	if err != nil || refreshed.Scnt != "retry-scnt" || refreshed.APIKey != "api-key" {
		t.Fatalf("portal bootstrap refresh: state=%#v err=%v", refreshed, err)
	}
	want := []string{
		"appleid.apple.com.cn" + appleAccountTokenPath,
		"account.apple.com.cn/account/manage/section/privacy",
		"account.apple.com.cn/bootstrap/portal",
		"appleid.apple.com.cn" + appleAccountTokenPath,
		"appleid.apple.com.cn/account/manage",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("bootstrap paths = %#v, want %#v", paths, want)
	}
}

func TestAppleAccountRefreshRejectsBootstrapWithoutScnt(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)
	client := NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/account/manage" {
			t.Fatal("manage must not run without scnt")
		}
		body := `{}`
		if request.URL.Path == "/account/manage/section/privacy" {
			body = `<html></html>`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})

	_, err := client.refresh(context.Background(), iCloudResourceChannelModel{
		Host: "appleid.apple.com", Cookie: "myacinfo=secret",
	}, now)
	var appleErr *appleAccountError
	if !errors.As(err, &appleErr) || appleErr.Category != "session_invalid" {
		t.Fatalf("missing scnt error = %#v, want session_invalid", err)
	}
}

func TestAppleAccountCreateKeepsCompletedActiveWhenDetailOmitsIt(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 20, 0, 0, time.UTC)
	paths := make([]string, 0, 3)
	client := NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		body := `{}`
		switch request.URL.Path {
		case "/account/manage/email/private/add":
			body = `{"emailAddress":"created@icloud.com"}`
		case "/account/manage/email/private/add/complete":
			body = `{"emailAddress":"created@icloud.com","id":"anonymous-id","active":true}`
		case "/account/manage/email/private/anonymous-id.em":
			body = `{"emailAddress":"created@icloud.com","forwardToEmail":"mailbox@relay.example"}`
		default:
			t.Fatalf("unexpected Apple Account path %q", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})

	alias, _, err := client.create(context.Background(), iCloudResourceChannelModel{
		Host: "appleid.apple.com", Cookie: "myacinfo=secret", Scnt: "scnt", APIKey: "api-key",
	}, now)
	if err != nil {
		t.Fatalf("create Apple Account alias: %v", err)
	}
	if alias.AnonymousID != "anonymous-id" || alias.Email != "created@icloud.com" ||
		alias.ForwardToEmail != "mailbox@relay.example" || !alias.Active {
		t.Fatalf("unexpected created alias: %#v", alias)
	}
	want := []string{
		"/account/manage/email/private/add",
		"/account/manage/email/private/add/complete",
		"/account/manage/email/private/anonymous-id.em",
	}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("create paths = %#v, want %#v", paths, want)
	}
}

func TestICloudProvisionFallsBackFromRateLimitedNewChannelToLegacyReconcile(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-fallback?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudAliasModel{}, &iCloudAliasRouteModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com",
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
		body := `{"success":true,"result":{"selectedForwardTo":"mailbox@relay.example","total":0,"hasMore":false,"hmeEmails":[]}}`
		switch request.URL.Path {
		case "/v1/hme/generate":
			body = `{"success":true,"result":{"hme":"candidate@icloud.com"}}`
		case "/v1/hme/reserve":
			body = `{"success":true,"result":{"hme":{"hme":"candidate@icloud.com","anonymousId":"candidate-id","forwardToEmail":"mailbox@relay.example","recipientMailId":"recipient-id","isActive":true}}}`
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
		resource.SelectedForwardTo != "mailbox@relay.example" || resource.NextProvisionAt == nil {
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
	if resource.SelectedForwardTo != "mailbox@relay.example" {
		t.Fatalf("created alias did not refresh forwarding mailbox: %#v", resource)
	}
	var alias iCloudAliasModel
	if err := db.Where("resource_id = ?", 1).First(&alias).Error; err != nil {
		t.Fatalf("read created alias: %v", err)
	}
	if alias.Email != "candidate@icloud.com" || alias.AnonymousID != "candidate-id" ||
		alias.ForwardToEmail != "mailbox@relay.example" || alias.RecipientMailID != "recipient-id" || alias.Status != iCloudResourceNormal {
		t.Fatalf("unexpected created alias: %#v", alias)
	}
	var runs []iCloudMaintenanceRunModel
	if err := db.Where("resource_id = ? AND kind = ?", 1, iCloudMaintenanceAlias).Order("id ASC").Find(&runs).Error; err != nil {
		t.Fatalf("read provision task history: %v", err)
	}
	if len(runs) != 2 || runs[0].Status != iCloudMaintenanceSucceeded || runs[1].Status != iCloudMaintenanceSucceeded ||
		runs[0].ValidationGeneration != 1 || runs[1].ValidationGeneration != 2 {
		t.Fatalf("unexpected provision task history: %#v", runs)
	}
	wantPaths := []string{"/v2/hme/list", "/v1/hme/generate", "/v2/hme/list", "/v1/hme/reserve"}
	if strings.Join(legacyPaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("legacy provision sequence = %#v, want %#v", legacyPaths, wantPaths)
	}
	if iCloudChannelHourlyLimit(iCloudChannelAppleAccount) != 20 || iCloudChannelHourlyLimit(iCloudChannelWeb) != 5 {
		t.Fatalf("unexpected hourly limits: new=%d old=%d", iCloudChannelHourlyLimit(iCloudChannelAppleAccount), iCloudChannelHourlyLimit(iCloudChannelWeb))
	}
}

func TestICloudRetryAfterPreservesProviderDelay(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 30, 0, 0, time.UTC)
	if got := iCloudRetryAfter("10800", now); got != 3*time.Hour {
		t.Fatalf("delta Retry-After = %v, want 3h", got)
	}
	retryAt := now.Add(5 * time.Hour)
	if got := iCloudRetryAfter(retryAt.Format(http.TimeFormat), now); got != 5*time.Hour {
		t.Fatalf("date Retry-After = %v, want 5h", got)
	}
}

func TestICloudCookieKeepaliveUsesRuntimeSettingForBothChannels(t *testing.T) {
	previous, existed := runtimeconfig.Snapshot()[runtimeconfig.ICloudCookieKeepaliveMinutesKey]
	runtimeconfig.Set(runtimeconfig.ICloudCookieKeepaliveMinutesKey, "7")
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set(runtimeconfig.ICloudCookieKeepaliveMinutesKey, previous)
		} else {
			runtimeconfig.Delete(runtimeconfig.ICloudCookieKeepaliveMinutesKey)
		}
	})

	now := time.Date(2026, 8, 14, 10, 35, 0, 0, time.UTC)
	expiresAt := now.Add(15 * time.Minute)
	if got := iCloudCookieKeepaliveInterval(); got != 7*time.Minute {
		t.Fatalf("keepalive interval = %v, want 7m", got)
	}
	if got := appleAccountNextKeepalive(iCloudResourceChannelModel{ManageExpiresAt: &expiresAt}, now); got == nil || !got.Equal(now.Add(7*time.Minute)) {
		t.Fatalf("Apple Account keepalive = %v, want %v", got, now.Add(7*time.Minute))
	}
}

func TestICloudProvisionKeepsExpiredAndFullResourcesAliveWithoutCreating(t *testing.T) {
	previous, existed := runtimeconfig.Snapshot()[runtimeconfig.ICloudCookieKeepaliveMinutesKey]
	runtimeconfig.Set(runtimeconfig.ICloudCookieKeepaliveMinutesKey, "8")
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set(runtimeconfig.ICloudCookieKeepaliveMinutesKey, previous)
		} else {
			runtimeconfig.Delete(runtimeconfig.ICloudCookieKeepaliveMinutesKey)
		}
	})

	redisServer := miniredis.RunT(t)
	queue := asynq.NewClient(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		_ = inspector.Close()
		_ = queue.Close()
	})
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-keepalive-only?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 10, 45, 0, 0, time.UTC)
	resources := []iCloudResourceModel{
		{ID: 1, ResourceType: "icloud", PrimaryEmail: "expired@example.com", Status: iCloudResourceNormal, ExpireAt: now.Add(-time.Minute), CredentialRevision: 1, NextProvisionAt: &now, CreatedAt: now, UpdatedAt: now},
		{ID: 2, ResourceType: "icloud", PrimaryEmail: "full@example.com", Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), AliasCount: iCloudMaxAliases, CredentialRevision: 1, NextProvisionAt: &now, CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&resources).Error; err != nil {
		t.Fatalf("create resources: %v", err)
	}
	channels := make([]iCloudResourceChannelModel, 0, len(resources))
	for _, resource := range resources {
		channels = append(channels, iCloudResourceChannelModel{
			ResourceID: resource.ID, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com",
			Cookie: testICloudOldCookie, DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "master",
			SessionStatus: iCloudSessionValid, NextKeepaliveAt: &now, CreatedAt: now, UpdatedAt: now,
		})
	}
	if err := db.Create(&channels).Error; err != nil {
		t.Fatalf("create channels: %v", err)
	}

	paths := make([]string, 0, len(resources))
	service := NewService(db, queue, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		body := `{"success":true,"result":{"selectedForwardTo":"mailbox@relay.example","total":0,"hasMore":false,"hmeEmails":[]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err := service.DispatchICloudProvisions(context.Background(), 10); err != nil {
		t.Fatalf("dispatch keepalive-only resources: %v", err)
	}
	tasks, err := inspector.ListPendingTasks(platform.QueueBackgroundICloudValidation)
	if err != nil || len(tasks) != len(resources) {
		t.Fatalf("pending keepalive tasks=%d err=%v", len(tasks), err)
	}
	for _, pending := range tasks {
		var task iCloudProvisionTask
		if err := json.Unmarshal(pending.Payload, &task); err != nil {
			t.Fatalf("decode provision task: %v", err)
		}
		if err := service.ProcessICloudProvision(context.Background(), task); err != nil {
			t.Fatalf("process keepalive for resource %d: %v", task.ResourceID, err)
		}
	}
	if len(paths) != len(resources) {
		t.Fatalf("keepalive request paths = %#v", paths)
	}
	for _, path := range paths {
		if path != "/v2/hme/list" {
			t.Fatalf("keepalive unexpectedly created an alias via %q", path)
		}
	}
	for _, expected := range resources {
		var resource iCloudResourceModel
		if err := db.First(&resource, expected.ID).Error; err != nil {
			t.Fatalf("read resource %d: %v", expected.ID, err)
		}
		wantNext := now.Add(8 * time.Minute)
		if resource.NextProvisionAt == nil || !resource.NextProvisionAt.Equal(wantNext) || resource.SelectedForwardTo != "mailbox@relay.example" {
			t.Fatalf("keepalive-only resource %d = %#v, want next %v", resource.ID, resource, wantNext)
		}
	}
}

func TestICloudProvisionRefreshKeepsCooldownStageAndProviderDelayWins(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-retry-after?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 10, 40, 0, 0, time.UTC)
	resource := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	pastCooldown := now.Add(-time.Minute)
	channel := iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
		Cookie: "myacinfo=secret", Scnt: "scnt", SessionStatus: iCloudSessionValid,
		CooldownUntil: &pastCooldown, CooldownStage: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	service := NewService(db, nil, nil)
	if err := service.persistICloudProvisionChannel(context.Background(), resource, channel, true, false, now); err != nil {
		t.Fatalf("persist refreshed channel: %v", err)
	}
	requestErr := &appleAccountError{Category: "rate_limited", RetryAfter: 3 * time.Hour}
	if err := service.applyICloudProvisionError(context.Background(), resource, channel, requestErr, now); !errors.Is(err, requestErr) {
		t.Fatalf("apply rate limit error = %v", err)
	}
	var stored iCloudResourceChannelModel
	if err := db.First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("read channel: %v", err)
	}
	if stored.CooldownStage != 2 || stored.CooldownUntil == nil || !stored.CooldownUntil.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("unexpected persisted provider cooldown: %#v", stored)
	}
}

func TestICloudCreatedAliasPersistenceRejectsChangedResourceState(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-persist-fence?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudAliasModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 10, 50, 0, 0, time.UTC)
	resource := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	channel := iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
		Cookie: "myacinfo=secret", SessionStatus: iCloudSessionValid, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).
		Update("status", iCloudResourcePending).Error; err != nil {
		t.Fatalf("change resource status: %v", err)
	}
	service := NewService(db, nil, nil)
	err = service.persistICloudCreatedAlias(context.Background(), resource, channel, hmeAlias{
		AnonymousID: "alias-id", Email: "alias@icloud.com", ForwardToEmail: "mailbox@relay.example", Active: true,
	}, true, now)
	if !errors.Is(err, errICloudValidationStale) {
		t.Fatalf("persist changed resource alias error = %v, want stale", err)
	}
	var count int64
	if err := db.Model(&iCloudAliasModel{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("created aliases after stale persist = %d, err=%v", count, err)
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
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com",
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
	if created != nil || err == nil {
		t.Fatalf("failed refresh result: created=%v err=%v", created, err)
	}
	var stored iCloudResourceChannelModel
	if err := db.First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("read channel: %v", err)
	}
	if stored.Cookie != channel.Cookie || stored.Scnt != channel.Scnt || stored.APIKey != channel.APIKey ||
		stored.SessionFailures != 0 || stored.SessionStatus != iCloudSessionValid {
		t.Fatalf("failed refresh overwrote stored channel: %#v", stored)
	}
	if err := db.First(&resource, resource.ID).Error; err != nil || resource.Status != iCloudResourceNormal {
		t.Fatalf("failed refresh changed resource health: resource=%#v err=%v", resource, err)
	}
}

func TestICloudProvisionRequiresThreeSessionFailuresBeforeInvalidation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-session-failures?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 11, 20, 0, 0, time.UTC)
	resource := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com",
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CredentialRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
	channel := iCloudResourceChannelModel{
		ID: 1, ResourceID: resource.ID, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com",
		Cookie: "secret", SessionStatus: iCloudSessionValid, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	service := NewService(db, nil, nil)
	sessionErr := &hmeError{Category: "session_invalid", SafeMessage: "iCloud session is invalid."}
	for failure := 1; failure <= iCloudSessionFailureLimit; failure++ {
		_ = service.applyICloudProvisionError(context.Background(), resource, channel, sessionErr, now)
		if err := db.First(&channel, channel.ID).Error; err != nil {
			t.Fatalf("read channel after failure %d: %v", failure, err)
		}
		wantStatus := iCloudSessionValid
		if failure == iCloudSessionFailureLimit {
			wantStatus = iCloudSessionInvalid
		}
		if channel.SessionFailures != uint8(failure) || channel.SessionStatus != wantStatus {
			t.Fatalf("channel after failure %d = %#v", failure, channel)
		}
		if failure < iCloudSessionFailureLimit && (channel.CooldownUntil == nil || !channel.CooldownUntil.Equal(now.Add(iCloudValidationRetryInterval))) {
			t.Fatalf("failure %d retry was not scheduled: %#v", failure, channel)
		}
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
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 11, 30, 0, 0, time.UTC)
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com",
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
	var run iCloudMaintenanceRunModel
	if err := db.Where("resource_id = ? AND kind = ?", 1, iCloudMaintenanceAlias).Take(&run).Error; err != nil {
		t.Fatalf("read invalid-channel run: %v", err)
	}
	if run.Status != iCloudMaintenanceCanceled || run.FinishedAt == nil || run.LastSafeError == "" {
		t.Fatalf("invalid-channel run = %#v", run)
	}
}

func TestICloudProvisionClaimRollsBackLeaseWhenRunCreationFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-claim-rollback?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com", Status: iCloudResourceNormal,
		ExpireAt: now.Add(time.Hour), CredentialRevision: 1, NextProvisionAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelWeb, SessionStatus: iCloudSessionInvalid, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_icloud_alias_run BEFORE INSERT ON icloud_maintenance_runs
		WHEN NEW.kind = 'alias' BEGIN SELECT RAISE(ABORT, 'forced run failure'); END`).Error; err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if _, _, claimed, err := service.claimICloudProvision(context.Background(), 1); err == nil || claimed {
		t.Fatalf("claim result: claimed=%v err=%v", claimed, err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.NextProvisionAt == nil || !resource.NextProvisionAt.Equal(now) {
		t.Fatalf("failed run creation leaked lease: %#v", resource.NextProvisionAt)
	}
	var count int64
	if err := db.Model(&iCloudMaintenanceRunModel{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("maintenance runs=%d err=%v", count, err)
	}
}

func TestICloudProvisionRecoversExpiredRunningRun(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-run-recovery?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 12, 30, 0, 0, time.UTC)
	staleStarted := now.Add(-iCloudProvisionLease - time.Second)
	freshStarted := now.Add(-iCloudProvisionLease + time.Second)
	runs := []iCloudMaintenanceRunModel{
		{ResourceID: 1, ValidationGeneration: 1, Kind: iCloudMaintenanceAlias, Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: 1, CredentialRevision: 1, QueuedAt: staleStarted, StartedAt: &staleStarted, CreatedAt: staleStarted, UpdatedAt: staleStarted},
		{ResourceID: 2, ValidationGeneration: 1, Kind: iCloudMaintenanceAlias, Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: 1, CredentialRevision: 1, QueuedAt: freshStarted, StartedAt: &freshStarted, CreatedAt: freshStarted, UpdatedAt: freshStarted},
	}
	if err := db.Create(&runs).Error; err != nil {
		t.Fatalf("create runs: %v", err)
	}
	service := NewService(db, nil, nil)
	if err := service.recoverStaleICloudProvisionRuns(context.Background(), now); err != nil {
		t.Fatalf("recover runs: %v", err)
	}
	if err := db.First(&runs[0], runs[0].ID).Error; err != nil {
		t.Fatalf("read stale run: %v", err)
	}
	if runs[0].Status != iCloudMaintenanceFailed || runs[0].FinishedAt == nil || runs[0].LastSafeError == "" {
		t.Fatalf("stale run = %#v", runs[0])
	}
	if err := db.First(&runs[1], runs[1].ID).Error; err != nil {
		t.Fatalf("read fresh run: %v", err)
	}
	if runs[1].Status != iCloudMaintenanceRunning || runs[1].FinishedAt != nil {
		t.Fatalf("fresh run = %#v", runs[1])
	}
}

func TestICloudProvisionMarksAllAttemptFailuresFailed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-provision-run-failed?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com", Status: iCloudResourceNormal,
		ExpireAt: now.Add(time.Hour), CredentialRevision: 1, NextProvisionAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com", Cookie: "invalid",
		DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "master",
		SessionStatus: iCloudSessionUnchecked, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.ProcessICloudProvision(context.Background(), iCloudProvisionTask{ResourceID: 1}); err != nil {
		t.Fatalf("process failed provision: %v", err)
	}
	var run iCloudMaintenanceRunModel
	if err := db.Where("resource_id = ? AND kind = ?", 1, iCloudMaintenanceAlias).Take(&run).Error; err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Status != iCloudMaintenanceFailed || run.FinishedAt == nil || run.LastSafeError == "" {
		t.Fatalf("failed run = %#v", run)
	}
}
