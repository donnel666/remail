package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailtransportdomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestICloudValidationStateMachineAndCookieRotation(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
	}{
		{name: "success", statusCode: http.StatusOK},
		{name: "retryable provider failure", statusCode: http.StatusServiceUnavailable},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:icloud-validation-%d?mode=memory&cache=shared", index)), &gorm.Config{})
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudGmailResourceModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}); err != nil {
				t.Fatalf("migrate database: %v", err)
			}
			now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
			if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
				t.Fatalf("create root: %v", err)
			}
			if err := db.Create(&iCloudGmailResourceModel{ID: 11, Email: "target@gmail.com", Status: "normal"}).Error; err != nil {
				t.Fatalf("create Gmail: %v", err)
			}
			cookie := "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
			probeStartedAt := now.Add(-time.Minute)
			if err := db.Create(&iCloudResourceModel{
				ID: 1, ResourceType: "icloud", PrimaryEmail: "main@icloud.com", Host: "p119-maildomainws.icloud.com", DSID: "123",
				ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie, GmailResourceID: 11,
				ExpireAt: now.Add(time.Hour), Status: iCloudResourceValidating, SessionStatus: iCloudSessionUnchecked,
				CredentialRevision: 1, CredentialUpdatedAt: now, ValidationGeneration: 1, CreatedAt: now, UpdatedAt: now,
				DeliveryProbeToken: "probe-token", DeliveryProbeAlias: "alias-000@icloud.com",
				DeliveryProbeStartedAt: &probeStartedAt, DeliveryProbeVerifiedAt: &probeStartedAt,
			}).Error; err != nil {
				t.Fatalf("create iCloud resource: %v", err)
			}

			service := NewService(db, nil, nil)
			service.now = func() time.Time { return now }
			service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				body := ""
				headers := http.Header{}
				if testCase.statusCode == http.StatusOK {
					body = iCloudHMEListJSON(t, iCloudMaxAliases, "target@gmail.com")
					headers.Set("Set-Cookie", "X-APPLE-WEBAUTH-TOKEN=rotated; Path=/")
				}
				return &http.Response{StatusCode: testCase.statusCode, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
			})})

			err = service.ProcessICloudValidation(context.Background(), iCloudValidationTask{
				ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 1,
			})
			if err != nil {
				t.Fatalf("process validation: %v", err)
			}
			var resource iCloudResourceModel
			if err := db.First(&resource, 1).Error; err != nil {
				t.Fatalf("read resource: %v", err)
			}
			if testCase.statusCode == http.StatusOK {
				if resource.Status != iCloudResourceNormal || resource.SessionStatus != iCloudSessionValid || resource.ValidationFailures != 0 || resource.CredentialRevision != 2 || resource.AliasCount != iCloudMaxAliases || !strings.Contains(resource.Cookie, "rotated") {
					t.Fatalf("unexpected successful state: %#v", resource)
				}
				var alias iCloudAliasModel
				if err := db.Where("resource_id = ?", 1).First(&alias).Error; err != nil || alias.Status != iCloudResourceNormal {
					t.Fatalf("unexpected alias state: %#v err=%v", alias, err)
				}
				return
			}
			if resource.Status != iCloudResourcePending || resource.ValidationGeneration != 2 || resource.ValidationFailures != 0 || resource.NextValidationAt == nil {
				t.Fatalf("unexpected retryable state: %#v", resource)
			}
		})
	}
}

func iCloudHMEListJSON(t *testing.T, count int, forwardTo string) string {
	t.Helper()
	aliases := make([]map[string]any, count)
	for index := range aliases {
		aliases[index] = map[string]any{
			"hme": fmt.Sprintf("alias-%03d@icloud.com", index), "anonymousId": fmt.Sprintf("anonymous-%03d", index),
			"forwardToEmail": forwardTo, "isActive": true,
		}
	}
	body, err := json.Marshal(map[string]any{"success": true, "result": map[string]any{
		"selectedForwardTo": forwardTo, "total": count, "hasMore": false, "hmeEmails": aliases,
	}})
	if err != nil {
		t.Fatalf("marshal HME list: %v", err)
	}
	return string(body)
}

