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
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).
		Update("selected_forward_to", "old@other.example").Error; err != nil {
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
		case "/account/manage/email/private/add":
			body = `{"emailAddress":"new-alias@icloud.com"}`
		case "/account/manage/email/private/add/complete":
			body = `{"emailAddress":"new-alias@icloud.com","id":"apple-id","active":true}`
		case "/account/manage/email/private/apple-id.em":
			body = `{"emailAddress":"new-alias@icloud.com","forwardToEmail":"anything@relay.example","active":true}`
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
		"/account/manage/email/private/add",
		"/account/manage/email/private/add/complete",
		"/account/manage/email/private/apple-id.em",
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
		resource.NextValidationAt != nil || resource.LastValidAt == nil {
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
	for _, channel := range storedChannels {
		statuses[channel.Kind] = channel.SessionStatus
	}
	if statuses[iCloudChannelAppleAccount] != iCloudSessionValid || statuses[iCloudChannelWeb] != iCloudSessionInvalid {
		t.Fatalf("unexpected channel statuses: %#v", statuses)
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
				resource.SelectedForwardTo != "mailbox@relay.example" || resource.NextValidationAt != nil || resource.NextProvisionAt != nil {
				t.Fatalf("creation block changed resource health: %#v", resource)
			}
		})
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
