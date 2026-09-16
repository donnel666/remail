package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	typeICloudFamilyRefresh = "icloud:family_refresh"
	familyRefreshTimeout    = 5 * time.Minute
	familyRefreshLease      = 10 * time.Minute
	familySnapshotTTL       = 7 * 24 * time.Hour
)

type AdminICloudFamilyMember struct {
	Email      string `json:"email"`
	ResourceID *uint  `json:"resourceId"`
	AliasCount *uint  `json:"aliasCount"`
	Organizer  bool   `json:"organizer"`
	Current    bool   `json:"current"`
}

type AdminICloudFamily struct {
	State             string                    `json:"state"`
	FamilyID          string                    `json:"familyId"`
	Members           []AdminICloudFamilyMember `json:"members"`
	SyncedAt          *time.Time                `json:"syncedAt"`
	LastError         string                    `json:"lastError"`
	CanRefresh        bool                      `json:"canRefresh"`
	UnavailableReason string                    `json:"unavailableReason"`
	ImportedCount     int                       `json:"importedCount"`
	FullCount         int                       `json:"fullCount"`
	AliasLimit        uint                      `json:"aliasLimit"`
}

type iCloudFamilyCache struct {
	Email     string
	State     string
	Snapshot  *iCloudFamilySnapshot
	SyncedAt  *time.Time
	LastError string
}

type iCloudFamilyRefreshTask struct {
	ResourceID uint   `json:"resourceId"`
	Token      string `json:"token"`
}

func familyRedisKey(id uint, suffix string) string {
	return fmt.Sprintf("icloud:family:{%d}:%s", id, suffix)
}

func (s *Service) loadFamilyCache(ctx context.Context, resource iCloudResourceModel) (iCloudFamilyCache, error) {
	cache := iCloudFamilyCache{Email: resource.PrimaryEmail, State: "idle"}
	if s.deviceRedis == nil {
		return cache, ErrICloudValidationTemp
	}
	data, err := s.deviceRedis.Get(ctx, familyRedisKey(resource.ID, "view")).Bytes()
	if errors.Is(err, redis.Nil) {
		return cache, nil
	}
	if err != nil || json.Unmarshal(data, &cache) != nil {
		return cache, ErrICloudValidationTemp
	}
	if !strings.EqualFold(cache.Email, resource.PrimaryEmail) {
		return iCloudFamilyCache{Email: resource.PrimaryEmail, State: "idle"}, nil
	}
	return cache, nil
}

func (s *Service) familyResource(ctx context.Context, id uint) (iCloudResourceModel, error) {
	var resource iCloudResourceModel
	err := s.db.WithContext(ctx).Where("id = ? AND status <> ?", id, iCloudResourceDeleted).First(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return resource, ErrICloudResourceNotFound
	}
	return resource, err
}

func (s *Service) familyChannels(ctx context.Context, id uint) ([]iCloudResourceChannelModel, error) {
	var channels []iCloudResourceChannelModel
	err := s.db.WithContext(ctx).Where("resource_id = ? AND kind IN ?", id, []string{iCloudChannelAppleAccount, iCloudChannelWeb}).Order("kind ASC").Find(&channels).Error
	return channels, err
}

func usableFamilyCookie(channel iCloudResourceChannelModel) bool {
	// Family access is checked by FamilyWS itself; another channel's failure
	// must not discard a Cookie that can still read the family.
	return validICloudFamilyCookie(firstNonEmpty(channel.SetupCookie, channel.Cookie))
}

func (s *Service) familyRefreshEligibility(ctx context.Context, resource iCloudResourceModel) (bool, string, error) {
	if resource.Status == iCloudResourceDisabled {
		return false, "Enable the resource before refreshing its family.", nil
	}
	channels, err := s.familyChannels(ctx, resource.ID)
	if err != nil {
		return false, "", err
	}
	for _, channel := range channels {
		if usableFamilyCookie(channel) {
			return true, "", nil
		}
	}
	if resource.DeviceCodeAPI == "" {
		return false, "Update Cookie or bind a device to refresh the family.", nil
	}
	var credentials int64
	err = s.db.WithContext(ctx).Model(&iCloudResourceCredentialModel{}).Where("resource_id = ? AND apple_password <> ''", resource.ID).Count(&credentials).Error
	if err != nil {
		return false, "", err
	}
	if credentials == 0 {
		return false, "Apple account credentials are required to refresh the family.", nil
	}
	return true, "", nil
}

