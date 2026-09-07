package api

// Preserve both forms because oapi-codegen changes which enum gets the short
// name when operations using the same inline values are added or reordered.
const (
	ConnectBotWebSocketParamsXBotChannelQq       ConnectBotWebSocketParamsXBotChannel = Qq
	ConnectBotWebSocketParamsXBotChannelTelegram ConnectBotWebSocketParamsXBotChannel = Telegram
	GetProjectsParamsAccessTypePrivate           GetProjectsParamsAccessType          = Private
	GetProjectsParamsAccessTypePublic            GetProjectsParamsAccessType          = Public
	// Keep the historical short names exported by older oapi-codegen output.
	Disable      PatchAdminDomainRequestStatusCommand = PatchAdminDomainRequestStatusCommandDisable
	Enable       PatchAdminDomainRequestStatusCommand = PatchAdminDomainRequestStatusCommandEnable
	MarkAbnormal PatchAdminDomainRequestStatusCommand = PatchAdminDomainRequestStatusCommandMarkAbnormal
	MarkNormal   PatchAdminDomainRequestStatusCommand = PatchAdminDomainRequestStatusCommandMarkNormal
)
