package icloud

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/appleweb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const testICloudFamilyResponse = `{
  "currentDsid":"child-dsid",
  "currentUserAppleId":"child@example.com",
  "family":{"familyId":"family-1","organizerDsid":"organizer-dsid"},
  "familyMembers":[
    {"dsid":"organizer-dsid"},
    {"dsid":"child-dsid"},
    {"dsid":"other-child-dsid"}
  ],
  "isLinkedToFamily":true,
  "isMemberOfFamily":true
}`

const testICloudFamilyCookie = testICloudOldCookie + "; myacinfo=family-session"

const testICloudPrimaryFamilyResponse = `{
  "currentDsid":"organizer-dsid",
  "currentUserAppleId":"primary@example.com",
  "family":{"familyId":"family-1","organizerDsid":"organizer-dsid"},
  "familyMembers":[
    {"dsid":"organizer-dsid"},
    {"dsid":"other-child-dsid"}
  ],
  "isLinkedToFamily":true,
  "isMemberOfFamily":true
}`

func TestICloudFamilyClientValidatesAuthoritativeMembership(t *testing.T) {
	client := newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != iCloudFamilyMembersEndpoint {
			t.Fatalf("family request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Cookie") != testICloudFamilyCookie || request.Header.Get("User-Agent") != appleweb.UserAgent ||
			request.Header.Get("Accept-Language") != appleweb.AcceptLanguage || request.Header.Get("Sec-CH-UA") != appleweb.SecCHUA ||
			request.Header.Get("Sec-CH-UA-Mobile") != "?0" || request.Header.Get("Sec-CH-UA-Platform") != appleweb.SecCHPlatform {
			t.Fatalf("family request did not reuse channel identity: headers=%v", request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(testICloudFamilyResponse))}, nil
	})})
	snapshot, err := client.fetch(context.Background(), iCloudResourceChannelModel{
		Kind: iCloudChannelWeb, Cookie: "mail-cookie=unused", SetupCookie: testICloudFamilyCookie, UserAgent: "windows-must-not-leak",
	})
	if err != nil {
		t.Fatalf("fetch family members: %v", err)
	}
	if snapshot.FamilyID != "family-1" || snapshot.OrganizerDSID != "organizer-dsid" || snapshot.CurrentDSID != "child-dsid" ||
		snapshot.CurrentUserAppleID != "child@example.com" || snapshot.RemoteExtraMemberCount != 2 || !snapshot.Linked || !snapshot.Member {
		t.Fatalf("unexpected family snapshot: %#v", snapshot)
	}

	duplicate := iCloudFamilyMembersResponse{}
	linked, member := true, true
	duplicate.IsLinkedToFamily, duplicate.IsMemberOfFamily = &linked, &member
	duplicate.CurrentDSID, duplicate.CurrentUserAppleID = "same", "child@example.com"
	duplicate.Family.FamilyID, duplicate.Family.OrganizerDSID = "family-1", "same"
	duplicate.FamilyMembers = append(duplicate.FamilyMembers, struct {
		DSID string `json:"dsid"`
	}{DSID: "same"}, struct {
		DSID string `json:"dsid"`
	}{DSID: "same"})
	_, err = validateICloudFamilyMembers(duplicate)
	var providerErr *iCloudFamilyError
	if !errors.As(err, &providerErr) || providerErr.Category != "provider_response_invalid" {
		t.Fatalf("duplicate DSID error = %#v", err)
	}
}

func TestICloudFamilyCookieValidationIsFamilySpecific(t *testing.T) {
	for _, test := range []struct {
		name   string
		cookie string
		valid  bool
	}{
		{name: "myacinfo", cookie: "dslang=CN-ZH; myacinfo=opaque", valid: true},
		{name: "caw", cookie: "site=CHN; caw=opaque", valid: true},
		{name: "ordinary cookie", cookie: "dslang=CN-ZH; site=CHN"},
		{name: "empty family token", cookie: "myacinfo=; site=CHN"},
		{name: "header injection", cookie: "myacinfo=opaque\r\nX-Test: injected"},
		{name: "nul", cookie: "caw=opaque\x00suffix"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validICloudFamilyCookie(test.cookie); got != test.valid {
				t.Fatalf("validICloudFamilyCookie(%q) = %v, want %v", test.cookie, got, test.valid)
			}
		})
	}
}

