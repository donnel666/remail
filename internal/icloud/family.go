package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/appleweb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudFamilyMembersEndpoint    = "https://familyws.icloud.apple.com/api/family-members"
	iCloudFamilyResponseMaxBytes   = 1 << 20
	iCloudFamilyCapacityCacheTTL   = 30 * time.Minute
	iCloudFamilySyncUnknown        = "unknown"
	iCloudFamilySyncReady          = "ready"
	iCloudFamilySyncFailed         = "failed"
	iCloudFamilySyncInactive       = "inactive"
	iCloudFamilyProviderMaxMembers = iCloudFamilyChildLimit + 1
)

func isICloudFamilyInviteFailure(category string) bool {
	switch strings.TrimSpace(category) {
	case "family_invite_expired", "family_invite_invalid", "family_invite_unavailable":
		return true
	default:
		return false
	}
}

func preserveICloudFamilyInviteFailure(current, next string) string {
	if isICloudFamilyInviteFailure(current) {
		return current
	}
	return next
}

var (
	ErrICloudFamilyConflict            = errors.New("icloud: Apple account belongs to a different family")
	ErrICloudFamilyCapacityUnavailable = errors.New("icloud: family capacity is unavailable")
)

type iCloudFamilyError struct {
	Category    string
	SafeMessage string
	Retryable   bool
	RetryAfter  time.Duration
	HTTPStatus  int
}

func (e *iCloudFamilyError) Error() string {
	if e == nil || strings.TrimSpace(e.SafeMessage) == "" {
		return "iCloud family service request failed."
	}
	return e.SafeMessage
}

type iCloudFamilyClient struct {
	httpClient appleHTTPDoer
	endpoint   string
	now        func() time.Time
}

func newICloudFamilyClient(client *http.Client) *iCloudFamilyClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if client.CheckRedirect == nil {
		clone := *client
		clone.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		client = &clone
	}
	return &iCloudFamilyClient{httpClient: client, endpoint: iCloudFamilyMembersEndpoint, now: time.Now}
}

type iCloudFamilySnapshot struct {
	CurrentDSID            string
	CurrentUserAppleID     string
	FamilyID               string
	OrganizerDSID          string
	RemoteExtraMemberCount uint8
	Linked                 bool
	Member                 bool
}

type iCloudFamilyMembersResponse struct {
	CurrentDSID        string `json:"currentDsid"`
	CurrentUserAppleID string `json:"currentUserAppleId"`
	Family             struct {
		FamilyID      string `json:"familyId"`
		OrganizerDSID string `json:"organizerDsid"`
	} `json:"family"`
	FamilyMembers []struct {
		DSID string `json:"dsid"`
	} `json:"familyMembers"`
	IsLinkedToFamily *bool `json:"isLinkedToFamily"`
	IsMemberOfFamily *bool `json:"isMemberOfFamily"`
}