func TestICloudAliasReadinessRequiresExactly750(t *testing.T) {
	aliases := make([]hmeAlias, iCloudMaxAliases+1)
	for index := range aliases {
		aliases[index] = hmeAlias{
			AnonymousID: fmt.Sprintf("id-%d", index), Email: fmt.Sprintf("alias-%d@icloud.com", index),
			ForwardToEmail: "target@gmail.com", Active: true,
		}
	}
	if iCloudAliasesReadyForGmail(nil, "target@gmail.com") ||
		iCloudAliasesReadyForGmail(aliases[:iCloudMaxAliases-1], "target@gmail.com") ||
		!iCloudAliasesReadyForGmail(aliases[:iCloudMaxAliases], "target@gmail.com") ||
		iCloudAliasesReadyForGmail(aliases, "target@gmail.com") {
		t.Fatal("iCloud alias readiness must accept only exactly 750 ready aliases")
	}
	aliases[0].ForwardToEmail = "other@gmail.com"
	if iCloudAliasesReadyForGmail(aliases[:iCloudMaxAliases], "target@gmail.com") {
		t.Fatal("per-alias Gmail mismatch must fail readiness")
	}
}

func TestSyncICloudAliasesRestoresProviderAliasFromDeletedState(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-alias-restore?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudAliasModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudAliasModel{
		ResourceID: 1, AnonymousID: "alias-id", Email: "old@icloud.com", Status: iCloudResourceDeleted,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("create deleted alias: %v", err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		return syncICloudAliasesTx(tx, 1, []hmeAlias{{
			AnonymousID: "alias-id", Email: "restored@icloud.com", ForwardToEmail: "target@gmail.com", Active: true,
		}}, "target@gmail.com", true, now)
	})
	if err != nil {
		t.Fatalf("sync aliases: %v", err)
	}
	var alias iCloudAliasModel
	if err := db.Where("resource_id = ? AND anonymous_id = ?", 1, "alias-id").First(&alias).Error; err != nil {
		t.Fatalf("read alias: %v", err)
	}
	if alias.Status != iCloudResourceNormal || alias.Email != "restored@icloud.com" || alias.LastSeenAt == nil {
		t.Fatalf("provider snapshot must restore the alias: %#v", alias)
	}
}