func TestICloudPrimaryKeepaliveSynchronizesFamilyState(t *testing.T) {
	db := openICloudFamilyTestDB(t, "primary-sync")
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	resource := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		CountryCode: "US", FamilyInviteURL: "invite", FamilySyncStatus: iCloudFamilySyncUnknown,
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), AliasCount: iCloudMaxAliases,
		NextProvisionAt: &now, CredentialRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatalf("create primary: %v", err)
	}
	channel := iCloudResourceChannelModel{
		ResourceID: resource.ID, Kind: iCloudChannelWeb, Host: "p1-maildomainws.icloud.com",
		Cookie: testICloudOldCookie, SetupCookie: testICloudFamilyCookie, UserAgent: "fixed-macos",
		DSID: "organizer-dsid", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "master",
		SessionStatus: iCloudSessionValid, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create old channel: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v2/hme/list" {
			t.Fatalf("unexpected HME path %q", request.URL.Path)
		}
		body := `{"success":true,"result":{"selectedForwardTo":"mailbox@relay.example","total":0,"hasMore":false,"hmeEmails":[]}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	service.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(testICloudPrimaryFamilyResponse))}, nil
	})})
	service.family.now = func() time.Time { return now }

	if err := service.ProcessICloudProvision(context.Background(), iCloudProvisionTask{ResourceID: resource.ID}); err != nil {
		t.Fatalf("process primary keepalive: %v", err)
	}
	if err := db.First(&resource, resource.ID).Error; err != nil {
		t.Fatalf("reload primary: %v", err)
	}
	if resource.FamilyID != "family-1" || resource.FamilyOrganizerDSID != "organizer-dsid" ||
		resource.FamilyRemoteMemberCount != 1 || resource.FamilySyncStatus != iCloudFamilySyncReady ||
		resource.FamilySyncedAt == nil || !resource.FamilySyncedAt.Equal(now) ||
		resource.FamilyNextSyncAt == nil || !resource.FamilyNextSyncAt.Equal(now.Add(iCloudCookieKeepaliveInterval())) ||
		resource.FamilySyncErrorCategory != "" {
		t.Fatalf("family state was not synchronized: %#v", resource)
	}
}

func TestICloudFamilyFailurePreservesInviteQuarantine(t *testing.T) {
	db := openICloudFamilyTestDB(t, "invite-quarantine-failure")
	now := time.Date(2026, 8, 17, 9, 10, 0, 0, time.UTC)
	resource := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		FamilyInviteURL: "expired-token", FamilySyncStatus: iCloudFamilySyncReady,
		FamilySyncErrorCategory: "family_invite_expired", Status: iCloudResourceNormal,
		ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	if err := service.persistICloudFamilyFailure(context.Background(), resource.ID, 0, "family_transport", now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&resource, resource.ID).Error; err != nil || resource.FamilySyncStatus != iCloudFamilySyncFailed || resource.FamilySyncErrorCategory != "family_invite_expired" {
		t.Fatalf("family failure removed invitation quarantine: resource=%#v err=%v", resource, err)
	}
}

func TestICloudFamilyRetryAfterSurvivesEarlierHMERetry(t *testing.T) {
	db := openICloudFamilyTestDB(t, "family-retry-after")
	now := time.Date(2026, 8, 17, 9, 15, 0, 0, time.UTC)
	familyDue := now
	resource := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		FamilySyncStatus: iCloudFamilySyncFailed, FamilyNextSyncAt: &familyDue,
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), AliasCount: iCloudMaxAliases,
		NextProvisionAt: &now, CredentialRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatalf("create primary: %v", err)
	}
	channel := iCloudResourceChannelModel{
		ResourceID: resource.ID, Kind: iCloudChannelWeb, Host: "p1-maildomainws.icloud.com",
		Cookie: testICloudOldCookie, SetupCookie: testICloudFamilyCookie,
		DSID: "organizer-dsid", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "master",
		SessionStatus: iCloudSessionValid, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	hmeCalls := 0
	familyCalls := 0
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.hme = NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		hmeCalls++
		status := http.StatusServiceUnavailable
		body := `{}`
		if hmeCalls > 1 {
			status = http.StatusOK
			body = `{"success":true,"result":{"selectedForwardTo":"mailbox@relay.example","total":0,"hasMore":false,"hmeEmails":[]}}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	service.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		familyCalls++
		header := make(http.Header)
		header.Set("Retry-After", "1200")
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: header, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})})
	service.family.now = func() time.Time { return now }

	if err := service.ProcessICloudProvision(context.Background(), iCloudProvisionTask{ResourceID: resource.ID}); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if err := db.First(&resource, resource.ID).Error; err != nil {
		t.Fatalf("reload first provision: %v", err)
	}
	if hmeCalls != 1 || familyCalls != 1 || resource.FamilyNextSyncAt == nil || !resource.FamilyNextSyncAt.Equal(now.Add(20*time.Minute)) ||
		resource.NextProvisionAt == nil || !resource.NextProvisionAt.Equal(now.Add(iCloudProvisionRetry)) {
		t.Fatalf("independent retry schedule was not persisted: hme=%d family=%d resource=%#v", hmeCalls, familyCalls, resource)
	}

	now = now.Add(iCloudProvisionRetry)
	if err := service.ProcessICloudProvision(context.Background(), iCloudProvisionTask{ResourceID: resource.ID}); err != nil {
		t.Fatalf("earlier HME retry: %v", err)
	}
	if err := db.First(&resource, resource.ID).Error; err != nil {
		t.Fatalf("reload HME retry: %v", err)
	}
	if familyCalls != 1 || resource.FamilyNextSyncAt == nil || !resource.FamilyNextSyncAt.Equal(familyDue.Add(20*time.Minute)) {
		t.Fatalf("earlier HME retry ignored FamilyWS Retry-After: calls=%d resource=%#v", familyCalls, resource)
	}
}

