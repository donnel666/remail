package icloud

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestProcessICloudValidationTriesEveryChannelAndAcceptsAnyAuthorizedForward(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "all-channels")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"selected_forward_to": "old@other.example",
		"alias_count":         iCloudMaxAliases,
	}).Error; err != nil {
		t.Fatalf("set stale forwarding target: %v", err)
	}
	if err := db.Create(&iCloudAliasModel{
		ResourceID: 1, AnonymousID: "old-id", Email: "old@icloud.com",
		ForwardToEmail: "old@other.example", Status: iCloudResourceNormal,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create historical alias: %v", err)
	}

	channels := []iCloudResourceChannelModel{
		{
			ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
			Cookie: "myacinfo=secret", Scnt: "imported-scnt", SessionStatus: iCloudSessionUnchecked,
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

	applePaths := make([]string, 0, 5)
	webCalls := 0
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		applePaths = append(applePaths, request.URL.Path)
		body := `{}`
		switch request.URL.Path {
		case appleAccountTokenPath:
			body = `{"timeOutInterval":15}`
		case "/account/manage":
			body = `{"apiKey":"api-key"}`
		case appleAccountPrivateEmailPath:
			body = `{"privateEmailList":[
				{"emailAddress":"new-alias@icloud.com","label":"ReMail","id":"apple-id","active":true},
				{"emailAddress":"old@icloud.com","id":"old-id","forwardToEmail":"old@other.example","active":true}
			],"inactivePrivateEmailList":[],"forwardToEmailAddress":"anything@relay.example","maxLimitReached":false}`
		default:
			t.Fatalf("unexpected Apple Account path %q", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		webCalls++
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})})

	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process validation: %v", err)
	}
	if got, want := strings.Join(applePaths, ","), strings.Join([]string{
		appleAccountTokenPath,
		"/account/manage",
		appleAccountPrivateEmailPath,
	}, ","); got != want {
		t.Fatalf("Apple Account sequence = %q, want %q", got, want)
	}
	if webCalls != 1 {
		t.Fatalf("legacy channel calls = %d, want 1 after new channel succeeded", webCalls)
	}

	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourceNormal || resource.SelectedForwardTo != "anything@relay.example" ||
		resource.NextProvisionAt == nil || !resource.NextProvisionAt.Equal(now) ||
		resource.NextValidationAt != nil || resource.LastValidAt == nil || resource.AliasCount != 2 || resource.LastAliasSyncAt == nil {
		t.Fatalf("unexpected validated resource: %#v", resource)
	}
	var alias iCloudAliasModel
	if err := db.Where("resource_id = ? AND anonymous_id = ?", 1, "apple-id").Take(&alias).Error; err != nil {
		t.Fatalf("read created alias: %v", err)
	}
	if alias.Email != "new-alias@icloud.com" || alias.ForwardToEmail != "anything@relay.example" || alias.Status != iCloudResourceNormal {
		t.Fatalf("unexpected Apple Account alias: %#v", alias)
	}
	var historical iCloudAliasModel
	if err := db.Where("resource_id = ? AND anonymous_id = ?", 1, "old-id").Take(&historical).Error; err != nil {
		t.Fatalf("read historical alias: %v", err)
	}
	if historical.Status != iCloudResourceDisabled {
		t.Fatalf("unauthorized historical alias status = %q, want disabled", historical.Status)
	}
	var storedChannels []iCloudResourceChannelModel
	if err := db.Order("kind").Find(&storedChannels).Error; err != nil {
		t.Fatalf("read channels: %v", err)
	}
	statuses := map[string]string{}
	failures := map[string]uint8{}
	for _, channel := range storedChannels {
		statuses[channel.Kind] = channel.SessionStatus
		failures[channel.Kind] = channel.SessionFailures
		if channel.Kind == iCloudChannelAppleAccount && channel.ProvisionWindowCount != 0 {
			t.Fatalf("listing existing aliases consumed a creation slot: %#v", channel)
		}
	}
	if statuses[iCloudChannelAppleAccount] != iCloudSessionValid || statuses[iCloudChannelWeb] != iCloudSessionUnchecked ||
		failures[iCloudChannelWeb] != 1 {
		t.Fatalf("unexpected channel results: statuses=%#v failures=%#v", statuses, failures)
	}
}

func TestProcessICloudValidationDefersBlockedChannelsWithoutCallingProvider(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 20, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "blocked-channels")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	setICloudForwardingSuffixes(t, "relay.example")
	cooldownUntil := now.Add(45 * time.Minute)
	windowAt := now
	channels := []iCloudResourceChannelModel{
		{
			ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
			Cookie: "myacinfo=secret", Scnt: "scnt", SessionStatus: iCloudSessionValid,
			CooldownUntil: &cooldownUntil, CooldownStage: 1, CreatedAt: now, UpdatedAt: now,
		},
		{
			ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com",
			Cookie: testICloudOldCookie, DSID: "123", ClientID: "client",
			ClientBuildNumber: "build", ClientMasteringNumber: "master", SessionStatus: iCloudSessionValid,
			ProvisionWindowAt: &windowAt, ProvisionWindowCount: uint8(iCloudChannelHourlyLimit(iCloudChannelWeb)),
			CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := db.Create(&channels).Error; err != nil {
		t.Fatalf("create channels: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("Apple Account must not be called during cooldown")
		return nil, nil
	})})
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("legacy HME must not be called after reaching the hourly limit")
		return nil, nil
	})})

	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.ValidationFailures != 0 ||
		resource.NextValidationAt == nil || !resource.NextValidationAt.Equal(cooldownUntil) {
		t.Fatalf("unexpected deferred resource: %#v", resource)
	}
}