func TestICloudValidationProvisionsOneAliasAtATime(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-validation-provision?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudGmailResourceModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	createICloudValidationResource(t, db, now)
	calls := make([]string, 0, 2)
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.Path)
		body := iCloudHMEListJSON(t, iCloudMaxAliases-1, "target@gmail.com")
		if request.URL.Path == "/v1/hme/generate" {
			body = `{"success":true,"result":{"hme":"candidate@icloud.com"}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err := service.ProcessICloudValidation(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 1,
	}); err != nil {
		t.Fatalf("process provisioning validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.AliasCount != iCloudMaxAliases-1 ||
		resource.AliasProvisionCandidate != "candidate@icloud.com" || resource.AliasProvisionReconcile ||
		resource.ValidationFailures != 0 || resource.NextValidationAt == nil {
		t.Fatalf("unexpected provisioning state: %#v", resource)
	}
	if len(calls) != 2 || calls[0] != "/v2/hme/list" || calls[1] != "/v1/hme/generate" {
		t.Fatalf("validation must perform one provisioning action: %#v", calls)
	}
}

func TestICloudProviderRateLimitUsesRetryAfter(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-validation-provider-rate-limit?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudGmailResourceModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	createICloudValidationResource(t, db, now)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"alias_provision_candidate": "candidate@icloud.com",
		"expire_at":                 now.Add(2 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("prepare candidate: %v", err)
	}
	calls := make([]string, 0, 2)
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.Path)
		if request.URL.Path == "/v1/hme/reserve" {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": {"900"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(iCloudHMEListJSON(t, iCloudMaxAliases-1, "target@gmail.com")))}, nil
	})})
	task := iCloudValidationTask{ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 1}
	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("process provider rate limit: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read provider rate limit: %v", err)
	}
	if len(calls) != 2 || resource.Status != iCloudResourcePending || resource.AliasProvisionReconcile ||
		resource.NextValidationAt == nil || !resource.NextValidationAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("provider rate limit must honor Retry-After: calls=%#v resource=%#v", calls, resource)
	}
}

func TestICloudValidationDiscardsExplicitlyRejectedCandidate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-validation-rejected-candidate?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudGmailResourceModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	createICloudValidationResource(t, db, now)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Update("alias_provision_candidate", "stale@icloud.com").Error; err != nil {
		t.Fatalf("store candidate: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body := iCloudHMEListJSON(t, iCloudMaxAliases-1, "target@gmail.com")
		if request.URL.Path == "/v1/hme/reserve" {
			body = `{"success":false,"errorCode":-41003}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err := service.ProcessICloudValidation(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 1,
	}); err != nil {
		t.Fatalf("process rejected candidate: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.AliasProvisionCandidate != "" ||
		resource.AliasProvisionReconcile || resource.ValidationFailures != 1 || resource.NextValidationAt == nil {
		t.Fatalf("rejected candidate must consume retry budget and be regenerated: %#v", resource)
	}
}

func TestICloudValidationRetriesReservedCandidateAfterReconcileMiss(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-validation-reserved-candidate?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudGmailResourceModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	createICloudValidationResource(t, db, now)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Update("alias_provision_candidate", "candidate@icloud.com").Error; err != nil {
		t.Fatalf("store candidate: %v", err)
	}
	paths := make([]string, 0, 3)
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		body := iCloudHMEListJSON(t, iCloudMaxAliases-1, "target@gmail.com")
		if request.URL.Path == "/v1/hme/reserve" {
			body = `{"success":true,"result":{"hme":{"hme":"candidate@icloud.com","anonymousId":"candidate-id","forwardToEmail":"target@gmail.com","isActive":true}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	task := iCloudValidationTask{ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 1}
	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("reserve candidate: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read reserved candidate: %v", err)
	}
	if !resource.AliasProvisionReconcile || resource.AliasProvisionCandidate != "candidate@icloud.com" {
		t.Fatalf("reserve must enter reconciliation: %#v", resource)
	}
	now = now.Add(iCloudAliasProvisionInterval)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"status": iCloudResourceValidating, "updated_at": now,
	}).Error; err != nil {
		t.Fatalf("claim reconciliation: %v", err)
	}
	task.ValidationGeneration++
	if err := service.ProcessICloudValidation(context.Background(), task); err != nil {
		t.Fatalf("reconcile candidate: %v", err)
	}
	resource = iCloudResourceModel{}
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read reconciliation: %v", err)
	}
	if len(paths) != 4 || paths[0] != "/v2/hme/list" || paths[1] != "/v1/hme/reserve" ||
		paths[2] != "/v2/hme/list" || paths[3] != "/v1/hme/reserve" ||
		!resource.AliasProvisionReconcile || resource.ValidationFailures != 0 {
		t.Fatalf("reconciliation must list before retrying the same candidate: paths=%#v resource=%#v", paths, resource)
	}
}

func TestICloudValidationActivatesOneInactiveAliasWithoutCreatingAnother(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-validation-activate?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudGmailResourceModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	createICloudValidationResource(t, db, now)
	paths := make([]string, 0, 2)
	listBody := strings.Replace(iCloudHMEListJSON(t, iCloudMaxAliases, "target@gmail.com"), `"isActive":true`, `"isActive":false`, 1)
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		body := listBody
		if request.URL.Path == "/v1/hme/activate" {
			body = `{"success":true,"result":{"anonymousId":"anonymous-000","isActive":true}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err := service.ProcessICloudValidation(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 1,
	}); err != nil {
		t.Fatalf("activate alias: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if len(paths) != 2 || paths[0] != "/v2/hme/list" || paths[1] != "/v1/hme/activate" ||
		resource.Status != iCloudResourcePending || resource.AliasCount != iCloudMaxAliases || resource.ValidationFailures != 0 {
		t.Fatalf("inactive alias must be activated without changing the alias count: paths=%#v resource=%#v", paths, resource)
	}
}

func TestICloudValidationDefersTransportErrorsAndKeepsRotatedCookie(t *testing.T) {
	t.Run("list transport error", func(t *testing.T) {
		db, err := gorm.Open(sqlite.Open("file:icloud-validation-list-transport?mode=memory&cache=shared"), &gorm.Config{})
		if err != nil {
			t.Fatalf("open database: %v", err)
		}
		if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudGmailResourceModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}); err != nil {
			t.Fatalf("migrate database: %v", err)
		}
		now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
		createICloudValidationResource(t, db, now)
		service := NewService(db, nil, nil)
		service.now = func() time.Time { return now }
		service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network unavailable")
		})})
		if err := service.ProcessICloudValidation(context.Background(), iCloudValidationTask{
			ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 1,
		}); err != nil {
			t.Fatalf("defer transport error: %v", err)
		}
		var resource iCloudResourceModel
		if err := db.First(&resource, 1).Error; err != nil {
			t.Fatalf("read resource: %v", err)
		}
		if resource.Status != iCloudResourcePending || resource.ValidationFailures != 0 || resource.NextValidationAt == nil {
			t.Fatalf("transport error must remain retryable: %#v", resource)
		}
	})

	t.Run("mutation transport error after list rotation", func(t *testing.T) {
		db, err := gorm.Open(sqlite.Open("file:icloud-validation-mutation-transport?mode=memory&cache=shared"), &gorm.Config{})
		if err != nil {
			t.Fatalf("open database: %v", err)
		}
		if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudGmailResourceModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}); err != nil {
			t.Fatalf("migrate database: %v", err)
		}
		now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
		createICloudValidationResource(t, db, now)
		service := NewService(db, nil, nil)
		service.now = func() time.Time { return now }
		service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/v1/hme/generate" {
				return nil, errors.New("network unavailable")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Set-Cookie": {"X-APPLE-WEBAUTH-TOKEN=rotated; Path=/"}},
				Body:       io.NopCloser(strings.NewReader(iCloudHMEListJSON(t, iCloudMaxAliases-1, "target@gmail.com"))),
			}, nil
		})})
		if err := service.ProcessICloudValidation(context.Background(), iCloudValidationTask{
			ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 1,
		}); err != nil {
			t.Fatalf("defer mutation error: %v", err)
		}
		var resource iCloudResourceModel
		if err := db.First(&resource, 1).Error; err != nil {
			t.Fatalf("read resource: %v", err)
		}
		if resource.Status != iCloudResourcePending || resource.ValidationFailures != 0 || resource.CredentialRevision != 2 ||
			!strings.Contains(resource.Cookie, "X-APPLE-WEBAUTH-TOKEN=rotated") {
			t.Fatalf("list cookie rotation must survive a later mutation error: %#v", resource)
		}
	})
}

