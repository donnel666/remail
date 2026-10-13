package runtimeconfig

import (
	"encoding/json"
	"io"
	"net"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/systemsettings/domain"
)

const (
	ICloudLivenessSMTPEnabledKey  = "icloud_liveness_smtp_enabled"
	ICloudLivenessSMTPServersKey  = "icloud_liveness_smtp_servers"
	ICloudLivenessSMTPIntervalKey = "icloud_liveness_smtp_interval_seconds"
	ICloudLivenessSMTPMaxServers  = 50
)

type ICloudLivenessSMTPServer struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Addr        string `json:"addr"`
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	From        string `json:"from"`
	HasPassword bool   `json:"hasPassword,omitempty"`
}

var smtpServerIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func ParseICloudLivenessSMTPServers(raw string) ([]ICloudLivenessSMTPServer, error) {
	if len(raw) > 1<<20 {
		return nil, domain.ErrInvalidValue
	}
	var servers []ICloudLivenessSMTPServer
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&servers) != nil || servers == nil || len(servers) > ICloudLivenessSMTPMaxServers || decoder.Decode(new(any)) != io.EOF {
		return nil, domain.ErrInvalidValue
	}
	seen := make(map[string]bool, len(servers))
	for i := range servers {
		server := &servers[i]
		if !smtpServerIDPattern.MatchString(server.ID) || seen[server.ID] || strings.TrimSpace(server.Name) == "" || utf8.RuneCountInString(server.Name) > 80 {
			return nil, domain.ErrInvalidValue
		}
		seen[server.ID] = true
		for _, value := range []string{server.Name, server.Addr, server.Username, server.Password, server.From} {
			if strings.ContainsAny(value, "\r\n\x00") || len(value) > 4096 {
				return nil, domain.ErrInvalidValue
			}
		}
		host, port, err := net.SplitHostPort(server.Addr)
		number, portErr := strconv.Atoi(port)
		address, mailErr := mail.ParseAddress(server.From)
		if err != nil || host == "" || strings.ContainsAny(host, " /\\@?#\t") || portErr != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port || mailErr != nil || address.Address != server.From || len(server.From) > 320 || strings.TrimSpace(server.Username) == "" || len(server.Username) > 320 {
			return nil, domain.ErrInvalidValue
		}
		server.HasPassword = false
	}
	return servers, nil
}

func RedactICloudLivenessSMTPServers(raw string) string {
	servers, err := ParseICloudLivenessSMTPServers(raw)
	if err != nil {
		return "[]"
	}
	for i := range servers {
		servers[i].HasPassword = servers[i].Password != ""
		servers[i].Password = ""
	}
	encoded, _ := json.Marshal(servers)
	return string(encoded)
}

// Match secrets by ID inside the settings transaction, never by row position.
func MergeICloudLivenessSMTPSecrets(persisted, updates []domain.Setting) error {
	seen := false
	for i := range updates {
		if updates[i].Key != ICloudLivenessSMTPServersKey {
			continue
		}
		if seen {
			return domain.ErrInvalidValue
		}
		seen = true
		servers, err := ParseICloudLivenessSMTPServers(updates[i].Value)
		if err != nil {
			return err
		}
		passwords := make(map[string]string)
		for _, setting := range persisted {
			if setting.Key == ICloudLivenessSMTPServersKey {
				stored, err := ParseICloudLivenessSMTPServers(setting.Value)
				if err != nil {
					continue
				}
				for _, server := range stored {
					passwords[server.ID] = server.Password
				}
			}
		}
		for j := range servers {
			if servers[j].Password == "" {
				servers[j].Password = passwords[servers[j].ID]
			}
		}
		encoded, err := json.Marshal(servers)
		if err != nil {
			return domain.ErrInvalidValue
		}
		updates[i].Value = string(encoded)
	}
	return nil
}

func invalidICloudLivenessSMTPFields(values map[string]string) map[string]string {
	fields := make(map[string]string)
	raw := values[ICloudLivenessSMTPServersKey]
	if raw == "" {
		raw = "[]"
	}
	servers, err := ParseICloudLivenessSMTPServers(raw)
	if err != nil {
		fields[ICloudLivenessSMTPServersKey] = "Invalid SMTP server list."
		return fields
	}
	for _, server := range servers {
		if server.Enabled && strings.TrimSpace(server.Password) == "" {
			fields[ICloudLivenessSMTPServersKey] = "Enabled SMTP servers require a password or authorization code."
			break
		}
	}
	return fields
}