func (c *iCloudFamilyClient) fetch(ctx context.Context, channel iCloudResourceChannelModel) (iCloudFamilySnapshot, error) {
	if c == nil || c.httpClient == nil || strings.TrimSpace(c.endpoint) == "" {
		return iCloudFamilySnapshot{}, &iCloudFamilyError{Category: "provider_unavailable", SafeMessage: "iCloud family service is unavailable.", Retryable: true}
	}
	cookie := strings.TrimSpace(channel.SetupCookie)
	if cookie == "" {
		cookie = strings.TrimSpace(channel.Cookie)
	}
	if (channel.Kind != "" && channel.Kind != iCloudChannelWeb && channel.Kind != iCloudChannelFamilySession) || !validICloudFamilyCookie(cookie) {
		return iCloudFamilySnapshot{}, &iCloudFamilyError{Category: "session_invalid", SafeMessage: "iCloud family session is invalid."}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return iCloudFamilySnapshot{}, &iCloudFamilyError{Category: "invalid_context", SafeMessage: "iCloud family request context is invalid."}
	}
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Accept-Language", appleweb.AcceptLanguage)
	request.Header.Set("Cache-Control", "no-cache")
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Pragma", "no-cache")
	request.Header.Set("Referer", "https://familyws.icloud.apple.com/members?wid=d&env=idms_prod_account&theme=light&locale=zh_CN")
	request.Header.Set("Sec-Fetch-Dest", "empty")
	request.Header.Set("Sec-Fetch-Mode", "cors")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	userAgent := strings.TrimSpace(channel.UserAgent)
	if userAgent == "" {
		userAgent = defaultICloudHMEUserAgent
	}
	request.Header.Set("User-Agent", userAgent)
	setAppleBrowserClientHints(request.Header, userAgent)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return iCloudFamilySnapshot{}, &iCloudFamilyError{Category: "provider_unavailable", SafeMessage: "iCloud family service is temporarily unavailable.", Retryable: true}
	}
	if response == nil || response.Body == nil {
		return iCloudFamilySnapshot{}, &iCloudFamilyError{Category: "provider_unavailable", SafeMessage: "iCloud family service is temporarily unavailable.", Retryable: true}
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, iCloudFamilyResponseMaxBytes+1))
	if readErr != nil || len(body) > iCloudFamilyResponseMaxBytes {
		return iCloudFamilySnapshot{}, &iCloudFamilyError{Category: "provider_response_invalid", SafeMessage: "iCloud family service returned an invalid response.", Retryable: true, HTTPStatus: response.StatusCode}
	}
	now := time.Now().UTC()
	if c.now != nil {
		now = c.now().UTC()
	}
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return iCloudFamilySnapshot{}, &iCloudFamilyError{Category: "session_invalid", SafeMessage: "iCloud family session is invalid.", HTTPStatus: response.StatusCode}
	case http.StatusTooManyRequests:
		return iCloudFamilySnapshot{}, &iCloudFamilyError{
			Category: "rate_limited", SafeMessage: "iCloud family service is rate limited.", Retryable: true,
			RetryAfter: iCloudResponseRetryAfter(response.Header.Get("Retry-After"), body, now), HTTPStatus: response.StatusCode,
		}
	default:
		return iCloudFamilySnapshot{}, &iCloudFamilyError{Category: "provider_unavailable", SafeMessage: "iCloud family service is temporarily unavailable.", Retryable: true, HTTPStatus: response.StatusCode}
	}
	var payload iCloudFamilyMembersResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return iCloudFamilySnapshot{}, &iCloudFamilyError{Category: "provider_response_invalid", SafeMessage: "iCloud family service returned an invalid response.", Retryable: true, HTTPStatus: response.StatusCode}
	}
	return validateICloudFamilyMembers(payload)
}

func validateICloudFamilyMembers(payload iCloudFamilyMembersResponse) (iCloudFamilySnapshot, error) {
	if payload.IsLinkedToFamily == nil || payload.IsMemberOfFamily == nil {
		return iCloudFamilySnapshot{}, invalidICloudFamilyResponse()
	}
	snapshot := iCloudFamilySnapshot{Linked: *payload.IsLinkedToFamily, Member: *payload.IsMemberOfFamily}
	if !snapshot.Linked || !snapshot.Member {
		return snapshot, nil
	}
	snapshot.CurrentDSID = normalizeICloudFamilyValue(payload.CurrentDSID, 64)
	snapshot.CurrentUserAppleID = strings.ToLower(normalizeICloudFamilyValue(payload.CurrentUserAppleID, 320))
	snapshot.FamilyID = normalizeICloudFamilyValue(payload.Family.FamilyID, 128)
	snapshot.OrganizerDSID = normalizeICloudFamilyValue(payload.Family.OrganizerDSID, 64)
	if snapshot.CurrentDSID == "" || snapshot.CurrentUserAppleID == "" || snapshot.FamilyID == "" || snapshot.OrganizerDSID == "" ||
		len(payload.FamilyMembers) == 0 || len(payload.FamilyMembers) > iCloudFamilyProviderMaxMembers {
		return iCloudFamilySnapshot{}, invalidICloudFamilyResponse()
	}
	seen := make(map[string]struct{}, len(payload.FamilyMembers))
	currentCount, organizerCount, extraCount := 0, 0, 0
	for _, member := range payload.FamilyMembers {
		dsid := normalizeICloudFamilyValue(member.DSID, 64)
		if dsid == "" {
			return iCloudFamilySnapshot{}, invalidICloudFamilyResponse()
		}
		if _, exists := seen[dsid]; exists {
			return iCloudFamilySnapshot{}, invalidICloudFamilyResponse()
		}
		seen[dsid] = struct{}{}
		if dsid == snapshot.CurrentDSID {
			currentCount++
		}
		if dsid == snapshot.OrganizerDSID {
			organizerCount++
		} else {
			extraCount++
		}
	}
	if currentCount != 1 || organizerCount != 1 || extraCount > iCloudFamilyChildLimit {
		return iCloudFamilySnapshot{}, invalidICloudFamilyResponse()
	}
	snapshot.RemoteExtraMemberCount = uint8(extraCount)
	return snapshot, nil
}