func TestRequestAdminICloudValidationRestartsProbeAndRejectsInactiveResources(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-admin-validation?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudGmailResourceModel{}, &iCloudAliasModel{},
		&iCloudMaintenanceRunModel{}, &governanceinfra.OperationLogModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	createICloudValidationResource(t, db, now)
	startedAt := now.Add(-iCloudDeliveryProbeTimeout)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"status": iCloudResourceAbnormal, "validation_failures": iCloudValidationMaxFailures,
		"delivery_probe_token": "old-token", "delivery_probe_alias": "alias-000@icloud.com",
		"delivery_probe_started_at": startedAt, "last_safe_error": "old failure",
	}).Error; err != nil {
		t.Fatalf("prepare abnormal resource: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.RequestAdminICloudValidation(context.Background(), 99, 1, "request-1", "/v1/admin/icloud/resources/:resourceId/validation"); err != nil {
		t.Fatalf("request validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	var log governanceinfra.OperationLogModel
	if err := db.First(&log).Error; err != nil {
		t.Fatalf("read operation log: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.ValidationGeneration != 2 || resource.ValidationFailures != 0 ||
		resource.NextValidationAt == nil || resource.DeliveryProbeToken != "" || resource.DeliveryProbeStartedAt != nil ||
		log.OperatorUserID != 99 || log.OperationType != "icloud.admin_resource.validate" {
		t.Fatalf("manual validation must restart the state machine: resource=%#v log=%#v", resource, log)
	}

	for _, status := range []string{iCloudResourceDisabled, iCloudResourceDeleted} {
		if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Update("status", status).Error; err != nil {
			t.Fatalf("set status %s: %v", status, err)
		}
		err := service.RequestAdminICloudValidation(context.Background(), 99, 1, "request-2", "/v1/admin/icloud/resources/:resourceId/validation")
		if status == iCloudResourceDisabled && !errors.Is(err, ErrICloudResourceStatus) {
			t.Fatalf("disabled resource error = %v", err)
		}
		if status == iCloudResourceDeleted && !errors.Is(err, ErrICloudResourceNotFound) {
			t.Fatalf("deleted resource error = %v", err)
		}
	}
}

func TestICloudValidationSendsProbeOnceAndRequiresGmailReceipt(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-validation-delivery?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudGmailResourceModel{}, &iCloudAliasModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	createICloudValidationResource(t, db, now)
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	listBody := iCloudHMEListJSON(t, iCloudMaxAliases, "target@gmail.com")
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(listBody))}, nil
	})})
	sent := make([]mailtransportdomain.OutboundMessage, 0, 1)
	service.SetDeliveryPort(iCloudDeliveryFunc(func(_ context.Context, message mailtransportdomain.OutboundMessage) error {
		sent = append(sent, message)
		return nil
	}))
	probeFound := false
	service.SetGmailDeliveryProbe(iCloudProbeFunc(func(_ context.Context, resourceID uint, recipient, token string, _ time.Time) (bool, error) {
		if resourceID != 11 || recipient != "alias-000@icloud.com" || !strings.Contains(token, "remail-icloud-probe-1-") {
			t.Fatalf("unexpected probe input: resource=%d recipient=%q token=%q", resourceID, recipient, token)
		}
		return probeFound, nil
	}))
	if err := service.ProcessICloudValidation(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 1, ExpectedCredentialRevision: 1,
	}); err != nil {
		t.Fatalf("start delivery probe: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read pending probe: %v", err)
	}
	if len(sent) != 1 || resource.Status != iCloudResourcePending || resource.DeliveryProbeStartedAt == nil ||
		!strings.Contains(sent[0].TextBody, resource.DeliveryProbeToken) {
		t.Fatalf("unexpected pending probe: resource=%#v sent=%#v", resource, sent)
	}
	probeFound = true
	now = now.Add(time.Minute)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"status": iCloudResourceValidating, "updated_at": now,
	}).Error; err != nil {
		t.Fatalf("claim second validation: %v", err)
	}
	if err := service.ProcessICloudValidation(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 2, ExpectedCredentialRevision: 1,
	}); err != nil {
		t.Fatalf("confirm delivery probe: %v", err)
	}
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read verified probe: %v", err)
	}
	if len(sent) != 1 || resource.Status != iCloudResourceNormal || resource.DeliveryProbeVerifiedAt == nil {
		t.Fatalf("Gmail receipt must be required before normal: resource=%#v sends=%d", resource, len(sent))
	}
	firstToken := resource.DeliveryProbeToken
	listBody = strings.Replace(listBody, "alias-000@icloud.com", "replacement@icloud.com", 1)
	now = now.Add(iCloudKeepaliveInterval)
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", 1).Updates(map[string]any{
		"status": iCloudResourceValidating, "updated_at": now,
	}).Error; err != nil {
		t.Fatalf("claim replacement validation: %v", err)
	}
	if err := service.ProcessICloudValidation(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 2, ExpectedCredentialRevision: 1,
	}); err != nil {
		t.Fatalf("replace delivery probe: %v", err)
	}
	resource = iCloudResourceModel{}
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read replacement probe: %v", err)
	}
	if len(sent) != 2 || sent[1].To != "replacement@icloud.com" || resource.Status != iCloudResourcePending ||
		resource.DeliveryProbeAlias != "replacement@icloud.com" || resource.DeliveryProbeVerifiedAt != nil ||
		resource.DeliveryProbeToken == firstToken {
		t.Fatalf("replacement alias must receive a fresh probe: resource=%#v sent=%#v", resource, sent)
	}
}

