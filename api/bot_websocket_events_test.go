package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestBotWebSocketSubscriptionNormalizesLegacyAndGlobalCursors(t *testing.T) {
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	for _, legacyID := range append([]uint64{123}, []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}...) {
		t.Run(fmt.Sprintf("legacy-%d", legacyID), func(t *testing.T) {
			topics, cursor, err := normalizeBotWebSocketSubscription(botWebSocketInbound{
				Topics: []string{botWebSocketTopicProjectLaunched, botWebSocketTopicLeaderboardSettled},
				Topic:  "ignored.invalid", After: now.Format(time.RFC3339Nano), AfterID: botWebSocketCursorID(legacyID),
			}, now)
			if err != nil || len(topics) != 2 || botWebSocketSourceAfterID(cursor.AfterID, botWebSocketTopicProjectLaunched) != legacyID {
				t.Fatalf("normalize legacy cursor = topics=%v cursor=%+v error=%v", topics, cursor, err)
			}
			if !botWebSocketLooksEncodedSourceID(cursor.AfterID) {
				t.Fatalf("legacy cursor was not versioned: %d", cursor.AfterID)
			}
		})
	}

	encoded, ok := botWebSocketEncodeSourceID(botWebSocketTopicLeaderboardSettled, 77)
	if !ok {
		t.Fatal("encode global cursor failed")
	}
	_, cursor, err := normalizeBotWebSocketSubscription(botWebSocketInbound{
		Topics: []string{botWebSocketTopicLeaderboardSettled}, After: now.Format(time.RFC3339Nano), AfterID: botWebSocketCursorID(encoded),
	}, now)
	if err != nil || cursor.AfterID != encoded || botWebSocketSourceAfterID(encoded, botWebSocketTopicLeaderboardSettled) != 77 {
		t.Fatalf("global cursor changed: cursor=%+v error=%v", cursor, err)
	}
	var stringFrame botWebSocketInbound
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"type":"subscribe","afterId":"%d"}`, encoded)), &stringFrame); err != nil || uint64(stringFrame.AfterID) != encoded {
		t.Fatalf("lossless string cursor decode failed: frame=%+v error=%v", stringFrame, err)
	}
}

func TestBotWebSocketDBEventsExposeOnlyPublicProjection(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:bot-websocket-events?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE operation_logs (id INTEGER PRIMARY KEY, operation_type TEXT, resource_type TEXT, resource_id TEXT, result TEXT, created_at DATETIME)`,
		`CREATE TABLE system_settings (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT, description TEXT, status TEXT, access_type TEXT)`,
		`CREATE TABLE leaderboard_settlements (id INTEGER PRIMARY KEY, business_date TEXT, status TEXT, settled_at DATETIME)`,
		`CREATE TABLE leaderboard_rewards (settlement_id INTEGER, user_id INTEGER, rank_no INTEGER, score INTEGER, reward_amount TEXT)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, nickname TEXT)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create fixture table: %v", err)
		}
	}
	eventAt := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	logs := []struct {
		ID, ResourceID, OperationType, ResourceType string
	}{
		{"10", "global_notice", "system_settings.upsert", "system_setting"},
		{"11", "announcements", "system_settings.upsert", "system_setting"},
		{"12", "product_price_multiplier_gmail", "system_settings.upsert", "system_setting"},
		{"13", "8", "core.project.price_updated", "project"},
		{"14", "7", "core.project.price_updated", "project"},
		{"15", "bulk", "core.project.bulk_update_products", "project"},
		{"16", "bulk", "system_settings.bulk_upsert", "system_setting"},
		{"17", "7", "core.project.create", "project"},
		{"18", "8", "core.project.approve", "project"},
	}
	for _, log := range logs {
		if err := db.Exec(`INSERT INTO operation_logs(id, operation_type, resource_type, resource_id, result, created_at) VALUES (?, ?, ?, ?, 'success', ?)`,
			log.ID, log.OperationType, log.ResourceType, log.ResourceID, eventAt).Error; err != nil {
			t.Fatalf("insert operation log: %v", err)
		}
	}
	announcements := `[{"id":1,"title":"公开标题","content":"公开内容","type":"default","startTime":"","endTime":"","enabled":true}]`
	for key, value := range map[string]string{
		"global_notice": "  维护通知  ", "announcement_enabled": "true", "announcements": announcements,
		"product_price_multiplier_gmail": "0.8", "private_secret": "must-not-leak",
	} {
		if err := db.Exec("INSERT INTO system_settings(key, value) VALUES (?, ?)", key, value).Error; err != nil {
			t.Fatalf("insert setting: %v", err)
		}
	}
	if err := db.Exec("INSERT INTO projects(id, name, description, status, access_type) VALUES (7, '公开项目', '公开说明', 'listed', 'public'), (8, '私有项目', '私有说明', 'listed', 'private')").Error; err != nil {
		t.Fatalf("insert projects: %v", err)
	}
	if err := db.Exec("INSERT INTO users(id, nickname) VALUES (99, 'winner@example.com')").Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := db.Exec("INSERT INTO leaderboard_settlements(id, business_date, status, settled_at) VALUES (1, '2026-08-30', 'completed', ?)", eventAt).Error; err != nil {
		t.Fatalf("insert settlement: %v", err)
	}
	if err := db.Exec("INSERT INTO leaderboard_rewards(settlement_id, user_id, rank_no, score, reward_amount) VALUES (1, 99, 1, 20, '12.340000')").Error; err != nil {
		t.Fatalf("insert reward: %v", err)
	}

	source := &botWebSocketDBEventSource{db: db}
	events, err := source.List(context.Background(), []string{
		botWebSocketTopicProjectLaunched,
		botWebSocketTopicLeaderboardSettled,
		botWebSocketTopicSystemNoticeUpdated,
		botWebSocketTopicSystemAnnouncementUpdated,
		botWebSocketTopicEmailDiscountUpdated,
		botWebSocketTopicProjectPriceUpdated,
	}, botWebSocketCursor{After: eventAt.Add(-time.Second)}, eventAt.Add(time.Second), 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 7 {
		t.Fatalf("events = %d, want 7: %+v", len(events), events)
	}
	for i := 1; i < len(events); i++ {
		if !botWebSocketCursorBefore(events[i-1].Cursor, events[i].Cursor) {
			t.Fatalf("events are not globally ordered: %+v", events)
		}
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	payload := string(encoded)
	for _, forbidden := range []string{"must-not-leak", "winner@example.com", "私有项目", "userId", "resourceId", "operationType"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("unsafe value %q leaked in %s", forbidden, payload)
		}
	}
	for _, wanted := range []string{`"notice":"维护通知"`, `"title":"公开标题"`, `"name":"匿名用户"`, `"rewardAmount":"12.34"`, `"projectId":7`, `"name":"公开项目"`, `"description":"公开说明"`, "项目价格已批量更新"} {
		if !strings.Contains(payload, wanted) {
			t.Fatalf("safe projection %s missing from %s", wanted, payload)
		}
	}
	prices, err := source.listProjectPriceUpdates(context.Background(), botWebSocketCursor{After: eventAt.Add(-time.Second)}, eventAt.Add(time.Second), 1)
	if err != nil || len(prices) != 1 {
		t.Fatalf("visible project update starved by private log: events=%+v error=%v", prices, err)
	}
	if data, ok := prices[0].Data.(botProjectPriceUpdatedData); !ok || data.ProjectID != 7 {
		t.Fatalf("unexpected first visible price event: %+v", prices[0])
	}
}

func TestBotActiveAnnouncementsBoundsWebSocketPayload(t *testing.T) {
	large := strings.Repeat("<", 1<<20)
	values := map[string]string{
		"announcement_enabled": "true",
		"announcements": `[{"id":3,"title":"three","content":"` + large + `","type":"default","startTime":"","endTime":"","enabled":true},` +
			`{"id":2,"title":"two","content":"` + large + `","type":"default","startTime":"","endTime":"","enabled":true},` +
			`{"id":1,"title":"one","content":"` + large + `","type":"default","startTime":"","endTime":"","enabled":true}]`,
	}
	items := botActiveAnnouncements(values, time.Now(), 20)
	encoded, err := json.Marshal(botSystemAnnouncementData{Announcements: items})
	if err != nil {
		t.Fatalf("marshal announcement event: %v", err)
	}
	if len(items) == 0 || len(encoded) > botWebSocketMaxResponseBytes {
		t.Fatalf("announcement projection items=%d bytes=%d", len(items), len(encoded))
	}
	if len(items[0].Content) > botWebSocketAnnouncementItemBytes {
		t.Fatalf("announcement item bytes=%d", len(items[0].Content))
	}
}

func TestBotSystemNoticeBoundsEscapedPayload(t *testing.T) {
	data := botSystemNoticeProjection(strings.Repeat("\\", 1<<20))
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal notice event: %v", err)
	}
	if len(data.Notice) > botWebSocketAnnouncementItemBytes || len(encoded) > botWebSocketAnnouncementDataBudget {
		t.Fatalf("notice bytes raw=%d encoded=%d", len(data.Notice), len(encoded))
	}
}
