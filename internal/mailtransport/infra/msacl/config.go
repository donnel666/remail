package msacl

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bogdanfinn/tls-client/profiles"
)

const (
	clientID = "9e5f94bc-e8a4-4e73-b8be-63364c29d753"
	scope    = "https://graph.microsoft.com/Mail.Read https://graph.microsoft.com/Mail.Send User.Read offline_access"

	tokenPollTimeout  = 15
	tokenPollInterval = 3
)

var (
	mailAPIBase          = strings.TrimRight(envString("CLOUD_MAIL_BASE", "https://remail.aishop6.com"), "/")
	mailAPIKey           = strings.TrimSpace(envString("CLOUD_MAIL_JWT", envString("CLOUD_MAIL_API_KEY", "")))
	mailDomains          = configuredMailDomains()
	mailPollTimeout      = envInt("CLOUD_MAIL_POLL_TIMEOUT", 60)
	mailPollInterval     = envInt("CLOUD_MAIL_POLL_INTERVAL", 2)
	mailLateArrivalGrace = envInt("CLOUD_MAIL_LATE_ARRIVAL_GRACE", 12)
	mailUseProxy         = envBool("CLOUD_MAIL_USE_PROXY", false)

	rngMu sync.Mutex
	rng   = rand.New(rand.NewSource(time.Now().UnixNano()))
)

type fingerprint struct {
	Profile         profiles.ClientProfile
	Impersonate     string
	UserAgent       string
	HeadersNavigate map[string]string
	HeadersCORS     map[string]string
}

type osProfile struct {
	platform        string
	platformVersion string
	uaOS            string
	langs           []string
}

type browserProfile struct {
	name           string
	versions       []string
	impersonateTpl string
	uaTpl          string
	secCH          func(string) string
	weight         int
}

var osPool = []osProfile{
	{"Windows", `"10.0.0"`, "Windows NT 10.0; Win64; x64", []string{"zh-CN,zh;q=0.9", "zh-CN,zh;q=0.9,en;q=0.8", "en-US,en;q=0.9", "en-US,en;q=0.9,zh-CN;q=0.8", "ja-JP,ja;q=0.9", "ko-KR,ko;q=0.9"}},
	{"Windows", `"10.0.0"`, "Windows NT 10.0; Win64; x64", []string{"de-DE,de;q=0.9", "fr-FR,fr;q=0.9", "es-ES,es;q=0.9", "pt-BR,pt;q=0.9", "ru-RU,ru;q=0.9", "it-IT,it;q=0.9"}},
	{"Windows", `"15.0.0"`, "Windows NT 10.0; Win64; x64", []string{"zh-CN,zh;q=0.9", "en-US,en;q=0.9", "zh-CN,zh;q=0.9,en;q=0.8", "en-US,en;q=0.9,zh-CN;q=0.8", "ja-JP,ja;q=0.9"}},
	{"Windows", `"15.0.0"`, "Windows NT 10.0; Win64; x64", []string{"de-DE,de;q=0.9", "fr-FR,fr;q=0.9", "es-ES,es;q=0.9"}},
	{"macOS", `"13.6.7"`, "Macintosh; Intel Mac OS X 10_15_7", []string{"zh-CN,zh;q=0.9", "en-US,en;q=0.9", "ja-JP,ja;q=0.9", "ko-KR,ko;q=0.9"}},
	{"macOS", `"13.6.7"`, "Macintosh; Intel Mac OS X 10_15_7", []string{"de-DE,de;q=0.9", "fr-FR,fr;q=0.9", "es-ES,es;q=0.9"}},
	{"macOS", `"14.5.0"`, "Macintosh; Intel Mac OS X 10_15_7", []string{"zh-CN,zh;q=0.9", "en-US,en;q=0.9", "zh-CN,zh;q=0.9,en;q=0.8", "en-US,en;q=0.9,zh-CN;q=0.8", "de-DE,de;q=0.9"}},
	{"macOS", `"15.1.0"`, "Macintosh; Intel Mac OS X 10_15_7", []string{"zh-CN,zh;q=0.9", "en-US,en;q=0.9", "zh-CN,zh;q=0.9,en;q=0.8"}},
	{"Linux", `""`, "X11; Linux x86_64", []string{"en-US,en;q=0.9", "zh-CN,zh;q=0.9", "de-DE,de;q=0.9", "en-US,en;q=0.9,zh-CN;q=0.8"}},
	{"Linux", `""`, "X11; Linux x86_64", []string{"fr-FR,fr;q=0.9", "es-ES,es;q=0.9", "ru-RU,ru;q=0.9"}},
}

var browserPool = []browserProfile{
	{
		name:           "Chrome",
		versions:       []string{"104", "107", "110", "116", "120", "124", "131"},
		impersonateTpl: "chrome%s",
		uaTpl:          "Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36",
		secCH:          chromeSecCHUA,
		weight:         70,
	},
	{
		name:           "Firefox",
		versions:       []string{"132"},
		impersonateTpl: "firefox",
		uaTpl:          "Mozilla/5.0 (%s; rv:132.0) Gecko/20100101 Firefox/132.0",
		secCH: func(string) string {
			return `"Firefox";v="132", "Not/A)Brand";v="99"`
		},
		weight: 10,
	},
}

var acceptHeaders = []string{
	"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
	"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
	"text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
	"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
	"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
	"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
}

var viewportWidths = []string{"1280", "1366", "1440", "1536", "1600", "1680", "1920", "2560"}
var secCHUAArchs = []string{`"x86"`, `"arm"`, ""}
var secCHUABitness = []string{`"64"`, ""}