func TestProcessICloudValidationRateLimitUsesRetryAfterWithoutHealthFailure(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 35, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "rate-limit")
	task := createICloudValidationTestResource(t, db, now, now.Add(6*time.Hour))
	run := iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: task.ValidationGeneration, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: task.ExpectedCredentialRevision, QueuedAt: now.Add(-time.Minute), StartedAt: &now,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create maintenance run: %v", err)
	}
	task.MaintenanceRunID, task.MaintenanceKind = run.ID, iCloudMaintenanceValidation
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).
		Update("validation_failures", 2).Error; err != nil {
		t.Fatalf("seed validation failures: %v", err)
	}
	channels := []iCloudResourceChannelModel{
		{
			ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
			Cookie: "myacinfo=secret", Scnt: "scnt", SessionStatus: iCloudSessionValid,
			CooldownStage: 1, CreatedAt: now, UpdatedAt: now,
		},
		{
			ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com",
			Cookie: testICloudOldCookie, DSID: "123", ClientID: "client",
			ClientBuildNumber: "build", ClientMasteringNumber: "master",
			SessionStatus: iCloudSessionInvalid, CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := db.Create(&channels).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Retry-After", "10800")
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: header, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})})

	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.ValidationFailures != 2 ||
		resource.NextValidationAt == nil || !resource.NextValidationAt.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("rate limit changed resource health or schedule: %#v", resource)
	}
	var channel iCloudResourceChannelModel
	if err := db.Where("resource_id = ? AND kind = ?", 1, iCloudChannelAppleAccount).Take(&channel).Error; err != nil {
		t.Fatalf("read channel: %v", err)
	}
	if channel.CooldownStage != 2 || channel.CooldownUntil == nil || !channel.CooldownUntil.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("unexpected rate-limit channel: %#v", channel)
	}
	var storedRun iCloudMaintenanceRunModel
	if err := db.First(&storedRun, run.ID).Error; err != nil {
		t.Fatalf("read maintenance run: %v", err)
	}
	if storedRun.Status != iCloudMaintenanceQueued || storedRun.Attempts != 1 || storedRun.StartedAt == nil ||
		!storedRun.StartedAt.Equal(now) || storedRun.FinishedAt != nil || !storedRun.QueuedAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("transient validation was not requeued: %#v", storedRun)
	}
}

func TestProcessICloudValidationRecordsAppleHTTPFailure(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 38, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "apple-http-failure")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
		Cookie: "opaque=secret", Scnt: "scnt", APIKey: "api-key", SessionStatus: iCloudSessionUnchecked,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != appleAccountPrivateEmailPath {
			t.Fatalf("unexpected Apple Account path %q", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})})

	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.ValidationFailures != 1 ||
		resource.LastSafeError != "Apple Account session is invalid. (stage=list, HTTP 401)" {
		t.Fatalf("Apple HTTP failure was not recorded safely: %#v", resource)
	}
}

func TestProcessICloudCredentialCheckTreatsExhaustedProxyRetryAsFailure(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 42, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "credential-proxy-exhausted")
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com", Status: iCloudResourceNormal,
		ExpireAt: now.Add(time.Hour), CredentialRevision: 4, ValidationGeneration: 5,
		LastSafeError: "previous error", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	manageExpiresAt := now.Add(time.Hour)
	nextKeepaliveAt := now.Add(time.Hour)
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
		Cookie: "myacinfo=secret", Scnt: "scnt", APIKey: "api-key",
		ManageExpiresAt: &manageExpiresAt, NextKeepaliveAt: &nextKeepaliveAt,
		SessionStatus: iCloudSessionUnchecked, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	run := iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: 5, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: 4, QueuedAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set(appleProxyRetryExhaustedHeader, "1")
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: header, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})})

	task := iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 5, ExpectedCredentialRevision: 4,
		PreserveResourceStatus: true, MaintenanceRunID: run.ID, MaintenanceKind: iCloudMaintenanceValidation,
	}
	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatal(err)
	}
	if resource.Status != iCloudResourceNormal || resource.ValidationFailures != 1 || resource.NextValidationAt != nil ||
		resource.LastSafeError == "previous error" || resource.LastValidAt != nil {
		t.Fatalf("exhausted proxy retry was not recorded as a credential failure: %#v", resource)
	}
	var storedRun iCloudMaintenanceRunModel
	if err := db.First(&storedRun, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedRun.Status != iCloudMaintenanceFailed || storedRun.FinishedAt == nil {
		t.Fatalf("exhausted proxy retry did not fail the maintenance run: %#v", storedRun)
	}
}