func TestICloudFamilySyncContinuesWithInvalidHMEChannel(t *testing.T) {
	db := openICloudFamilyTestDB(t, "invalid-hme-family-sync")
	now := time.Date(2026, 8, 17, 9, 25, 0, 0, time.UTC)
	resource := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		FamilySyncStatus: iCloudFamilySyncUnknown, Status: iCloudResourceNormal,
		ExpireAt: now.Add(time.Hour), AliasCount: iCloudMaxAliases, NextProvisionAt: &now,
		CredentialRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceChannelModel{
		ResourceID: resource.ID, Kind: iCloudChannelWeb, Cookie: "expired", SetupCookie: testICloudFamilyCookie,
		SessionStatus: iCloudSessionInvalid, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	familyCalls := 0
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		familyCalls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(testICloudPrimaryFamilyResponse))}, nil
	})})
	if err := service.ProcessICloudProvision(context.Background(), iCloudProvisionTask{ResourceID: resource.ID}); err != nil {
		t.Fatalf("process invalid HME channel: %v", err)
	}
	if err := db.First(&resource, resource.ID).Error; err != nil {
		t.Fatal(err)
	}
	if familyCalls != 1 || resource.FamilySyncStatus != iCloudFamilySyncReady || resource.FamilyRemoteMemberCount != 1 {
		t.Fatalf("FamilyWS was blocked by invalid HME channel: calls=%d resource=%#v", familyCalls, resource)
	}

	if err := db.Model(&resource).Updates(map[string]any{
		"family_sync_status": iCloudFamilySyncFailed, "family_next_sync_at": nil, "next_provision_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessICloudProvision(context.Background(), iCloudProvisionTask{ResourceID: resource.ID}); err != nil {
		t.Fatalf("process terminal family state: %v", err)
	}
	if familyCalls != 1 {
		t.Fatalf("terminal family state retried unexpectedly: calls=%d", familyCalls)
	}
}

func TestICloudPrimaryFamilySessionInvalidFailsClosed(t *testing.T) {
	db := openICloudFamilyTestDB(t, "primary-session-invalid")
	now := time.Date(2026, 8, 17, 9, 30, 0, 0, time.UTC)
	resource := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		FamilyID: "family-1", FamilyOrganizerDSID: "organizer-dsid", FamilyRemoteMemberCount: 2,
		FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: iCloudTimePointer(now.Add(-time.Minute)),
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatalf("create primary: %v", err)
	}
	channel := iCloudResourceChannelModel{
		ResourceID: resource.ID, Kind: iCloudChannelWeb, Cookie: testICloudOldCookie, SetupCookie: testICloudFamilyCookie,
		SessionStatus: iCloudSessionValid, NextKeepaliveAt: iCloudTimePointer(now.Add(time.Hour)), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create family channel: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})})
	if err := service.syncICloudPrimaryFamily(context.Background(), resource, channel, now); err != nil {
		t.Fatalf("sync invalid family session: %v", err)
	}
	var storedResource iCloudResourceModel
	if err := db.First(&storedResource, resource.ID).Error; err != nil {
		t.Fatalf("reload primary: %v", err)
	}
	var storedChannel iCloudResourceChannelModel
	if err := db.First(&storedChannel, channel.ID).Error; err != nil {
		t.Fatalf("reload family channel: %v", err)
	}
	if storedResource.FamilySyncStatus != iCloudFamilySyncFailed || storedResource.FamilySyncErrorCategory != "session_invalid" ||
		storedChannel.SessionStatus != iCloudSessionInvalid || storedChannel.NextKeepaliveAt != nil {
		t.Fatalf("invalid family session did not fail closed: resource=%#v channel=%#v", storedResource, storedChannel)
	}
}

