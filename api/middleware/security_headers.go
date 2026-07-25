package middleware

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
)

const contentSecurityPolicyTemplate = "default-src 'self'; base-uri 'self'; connect-src 'self' https://challenges.cloudflare.com; font-src 'self' data:; form-action 'self'; frame-ancestors 'none'; frame-src %s; img-src 'self' data: blob: https:; media-src 'self' data: blob:; object-src 'none'; script-src 'self' https://challenges.cloudflare.com; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:"

func contentSecurityPolicy() string {
	frameSources := "https://challenges.cloudflare.com"
	gateway, err := url.Parse(strings.TrimSpace(runtimeconfig.String("epay_gateway_url", "")))
	if err == nil && gateway.Scheme == "https" && gateway.Host != "" && gateway.User == nil {
		frameSources += " https://" + gateway.Host
	}
	return fmt.Sprintf(contentSecurityPolicyTemplate, frameSources)
}

// SecurityHeaders applies browser protections without blocking the embedded SPA.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", contentSecurityPolicy())
		c.Header("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Strict-Transport-Security", "max-age=31536000")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Next()
	}
}
