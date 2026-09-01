package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	billingapi "github.com/donnel666/remail/internal/billing/api"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	"github.com/donnel666/remail/internal/botdisplay"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
)

const (
	botWebSocketTopicProjectLaunched                  = "project.launched"
	botWebSocketTopicLeaderboardSettled               = "leaderboard.settled"
	botWebSocketTopicSystemNoticeUpdated              = "system.notice.updated"
	botWebSocketTopicSystemAnnouncementUpdated        = "system.announcement.updated"
	botWebSocketTopicEmailDiscountUpdated             = "email.discount.updated"
	botWebSocketTopicProjectPriceUpdated              = "project.price.updated"
	botWebSocketCursorSourceStride             uint64 = 8
	botWebSocketCursorVersionMarker            uint64 = 1 << 63
	botWebSocketAnnouncementDataBudget                = botWebSocketMaxResponseBytes - botWebSocketMaxPayloadBytes
	botWebSocketAnnouncementItemBytes                 = 256 << 10
)

var botWebSocketTopics = []string{
	botWebSocketTopicProjectLaunched,
	botWebSocketTopicLeaderboardSettled,
	botWebSocketTopicSystemNoticeUpdated,
	botWebSocketTopicSystemAnnouncementUpdated,
	botWebSocketTopicEmailDiscountUpdated,
	botWebSocketTopicProjectPriceUpdated,
}

type botWebSocketCursor struct {
	After   time.Time `json:"after"`
	AfterID uint64    `json:"-"`
}

// botWebSocketCursorID accepts legacy JSON numbers and the lossless decimal
// string used by the multi-source cursor protocol.
type botWebSocketCursorID uint64

func (id *botWebSocketCursorID) UnmarshalJSON(payload []byte) error {
	raw := strings.TrimSpace(string(payload))
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		if err := json.Unmarshal(payload, &raw); err != nil {
			return err
		}
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return errors.New("invalid Bot event cursor ID")
	}
	*id = botWebSocketCursorID(parsed)
	return nil
}

func (cursor botWebSocketCursor) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		After   time.Time `json:"after"`
		AfterID string    `json:"afterId"`
	}{After: cursor.After, AfterID: strconv.FormatUint(cursor.AfterID, 10)})
}

type botWebSocketEvent struct {
	Topic  string
	Cursor botWebSocketCursor
	Data   any
}

type botProjectLaunchedProject struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type botProjectLaunchedData struct {
	Project botProjectLaunchedProject `json:"project"`
}

type botWebSocketEventSource interface {
	List(context.Context, []string, botWebSocketCursor, time.Time, int) ([]botWebSocketEvent, error)
}

type botWebSocketDBEventSource struct{ db *gorm.DB }

func newBotWebSocketDBEventSource(db *gorm.DB) botWebSocketEventSource {
	if db == nil {
		return nil
	}
	return &botWebSocketDBEventSource{db: db}
}

func normalizeBotWebSocketSubscription(frame botWebSocketInbound, now time.Time) ([]string, botWebSocketCursor, error) {
	requested := append([]string(nil), frame.Topics...)
	if len(requested) == 0 {
		topic := strings.TrimSpace(frame.Topic)
		requested = append(requested, topic)
	}
	seen := make(map[string]struct{}, len(requested))
	topics := make([]string, 0, len(requested))
	for _, raw := range requested {
		topic := strings.TrimSpace(raw)
		if !supportedBotWebSocketTopic(topic) {
			return nil, botWebSocketCursor{}, errors.New("unsupported Bot event topic")
		}
		if _, exists := seen[topic]; exists {
			continue
		}
		seen[topic] = struct{}{}
		topics = append(topics, topic)
	}
	if len(topics) == 0 || len(topics) > len(botWebSocketTopics) {
		return nil, botWebSocketCursor{}, errors.New("invalid Bot event topics")
	}
	after := strings.TrimSpace(frame.After)
	if after == "" {
		if frame.AfterID != 0 {
			return nil, botWebSocketCursor{}, errors.New("bot event cursor time is required")
		}
		return topics, botWebSocketCursor{After: now.UTC().Add(-botWebSocketEventReadyDelay)}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, after)
	if err != nil {
		return nil, botWebSocketCursor{}, errors.New("invalid Bot event cursor")
	}
	afterID := uint64(frame.AfterID)
	if afterID != 0 {
		if afterID&botWebSocketCursorVersionMarker != 0 {
			if !botWebSocketLooksEncodedSourceID(afterID) {
				return nil, botWebSocketCursor{}, errors.New("invalid Bot event cursor ID")
			}
		} else {
			if !slicesContainBotWebSocketTopic(topics, botWebSocketTopicProjectLaunched) {
				return nil, botWebSocketCursor{}, errors.New("legacy Bot event cursor requires project topic")
			}
			encoded, ok := botWebSocketEncodeSourceID(botWebSocketTopicProjectLaunched, afterID)
			if !ok {
				return nil, botWebSocketCursor{}, errors.New("bot event cursor overflow")
			}
			afterID = encoded
		}
	}
	return topics, botWebSocketCursor{After: parsed.UTC(), AfterID: afterID}, nil
}

