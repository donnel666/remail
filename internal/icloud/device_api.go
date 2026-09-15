package icloud

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/kitesim"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func deviceSMSBaseURL() string {
	return strings.TrimRight(runtimeconfig.String(runtimeconfig.ICloudDeviceSMSBaseURLKey, "https://remail.aishop6.com"), "/")
}

func (h *handler) devicePlatformBalance(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var result *DeviceBalance
	var err error
	if c.Request.Method == http.MethodGet {
		result, err = h.service.deviceBalance(c.Request.Context())
	} else {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
		var request struct {
			CardKey string `json:"cardKey"`
		}
		if c.ShouldBindJSON(&request) != nil {
			writeICloudError(c, ErrICloudOnboardingInvalid)
			return
		}
		card := strings.TrimSpace(request.CardKey)
		if card == "" || len(card) > 256 || strings.ContainsAny(card, "\r\n\x00") {
			writeICloudError(c, ErrICloudOnboardingInvalid)
			return
		}
		if !DeviceCodeConfigured() {
			writeICloudError(c, errDeviceUnauthorized)
			return
		}
		userID, _ := middleware.GetCurrentUserID(c)
		// Record the request before external I/O; neither the card nor the API
		// key belongs in audit logs. Never replay a payment after a timeout.
		if err = h.service.operationLogs.Create(c.Request.Context(), &governancedomain.OperationLog{
			OperatorUserID: userID, OperationType: "icloud.device.recharge.request", ResourceType: "device_platform", ResourceID: "919", Path: c.FullPath(), Result: "success", SafeSummary: "Requested device platform card redemption; vendor result is reported by the response.", RequestID: middleware.GetRequestID(c),
		}); err != nil {
			writeICloudError(c, ErrICloudOnboardingTemporary)
			return
		}
		result, err = h.service.rechargeDeviceBalance(c.Request.Context(), card)
	}
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *handler) deviceBinding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseUint(c.Param("resourceId"), 10, 64)
	if err != nil || id == 0 {
		writeICloudError(c, ErrICloudResourceNotFound)
		return
	}
	var resource iCloudResourceModel
	if err := h.service.db.WithContext(c.Request.Context()).Where("id = ? AND status <> ?", id, iCloudResourceDeleted).First(&resource).Error; err != nil {
		writeICloudError(c, ErrICloudResourceNotFound)
		return
	}
	if resource.DeviceCodeAPI != "" {
		c.JSON(http.StatusOK, DeviceBinding{Status: "success", CodeAPI: resource.DeviceCodeAPI, RemoteID: resource.DeviceAccountID})
		return
	}
	var binding deviceBindingModel
	err = h.service.db.WithContext(c.Request.Context()).Where("email = ?", strings.ToLower(resource.PrimaryEmail)).Take(&binding).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeICloudError(c, ErrICloudOnboardingTemporary)
		return
	}
	if c.Request.Method == http.MethodGet {
		c.JSON(http.StatusOK, DeviceBinding{Status: firstNonEmpty(resource.DeviceBindStatus, "unbound"), CodeAPI: resource.DeviceCodeAPI, RemoteID: resource.DeviceAccountID, LastError: binding.LastError})
		return
	}
	if resource.Status == iCloudResourceDisabled || resource.KitesimPhoneID == nil {
		writeICloudError(c, ErrICloudResourceStatus)
		return
	}
	var credential iCloudResourceCredentialModel
	if err := h.service.db.WithContext(c.Request.Context()).First(&credential, resource.ID).Error; err != nil {
		writeICloudError(c, ErrICloudOnboardingInvalid)
		return
	}
	userID, _ := middleware.GetCurrentUserID(c)
	view, err := h.service.ensureDeviceBinding(c.Request.Context(), resource.PrimaryEmail, credential.ApplePassword, *resource.KitesimPhoneID, resource.BoundPhoneNumber, &resource.ID, h.service.now(), true, &governancedomain.OperationLog{
		OperatorUserID: userID, OperationType: "icloud.device.bind", ResourceType: "icloud_resource", ResourceID: strconv.FormatUint(id, 10), Path: c.FullPath(), Result: "success", SafeSummary: "Requested Apple device binding", RequestID: middleware.GetRequestID(c),
	})
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// RegisterDeviceCallbackRoutes grants a temporary, per-binding SMS callback to
// the device platform. It cannot rotate an administrator's Kitesim pickup link.
func RegisterDeviceCallbackRoutes(r *gin.Engine, s *Service, rdb redis.UniversalClient) {
	r.GET("/sms/icloud-device/:token", func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() }, middleware.RateLimitPerClientIP(rdb, "icloud_device_sms", 1200, 60), func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("X-Robots-Tag", "noindex, nofollow")
		token := c.Param("token")
		if len(token) != 43 {
			c.String(http.StatusNotFound, "Invalid URL|")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		load := func() (*deviceBindingModel, error) {
			var binding deviceBindingModel
			err := s.db.WithContext(ctx).Where("callback_token = ? AND status = ? AND deadline_at > ?", token, "binding", s.now()).Take(&binding).Error
			if err != nil {
				return nil, err
			}
			if binding.ResourceID != nil {
				var count int64
				err = s.db.WithContext(ctx).Model(&iCloudResourceModel{}).Where("id = ? AND LOWER(primary_email) = ? AND kitesim_phone_id = ? AND status NOT IN ? AND device_bind_status IN ?", *binding.ResourceID, binding.Email, binding.PhoneID, []string{iCloudResourceDisabled, iCloudResourceDeleted}, []string{"pending", "binding"}).Count(&count).Error
				if err != nil {
					return nil, err
				}
				if count != 1 {
					return nil, gorm.ErrRecordNotFound
				}
			}
			return &binding, nil
		}
		binding, err := load()
		if err != nil {
			c.String(http.StatusNotFound, "Invalid URL|")
			return
		}
		fetcher, ok := s.smsPhones.(interface {
			FetchSMSMessages(context.Context, uint) ([]kitesim.MessageItem, error)
		})
		if !ok {
			c.String(http.StatusServiceUnavailable, "Service unavailable|")
			return
		}
		allowed, err := rdb.SetNX(ctx, "icloud:device:sms:"+strconv.FormatUint(uint64(binding.PhoneID), 10), "1", 3*time.Second).Result()
		if err != nil {
			c.String(http.StatusServiceUnavailable, "Service unavailable|")
			return
		}
		if !allowed {
			c.Header("Retry-After", "3")
			c.String(http.StatusTooManyRequests, "Too many requests|")
			return
		}
		messages, err := fetcher.FetchSMSMessages(ctx, binding.PhoneID)
		if _, loadErr := load(); loadErr != nil {
			c.String(http.StatusNotFound, "Invalid URL|")
			return
		}
		if err != nil {
			c.String(http.StatusBadGateway, "Service unavailable|")
			return
		}
		window := runtimeconfig.Duration(runtimeconfig.KitesimSMSWindowSecondsKey, 2*time.Minute, time.Second, 1)
		if binding.SubmittedAt != nil {
			window = min(window, s.now().Sub(*binding.SubmittedAt))
		}
		message := "No message"
		if latest := kitesim.LatestSMSMessage(messages, s.now(), window); latest != nil {
			message = latest.Content
		}
		c.String(http.StatusOK, "%s|%s", message, binding.DeadlineAt.Format(time.RFC3339))
	})
}