func invalidICloudFamilyResponse() *iCloudFamilyError {
	return &iCloudFamilyError{Category: "provider_response_invalid", SafeMessage: "iCloud family service returned an invalid response.", Retryable: true}
}

func normalizeICloudFamilyValue(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func validICloudFamilyCookie(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || len(value) > iCloudImportCookieMaxBytes || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	for _, part := range strings.Split(value, ";") {
		name, cookieValue, found := strings.Cut(strings.TrimSpace(part), "=")
		if found && strings.TrimSpace(cookieValue) != "" && (strings.EqualFold(name, "myacinfo") || strings.EqualFold(name, "caw")) {
			return true
		}
	}
	return false
}

func (s *Service) syncICloudPrimaryFamily(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, now time.Time) error {
	_, err := s.syncICloudPrimaryFamilyScheduled(ctx, resource, channel, now)
	return err
}

func (s *Service) syncICloudPrimaryFamilyScheduled(ctx context.Context, resource iCloudResourceModel, channel iCloudResourceChannelModel, now time.Time) (time.Time, error) {
	if s == nil || s.db == nil || resource.ID == 0 || resource.AccountRole != "primary" || channel.Kind != iCloudChannelWeb {
		return time.Time{}, nil
	}
	ctx = withAppleRouteEmail(ctx, resource.PrimaryEmail)
	client := s.family
	if client == nil {
		client = newRoutedICloudFamilyClient(s.appleRoutes)
	}
	snapshot, err := client.fetch(ctx, channel)
	if err != nil {
		category := "provider_unavailable"
		retryable := true
		retryAt := time.Time{}
		var providerErr *iCloudFamilyError
		if errors.As(err, &providerErr) {
			if strings.TrimSpace(providerErr.Category) != "" {
				category = providerErr.Category
			}
			retryable = providerErr.Retryable
			if retryable {
				delay := providerErr.RetryAfter
				if delay <= 0 {
					delay = iCloudProvisionRetry
				}
				retryAt = now.Add(delay)
			}
		} else if retryable {
			retryAt = now.Add(iCloudProvisionRetry)
		}
		return retryAt, s.persistICloudFamilyFailure(ctx, resource.ID, channel.ID, category, retryAt, now)
	}
	nextSyncAt := now.Add(iCloudCookieKeepaliveInterval())
	if !snapshot.Linked || !snapshot.Member {
		return nextSyncAt, s.persistICloudFamilyState(ctx, resource.ID, iCloudFamilyStateUpdate{
			Status: iCloudFamilySyncInactive, RemoteExtraMemberCount: 0,
			ErrorCategory: "family_sharing_inactive", SyncedAt: &now, NextSyncAt: &nextSyncAt,
		})
	}
	if !strings.EqualFold(snapshot.CurrentUserAppleID, resource.PrimaryEmail) {
		return time.Time{}, s.persistICloudFamilyFailure(ctx, resource.ID, channel.ID, "family_identity_mismatch", time.Time{}, now)
	}
	if snapshot.CurrentDSID != snapshot.OrganizerDSID {
		return nextSyncAt, s.persistICloudFamilyState(ctx, resource.ID, iCloudFamilyStateUpdate{
			Status: iCloudFamilySyncInactive, RemoteExtraMemberCount: 0,
			ErrorCategory: "family_sharing_inactive", SyncedAt: &now, NextSyncAt: &nextSyncAt,
		})
	}
	return nextSyncAt, s.persistICloudFamilyState(ctx, resource.ID, iCloudFamilyStateUpdate{
		FamilyID: snapshot.FamilyID, OrganizerDSID: snapshot.OrganizerDSID,
		RemoteExtraMemberCount: snapshot.RemoteExtraMemberCount, Status: iCloudFamilySyncReady, SyncedAt: &now, NextSyncAt: &nextSyncAt,
	})
}

type iCloudFamilyStateUpdate struct {
	FamilyID               string
	OrganizerDSID          string
	RemoteExtraMemberCount uint8
	Status                 string
	SyncedAt               *time.Time
	NextSyncAt             *time.Time
	ErrorCategory          string
}

func (s *Service) persistICloudFamilyState(ctx context.Context, resourceID uint, update iCloudFamilyStateUpdate) error {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(
			"id", "account_role", "family_remote_member_count", "family_synced_at", "family_sync_error_category",
		).First(&current, resourceID).Error; err != nil || current.AccountRole != "primary" {
			return ErrICloudValidationTemp
		}
		if update.SyncedAt != nil && current.FamilySyncedAt != nil &&
			(current.FamilySyncedAt.After(*update.SyncedAt) ||
				current.FamilySyncedAt.Equal(*update.SyncedAt) && current.FamilyRemoteMemberCount > update.RemoteExtraMemberCount) {
			return nil
		}
		values := map[string]any{
			"family_remote_member_count": update.RemoteExtraMemberCount,
			"family_sync_status":         update.Status,
			"family_synced_at":           update.SyncedAt,
			"family_sync_error_category": preserveICloudFamilyInviteFailure(current.FamilySyncErrorCategory, update.ErrorCategory),
			"updated_at":                 now().UTC().Truncate(time.Millisecond),
		}
		if update.NextSyncAt != nil {
			values["family_next_sync_at"] = update.NextSyncAt
		}
		if update.FamilyID != "" {
			values["family_id"] = update.FamilyID
		}
		if update.OrganizerDSID != "" {
			values["family_organizer_dsid"] = update.OrganizerDSID
		}
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ?", resourceID).Updates(values).Error; err != nil {
			return ErrICloudValidationTemp
		}
		return nil
	})
}