func TestICloudPrimaryFamilyOlderSnapshotCannotReduceNewerCount(t *testing.T) {
	db := openICloudFamilyTestDB(t, "primary-stale-snapshot")
	newer := time.Date(2026, 8, 17, 9, 45, 0, 0, time.UTC)
	resource := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		FamilyRemoteMemberCount: 4, FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: &newer,
		Status: iCloudResourceNormal, ExpireAt: newer.Add(time.Hour), CreatedAt: newer, UpdatedAt: newer,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatalf("create primary: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return newer.Add(time.Minute) }
	older := newer.Add(-time.Minute)
	if err := service.persistICloudFamilyState(context.Background(), resource.ID, iCloudFamilyStateUpdate{
		RemoteExtraMemberCount: 3, Status: iCloudFamilySyncReady, SyncedAt: &older,
	}); err != nil {
		t.Fatalf("persist older family snapshot: %v", err)
	}
	if err := db.First(&resource, resource.ID).Error; err != nil {
		t.Fatalf("reload primary: %v", err)
	}
	if resource.FamilyRemoteMemberCount != 4 || resource.FamilySyncedAt == nil || !resource.FamilySyncedAt.Equal(newer) {
		t.Fatalf("older family snapshot replaced newer state: %#v", resource)
	}
}

func TestICloudFamilyCapacityFailsClosedAndCountsReservations(t *testing.T) {
	db := openICloudFamilyTestDB(t, "capacity")
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	resources := []iCloudResourceModel{
		{ID: 1, ResourceType: "icloud", PrimaryEmail: "one@example.com", AccountRole: "primary", CountryCode: "US", FamilyInviteURL: "one", FamilyID: "family-1", FamilyOrganizerDSID: "org-1", FamilyRemoteMemberCount: 2, FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: iCloudTimePointer(now.Add(-time.Minute)), Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: 2, ResourceType: "icloud", PrimaryEmail: "two@example.com", AccountRole: "primary", CountryCode: "US", FamilyInviteURL: "two", FamilyID: "family-2", FamilyOrganizerDSID: "org-2", FamilyRemoteMemberCount: 1, FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: iCloudTimePointer(now.Add(-time.Minute)), Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: 3, ResourceType: "icloud", PrimaryEmail: "stale@example.com", AccountRole: "primary", CountryCode: "US", FamilyInviteURL: "stale", FamilyID: "family-3", FamilyOrganizerDSID: "org-3", FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: iCloudTimePointer(now.Add(-iCloudFamilyCapacityCacheTTL - time.Second)), Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: 4, ResourceType: "icloud", PrimaryEmail: "failed@example.com", AccountRole: "primary", CountryCode: "US", FamilyInviteURL: "failed", FamilyID: "family-4", FamilyOrganizerDSID: "org-4", FamilySyncStatus: iCloudFamilySyncFailed, FamilySyncedAt: iCloudTimePointer(now), Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: 5, ResourceType: "icloud", PrimaryEmail: "quarantined@example.com", AccountRole: "primary", CountryCode: "US", FamilyInviteURL: "bad", FamilyID: "family-5", FamilyOrganizerDSID: "org-5", FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: iCloudTimePointer(now), FamilySyncErrorCategory: "family_invite_invalid", Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&resources).Error; err != nil {
		t.Fatalf("create primaries: %v", err)
	}
	reservations := []iCloudOnboardingTaskModel{
		{ID: 10, PrimaryEmail: "one-child@example.com", AccountRole: "child", FamilyPrimaryResourceID: testICloudFamilyUintPtr(1), Status: iCloudOnboardingProcessing, FamilyReservationConfirmed: false},
		{ID: 20, PrimaryEmail: "two-child-a@example.com", AccountRole: "child", FamilyPrimaryResourceID: testICloudFamilyUintPtr(2), Status: iCloudOnboardingProcessing, FamilyReservationConfirmed: false},
		{ID: 21, PrimaryEmail: "two-child-b@example.com", AccountRole: "child", FamilyPrimaryResourceID: testICloudFamilyUintPtr(2), Status: iCloudOnboardingWaiting, FamilyReservationConfirmed: false},
		{ID: 22, PrimaryEmail: "two-confirmed@example.com", AccountRole: "child", FamilyPrimaryResourceID: testICloudFamilyUintPtr(2), Status: iCloudOnboardingProcessing, FamilyReservationConfirmed: true},
	}
	if err := db.Create(&reservations).Error; err != nil {
		t.Fatalf("create reservations: %v", err)
	}
	task := &iCloudOnboardingTaskModel{ID: 99, CountryCode: "US"}
	selected, err := NewService(db, nil, nil).selectICloudFamilyPrimaryID(context.Background(), db, task, now)
	if err != nil || selected != 1 {
		t.Fatalf("selected primary = %d, err=%v", selected, err)
	}
	task.FamilyPrimaryResourceID = testICloudFamilyUintPtr(1)
	selected, err = NewService(db, nil, nil).selectICloudFamilyPrimaryID(context.Background(), db, task, now)
	if err != nil || selected != 2 {
		t.Fatalf("replacement primary = %d, err=%v", selected, err)
	}
	task.CountryCode = "CA"
	selected, err = NewService(db, nil, nil).selectICloudFamilyPrimaryID(context.Background(), db, task, now)
	if err != nil || selected != 0 {
		t.Fatalf("cross-country selection = %d, err=%v", selected, err)
	}
}

func TestICloudFamilyReconcileConfirmsReservationByStableIdentity(t *testing.T) {
	db := openICloudFamilyTestDB(t, "reconcile")
	now := time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC)
	primary := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		CountryCode: "US", FamilyInviteURL: "invite", FamilyID: "family-1", FamilyOrganizerDSID: "organizer-dsid", FamilySyncStatus: iCloudFamilySyncReady,
		FamilySyncedAt: iCloudTimePointer(now.Add(-time.Minute)), Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&primary).Error; err != nil {
		t.Fatalf("create primary: %v", err)
	}
	task := iCloudOnboardingTaskModel{
		ID: 10, PrimaryEmail: "child@example.com", AccountRole: "child", FamilyPrimaryResourceID: testICloudFamilyUintPtr(primary.ID),
		Status: iCloudOnboardingProcessing, Generation: 3, ClaimToken: "claim", FamilyReservationConfirmed: false,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	responseBody := testICloudFamilyResponse
	service.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responseBody))}, nil
	})})
	channel := &AppleOnboardingChannel{Kind: iCloudChannelFamilySession, Cookie: testICloudFamilyCookie, UserAgent: "fixed-macos"}
	if err := service.reconcileICloudOnboardingFamily(context.Background(), &task, channel); err != nil {
		t.Fatalf("reconcile matching family: %v", err)
	}
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if err := db.First(&primary, primary.ID).Error; err != nil {
		t.Fatalf("reload primary: %v", err)
	}
	if !task.FamilyReservationConfirmed || primary.FamilyRemoteMemberCount != 2 || primary.FamilySyncStatus != iCloudFamilySyncUnknown ||
		primary.FamilyNextSyncAt == nil || !primary.FamilyNextSyncAt.Equal(now) || primary.NextProvisionAt == nil || !primary.NextProvisionAt.Equal(now) {
		t.Fatalf("matching family was not committed: task=%#v primary=%#v", task, primary)
	}
	selected, err := service.selectICloudFamilyPrimaryID(context.Background(), db, &iCloudOnboardingTaskModel{ID: 99, CountryCode: "US"}, now)
	if err != nil || selected != 0 {
		t.Fatalf("family remained selectable before organizer resync: selected=%d err=%v", selected, err)
	}
	if err := db.Model(&primary).Update("family_remote_member_count", 4).Error; err != nil {
		t.Fatalf("advance authoritative family count: %v", err)
	}
	if err := db.Model(&task).Updates(map[string]any{"family_reservation_confirmed": false, "claim_token": "claim-stale"}).Error; err != nil {
		t.Fatalf("reset task for stale snapshot: %v", err)
	}
	task.FamilyReservationConfirmed = false
	task.ClaimToken = "claim-stale"
	if err := service.reconcileICloudOnboardingFamily(context.Background(), &task, channel); err != nil {
		t.Fatalf("reconcile stale matching family: %v", err)
	}
	if err := db.First(&primary, primary.ID).Error; err != nil {
		t.Fatalf("reload primary after stale snapshot: %v", err)
	}
	if primary.FamilyRemoteMemberCount != 4 {
		t.Fatalf("stale child snapshot reduced family count: %#v", primary)
	}

	if err := db.Model(&task).Updates(map[string]any{"family_reservation_confirmed": false, "claim_token": "claim-2"}).Error; err != nil {
		t.Fatalf("reset task: %v", err)
	}
	task.FamilyReservationConfirmed = false
	task.ClaimToken = "claim-2"
	responseBody = strings.Replace(testICloudFamilyResponse, "family-1", "other-family", 1)
	err = service.reconcileICloudOnboardingFamily(context.Background(), &task, channel)
	if !errors.Is(err, ErrICloudFamilyConflict) || iCloudFamilyErrorCategory(err) != "family_conflict" || iCloudFamilyErrorRetryable(err) {
		t.Fatalf("different family error = %#v", err)
	}
}