func (s *Service) GetAdminICloudFamily(ctx context.Context, id uint) (*AdminICloudFamily, error) {
	resource, err := s.familyResource(ctx, id)
	if err != nil {
		return nil, err
	}
	cache, err := s.loadFamilyCache(ctx, resource)
	if err != nil {
		return nil, err
	}
	view := &AdminICloudFamily{State: cache.State, Members: []AdminICloudFamilyMember{}, SyncedAt: cache.SyncedAt, LastError: cache.LastError, AliasLimit: iCloudMaxAliases}
	view.CanRefresh, view.UnavailableReason, err = s.familyRefreshEligibility(ctx, resource)
	if err != nil {
		return nil, err
	}
	active, err := s.deviceRedis.Exists(ctx, familyRedisKey(id, "job")).Result()
	if err != nil {
		return nil, ErrICloudValidationTemp
	}
	if active > 0 && !familyRefreshPending(view.State) {
		view.State = "queued"
	}
	if active == 0 && familyRefreshPending(view.State) {
		view.State, view.LastError = "failed", "Family refresh timed out. Please retry."
	}
	if cache.Snapshot == nil {
		return view, nil
	}
	view.FamilyID = cache.Snapshot.FamilyID
	emails := make([]string, 0, len(cache.Snapshot.Members))
	for _, member := range cache.Snapshot.Members {
		if member.Email != "" {
			emails = append(emails, member.Email)
		}
	}
	var matches []iCloudResourceModel
	if len(emails) > 0 {
		if err := s.db.WithContext(ctx).Select("id", "primary_email", "alias_count").Where("primary_email IN ? AND status <> ?", emails, iCloudResourceDeleted).Find(&matches).Error; err != nil {
			return nil, err
		}
	}
	byEmail := make(map[string]iCloudResourceModel, len(matches))
	for _, match := range matches {
		byEmail[strings.ToLower(match.PrimaryEmail)] = match
	}
	for _, member := range cache.Snapshot.Members {
		row := AdminICloudFamilyMember{Email: member.Email, Organizer: member.DSID == cache.Snapshot.OrganizerDSID, Current: member.DSID == cache.Snapshot.CurrentDSID}
		if match, ok := byEmail[member.Email]; ok {
			row.ResourceID, row.AliasCount = &match.ID, &match.AliasCount
			view.ImportedCount++
			if match.AliasCount >= iCloudMaxAliases {
				view.FullCount++
			}
		}
		view.Members = append(view.Members, row)
	}
	return view, nil
}

func familyRefreshPending(state string) bool {
	return state == "queued" || state == "querying" || state == "logging_in" || state == "waiting"
}

// A worker that lost its lease cannot overwrite a newer refresh or clear its job.
var writeFamilyCacheScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[2], ARGV[2], 'EX', ARGV[3])
if ARGV[4] == '1' then redis.call('DEL', KEYS[1]) end
return 1`)

func (s *Service) writeFamilyCache(ctx context.Context, task iCloudFamilyRefreshTask, cache iCloudFamilyCache, done bool) error {
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	finish := "0"
	if done {
		finish = "1"
	}
	updated, err := writeFamilyCacheScript.Run(ctx, s.deviceRedis, []string{familyRedisKey(task.ResourceID, "job"), familyRedisKey(task.ResourceID, "view")}, task.Token, string(data), int64(familySnapshotTTL/time.Second), finish).Int()
	if err != nil {
		return ErrICloudValidationTemp
	}
	if updated != 1 {
		return errICloudRefreshStale
	}
	return nil
}

func (s *Service) RefreshAdminICloudFamily(ctx context.Context, id uint) (*AdminICloudFamily, error) {
	view, err := s.GetAdminICloudFamily(ctx, id)
	if err != nil {
		return nil, err
	}
	if !view.CanRefresh || familyRefreshPending(view.State) || view.State == "ready" && view.SyncedAt != nil && s.now().Sub(*view.SyncedAt) < time.Minute {
		return view, nil
	}
	if s.queue == nil {
		return nil, ErrICloudValidationTemp
	}
	resource, err := s.familyResource(ctx, id)
	if err != nil {
		return nil, err
	}
	cache, err := s.loadFamilyCache(ctx, resource)
	if err != nil {
		return nil, err
	}
	task := iCloudFamilyRefreshTask{ResourceID: id, Token: platform.NewUUIDV7String()}
	claimed, err := s.deviceRedis.SetNX(ctx, familyRedisKey(id, "job"), task.Token, familyRefreshLease).Result()
	if err != nil {
		return nil, ErrICloudValidationTemp
	}
	if !claimed {
		return s.GetAdminICloudFamily(ctx, id)
	}
	enqueued := false
	defer func() {
		if !enqueued {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = releaseFamilyLoginScript.Run(cleanupCtx, s.deviceRedis, []string{familyRedisKey(id, "job")}, task.Token).Err()
		}
	}()
	cache.State, cache.LastError = "queued", ""
	if err := s.writeFamilyCache(ctx, task, cache, false); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudFamilyRefresh, payload), asynq.Queue(platform.QueueDefault), asynq.Timeout(familyRefreshTimeout), asynq.MaxRetry(2), asynq.Retention(0))
	if err != nil {
		cache.State, cache.LastError = "failed", "Family refresh could not be queued. Please retry."
		_ = s.writeFamilyCache(context.WithoutCancel(ctx), task, cache, true)
		return nil, ErrICloudValidationTemp
	}
	enqueued = true
	return s.GetAdminICloudFamily(ctx, id)
}

func (h *handler) familyDetails(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseUint(c.Param("resourceId"), 10, 64)
	if err != nil || id == 0 {
		writeICloudError(c, ErrICloudResourceNotFound)
		return
	}
	var view *AdminICloudFamily
	status := http.StatusOK
	if c.Request.Method == http.MethodPost {
		view, err = h.service.RefreshAdminICloudFamily(c.Request.Context(), uint(id))
		status = http.StatusAccepted
	} else {
		view, err = h.service.GetAdminICloudFamily(c.Request.Context(), uint(id))
	}
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.JSON(status, view)
}