func envString(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	i, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return i
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

// configuredMailDomains reads the env auxiliary-domain override (used only by
// the aliastest harness and unit tests). Production injects the list from
// domain_resources (purpose=binding) via SetAuxiliaryDomains; there is NO
// hardcoded default — an unconfigured list is empty and hard-fails generation.
func configuredMailDomains() []string {
	raw := strings.TrimSpace(os.Getenv("CLOUD_MAIL_DOMAINS"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("REMAIL_MAIL_DOMAINS"))
	}
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	domains := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		domain := strings.Trim(strings.ToLower(strings.TrimSpace(part)), ".")
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return domains
}

func chromeSecCHUA(ver string) string {
	v, _ := strconv.Atoi(ver)
	grease := `"Not/A)Brand";v="24"`
	if v <= 110 {
		grease = `"Not A;Brand";v="99"`
	} else if v <= 127 {
		grease = `"Not)A;Brand";v="24"`
	}
	return fmt.Sprintf(`"Chromium";v="%s", "Google Chrome";v="%s", %s`, ver, ver, grease)
}

func pickString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[rng.Intn(len(values))]
}

func weightedBrowserChoice(items []browserProfile) browserProfile {
	total := 0
	for _, item := range items {
		total += item.weight
	}
	n := rng.Intn(total)
	for _, item := range items {
		if n < item.weight {
			return item
		}
		n -= item.weight
	}
	return items[len(items)-1]
}

func profileForBrowser(name, version string) profiles.ClientProfile {
	switch strings.ToLower(name) {
	case "firefox":
		return profiles.Firefox_132
	case "edge", "chrome":
		switch version {
		case "103":
			return profiles.Chrome_103
		case "104":
			return profiles.Chrome_104
		case "107":
			return profiles.Chrome_107
		case "110":
			return profiles.Chrome_110
		case "116":
			return profiles.Chrome_116_PSK
		case "117":
			return profiles.Chrome_117
		case "120":
			return profiles.Chrome_120
		case "124":
			return profiles.Chrome_124
		case "131":
			return profiles.Chrome_131
		default:
			return profiles.Chrome_124
		}
	default:
		return profiles.Chrome_124
	}
}

func generateFingerprint() fingerprint {
	rngMu.Lock()
	defer rngMu.Unlock()

	osItem := osPool[rng.Intn(len(osPool))]

	var (
		profile     profiles.ClientProfile
		impersonate string
		ua          string
		secCHUA     string
	)

	if osItem.platform == "macOS" && rng.Float64() < 0.05 {
		if rng.Intn(2) == 0 {
			profile = profiles.Safari_15_6_1
			impersonate = "safari_15_6_1"
			ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.6 Safari/605.1.15"
			secCHUA = `"Safari";v="15.6", "Not/A)Brand";v="99"`
		} else {
			profile = profiles.Safari_16_0
			impersonate = "safari_16_0"
			ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15"
			secCHUA = `"Safari";v="16.0", "Not/A)Brand";v="99"`
		}
	} else {
		browser := weightedBrowserChoice(browserPool)
		version := pickString(browser.versions)
		profile = profileForBrowser(browser.name, version)
		if strings.Count(browser.uaTpl, "%s") == 3 {
			ua = fmt.Sprintf(browser.uaTpl, osItem.uaOS, version, version)
		} else {
			ua = fmt.Sprintf(browser.uaTpl, osItem.uaOS, version)
		}
		if browser.impersonateTpl == "firefox" {
			impersonate = "firefox"
		} else {
			impersonate = fmt.Sprintf(browser.impersonateTpl, version)
		}
		secCHUA = browser.secCH(version)
	}

	lang := pickString(osItem.langs)
	accept := pickString(acceptHeaders)
	vw := pickString(viewportWidths)
	arch := pickString(secCHUAArchs)
	bitness := pickString(secCHUABitness)

	navHeaders := map[string]string{
		"sec-ch-ua":                  secCHUA,
		"sec-ch-ua-mobile":           "?0",
		"sec-ch-ua-platform":         fmt.Sprintf(`"%s"`, osItem.platform),
		"sec-ch-ua-platform-version": osItem.platformVersion,
		"Upgrade-Insecure-Requests":  "1",
		"Sec-Fetch-Site":             "same-origin",
		"Sec-Fetch-Mode":             "navigate",
		"Sec-Fetch-User":             "?1",
		"Sec-Fetch-Dest":             "document",
		"Accept-Language":            lang,
		"Accept":                     accept,
		"Viewport-Width":             vw,
	}
	if arch != "" {
		navHeaders["sec-ch-ua-arch"] = arch
	}
	if bitness != "" {
		navHeaders["sec-ch-ua-bitness"] = bitness
	}

	corsHeaders := map[string]string{
		"sec-ch-ua":                  secCHUA,
		"sec-ch-ua-mobile":           "?0",
		"sec-ch-ua-platform":         fmt.Sprintf(`"%s"`, osItem.platform),
		"sec-ch-ua-platform-version": osItem.platformVersion,
		"Sec-Fetch-Site":             "same-origin",
		"Sec-Fetch-Mode":             "cors",
		"Sec-Fetch-Dest":             "empty",
		"Accept":                     "application/json",
		"Accept-Language":            lang,
	}

	return fingerprint{
		Profile:         profile,
		Impersonate:     impersonate,
		UserAgent:       ua,
		HeadersNavigate: navHeaders,
		HeadersCORS:     corsHeaders,
	}
}