func TestICloudFamilyConcurrentStaleReconciliationsFailClosed(t *testing.T) {
	db := openICloudFamilyTestDB(t, "concurrent-stale-reconcile")
	now := time.Date(2026, 8, 17, 11, 30, 0, 0, time.UTC)
	primary := iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary", CountryCode: "US",
		FamilyInviteURL: "invite", FamilyID: "family-1", FamilyOrganizerDSID: "organizer-dsid",
		FamilyRemoteMemberCount: 3, FamilySyncStatus: iCloudFamilySyncReady, FamilySyncedAt: iCloudTimePointer(now.Add(-time.Minute)),
		Status: iCloudResourceNormal, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&primary).Error; err != nil {
		t.Fatal(err)
	}
	tasks := []iCloudOnboardingTaskModel{
		{ID: 10, PrimaryEmail: "child@example.com", AccountRole: "child", FamilyPrimaryResourceID: testICloudFamilyUintPtr(primary.ID), Status: iCloudOnboardingProcessing, Generation: 1, ClaimToken: "claim-1"},
		{ID: 11, PrimaryEmail: "child@example.com", AccountRole: "child", FamilyPrimaryResourceID: testICloudFamilyUintPtr(primary.ID), Status: iCloudOnboardingProcessing, Generation: 1, ClaimToken: "claim-2"},
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	staleSnapshot := `{
		"currentDsid":"child-dsid","currentUserAppleId":"child@example.com",
		"family":{"familyId":"family-1","organizerDsid":"organizer-dsid"},
		"familyMembers":[{"dsid":"organizer-dsid"},{"dsid":"child-dsid"},{"dsid":"a"},{"dsid":"b"},{"dsid":"c"}],
		"isLinkedToFamily":true,"isMemberOfFamily":true}`
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	service.family = newICloudFamilyClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(staleSnapshot))}, nil
	})})
	channel := &AppleOnboardingChannel{Kind: iCloudChannelFamilySession, Cookie: testICloudFamilyCookie, UserAgent: "fixed-macos"}
	for index := range tasks {
		if err := service.reconcileICloudOnboardingFamily(context.Background(), &tasks[index], channel); err != nil {
			t.Fatalf("reconcile task %d: %v", tasks[index].ID, err)
		}
	}
	if err := db.First(&primary, primary.ID).Error; err != nil {
		t.Fatal(err)
	}
	if primary.FamilyRemoteMemberCount != 4 || primary.FamilySyncStatus != iCloudFamilySyncUnknown {
		t.Fatalf("stale reconciliations did not close capacity: %#v", primary)
	}
	selected, err := service.selectICloudFamilyPrimaryID(context.Background(), db, &iCloudOnboardingTaskModel{ID: 99, CountryCode: "US"}, now)
	if err != nil || selected != 0 {
		t.Fatalf("stale family was selected before organizer resync: selected=%d err=%v", selected, err)
	}
}

func openICloudFamilyTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:icloud-family-"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudResourceModel{}, &iCloudResourceChannelModel{}, &iCloudAliasModel{},
		&iCloudAliasRouteModel{}, &iCloudMaintenanceRunModel{}, &iCloudOnboardingTaskModel{},
	); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return db
}

func testICloudFamilyUintPtr(value uint) *uint { return &value }
