package runtimeconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/stretchr/testify/require"
)

func TestICloudLivenessSMTPConfiguration(t *testing.T) {
	for _, value := range []string{"1", "60", "300", "86400"} {
		require.NoError(t, Validate(ICloudLivenessSMTPIntervalKey, value))
	}
	for _, value := range []string{"0", "-1", "1.5", "86401", "invalid", ""} {
		require.Error(t, Validate(ICloudLivenessSMTPIntervalKey, value))
	}
	server := ICloudLivenessSMTPServer{ID: "qq-1", Name: "QQ 1", Enabled: true, Addr: "smtp.qq.com:465", Username: "probe@qq.com", Password: "authorization-code", From: "probe@qq.com"}
	encode := func(servers ...ICloudLivenessSMTPServer) string {
		raw, err := json.Marshal(servers)
		require.NoError(t, err)
		return string(raw)
	}
	settings := []domain.Setting{{Key: ICloudLivenessSMTPEnabledKey, Value: "true"}, {Key: ICloudLivenessSMTPServersKey, Value: encode(server)}}
	require.NoError(t, ValidatePersistedUpdates(nil, settings))
	for _, value := range []string{"smtp.qq.com", "smtp.qq.com:0", "smtp.qq.com:65536", "smtp.qq.com:0465", "smtp.qq.com:+587", "https://smtp.qq.com:465", "smtp.qq.com:465\r\n", ":465"} {
		invalid := server
		invalid.Addr = value
		require.Error(t, Validate(ICloudLivenessSMTPServersKey, encode(invalid)), value)
	}
	require.Error(t, Validate(ICloudLivenessSMTPServersKey, encode(server, server)), "duplicate IDs must be rejected")
	invalid := server
	invalid.ID = "new"
	invalid.Password = ""
	invalid.HasPassword = true
	require.Error(t, ValidatePersistedUpdates(nil, []domain.Setting{{Key: ICloudLivenessSMTPServersKey, Value: encode(invalid)}}), "enabled entries must have a stored password")
	invalid.Enabled = false
	require.NoError(t, ValidatePersistedUpdates(nil, []domain.Setting{{Key: ICloudLivenessSMTPServersKey, Value: encode(invalid)}}))
	for _, raw := range []string{"null", `{}`, `[] []`, strings.Repeat(" ", 1<<20+1), `[{"id":"x","password":"secret","unknown":true}]`} {
		require.Error(t, Validate(ICloudLivenessSMTPServersKey, raw))
	}
	server.Addr = "smtp.qq.com:587"
	require.NoError(t, Validate(ICloudLivenessSMTPServersKey, encode(server)))
	server.From = "a@qq.com\r\nBcc: x@test.com"
	require.Error(t, Validate(ICloudLivenessSMTPServersKey, encode(server)))
}

func TestICloudLivenessSMTPSecretsFollowIDsAndAreRedacted(t *testing.T) {
	first := ICloudLivenessSMTPServer{ID: "a", Name: "A", Enabled: true, Addr: "smtp.qq.com:465", Username: "a@qq.com", Password: "secret-a", From: "a@qq.com"}
	second := first
	second.ID, second.Name, second.Username, second.From, second.Password = "b", "B", "b@qq.com", "b@qq.com", "secret-b"
	raw, err := json.Marshal([]ICloudLivenessSMTPServer{first, second})
	require.NoError(t, err)
	persisted := []domain.Setting{{Key: ICloudLivenessSMTPServersKey, Value: string(raw)}}
	redacted := RedactICloudLivenessSMTPServers(string(raw))
	require.NotContains(t, redacted, "secret-a")
	require.NotContains(t, redacted, "secret-b")
	require.Contains(t, redacted, `"hasPassword":true`)
	first.Password, second.Password = "replacement-a", ""
	raw, err = json.Marshal([]ICloudLivenessSMTPServer{second, first})
	require.NoError(t, err)
	updates := []domain.Setting{{Key: ICloudLivenessSMTPServersKey, Value: string(raw)}}
	require.NoError(t, MergeICloudLivenessSMTPSecrets(persisted, updates))
	stored, err := ParseICloudLivenessSMTPServers(updates[0].Value)
	require.NoError(t, err)
	require.Equal(t, "secret-b", stored[0].Password)
	require.Equal(t, "replacement-a", stored[1].Password)
	updates[0].Value = `[]`
	require.NoError(t, MergeICloudLivenessSMTPSecrets(persisted, updates))
	require.JSONEq(t, `[]`, updates[0].Value)
	require.Equal(t, `[]`, RedactICloudLivenessSMTPServers(`invalid-secret`))
	require.Error(t, MergeICloudLivenessSMTPSecrets(persisted, append(updates, updates[0])))
	first.Password = "repaired-secret"
	raw, err = json.Marshal([]ICloudLivenessSMTPServer{first})
	require.NoError(t, err)
	updates[0].Value = string(raw)
	require.NoError(t, MergeICloudLivenessSMTPSecrets([]domain.Setting{{Key: ICloudLivenessSMTPServersKey, Value: "invalid"}}, updates))
	require.NoError(t, ValidatePersistedUpdates(nil, updates))
}