func TestProcessICloudValidationStopsAfterMaximumFailuresForInvalidSession(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 40, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "max-failures")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).
		Update("validation_failures", iCloudValidationMaxFailures-1).Error; err != nil {
		t.Fatalf("seed validation failures: %v", err)
	}
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
		Cookie: "myacinfo=secret", Scnt: "scnt", SessionStatus: iCloudSessionInvalid,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid session must not call Apple")
		return nil, nil
	})})

	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourceAbnormal || resource.ValidationFailures != iCloudValidationMaxFailures ||
		resource.NextValidationAt != nil || resource.NextProvisionAt != nil {
		t.Fatalf("unexpected terminal validation resource: %#v", resource)
	}
	tasks, err := service.iCloudValidationCandidates(context.Background(), 10)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("terminal validation candidates = %#v, err=%v", tasks, err)
	}
}

func TestProcessICloudValidationCreationBlockPreservesExistingHealth(t *testing.T) {
	for _, test := range []struct {
		name       string
		expireAt   func(time.Time) time.Time
		aliasCount uint
	}{
		{name: "expired", expireAt: func(now time.Time) time.Time { return now.Add(-time.Minute) }},
		{name: "full", expireAt: func(now time.Time) time.Time { return now.Add(time.Hour) }, aliasCount: iCloudMaxAliases},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 14, 9, 50, 0, 0, time.UTC)
			db := openICloudValidationTestDB(t, "creation-block-"+test.name)
			task := createICloudValidationTestResource(t, db, now, test.expireAt(now))
			setICloudForwardingSuffixes(t, "relay.example")
			lastValidAt := now.Add(-time.Hour)
			if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
				"selected_forward_to": "mailbox@relay.example", "last_valid_at": lastValidAt,
				"validation_failures": 2, "alias_count": test.aliasCount,
			}).Error; err != nil {
				t.Fatalf("seed healthy resource: %v", err)
			}
			service := NewService(db, nil, nil)
			service.now = func() time.Time { return now }
			if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
				t.Fatalf("process validation: %v", err)
			}
			var resource iCloudResourceModel
			if err := db.First(&resource, 1).Error; err != nil {
				t.Fatalf("read resource: %v", err)
			}
			if resource.Status != iCloudResourceNormal || resource.ValidationFailures != 2 ||
				resource.SelectedForwardTo != "mailbox@relay.example" || resource.NextValidationAt != nil ||
				resource.NextProvisionAt != nil {
				t.Fatalf("creation block changed resource health: %#v", resource)
			}
		})
	}
}

