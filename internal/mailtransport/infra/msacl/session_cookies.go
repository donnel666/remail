package msacl

import (
	"errors"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

type SessionCookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Path     string    `json:"path,omitempty"`
	Domain   string    `json:"domain,omitempty"`
	Host     string    `json:"host,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"httpOnly,omitempty"`
}

type sessionCookieClient interface {
	GetCookies(*url.URL) []*http.Cookie
	SetCookies(*url.URL, []*http.Cookie)
}

func (s *Session) SnapshotCookies(rawURLs ...string) ([]SessionCookie, error) {
	client, ok := s.client.(sessionCookieClient)
	if !ok {
		return nil, errors.New("session cookie jar is unavailable")
	}
	seen := make(map[string]struct{})
	result := make([]SessionCookie, 0)
	for _, rawURL := range rawURLs {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Hostname() == "" {
			return nil, errors.New("invalid cookie snapshot URL")
		}
		for _, cookie := range client.GetCookies(parsed) {
			if cookie == nil || strings.TrimSpace(cookie.Name) == "" || cookie.Value == "" {
				continue
			}
			key := strings.ToLower(firstCookieValue(cookie.Domain, parsed.Hostname())) + "\x00" + cookie.Path + "\x00" + cookie.Name
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, SessionCookie{
				Name: cookie.Name, Value: cookie.Value, Path: cookie.Path, Domain: cookie.Domain, Host: parsed.Hostname(),
				Expires: cookie.Expires, Secure: cookie.Secure, HTTPOnly: cookie.HttpOnly,
			})
		}
	}
	return result, nil
}

func (s *Session) RestoreCookies(cookies []SessionCookie) error {
	client, ok := s.client.(sessionCookieClient)
	if !ok {
		return errors.New("session cookie jar is unavailable")
	}
	for _, item := range cookies {
		if strings.TrimSpace(item.Name) == "" || item.Value == "" {
			continue
		}
		host := strings.TrimPrefix(strings.TrimSpace(firstCookieValue(item.Domain, item.Host)), ".")
		if host == "" {
			return errors.New("cookie domain is missing")
		}
		parsed := &url.URL{Scheme: "https", Host: host, Path: "/"}
		path := item.Path
		if path == "" {
			path = "/"
		}
		client.SetCookies(parsed, []*http.Cookie{{
			Name: item.Name, Value: item.Value, Path: path, Domain: item.Domain,
			Expires: item.Expires, Secure: item.Secure, HttpOnly: item.HTTPOnly,
		}})
	}
	return nil
}

func firstCookieValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