func (s *Service) persistICloudFamilyFailure(ctx context.Context, resourceID, channelID uint, category string, retryAt, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "account_role", "family_sync_error_category").First(&current, resourceID).Error; err != nil || current.AccountRole != "primary" {
			return ErrICloudValidationTemp
		}
		updates := map[string]any{
			"family_sync_status": iCloudFamilySyncFailed, "family_sync_error_category": preserveICloudFamilyInviteFailure(current.FamilySyncErrorCategory, category),
			"family_next_sync_at": nil, "updated_at": now,
		}
		if !retryAt.IsZero() {
			updates["family_next_sync_at"] = retryAt
		}
		result := tx.Model(&iCloudResourceModel{}).Where("id = ? AND account_role = ?", resourceID, "primary").Updates(updates)
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrICloudValidationTemp
		}
		if (category == "session_invalid" || category == "family_identity_mismatch") && channelID != 0 {
			if err := tx.Model(&iCloudResourceChannelModel{}).Where("id = ? AND resource_id = ?", channelID, resourceID).Updates(map[string]any{
				"session_status": iCloudSessionInvalid, "next_keepalive_at": nil, "updated_at": now,
			}).Error; err != nil {
				return ErrICloudValidationTemp
			}
		}
		return nil
	})
}

func (s *Service) selectICloudFamilyPrimaryID(ctx context.Context, tx *gorm.DB, task *iCloudOnboardingTaskModel, now time.Time) (uint, error) {
	if s == nil || tx == nil || task == nil || task.ID == 0 {
		return 0, ErrICloudOnboardingTemporary
	}
	countryCode := strings.ToUpper(strings.TrimSpace(task.CountryCode))
	if countryCode == "" {
		return 0, nil
	}
	excludedPrimaryID := uint(0)
	if task.FamilyPrimaryResourceID != nil {
		excludedPrimaryID = *task.FamilyPrimaryResourceID
	}
	query := `SELECT p.id
		FROM icloud_resources AS p
		WHERE p.account_role = 'primary' AND p.family_invite_url <> ''
		  AND p.status NOT IN ('disabled', 'deleted')
		  AND p.id <> ?
		  AND p.country_code = ?
		  AND p.family_sync_status = 'ready' AND p.family_synced_at >= ?
		  AND p.family_sync_error_category NOT IN ('family_invite_expired', 'family_invite_invalid', 'family_invite_unavailable')
		  AND p.family_id <> '' AND p.family_organizer_dsid <> ''
		  AND (p.family_remote_member_count +
		       (SELECT COUNT(*) FROM icloud_resources AS ot
		        WHERE ot.family_primary_resource_id = p.id
		          AND ot.task_kind = 'onboarding'
		          AND ot.id <> ? AND ot.onboarding_status IN ('processing', 'waiting')
		          AND ot.family_reservation_confirmed = 0)) < ?
		ORDER BY (p.family_remote_member_count +
		       (SELECT COUNT(*) FROM icloud_resources AS ot
		        WHERE ot.family_primary_resource_id = p.id
		          AND ot.task_kind = 'onboarding'
		          AND ot.id <> ? AND ot.onboarding_status IN ('processing', 'waiting')
		          AND ot.family_reservation_confirmed = 0)) ASC,
		  p.family_synced_at DESC, p.id ASC LIMIT 1`
	if tx.Name() == "mysql" {
		query += " FOR UPDATE"
	}
	var row struct {
		ID uint `gorm:"column:id"`
	}
	err := tx.WithContext(ctx).Raw(
		query, excludedPrimaryID, countryCode, now.Add(-iCloudFamilyCapacityCacheTTL), task.ID, iCloudFamilyChildLimit, task.ID,
	).Scan(&row).Error
	if err != nil {
		return 0, ErrICloudOnboardingTemporary
	}
	return row.ID, nil
}