func botWebSocketLooksEncodedSourceID(value uint64) bool {
	if value&botWebSocketCursorVersionMarker == 0 {
		return false
	}
	payload := value &^ botWebSocketCursorVersionMarker
	ordinal := payload % botWebSocketCursorSourceStride
	return payload/botWebSocketCursorSourceStride > 0 && ordinal >= 1 && ordinal <= uint64(len(botWebSocketTopics))
}

func supportedBotWebSocketTopic(topic string) bool {
	return botWebSocketTopicOrdinal(topic) != 0
}

func slicesContainBotWebSocketTopic(topics []string, wanted string) bool {
	for _, topic := range topics {
		if topic == wanted {
			return true
		}
	}
	return false
}

func botWebSocketTopicOrdinal(topic string) uint64 {
	for i, candidate := range botWebSocketTopics {
		if topic == candidate {
			return uint64(i + 1)
		}
	}
	return 0
}

func botWebSocketEncodeSourceID(topic string, rowID uint64) (uint64, bool) {
	ordinal := botWebSocketTopicOrdinal(topic)
	if ordinal == 0 || rowID == 0 || rowID > (botWebSocketCursorVersionMarker-1-ordinal)/botWebSocketCursorSourceStride {
		return 0, false
	}
	return botWebSocketCursorVersionMarker | (rowID*botWebSocketCursorSourceStride + ordinal), true
}

// botWebSocketSourceAfterID translates the global cursor into the last row ID
// that cannot produce an event after it for one durable source.
func botWebSocketSourceAfterID(globalID uint64, topic string) uint64 {
	ordinal := botWebSocketTopicOrdinal(topic)
	if ordinal == 0 || globalID == 0 {
		return 0
	}
	if globalID&botWebSocketCursorVersionMarker == 0 {
		if topic == botWebSocketTopicProjectLaunched {
			return globalID
		}
		return 0
	}
	payload := globalID &^ botWebSocketCursorVersionMarker
	if payload < ordinal {
		return 0
	}
	return (payload - ordinal) / botWebSocketCursorSourceStride
}

func botWebSocketCursorBefore(left, right botWebSocketCursor) bool {
	if !left.After.Equal(right.After) {
		return left.After.Before(right.After)
	}
	return left.AfterID < right.AfterID
}