func createICloudValidationResource(t *testing.T, db *gorm.DB, now time.Time) {
	t.Helper()
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudGmailResourceModel{ID: 11, Email: "target@gmail.com", Status: "normal"}).Error; err != nil {
		t.Fatalf("create Gmail: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "main@icloud.com", Host: "p119-maildomainws.icloud.com", DSID: "123",
		ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering",
		Cookie:          "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token",
		GmailResourceID: 11, ExpireAt: now.Add(time.Hour), Status: iCloudResourceValidating,
		SessionStatus: iCloudSessionUnchecked, CredentialRevision: 1, CredentialUpdatedAt: now,
		ValidationGeneration: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create iCloud resource: %v", err)
	}
}

type iCloudDeliveryFunc func(context.Context, mailtransportdomain.OutboundMessage) error

func (send iCloudDeliveryFunc) Send(ctx context.Context, message mailtransportdomain.OutboundMessage) error {
	return send(ctx, message)
}

type iCloudProbeFunc func(context.Context, uint, string, string, time.Time) (bool, error)

func (probe iCloudProbeFunc) ProbeICloudDelivery(ctx context.Context, resourceID uint, recipient, token string, since time.Time) (bool, error) {
	return probe(ctx, resourceID, recipient, token, since)
}

func TestICloudValidationDispatcherRecoversStaleLease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-validation-stale?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudResourceModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "stale@icloud.com", Host: "p119-maildomainws.icloud.com",
		DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering",
		Cookie: "cookie", GmailResourceID: 2, ExpireAt: now.Add(time.Hour), Status: iCloudResourceValidating,
		ValidationGeneration: 4, CredentialRevision: 1, UpdatedAt: now.Add(-iCloudValidationRunningLease - time.Second),
	}).Error; err != nil {
		t.Fatalf("create stale resource: %v", err)
	}
	service := NewService(db, nil, nil)
	if err := service.recoverStaleICloudValidations(context.Background(), now); err != nil {
		t.Fatalf("recover stale validation: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, 1).Error; err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if resource.Status != iCloudResourcePending || resource.ValidationGeneration != 5 {
		t.Fatalf("unexpected recovered state: %#v", resource)
	}
}

