package kitesim

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/businessday"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SMSLink struct {
	Enabled       bool   `json:"enabled"`
	CanGenerate   bool   `json:"canGenerate"`
	ExpiresAt     string `json:"expiresAt"`
	WindowSeconds int    `json:"windowSeconds"`
	Path          string `json:"path,omitempty"`
}

func smsWindowSeconds() int {
	return min(runtimeconfig.Int(runtimeconfig.KitesimSMSWindowSecondsKey, 120, 1), 86400)
}

func smsLinkPhones(db *gorm.DB) *gorm.DB {
	return db.Model(&phoneModel{}).
		Where("deleted_at IS NULL AND account_id IN (?)",
			db.Model(&accountModel{}).Select("id").Where("deleted_at IS NULL"))
}

func (s *Service) smsLinkView(phone phoneModel) SMSLink {
	view := SMSLink{Enabled: phone.SMSLinkTokenHash != nil, WindowSeconds: smsWindowSeconds()}
	if expires := parseProviderTime(phone.ExpireTime); expires != nil {
		view.ExpiresAt = expires.Format(time.RFC3339)
		view.CanGenerate = phone.DisabledAt == nil && phone.DeletedAt == nil &&
			PhoneStatus(phone.Status) == PhoneActive && s.now().Before(*expires)
	}
	return view
}

func (s *Service) updateSMSLink(ctx context.Context, phoneID uint, enabled bool, meta MutationMeta) (*SMSLink, error) {
	var phone phoneModel
	path := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := smsLinkPhones(tx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&phone, phoneID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPhoneMissing
			}
			return err
		}
		phone.SMSLinkTokenHash = nil
		if enabled {
			if !s.smsLinkView(phone).CanGenerate {
				return ErrOperationState
			}
			var secret [32]byte
			if _, err := rand.Read(secret[:]); err != nil {
				return err
			}
			token := base64.RawURLEncoding.EncodeToString(secret[:])
			hash := smsLinkHash(token)
			phone.SMSLinkTokenHash = &hash
			path = "/sms/" + token
		}
		if err := tx.Model(&phoneModel{}).Where("id = ?", phoneID).Update("sms_link_token_hash", phone.SMSLinkTokenHash).Error; err != nil {
			return err
		}
		operation := "kitesim.phone.sms_link.revoke"
		if enabled {
			operation = "kitesim.phone.sms_link.rotate"
		}
		return s.createAudit(platform.WithGormTx(ctx, tx), meta, operation, "kitesim_phone", strconv.FormatUint(uint64(phoneID), 10), operation)
	})
	if err != nil {
		return nil, err
	}
	view := s.smsLinkView(phone)
	view.Path = path
	return &view, nil
}

func (h *handler) smsLink(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	phoneID, err := pathID(c.Param("phoneId"))
	if err != nil {
		writeError(c, ErrPhoneMissing)
		return
	}
	var phone phoneModel
	if err := smsLinkPhones(h.service.db.WithContext(c.Request.Context())).First(&phone, phoneID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = ErrPhoneMissing
		}
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.service.smsLinkView(phone))
}

func (h *handler) createSMSLink(c *gin.Context) { h.setSMSLink(c, true) }
func (h *handler) deleteSMSLink(c *gin.Context) { h.setSMSLink(c, false) }

func (h *handler) setSMSLink(c *gin.Context, enabled bool) {
	c.Header("Cache-Control", "no-store")
	phoneID, err := pathID(c.Param("phoneId"))
	if err != nil {
		writeError(c, ErrPhoneMissing)
		return
	}
	view, err := h.service.updateSMSLink(c.Request.Context(), phoneID, enabled, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func smsLinkHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// RegisterPublicRoutes exposes only the current message of the phone named by
// an opaque, revocable credential. It never claims or consumes Apple challenges.
func RegisterPublicRoutes(r *gin.Engine, service *Service, rdb redis.UniversalClient) {
	public := r.Group("/sms")
	public.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("X-Robots-Tag", "noindex, nofollow")
		c.Next()
	}, middleware.RateLimitPerClientIP(rdb, "kitesim_sms", 120, 60))
	public.GET("/:token", func(c *gin.Context) {
		ctx := c.Request.Context()
		token := c.Param("token")
		if len(token) != 43 {
			c.String(http.StatusNotFound, "Invalid URL|")
			return
		}
		secret, err := base64.RawURLEncoding.Strict().DecodeString(token)
		if err != nil || len(secret) != 32 {
			c.String(http.StatusNotFound, "Invalid URL|")
			return
		}
		hash := smsLinkHash(token)
		var phone phoneModel
		load := func() bool {
			// Re-read after the upstream call so revocation, renewal and expiry
			// during a slow request also apply to its response.
			phone = phoneModel{}
			err := smsLinkPhones(service.db.WithContext(ctx)).Where("sms_link_token_hash = ?", hash).Take(&phone).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.String(http.StatusNotFound, "Invalid URL|")
				return false
			}
			if err != nil {
				c.String(http.StatusServiceUnavailable, "Service unavailable|")
				return false
			}
			expires := parseProviderTime(phone.ExpireTime)
			if expires == nil {
				c.String(http.StatusServiceUnavailable, "Service unavailable|")
				return false
			}
			date := expires.In(businessday.Shanghai).Format(time.DateOnly)
			if !service.now().Before(*expires) {
				c.String(http.StatusGone, "URL expired|%s", date)
				return false
			}
			if phone.DisabledAt != nil || PhoneStatus(phone.Status) != PhoneActive {
				c.String(http.StatusGone, "URL unavailable|%s", date)
				return false
			}
			return true
		}
		if !load() {
			return
		}
		expires := parseProviderTime(phone.ExpireTime).In(businessday.Shanghai).Format(time.DateOnly)
		allowed, err := rdb.SetNX(ctx, "kitesim:sms:poll:"+strconv.FormatUint(uint64(phone.ID), 10), "1", 3*time.Second).Result()
		if err != nil {
			c.String(http.StatusServiceUnavailable, "Service unavailable|%s", expires)
			return
		}
		if !allowed {
			c.Header("Retry-After", "3")
			c.String(http.StatusTooManyRequests, "Too many requests|%s", expires)
			return
		}
		fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		messages, fetchErr := service.FetchSMSMessages(fetchCtx, phone.ID)
		if !load() {
			return
		}
		expires = parseProviderTime(phone.ExpireTime).In(businessday.Shanghai).Format(time.DateOnly)
		if fetchErr != nil {
			c.String(http.StatusBadGateway, "Service unavailable|%s", expires)
			return
		}
		content := "No message"
		if latest := latestSMSMessage(messages, service.now(), time.Duration(smsWindowSeconds())*time.Second); latest != nil {
			content = latest.Content
		}
		c.String(http.StatusOK, "%s|%s", content, expires)
	})
}

func latestSMSMessage(messages []MessageItem, now time.Time, window time.Duration) *MessageItem {
	var latest *MessageItem
	var latestAt time.Time
	for i := range messages {
		message := &messages[i]
		receivedAt := parseProviderTime(message.Time)
		if strings.TrimSpace(message.Content) == "" || receivedAt == nil || receivedAt.After(now) || receivedAt.Before(now.Add(-window)) {
			continue
		}
		if latest == nil || receivedAt.After(latestAt) {
			latest, latestAt = message, *receivedAt
		}
	}
	return latest
}
