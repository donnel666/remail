package middleware

import (
	"context"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
)

const (
	BotSubjectHeaderName = "X-Bot-Subject"
	BotSceneHeaderName   = "X-Bot-Scene"
	BotGroupHeaderName   = "X-Bot-Group"

	BotScenePrivate = "private"
	BotSceneGroup   = "group"

	contextKeyBotIntegration = "bot_integration"
	contextKeyBotIdentity    = "bot_identity"
)

// BotIntegration is immutable metadata owned by the authenticated System Key.
// Callers cannot select a platform or namespace through request headers.
type BotIntegration struct {
	SystemKeyID      uint
	Platform         string
	SubjectNamespace string
	AllowedGroupIDs  []string
}

// BotIdentity combines the authenticated integration with the opaque sender
// identifier supplied by the bot adapter for the current event.
type BotIdentity struct {
	BotIntegration
	Subject string
	Scene   string
	GroupID string
}

type BotSystemKeyAuthenticator interface {
	AuthenticateBotSystemKey(ctx context.Context, plain string) (*settingsdomain.SystemKey, error)
}

// BotSystemKeyRequired authenticates purpose=bot keys and pins their platform
// and subject namespace into the request context.
func BotSystemKeyRequired(authenticator BotSystemKeyAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authenticator == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"message": "Service is temporarily unavailable.", "requestId": GetRequestID(c),
			})
			return
		}
		key, err := authenticator.AuthenticateBotSystemKey(c.Request.Context(), strings.TrimSpace(c.GetHeader(SystemKeyHeaderName)))
		if err != nil {
			abortSystemKeyAuthentication(c, err)
			return
		}
		if key == nil || key.ID == 0 || key.Purpose != settingsdomain.SystemKeyPurposeBot || key.Platform == "" || key.SubjectNamespace == "" || len(key.AllowedGroupIDs) == 0 {
			abortSystemKeyAuthentication(c, settingsdomain.ErrInvalidSystemKey)
			return
		}
		integration := BotIntegration{
			SystemKeyID: key.ID, Platform: key.Platform, SubjectNamespace: key.SubjectNamespace,
			AllowedGroupIDs: append([]string(nil), key.AllowedGroupIDs...),
		}
		c.Set(contextKeySystemKeyID, key.ID)
		c.Set(contextKeyBotIntegration, integration)
		c.Next()
	}
}

// BotIdentityRequired accepts only the current adapter's opaque sender and
// scene. Platform and namespace always come from the authenticated key above.
func BotIdentityRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		integration, ok := GetCurrentBotIntegration(c)
		subject := strings.TrimSpace(c.GetHeader(BotSubjectHeaderName))
		scene := strings.ToLower(strings.TrimSpace(c.GetHeader(BotSceneHeaderName)))
		groupID := strings.TrimSpace(c.GetHeader(BotGroupHeaderName))
		if !ok || !validBotSubject(integration.Platform, integration.SubjectNamespace, subject) || !validBotScene(integration, scene, groupID) {
			abortBotIdentity(c)
			return
		}
		c.Set(contextKeyBotIdentity, BotIdentity{
			BotIntegration: integration, Subject: subject, Scene: scene, GroupID: groupID,
		})
		c.Next()
	}
}

func BotPrivateRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := GetCurrentBotIdentity(c)
		if !ok || identity.Scene != BotScenePrivate {
			abortBotIdentity(c)
			return
		}
		c.Next()
	}
}

func GetCurrentBotIntegration(c *gin.Context) (BotIntegration, bool) {
	value, exists := c.Get(contextKeyBotIntegration)
	if !exists {
		return BotIntegration{}, false
	}
	integration, ok := value.(BotIntegration)
	if !ok || integration.SystemKeyID == 0 || integration.Platform == "" || integration.SubjectNamespace == "" {
		return BotIntegration{}, false
	}
	integration.AllowedGroupIDs = append([]string(nil), integration.AllowedGroupIDs...)
	return integration, true
}

func GetCurrentBotIdentity(c *gin.Context) (BotIdentity, bool) {
	value, exists := c.Get(contextKeyBotIdentity)
	if !exists {
		return BotIdentity{}, false
	}
	identity, ok := value.(BotIdentity)
	if !ok || identity.SystemKeyID == 0 || identity.Platform == "" || identity.SubjectNamespace == "" || identity.Subject == "" {
		return BotIdentity{}, false
	}
	identity.AllowedGroupIDs = append([]string(nil), identity.AllowedGroupIDs...)
	return identity, true
}

func validBotScene(integration BotIntegration, scene, groupID string) bool {
	switch scene {
	case BotScenePrivate:
		return groupID == ""
	case BotSceneGroup:
		if !validBotOpaqueID(groupID, 128) {
			return false
		}
		for _, allowed := range integration.AllowedGroupIDs {
			if groupID == allowed {
				return true
			}
		}
	}
	return false
}

func validBotOpaqueID(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validBotSubject(platform, namespace, subject string) bool {
	if !validBotOpaqueID(subject, 255) {
		return false
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	if platform == "aiocqhttp" || strings.HasPrefix(platform, "qq") || strings.HasPrefix(platform, "onebot") || namespace == "qq" || strings.HasPrefix(namespace, "qq:") {
		return validPositiveDecimalID(subject)
	}
	return true
}

func validPositiveDecimalID(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func abortBotIdentity(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"message": "Authentication is required.", "requestId": GetRequestID(c),
	})
}