func TestICloudMaintenanceRunKeepsAliasIntentAcrossGenerations(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-maintenance-alias-generations?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "alias@icloud.com", Status: iCloudResourcePending,
		CredentialRevision: 2, ValidationGeneration: 4, ExpireAt: now.Add(time.Hour),
		NextValidationAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	if err := db.Create(&iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: 4, Kind: iCloudMaintenanceAlias,
		Status: iCloudMaintenanceQueued, MaxAttempts: 3, CredentialRevision: 2,
		QueuedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create maintenance run: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	task, claimed, err := service.markICloudValidationDispatched(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 4, ExpectedCredentialRevision: 2,
	})
	if err != nil || !claimed || task.MaintenanceKind != iCloudMaintenanceAlias {
		t.Fatalf("claim alias maintenance: task=%#v claimed=%v err=%v", task, claimed, err)
	}
	next := now.Add(time.Minute)
	if err := service.applyICloudValidationResult(context.Background(), task, iCloudValidationResult{
		Deferred: true, Retryable: true, SafeMessage: "Alias provisioning continues.", NextValidationAt: &next,
	}); err != nil {
		t.Fatalf("apply deferred alias maintenance: %v", err)
	}
	var runs []iCloudMaintenanceRunModel
	if err := db.Order("validation_generation ASC").Find(&runs).Error; err != nil {
		t.Fatalf("list maintenance runs: %v", err)
	}
	if len(runs) != 2 || runs[0].Kind != iCloudMaintenanceAlias || runs[0].Status != iCloudMaintenanceSucceeded ||
		runs[1].ValidationGeneration != 5 || runs[1].Kind != iCloudMaintenanceAlias || runs[1].Status != iCloudMaintenanceQueued {
		t.Fatalf("alias maintenance intent was not preserved: %#v", runs)
	}
}

func TestICloudLegacyValidationTaskFinishesBackfilledMaintenanceRun(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-maintenance-legacy-payload?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudMaintenanceRunModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 8, 13, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "legacy@icloud.com", Status: iCloudResourceValidating,
		CredentialRevision: 2, ValidationGeneration: 4, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}
	if err := db.Create(&iCloudMaintenanceRunModel{
		ResourceID: 1, ValidationGeneration: 4, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: 3, CredentialRevision: 2,
		QueuedAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create maintenance run: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.applyICloudValidationResult(context.Background(), iCloudValidationTask{
		ResourceID: 1, OwnerUserID: 7, ValidationGeneration: 4, ExpectedCredentialRevision: 2,
	}, iCloudValidationResult{Valid: true}); err != nil {
		t.Fatalf("apply legacy validation result: %v", err)
	}
	var run iCloudMaintenanceRunModel
	if err := db.First(&run).Error; err != nil {
		t.Fatalf("read maintenance run: %v", err)
	}
	if run.Status != iCloudMaintenanceSucceeded || run.FinishedAt == nil {
		t.Fatalf("legacy task left maintenance run active: %#v", run)
	}
}