func (s *Service) reconcileICloudOnboardingFamily(ctx context.Context, task *iCloudOnboardingTaskModel, channel *AppleOnboardingChannel) error {
	if s == nil || s.db == nil || task == nil || task.ID == 0 || task.FamilyPrimaryResourceID == nil || channel == nil {
		return ErrICloudFamilyCapacityUnavailable
	}
	ctx = withAppleRouteEmail(ctx, task.PrimaryEmail)
	client := s.family
	if client == nil {
		client = newRoutedICloudFamilyClient(s.appleRoutes)
	}
	snapshot, err := client.fetch(ctx, iCloudResourceChannelModel{
		Kind: channel.Kind, Host: channel.Host, Cookie: channel.Cookie, SetupCookie: channel.SetupCookie,
		UserAgent: channel.UserAgent,
	})
	if err != nil {
		return err
	}
	if !snapshot.Linked || !snapshot.Member {
		return &iCloudFamilyError{Category: "family_membership_pending", SafeMessage: "Apple family membership is not visible yet.", Retryable: true}
	}
	if !strings.EqualFold(snapshot.CurrentUserAppleID, task.PrimaryEmail) {
		return &iCloudFamilyError{Category: "family_identity_mismatch", SafeMessage: "Apple family session belongs to a different account."}
	}
	if snapshot.CurrentDSID == snapshot.OrganizerDSID {
		return &iCloudFamilyError{Category: "family_conflict", SafeMessage: "The child Apple account is the organizer of another family."}
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var primary iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&primary, *task.FamilyPrimaryResourceID).Error; err != nil {
			return ErrICloudOnboardingTemporary
		}
		if primary.AccountRole != "primary" || primary.FamilyID == "" || primary.FamilyOrganizerDSID == "" {
			return ErrICloudFamilyCapacityUnavailable
		}
		if snapshot.FamilyID != primary.FamilyID || snapshot.OrganizerDSID != primary.FamilyOrganizerDSID {
			return ErrICloudFamilyConflict
		}
		remoteMemberCount := max(primary.FamilyRemoteMemberCount, snapshot.RemoteExtraMemberCount)
		if err := tx.Model(&iCloudResourceModel{}).Where("id = ?", primary.ID).Updates(map[string]any{
			"family_remote_member_count": remoteMemberCount,
			"family_sync_status":         iCloudFamilySyncUnknown,
			"family_next_sync_at":        now, "next_provision_at": now,
			"family_sync_error_category": preserveICloudFamilyInviteFailure(primary.FamilySyncErrorCategory, ""), "updated_at": now,
		}).Error; err != nil {
			return ErrICloudOnboardingTemporary
		}
		result := tx.Model(&iCloudOnboardingTaskModel{}).
			Where("id = ? AND generation = ? AND claim_token = ?", task.ID, task.Generation, task.ClaimToken).
			Updates(map[string]any{"family_reservation_confirmed": true, "updated_at": now})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrICloudImportClaim
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = s.ScheduleICloudProvisionDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}

func iCloudFamilyErrorCategory(err error) string {
	var providerErr *iCloudFamilyError
	if errors.As(err, &providerErr) {
		return providerErr.Category
	}
	if errors.Is(err, ErrICloudFamilyConflict) {
		return "family_conflict"
	}
	if errors.Is(err, ErrICloudFamilyCapacityUnavailable) {
		return "family_capacity_unknown"
	}
	return ""
}

func iCloudFamilyErrorRetryable(err error) bool {
	var providerErr *iCloudFamilyError
	return errors.As(err, &providerErr) && providerErr.Retryable || errors.Is(err, ErrICloudFamilyCapacityUnavailable)
}
