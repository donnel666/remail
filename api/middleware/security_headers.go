package middleware

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
)

const contentSecurityPolicyTemplate = "default-src 'self'; base-uri 'self'; connect-src 'self' https://challenges.cloudflare.com; font-src 'self' data:; form-action 'self'; frame-ancestors 'self'; frame-src %s; img-src 'self' data: blob: https:; media-src 'self' data: blob:; object-src 'none'; script-src 'self' https://challenges.cloudflare.com; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:"

func contentSecurityPolicy() string {
	frameSources := []string{"'self'", "https://challenges.cloudflare.com"}
	seen := map[string]struct{}{
		"'self'":                            {},
		"https://challenges.cloudflare.com": {},
	}
	frameSources = addFrameURLSource(frameSources, seen, runtimeconfig.String("epay_gateway_url", ""))
	frameSources = addFrameURLSource(frameSources, seen, runtimeconfig.String("epusdt_gateway_url", ""))
	for _, raw := range strings.Split(runtimeconfig.String("epusdt_allowed_hosts", ""), ",") {
		frameSources = addFrameHostSource(frameSources, seen, raw)
	}
	return fmt.Sprintf(contentSecurityPolicyTemplate, strings.Join(frameSources, " "))
}

func addFrameURLSource(sources []string, seen map[string]struct{}, raw string) []string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return sources
	}
	return addFrameSource(sources, seen, parsed.Host)
}

func addFrameHostSource(sources []string, seen map[string]struct{}, raw string) []string {
	entry := strings.TrimSpace(raw)
	if entry == "" || strings.ContainsAny(entry, "/?#@") {
		return sources
	}
	parsed, err := url.Parse("https://" + entry)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return sources
	}
	return addFrameSource(sources, seen, parsed.Host)
}

func addFrameSource(sources []string, seen map[string]struct{}, host string) []string {
	source := "https://" + strings.ToLower(strings.TrimSpace(host))
	if source == "https://" {
		return sources
	}
	if _, exists := seen[source]; exists {
		return sources
	}
	seen[source] = struct{}{}
	return append(sources, source)
}

// SecurityHeaders applies browser protections without blocking the embedded SPA.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", contentSecurityPolicy())
		c.Header("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Strict-Transport-Security", "max-age=31536000")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Next()
	}
}