func TestProcessICloudCredentialValidationCancelsAtAliasLimit(t *testing.T) {
	now := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	lastCheckedAt := now.Add(-2 * time.Hour)
	lastValidAt := now.Add(-time.Hour)
	db := openICloudValidationTestDB(t, "credential-alias-limit")
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "full@example.com", Status: iCloudResourceNormal, ForSale: true,
		ExpireAt: now.Add(time.Hour), CredentialRevision: 4, ValidationGeneration: 5, ValidationFailures: 2,
		AliasCount: iCloudMaxAliases, SelectedForwardTo: "mailbox@relay.example", LastSafeError: "existing resource error",
		NextValidationAt: &now, NextProvisionAt: &now, LastCheckedAt: &lastCheckedAt, LastValidAt: &lastValidAt,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	channels := []iCloudResourceChannelModel{
		{
			ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com", Cookie: "expired", Scnt: "scnt",
			SessionStatus: iCloudSessionInvalid, SessionFailures: 3, NextKeepaliveAt: &now,
			LastCheckedAt: &lastCheckedAt, LastValidAt: &lastValidAt, CreatedAt: now, UpdatedAt: now,
		},
		{
			ResourceID: 1, Kind: iCloudChannelWeb, Host: "p1-maildomainws.icloud.com", Cookie: "unchecked",
			SessionStatus: iCloudSessionUnchecked, NextKeepaliveAt: &now,
			LastCheckedAt: &lastCheckedAt, LastValidAt: &lastValidAt, CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := db.Create(&channels).Error; err != nil {
		t.Fatal(err)
	}
	validationRun := iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: 5, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: 4, QueuedAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	aliasRun := iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: 1, Kind: iCloudMaintenanceAlias,
		Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: 1,
		CredentialRevision: 4, QueuedAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&validationRun).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&aliasRun).Error; err != nil {
		t.Fatal(err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("full resource must not call Apple Account")
		return nil, nil
	})})
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("full resource must not call legacy HME")
		return nil, nil
	})})
	task := iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 5, ExpectedCredentialRevision: 4,
		PreserveResourceStatus: true, MaintenanceRunID: validationRun.ID, MaintenanceKind: iCloudMaintenanceValidation,
	}
	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatal(err)
	}
	if resource.Status != iCloudResourceNormal || !resource.ForSale || resource.ValidationFailures != 2 ||
		resource.LastSafeError != "existing resource error" || resource.SelectedForwardTo != "mailbox@relay.example" ||
		resource.LastCheckedAt == nil || !resource.LastCheckedAt.Equal(lastCheckedAt) || resource.LastValidAt == nil || !resource.LastValidAt.Equal(lastValidAt) ||
		resource.NextValidationAt != nil || resource.NextProvisionAt != nil {
		t.Fatalf("full credential validation changed resource health: %#v", resource)
	}
	var storedChannels []iCloudResourceChannelModel
	if err := db.Order("kind").Find(&storedChannels).Error; err != nil {
		t.Fatal(err)
	}
	if len(storedChannels) != 2 || storedChannels[0].SessionStatus != iCloudSessionInvalid || storedChannels[0].SessionFailures != 3 ||
		storedChannels[1].SessionStatus != iCloudSessionUnchecked {
		t.Fatalf("full credential validation changed Cookie state: %#v", storedChannels)
	}
	for _, channel := range storedChannels {
		if channel.NextKeepaliveAt != nil || channel.LastCheckedAt == nil || !channel.LastCheckedAt.Equal(lastCheckedAt) ||
			channel.LastValidAt == nil || !channel.LastValidAt.Equal(lastValidAt) {
			t.Fatalf("full credential validation changed Cookie timestamps: %#v", channel)
		}
	}
	if err := db.First(&validationRun, validationRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if validationRun.Status != iCloudMaintenanceCanceled || validationRun.FinishedAt == nil {
		t.Fatalf("full credential validation was not canceled: %#v", validationRun)
	}
	if err := db.First(&aliasRun, aliasRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if aliasRun.Status != iCloudMaintenanceRunning || aliasRun.FinishedAt != nil {
		t.Fatalf("alias run was canceled with validation: %#v", aliasRun)
	}
}

func TestProcessICloudValidationCompletesLegacyGenerateReserveInOneRun(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 30, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "legacy-create")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com",
		Cookie: testICloudOldCookie, DSID: "123", ClientID: "client",
		ClientBuildNumber: "build", ClientMasteringNumber: "master",
		SessionStatus: iCloudSessionUnchecked, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	paths := make([]string, 0, 4)
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		body := `{"success":true,"result":{"selectedForwardTo":"dropbox@relay.example","forwardToEmails":["dropbox@relay.example"],"total":0,"hasMore":false,"hmeEmails":[]}}`
		switch request.URL.Path {
		case "/v1/hme/generate":
			body = `{"success":true,"result":{"hme":"legacy-alias@icloud.com"}}`
		case "/v1/hme/reserve":
			body = `{"success":true,"result":{"hme":{"hme":"legacy-alias@icloud.com","anonymousId":"legacy-id","forwardToEmail":"dropbox@relay.example","recipientMailId":"relay-recipient-id","domain":"icloud.com","isActive":true}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})

	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process validation: %v", err)
	}
	wantPaths := []string{"/v2/hme/list", "/v1/hme/generate", "/v2/hme/list", "/v1/hme/reserve"}
	if strings.Join(paths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("legacy validation sequence = %#v, want %#v", paths, wantPaths)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourceNormal || resource.SelectedForwardTo != "dropbox@relay.example" ||
		resource.NextProvisionAt == nil || !resource.NextProvisionAt.Equal(now) {
		t.Fatalf("unexpected validated resource: %#v", resource)
	}
	var alias iCloudAliasModel
	if err := db.Where("resource_id = ?", 1).Take(&alias).Error; err != nil {
		t.Fatalf("read alias: %v", err)
	}
	if alias.AnonymousID != "legacy-id" || alias.ForwardToEmail != "dropbox@relay.example" ||
		alias.RecipientMailID != "relay-recipient-id" || alias.ProviderDomain != "icloud.com" || alias.Status != iCloudResourceNormal {
		t.Fatalf("unexpected legacy alias: %#v", alias)
	}
	var route iCloudAliasRouteModel
	if err := db.Where("alias_id = ?", alias.ID).Take(&route).Error; err != nil {
		t.Fatalf("read alias route: %v", err)
	}
	if route.ForwardToEmail != alias.ForwardToEmail || route.RecipientMailID != alias.RecipientMailID {
		t.Fatalf("unexpected alias route: %#v", route)
	}
}

func TestProcessICloudValidationAcceptsReconciledLegacyAlias(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 45, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "legacy-reconcile")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	run := iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: task.ValidationGeneration, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: task.ExpectedCredentialRevision, QueuedAt: now, StartedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create maintenance run: %v", err)
	}
	task.MaintenanceRunID, task.MaintenanceKind = run.ID, iCloudMaintenanceValidation
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).
		Update("alias_provision_candidate", "reconciled@icloud.com").Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	channels := []iCloudResourceChannelModel{
		{
			ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
			Cookie: "myacinfo=expired", Scnt: "expired", SessionStatus: iCloudSessionInvalid,
			SessionFailures: iCloudSessionFailureLimit, CreatedAt: now, UpdatedAt: now,
		},
		{
			ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com",
			Cookie: testICloudOldCookie, DSID: "123", ClientID: "client",
			ClientBuildNumber: "build", ClientMasteringNumber: "master",
			SessionStatus: iCloudSessionValid, CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := db.Create(&channels).Error; err != nil {
		t.Fatalf("create channels: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v2/hme/list" {
			t.Fatalf("unexpected legacy validation path %q", request.URL.Path)
		}
		body := `{"success":true,"result":{"selectedForwardTo":"dropbox@relay.example","forwardToEmails":["dropbox@relay.example"],"total":1,"hasMore":false,"hmeEmails":[{"hme":"reconciled@icloud.com","anonymousId":"reconciled-id","forwardToEmail":"dropbox@relay.example","recipientMailId":"relay-recipient-id","domain":"icloud.com","isActive":true}]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})

	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourceNormal || resource.SelectedForwardTo != "dropbox@relay.example" ||
		resource.AliasProvisionCandidate != "" || resource.LastValidAt == nil {
		t.Fatalf("unexpected reconciled resource: %#v", resource)
	}
	var storedRun iCloudMaintenanceRunModel
	if err := db.First(&storedRun, run.ID).Error; err != nil {
		t.Fatalf("read maintenance run: %v", err)
	}
	if storedRun.Status != iCloudMaintenanceSucceeded || storedRun.FinishedAt == nil {
		t.Fatalf("successful legacy validation was not recorded: %#v", storedRun)
	}
}

func TestProcessICloudValidationRejectsInactiveReconciledLegacyAlias(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 50, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "inactive-legacy-reconcile")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).
		Update("alias_provision_candidate", "inactive@icloud.com").Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelWeb, Host: "p119-maildomainws.icloud.com",
		Cookie: testICloudOldCookie, DSID: "123", ClientID: "client",
		ClientBuildNumber: "build", ClientMasteringNumber: "master",
		SessionStatus: iCloudSessionValid, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		body := `{"success":true,"result":{"selectedForwardTo":"dropbox@relay.example","total":1,"hasMore":false,"hmeEmails":[{"hme":"inactive@icloud.com","anonymousId":"inactive-id","forwardToEmail":"dropbox@relay.example","recipientMailId":"relay-recipient-id","isActive":false}]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})

	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.ValidationFailures != 1 ||
		resource.NextValidationAt == nil || resource.LastValidAt != nil {
		t.Fatalf("inactive alias validated resource: %#v", resource)
	}
	var alias iCloudAliasModel
	if err := db.Where("resource_id = ? AND anonymous_id = ?", 1, "inactive-id").Take(&alias).Error; err != nil {
		t.Fatalf("read inactive alias: %v", err)
	}
	if alias.Status != iCloudResourceDisabled {
		t.Fatalf("inactive alias status = %q, want %q", alias.Status, iCloudResourceDisabled)
	}
}

func TestProcessICloudValidationRetriesTemporaryScopeReadFailure(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 55, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "temporary-scope-read")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"validation_failures": 2, "selected_forward_to": "dropbox@relay.example",
	}).Error; err != nil {
		t.Fatalf("seed resource: %v", err)
	}
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
		Cookie: "myacinfo=secret", Scnt: "scnt", SessionStatus: iCloudSessionValid,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	resourceReads := 0
	callbackName := "test:fail_icloud_validation_scope_read"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "icloud_resources" {
			return
		}
		resourceReads++
		if resourceReads == 2 {
			_ = tx.AddError(fmt.Errorf("temporary scope read failure"))
		}
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("provider must not be called after a scope read failure")
		return nil, nil
	})})
	processErr := service.ProcessICloudValidation(context.Background(), task)
	_ = db.Callback().Query().Remove(callbackName)
	if processErr != nil {
		t.Fatalf("process validation: %v", processErr)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.ValidationFailures != 2 ||
		resource.NextValidationAt == nil || !resource.NextValidationAt.Equal(now.Add(iCloudValidationRetryInterval)) ||
		resource.SelectedForwardTo != "dropbox@relay.example" {
		t.Fatalf("temporary scope error changed resource health: %#v", resource)
	}
}

func TestProcessICloudValidationIgnoresStaleScope(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 57, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "stale-scope")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
		Cookie: "myacinfo=secret", Scnt: "scnt", SessionStatus: iCloudSessionValid,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	resourceReads := 0
	var callbackErr error
	callbackName := "test:stale_icloud_validation_scope"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "icloud_resources" {
			return
		}
		resourceReads++
		if resourceReads == 2 {
			_, callbackErr = tx.Statement.ConnPool.ExecContext(tx.Statement.Context,
				`UPDATE icloud_resources SET status = 'normal', credential_revision = 5, selected_forward_to = 'new@relay.example', last_safe_error = 'new credentials' WHERE id = 1`)
			if callbackErr != nil {
				_ = tx.AddError(callbackErr)
			}
		}
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	processErr := service.ProcessICloudValidation(context.Background(), task)
	_ = db.Callback().Query().Remove(callbackName)
	if callbackErr != nil {
		t.Fatalf("make validation stale: %v", callbackErr)
	}
	if processErr != nil {
		t.Fatalf("process validation: %v", processErr)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourceNormal || resource.CredentialRevision != 5 ||
		resource.SelectedForwardTo != "new@relay.example" || resource.LastSafeError != "new credentials" {
		t.Fatalf("stale validation overwrote new state: %#v", resource)
	}
}

func TestProcessICloudValidationRejectsUnapprovedForwardingDomain(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "unapproved-forward")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	setICloudForwardingSuffixes(t, "relay.example")
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
		Cookie: "myacinfo=secret", Scnt: "scnt", SessionStatus: iCloudSessionUnchecked,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = newICloudValidationAppleClient(t, "alias-id", "owner@other.example")
	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.SelectedForwardTo != "" || resource.ValidationFailures != 1 ||
		resource.NextValidationAt == nil || resource.NextProvisionAt != nil {
		t.Fatalf("unexpected rejected resource: %#v", resource)
	}
	var alias iCloudAliasModel
	if err := db.Where("anonymous_id = ?", "alias-id").Take(&alias).Error; err != nil {
		t.Fatalf("read rejected alias: %v", err)
	}
	if alias.Status != iCloudResourceDisabled {
		t.Fatalf("unapproved alias status = %q, want disabled", alias.Status)
	}
	if iCloudForwardingDomainAllowed("owner@sub.relay.example", iCloudForwardingDomains("relay.example")) {
		t.Fatal("forwarding authorization must compare the exact domain")
	}
	allowed := iCloudForwardingDomains("relay.example， backup.example\tthird.example")
	for _, email := range []string{"owner@relay.example", "owner@backup.example", "owner@third.example"} {
		if !iCloudForwardingDomainAllowed(email, allowed) {
			t.Fatalf("configured forwarding domain did not match %q", email)
		}
	}
}

func TestProcessICloudValidationKeepsPreparedForwardingAddressOnMismatch(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 15, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "prepared-forward-mismatch")
	task := createICloudValidationTestResource(t, db, now, now.Add(time.Hour))
	setICloudForwardingSuffixes(t, "relay.example")
	required := "prepared@relay.example"
	requireUpdate := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"selected_forward_to": required,
		"required_forward_to": required,
	})
	if requireUpdate.Error != nil {
		t.Fatalf("set prepared forwarding address: %v", requireUpdate.Error)
	}
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: 1, Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com",
		Cookie: "myacinfo=secret", Scnt: "scnt", SessionStatus: iCloudSessionUnchecked,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.apple = newICloudValidationAppleClient(t, "alias-id", "wrong@relay.example")
	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.SelectedForwardTo != required || resource.ValidationFailures != 1 {
		t.Fatalf("mismatched forwarding address polluted resource: %#v", resource)
	}
	var alias iCloudAliasModel
	if err := db.Where("anonymous_id = ?", "alias-id").Take(&alias).Error; err != nil {
		t.Fatalf("read mismatched alias: %v", err)
	}
	if alias.Status != iCloudResourceDisabled {
		t.Fatalf("mismatched alias status = %q, want disabled", alias.Status)
	}
}

func TestICloudValidationRetryReclaimsFinishedRun(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 30, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "retry")
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com",
		Status: iCloudResourcePending, ExpireAt: now.Add(time.Hour), CredentialRevision: 4,
		ValidationGeneration: 5, NextValidationAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	finishedAt := now.Add(-time.Minute)
	if err := db.Create(&iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: 5, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceFailed, Attempts: 1, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: 4, QueuedAt: now.Add(-time.Minute), FinishedAt: &finishedAt,
		LastSafeError: "temporary", CreatedAt: now.Add(-time.Minute), UpdatedAt: finishedAt,
	}).Error; err != nil {
		t.Fatalf("create maintenance run: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	task, claimed, err := service.markICloudValidationDispatched(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 5, ExpectedCredentialRevision: 4,
	})
	if err != nil || !claimed || task.MaintenanceRunID == 0 {
		t.Fatalf("retry claim = %#v, %t, %v", task, claimed, err)
	}
	var run iCloudMaintenanceRunModel
	if err := db.First(&run, task.MaintenanceRunID).Error; err != nil {
		t.Fatalf("read maintenance run: %v", err)
	}
	if run.Status != iCloudMaintenanceRunning || run.Attempts != 2 || run.FinishedAt != nil || run.LastSafeError != "" {
		t.Fatalf("unexpected maintenance run: %#v", run)
	}
}

func TestICloudValidationRetryDoesNotReuseAliasRunForSameGeneration(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 40, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "retry-kind")
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com",
		Status: iCloudResourcePending, ExpireAt: now.Add(time.Hour), CredentialRevision: 4,
		ValidationGeneration: 5, NextValidationAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	finishedAt := now.Add(-time.Minute)
	aliasRun := iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: 5, Kind: iCloudMaintenanceAlias,
		Status: iCloudMaintenanceQueued, Attempts: 0, MaxAttempts: 1,
		CredentialRevision: 4, QueuedAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	validationRun := iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: 5, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceFailed, Attempts: 1, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: 4, QueuedAt: now.Add(-time.Minute), FinishedAt: &finishedAt,
		LastSafeError: "temporary", CreatedAt: now.Add(-time.Minute), UpdatedAt: finishedAt,
	}
	if err := db.Create(&aliasRun).Error; err != nil {
		t.Fatalf("create alias run: %v", err)
	}
	if err := db.Create(&validationRun).Error; err != nil {
		t.Fatalf("create validation run: %v", err)
	}

	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	task, claimed, err := service.markICloudValidationDispatched(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 5, ExpectedCredentialRevision: 4,
	})
	if err != nil || !claimed || task.MaintenanceRunID != validationRun.ID || task.MaintenanceKind != iCloudMaintenanceValidation {
		t.Fatalf("validation claim = %#v, %t, %v; validation run id=%d", task, claimed, err, validationRun.ID)
	}
	var storedAliasRun iCloudMaintenanceRunModel
	if err := db.First(&storedAliasRun, aliasRun.ID).Error; err != nil {
		t.Fatalf("read alias run: %v", err)
	}
	if storedAliasRun.Status != iCloudMaintenanceQueued || storedAliasRun.Attempts != 0 {
		t.Fatalf("alias run was modified by validation claim: %#v", storedAliasRun)
	}
	var storedValidationRun iCloudMaintenanceRunModel
	if err := db.First(&storedValidationRun, validationRun.ID).Error; err != nil {
		t.Fatalf("read validation run: %v", err)
	}
	if storedValidationRun.Status != iCloudMaintenanceRunning || storedValidationRun.Attempts != 2 || storedValidationRun.FinishedAt != nil {
		t.Fatalf("validation run was not reclaimed: %#v", storedValidationRun)
	}
}

func TestICloudValidationStopsAtMaximumAttempts(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     string
		wantStatus string
	}{
		{name: "normal", status: iCloudResourceNormal, wantStatus: iCloudResourceNormal},
		{name: "pending", status: iCloudResourcePending, wantStatus: iCloudResourceAbnormal},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC)
			db := openICloudValidationTestDB(t, "max-attempts-"+test.name)
			if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&iCloudResourceModel{
				ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com", Status: test.status,
				ExpireAt: now.Add(time.Hour), CredentialRevision: 1, ValidationGeneration: 1,
				NextValidationAt: &now, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			finishedAt := now.Add(-time.Minute)
			if err := db.Create(&iCloudMaintenanceRunModel{
				ResourceID: 1, ValidationGeneration: 1, Kind: iCloudMaintenanceValidation,
				Status: iCloudMaintenanceFailed, Attempts: iCloudValidationMaxFailures, MaxAttempts: iCloudValidationMaxFailures,
				CredentialRevision: 1, QueuedAt: finishedAt, FinishedAt: &finishedAt, CreatedAt: finishedAt, UpdatedAt: finishedAt,
			}).Error; err != nil {
				t.Fatal(err)
			}
			service := NewService(db, nil, nil)
			service.now = func() time.Time { return now }
			_, claimed, err := service.markICloudValidationDispatched(context.Background(), iCloudValidationTask{
				ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 1,
			})
			if err != nil || claimed {
				t.Fatalf("maximum-attempt validation claim = %t, err=%v", claimed, err)
			}
			var resource iCloudResourceModel
			if err := db.First(&resource, 1).Error; err != nil {
				t.Fatal(err)
			}
			if resource.Status != test.wantStatus || resource.NextValidationAt != nil {
				t.Fatalf("resource was not terminally handled: %#v", resource)
			}
		})
	}
}

func TestICloudValidationLeaseRecoveryCountsExpiredAttempt(t *testing.T) {
	now := time.Date(2026, 8, 14, 11, 20, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "lease-attempt")
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com", Status: iCloudResourceNormal,
		ExpireAt: now.Add(time.Hour), CredentialRevision: 1, ValidationGeneration: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	startedAt := now.Add(-iCloudValidationRunningLease - time.Minute)
	if err := db.Create(&iCloudMaintenanceRunModel{
		ID: 1, ResourceID: 1, ValidationGeneration: 1, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: 1, QueuedAt: startedAt, StartedAt: &startedAt, CreatedAt: startedAt, UpdatedAt: startedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.recoverStaleICloudValidations(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var run iCloudMaintenanceRunModel
	if err := db.First(&run, 1).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != iCloudMaintenanceQueued || run.Attempts != 2 || run.StartedAt != nil {
		t.Fatalf("expired lease was not counted: %#v", run)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatal(err)
	}
	if resource.Status != iCloudResourceNormal || resource.NextValidationAt == nil || !resource.NextValidationAt.Equal(now) {
		t.Fatalf("normal resource lease recovery changed scheduling incorrectly: %#v", resource)
	}
}

func TestICloudValidationRecoversOrphanValidatingResource(t *testing.T) {
	now := time.Date(2026, 8, 14, 11, 35, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "orphan-validating")
	staleAt := now.Add(-iCloudValidationRunningLease - time.Minute)
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: staleAt, UpdatedAt: staleAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "orphan@example.com", Status: iCloudResourceValidating,
		CredentialRevision: 1, ValidationGeneration: 1, UpdatedAt: staleAt, CreatedAt: staleAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.recoverStaleICloudValidations(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatal(err)
	}
	if resource.Status != iCloudResourcePending || resource.NextValidationAt == nil || !resource.NextValidationAt.Equal(now) || resource.NextProvisionAt != nil {
		t.Fatalf("orphan validating resource was not recovered: %#v", resource)
	}
}

func TestICloudValidationSkipsCookieMaintenance(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name             string
		onboardingStatus string
		category         string
	}{
		{name: "active", onboardingStatus: iCloudOnboardingProcessing},
		{name: "blacklisted terminal", onboardingStatus: iCloudOnboardingFailed, category: "phone_blacklisted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openICloudValidationTestDB(t, "cookie-maintenance-"+strings.ReplaceAll(test.name, " ", "-"))
			if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&iCloudResourceModel{
				ID: 1, ResourceType: "icloud", PrimaryEmail: "maintenance@example.com", Status: iCloudResourceNormal,
				CredentialRevision: 4, ValidationGeneration: 5, NextValidationAt: &now,
				WorkflowTaskKind: "refresh", OnboardingStatus: test.onboardingStatus,
				WorkflowExpectedCredential: 4, WorkflowLastErrorCategory: test.category,
				CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			service := NewService(db, nil, nil)
			service.now = func() time.Time { return now }
			candidates, err := service.iCloudValidationCandidates(context.Background(), 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(candidates) != 0 {
				t.Fatalf("maintenance resource was selected: %+v", candidates)
			}
			task := iCloudValidationTask{ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 5, ExpectedCredentialRevision: 4}
			_, claimed, err := service.markICloudValidationDispatched(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			if claimed {
				t.Fatal("maintenance resource was claimed after the candidate recheck")
			}
		})
	}
}

func TestICloudValidationAllowsStaleCookieMaintenanceRevision(t *testing.T) {
	now := time.Date(2026, 8, 23, 11, 0, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "cookie-maintenance-stale-revision")
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "stale-maintenance@example.com", Status: iCloudResourceNormal,
		CredentialRevision: 5, ValidationGeneration: 6, NextValidationAt: &now,
		WorkflowTaskKind: "refresh", OnboardingStatus: iCloudOnboardingProcessing,
		WorkflowExpectedCredential: 4, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	candidates, err := service.iCloudValidationCandidates(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ExpectedCredentialRevision != 5 {
		t.Fatalf("stale maintenance blocked current credential validation: %#v", candidates)
	}
	resource := iCloudResourceModel{
		WorkflowTaskKind: "refresh", OnboardingStatus: iCloudOnboardingProcessing,
		WorkflowExpectedCredential: 4, CredentialRevision: 5,
	}
	if iCloudCookieMaintenanceBlocksValidation(&resource) {
		t.Fatal("stale Cookie maintenance still blocked validation")
	}
}

func TestICloudValidationReleaseRestoresValidatingResourceDuringCookieMaintenance(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 45, 0, 0, time.UTC)
	db := openICloudValidationTestDB(t, "release-cookie-maintenance")
	nextProvisionAt := now.Add(time.Minute)
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "maintenance@example.com", Status: iCloudResourceValidating,
		CredentialRevision: 4, ValidationGeneration: 5, NextProvisionAt: &nextProvisionAt,
		WorkflowTaskKind: "refresh", OnboardingStatus: iCloudOnboardingProcessing,
		WorkflowExpectedCredential: 4, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	run := iCloudMaintenanceRunModel{
		ID: 1, ResourceID: 1, ValidationGeneration: 5, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: 4, QueuedAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	err := service.releaseICloudValidation(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 5, ExpectedCredentialRevision: 4,
		MaintenanceRunID: run.ID, MaintenanceKind: iCloudMaintenanceValidation,
	}, "validation worker stopped")
	if err != nil {
		t.Fatal(err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatal(err)
	}
	if resource.Status != iCloudResourcePending || resource.NextValidationAt == nil || !resource.NextValidationAt.Equal(now) || resource.NextProvisionAt != nil {
		t.Fatalf("validating resource was left owned by validation: %#v", resource)
	}
	var storedRun iCloudMaintenanceRunModel
	if err := db.First(&storedRun, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedRun.Status != iCloudMaintenanceCanceled || storedRun.FinishedAt == nil {
		t.Fatalf("validation run was not canceled: %#v", storedRun)
	}
}

func openICloudValidationTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:icloud-validation-%s?mode=memory&cache=shared", name)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudResourceChannelModel{},
		&iCloudAliasModel{}, &iCloudAliasRouteModel{}, &iCloudMaintenanceRunModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db
}

func createICloudValidationTestResource(t *testing.T, db *gorm.DB, now, expireAt time.Time) iCloudValidationTask {
	t.Helper()
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "owner@example.com",
		Status: iCloudResourceValidating, ExpireAt: expireAt, CredentialRevision: 4,
		ValidationGeneration: 5, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	return iCloudValidationTask{ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 5, ExpectedCredentialRevision: 4}
}

func setICloudForwardingSuffixes(t *testing.T, value string) {
	t.Helper()
	previous := runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, "")
	runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, value)
	t.Cleanup(func() { runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, previous) })
}

func newICloudValidationAppleClient(t *testing.T, anonymousID, forwardToEmail string) *AppleAccountClient {
	t.Helper()
	return NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body := `{}`
		switch request.URL.Path {
		case appleAccountTokenPath:
			body = `{"timeOutInterval":15}`
		case "/account/manage":
			body = `{"apiKey":"api-key"}`
		case appleAccountPrivateEmailPath:
			body = fmt.Sprintf(`{"privateEmailList":[],"inactivePrivateEmailList":[],"forwardToEmailAddress":%q,"maxLimitReached":false}`, forwardToEmail)
		case "/account/manage/email/private/add":
			body = `{"emailAddress":"created@icloud.com"}`
		case "/account/manage/email/private/add/complete":
			body = fmt.Sprintf(`{"emailAddress":"created@icloud.com","id":%q,"active":true}`, anonymousID)
		case "/account/manage/email/private/" + anonymousID + ".em":
			body = fmt.Sprintf(`{"emailAddress":"created@icloud.com","forwardToEmail":%q,"active":true}`, forwardToEmail)
		default:
			t.Fatalf("unexpected Apple Account path %q", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
}