func (s *botWebSocketDBEventSource) List(ctx context.Context, topics []string, cursor botWebSocketCursor, cutoff time.Time, limit int) ([]botWebSocketEvent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("bot event source unavailable")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	selected := make(map[string]bool, len(topics))
	for _, topic := range topics {
		selected[topic] = true
	}
	events := make([]botWebSocketEvent, 0, limit)
	if selected[botWebSocketTopicProjectLaunched] {
		items, err := s.listProjectLaunches(ctx, cursor, cutoff, limit)
		if err != nil {
			return nil, err
		}
		events = append(events, items...)
	}
	if selected[botWebSocketTopicLeaderboardSettled] {
		items, err := s.listLeaderboardSettlements(ctx, cursor, cutoff, limit)
		if err != nil {
			return nil, err
		}
		events = append(events, items...)
	}
	if selected[botWebSocketTopicSystemNoticeUpdated] || selected[botWebSocketTopicSystemAnnouncementUpdated] || selected[botWebSocketTopicEmailDiscountUpdated] {
		items, err := s.listSystemSettingEvents(ctx, selected, cursor, cutoff, limit)
		if err != nil {
			return nil, err
		}
		events = append(events, items...)
	}
	if selected[botWebSocketTopicProjectPriceUpdated] {
		items, err := s.listProjectPriceUpdates(ctx, cursor, cutoff, limit)
		if err != nil {
			return nil, err
		}
		events = append(events, items...)
	}
	sort.Slice(events, func(i, j int) bool { return botWebSocketCursorBefore(events[i].Cursor, events[j].Cursor) })
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func (s *botWebSocketDBEventSource) listProjectLaunches(ctx context.Context, cursor botWebSocketCursor, cutoff time.Time, limit int) ([]botWebSocketEvent, error) {
	var rows []struct {
		ID          uint64    `gorm:"column:id"`
		CreatedAt   time.Time `gorm:"column:created_at"`
		ProjectID   uint      `gorm:"column:project_id"`
		Name        string    `gorm:"column:name"`
		Description string    `gorm:"column:description"`
	}
	afterID := botWebSocketSourceAfterID(cursor.AfterID, botWebSocketTopicProjectLaunched)
	err := s.db.WithContext(ctx).Table("operation_logs AS operation").
		Select("operation.id, operation.created_at, project.id AS project_id, project.name, project.description").
		Joins("JOIN projects AS project ON project.id = operation.resource_id").
		Where("operation.operation_type IN ? AND operation.resource_type = ? AND operation.result = ?", []string{"core.project.create", "core.project.approve"}, "project", "success").
		Where("project.status = ? AND project.access_type = ?", "listed", "public").
		Where("operation.created_at <= ?", cutoff).
		Where("operation.created_at > ? OR (operation.created_at = ? AND operation.id > ?)", cursor.After, cursor.After, afterID).
		Order("operation.created_at ASC, operation.id ASC").Limit(limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	events := make([]botWebSocketEvent, 0, len(rows))
	for _, row := range rows {
		encoded, ok := botWebSocketEncodeSourceID(botWebSocketTopicProjectLaunched, row.ID)
		if !ok {
			return nil, errors.New("project launch cursor overflow")
		}
		events = append(events, botWebSocketEvent{
			Topic:  botWebSocketTopicProjectLaunched,
			Cursor: botWebSocketCursor{After: row.CreatedAt.UTC(), AfterID: encoded},
			Data: botProjectLaunchedData{Project: botProjectLaunchedProject{
				ID: row.ProjectID, Name: row.Name, Description: row.Description,
			}},
		})
	}
	return events, nil
}

type botLeaderboardSettledData struct {
	BusinessDate string                                `json:"businessDate"`
	SettledAt    time.Time                             `json:"settledAt"`
	Items        []billingapi.BotLeaderboardRewardItem `json:"items"`
}

func (s *botWebSocketDBEventSource) listLeaderboardSettlements(ctx context.Context, cursor botWebSocketCursor, cutoff time.Time, limit int) ([]botWebSocketEvent, error) {
	var settlements []struct {
		ID           uint64    `gorm:"column:id"`
		BusinessDate string    `gorm:"column:business_date"`
		SettledAt    time.Time `gorm:"column:settled_at"`
	}
	afterID := botWebSocketSourceAfterID(cursor.AfterID, botWebSocketTopicLeaderboardSettled)
	err := s.db.WithContext(ctx).Table("leaderboard_settlements").
		Select("id, business_date, settled_at").
		Where("status = ?", "completed").
		Where("settled_at <= ?", cutoff).
		Where("settled_at > ? OR (settled_at = ? AND id > ?)", cursor.After, cursor.After, afterID).
		Order("settled_at ASC, id ASC").Limit(limit).Scan(&settlements).Error
	if err != nil || len(settlements) == 0 {
		return nil, err
	}
	ids := make([]uint64, len(settlements))
	for i := range settlements {
		ids[i] = settlements[i].ID
	}
	var rewards []struct {
		SettlementID uint64 `gorm:"column:settlement_id"`
		UserID       uint   `gorm:"column:user_id"`
		Nickname     string `gorm:"column:nickname"`
		Email        string `gorm:"column:email"`
		Rank         int    `gorm:"column:rank_no"`
		Score        int    `gorm:"column:score"`
		Amount       string `gorm:"column:reward_amount"`
	}
	if err := s.db.WithContext(ctx).Table("leaderboard_rewards AS reward").
		Select("reward.settlement_id, reward.user_id, COALESCE(user.nickname, '') AS nickname, COALESCE(user.email, '') AS email, reward.rank_no, reward.score, reward.reward_amount").
		Joins("JOIN users AS user ON user.id = reward.user_id").
		Where("reward.settlement_id IN ?", ids).
		Order("reward.settlement_id ASC, reward.rank_no ASC").Scan(&rewards).Error; err != nil {
		return nil, err
	}
	bySettlement := make(map[uint64][]billingapi.BotLeaderboardRewardItem, len(ids))
	for _, reward := range rewards {
		amount := "0.00"
		if parsed, err := billingdomain.ParseMoney(reward.Amount); err == nil {
			amount = billingdomain.MoneyString(parsed)
		}
		bySettlement[reward.SettlementID] = append(bySettlement[reward.SettlementID], billingapi.BotLeaderboardRewardItem{
			Rank: reward.Rank, Name: botdisplay.Name(reward.Nickname, reward.Email, reward.UserID),
			SuccessCount: reward.Score, RewardAmount: amount,
		})
	}
	events := make([]botWebSocketEvent, 0, len(settlements))
	for _, settlement := range settlements {
		encoded, ok := botWebSocketEncodeSourceID(botWebSocketTopicLeaderboardSettled, settlement.ID)
		if !ok {
			return nil, errors.New("leaderboard cursor overflow")
		}
		items := bySettlement[settlement.ID]
		if items == nil {
			items = []billingapi.BotLeaderboardRewardItem{}
		}
		events = append(events, botWebSocketEvent{
			Topic:  botWebSocketTopicLeaderboardSettled,
			Cursor: botWebSocketCursor{After: settlement.SettledAt.UTC(), AfterID: encoded},
			Data:   botLeaderboardSettledData{BusinessDate: settlement.BusinessDate, SettledAt: settlement.SettledAt.UTC(), Items: items},
		})
	}
	return events, nil
}

type botSystemNoticeData struct {
	Notice string `json:"notice"`
}

func botSystemNoticeProjection(value string) botSystemNoticeData {
	data := botSystemNoticeData{Notice: truncateBotWebSocketUTF8(strings.TrimSpace(value), botWebSocketAnnouncementItemBytes)}
	if encoded, err := json.Marshal(data); err == nil && len(encoded) <= botWebSocketAnnouncementDataBudget {
		return data
	}
	data.Notice = truncateBotWebSocketUTF8(data.Notice, botWebSocketAnnouncementDataBudget/6)
	return data
}

type botSystemAnnouncement struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type botSystemAnnouncementData struct {
	Announcements []botSystemAnnouncement `json:"announcements"`
}

type botMessageData struct {
	Message string `json:"message"`
}

func (s *botWebSocketDBEventSource) listSystemSettingEvents(ctx context.Context, selected map[string]bool, cursor botWebSocketCursor, cutoff time.Time, limit int) ([]botWebSocketEvent, error) {
	topics := make([]string, 0, 3)
	resourceIDs := make([]string, 0, 7)
	if selected[botWebSocketTopicSystemNoticeUpdated] {
		topics = append(topics, botWebSocketTopicSystemNoticeUpdated)
		resourceIDs = append(resourceIDs, "global_notice")
	}
	if selected[botWebSocketTopicSystemAnnouncementUpdated] {
		topics = append(topics, botWebSocketTopicSystemAnnouncementUpdated)
		resourceIDs = append(resourceIDs, "announcements", "announcement_enabled")
	}
	if selected[botWebSocketTopicEmailDiscountUpdated] {
		topics = append(topics, botWebSocketTopicEmailDiscountUpdated)
		resourceIDs = append(resourceIDs,
			runtimeconfig.MicrosoftPriceMultiplierKey, runtimeconfig.GmailPriceMultiplierKey,
			runtimeconfig.ICloudPriceMultiplierKey, runtimeconfig.DomainPriceMultiplierKey,
		)
	}
	minAfterID := uint64(math.MaxUint64)
	for _, topic := range topics {
		minAfterID = min(minAfterID, botWebSocketSourceAfterID(cursor.AfterID, topic))
	}
	var logs []struct {
		ID            uint64    `gorm:"column:id"`
		ResourceID    string    `gorm:"column:resource_id"`
		OperationType string    `gorm:"column:operation_type"`
		CreatedAt     time.Time `gorm:"column:created_at"`
	}
	err := s.db.WithContext(ctx).Table("operation_logs").
		Select("id, resource_id, operation_type, created_at").
		Where("operation_type IN ?", []string{"system_settings.upsert", "system_settings.delete"}).
		Where("result = ?", "success").Where("resource_id IN ?", resourceIDs).
		Where("created_at <= ?", cutoff).
		Where("created_at > ? OR (created_at = ? AND id > ?)", cursor.After, cursor.After, minAfterID).
		Order("created_at ASC, id ASC").Limit(limit * len(topics)).Scan(&logs).Error
	if err != nil || len(logs) == 0 {
		return nil, err
	}
	values, err := s.botPublicSettingValues(ctx)
	if err != nil {
		return nil, err
	}
	notice := botSystemNoticeProjection(values["global_notice"])
	announcements := botSystemAnnouncementData{Announcements: botActiveAnnouncements(values, time.Now(), 20)}
	discount := botMessageData{Message: "邮箱折扣已更新，请查看最新项目价格。"}
	events := make([]botWebSocketEvent, 0, len(logs))
	for _, log := range logs {
		for _, topic := range botSystemSettingLogTopics(log.ResourceID, selected) {
			encoded, ok := botWebSocketEncodeSourceID(topic, log.ID)
			if !ok {
				return nil, errors.New("system setting cursor overflow")
			}
			event := botWebSocketEvent{Topic: topic, Cursor: botWebSocketCursor{After: log.CreatedAt.UTC(), AfterID: encoded}}
			if !botWebSocketCursorBefore(cursor, event.Cursor) {
				continue
			}
			switch topic {
			case botWebSocketTopicSystemNoticeUpdated:
				event.Data = notice
			case botWebSocketTopicSystemAnnouncementUpdated:
				event.Data = announcements
			case botWebSocketTopicEmailDiscountUpdated:
				event.Data = discount
			}
			events = append(events, event)
		}
	}
	return events, nil
}

func botSystemSettingLogTopics(resourceID string, selected map[string]bool) []string {
	topics := make([]string, 0, 3)
	if selected[botWebSocketTopicSystemNoticeUpdated] && resourceID == "global_notice" {
		topics = append(topics, botWebSocketTopicSystemNoticeUpdated)
	}
	if selected[botWebSocketTopicSystemAnnouncementUpdated] && (resourceID == "announcements" || resourceID == "announcement_enabled") {
		topics = append(topics, botWebSocketTopicSystemAnnouncementUpdated)
	}
	if selected[botWebSocketTopicEmailDiscountUpdated] && (resourceID == runtimeconfig.MicrosoftPriceMultiplierKey || resourceID == runtimeconfig.GmailPriceMultiplierKey || resourceID == runtimeconfig.ICloudPriceMultiplierKey || resourceID == runtimeconfig.DomainPriceMultiplierKey) {
		topics = append(topics, botWebSocketTopicEmailDiscountUpdated)
	}
	return topics
}

func (s *botWebSocketDBEventSource) botPublicSettingValues(ctx context.Context) (map[string]string, error) {
	keys := []string{
		"global_notice", "announcement_enabled", "announcements",
	}
	var rows []struct {
		Key   string `gorm:"column:key"`
		Value string `gorm:"column:value"`
	}
	if err := s.db.WithContext(ctx).Table("system_settings").Select("`key`, value").Where("`key` IN ?", keys).Scan(&rows).Error; err != nil {
		return nil, err
	}
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		values[strings.ToLower(strings.TrimSpace(row.Key))] = row.Value
	}
	return values, nil
}

func botActiveAnnouncements(values map[string]string, now time.Time, limit int) []botSystemAnnouncement {
	if limit <= 0 {
		return []botSystemAnnouncement{}
	}
	if enabled, err := strconv.ParseBool(strings.TrimSpace(values["announcement_enabled"])); err == nil && !enabled {
		return []botSystemAnnouncement{}
	}
	var announcements []runtimeconfig.Announcement
	if json.Unmarshal([]byte(values["announcements"]), &announcements) != nil {
		return []botSystemAnnouncement{}
	}
	active := make([]runtimeconfig.Announcement, 0, len(announcements))
	for _, announcement := range announcements {
		start, startErr := time.Parse(time.RFC3339, announcement.StartTime)
		end, endErr := time.Parse(time.RFC3339, announcement.EndTime)
		if !announcement.Enabled || (startErr == nil && start.After(now)) || (endErr == nil && !end.After(now)) {
			continue
		}
		active = append(active, announcement)
	}
	sort.SliceStable(active, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339, active[i].StartTime)
		right, rightErr := time.Parse(time.RFC3339, active[j].StartTime)
		if leftErr == nil && rightErr == nil && !left.Equal(right) {
			return left.After(right)
		}
		if (leftErr == nil) != (rightErr == nil) {
			return leftErr == nil
		}
		return active[i].ID > active[j].ID
	})
	if limit > 0 && len(active) > limit {
		active = active[:limit]
	}
	result := make([]botSystemAnnouncement, 0, len(active))
	for i := range active {
		item := botSystemAnnouncement{
			Title:   truncateBotWebSocketUTF8(active[i].Title, botWebSocketAnnouncementItemBytes),
			Content: truncateBotWebSocketUTF8(active[i].Content, botWebSocketAnnouncementItemBytes),
		}
		candidate := append(result, item)
		encoded, err := json.Marshal(botSystemAnnouncementData{Announcements: candidate})
		if err != nil || len(encoded) > botWebSocketAnnouncementDataBudget {
			break
		}
		result = candidate
	}
	return result
}

func truncateBotWebSocketUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	suffix := "…"
	cut := limit - len(suffix)
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + suffix
}

type botProjectPriceUpdatedData struct {
	ProjectID uint   `json:"projectId"`
	Name      string `json:"name"`
	Message   string `json:"message"`
}

func (s *botWebSocketDBEventSource) listProjectPriceUpdates(ctx context.Context, cursor botWebSocketCursor, cutoff time.Time, limit int) ([]botWebSocketEvent, error) {
	var updates []struct {
		ID        uint64    `gorm:"column:id"`
		CreatedAt time.Time `gorm:"column:created_at"`
		ProjectID uint      `gorm:"column:project_id"`
		Name      string    `gorm:"column:name"`
	}
	afterID := botWebSocketSourceAfterID(cursor.AfterID, botWebSocketTopicProjectPriceUpdated)
	err := s.db.WithContext(ctx).Table("operation_logs AS operation").
		Select("operation.id, operation.created_at, project.id AS project_id, project.name").
		Joins("JOIN projects AS project ON project.id = operation.resource_id").
		Where("operation.operation_type = ? AND operation.resource_type = ? AND operation.result = ?", "core.project.price_updated", "project", "success").
		Where("project.status = ? AND project.access_type = ?", "listed", "public").
		Where("operation.created_at <= ?", cutoff).
		Where("operation.created_at > ? OR (operation.created_at = ? AND operation.id > ?)", cursor.After, cursor.After, afterID).
		Order("operation.created_at ASC, operation.id ASC").Limit(limit).Scan(&updates).Error
	if err != nil {
		return nil, err
	}
	var bulk []struct {
		ID        uint64    `gorm:"column:id"`
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	if err := s.db.WithContext(ctx).Table("operation_logs").Select("id, created_at").
		Where("operation_type = ? AND resource_type = ? AND resource_id = ? AND result = ?", "core.project.bulk_update_products", "project", "bulk", "success").
		Where("created_at <= ?", cutoff).
		Where("created_at > ? OR (created_at = ? AND id > ?)", cursor.After, cursor.After, afterID).
		Order("created_at ASC, id ASC").Limit(limit).Scan(&bulk).Error; err != nil {
		return nil, err
	}
	events := make([]botWebSocketEvent, 0, len(updates)+len(bulk))
	for _, update := range updates {
		encoded, ok := botWebSocketEncodeSourceID(botWebSocketTopicProjectPriceUpdated, update.ID)
		if !ok {
			return nil, errors.New("project price cursor overflow")
		}
		events = append(events, botWebSocketEvent{
			Topic:  botWebSocketTopicProjectPriceUpdated,
			Cursor: botWebSocketCursor{After: update.CreatedAt.UTC(), AfterID: encoded},
			Data:   botProjectPriceUpdatedData{ProjectID: update.ProjectID, Name: update.Name, Message: "项目价格已更新，请查看最新价格。"},
		})
	}
	for _, update := range bulk {
		encoded, ok := botWebSocketEncodeSourceID(botWebSocketTopicProjectPriceUpdated, update.ID)
		if !ok {
			return nil, errors.New("project price cursor overflow")
		}
		events = append(events, botWebSocketEvent{
			Topic:  botWebSocketTopicProjectPriceUpdated,
			Cursor: botWebSocketCursor{After: update.CreatedAt.UTC(), AfterID: encoded},
			Data:   botMessageData{Message: "项目价格已批量更新，请查看最新价格。"},
		})
	}
	sort.Slice(events, func(i, j int) bool { return botWebSocketCursorBefore(events[i].Cursor, events[j].Cursor) })
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}
