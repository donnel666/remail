package api

// Preserve exported constants generated before BotProfileResponse added new
// enum name collisions. External Go clients may still import these names.
const (
	Qq       ConnectBotWebSocketParamsXBotChannel = ConnectBotWebSocketParamsXBotChannelQq
	Telegram ConnectBotWebSocketParamsXBotChannel = ConnectBotWebSocketParamsXBotChannelTelegram
	Private  GetProjectsParamsAccessType          = GetProjectsParamsAccessTypePrivate
	Public   GetProjectsParamsAccessType          = GetProjectsParamsAccessTypePublic
)
