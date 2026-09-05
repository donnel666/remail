package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/api/middleware"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/net/websocket"
)

func botWebSocketTestServer(t *testing.T, dispatch http.Handler, eventSources ...botWebSocketEventSource) (*httptest.Server, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	bot := router.Group("/v1/bot")
	auth := botRouterAuthenticator(func(_ context.Context, plain string) (*settingsdomain.SystemKey, error) {
		if plain != "sk_test" {
			return nil, settingsdomain.ErrInvalidSystemKey
		}
		return &settingsdomain.SystemKey{
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot,
			Platform: "qq", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"10001"},
		}, nil
	})
	bot.Use(middleware.BotSystemKeyRequired(auth))
	bot.Use(middleware.BotChannelRequired())
	registerBotWebSocketRoute(bot, dispatch, auth, eventSources...)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/bot/ws"
}

func TestBotWebSocketSessionReauthenticatesDeletedKey(t *testing.T) {
	active := true
	auth := botRouterAuthenticator(func(context.Context, string) (*settingsdomain.SystemKey, error) {
		if !active {
			return nil, settingsdomain.ErrInvalidSystemKey
		}
		return &settingsdomain.SystemKey{
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot,
			Platform: "qq", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"10001"},
		}, nil
	})
	now := time.Now()
	session := &botWebSocketSession{
		ctx: context.Background(), plainKey: "sk_test", authenticator: auth,
		integration:   middleware.BotIntegration{SystemKeyID: 9, Platform: "qq", SubjectNamespace: "qq:main"},
		authenticated: now,
	}
	if !session.reauthenticate(now.Add(botWebSocketReauthInterval - time.Second)) {
		t.Fatal("session reauthenticated before the interval elapsed")
	}
	active = false
	if session.reauthenticate(now.Add(botWebSocketReauthInterval)) {
		t.Fatal("session remained authenticated after its Bot Key was deleted")
	}
}

func dialBotWebSocket(t *testing.T, target string) *websocket.Conn {
	t.Helper()
	config, err := websocket.NewConfig(target, "http://localhost")
	if err != nil {
		t.Fatalf("websocket config: %v", err)
	}
	config.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
	config.Header.Set(middleware.BotChannelHeaderName, "qq")
	conn, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func receiveBotWebSocketFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var frame map[string]any
	if err := websocket.JSON.Receive(conn, &frame); err != nil {
		t.Fatalf("websocket receive: %v", err)
	}
	return frame
}

func TestBotWebSocketRejectsChannelThatDoesNotMatchKey(t *testing.T) {
	_, target := botWebSocketTestServer(t, http.NotFoundHandler())
	config, err := websocket.NewConfig(target, "http://localhost")
	if err != nil {
		t.Fatalf("websocket config: %v", err)
	}
	config.Header.Set(middleware.SystemKeyHeaderName, "sk_test")
	config.Header.Set(middleware.BotChannelHeaderName, "telegram")
	conn, err := websocket.DialConfig(config)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("WebSocket accepted a Telegram channel with a QQ System Key")
	}
}

type botWebSocketEventSourceFunc func(context.Context, []string, botWebSocketCursor, time.Time, int) ([]botWebSocketEvent, error)

func (f botWebSocketEventSourceFunc) List(ctx context.Context, topics []string, cursor botWebSocketCursor, cutoff time.Time, limit int) ([]botWebSocketEvent, error) {
	return f(ctx, topics, cursor, cutoff, limit)
}

func TestBotWebSocketHeartbeatAndRequestTunnel(t *testing.T) {
	var calls atomic.Int32
	dispatch := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get(middleware.SystemKeyHeaderName) != "sk_test" ||
			request.Header.Get(middleware.BotChannelHeaderName) != "qq" ||
			request.Header.Get(middleware.BotSubjectHeaderName) != "123456789" ||
			request.Header.Get(middleware.BotSceneHeaderName) != middleware.BotSceneGroup ||
			request.Header.Get(middleware.BotGroupHeaderName) != "10001" {
			t.Errorf("unexpected trusted headers: %v", request.Header)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if request.URL.Path != "/v1/bot/projects" || request.URL.Query().Get("search") != "GitHub" {
			t.Errorf("unexpected request target: %s", request.URL.String())
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"items":[],"total":0,"offset":0,"limit":20}`))
	})
	_, target := botWebSocketTestServer(t, dispatch)
	conn := dialBotWebSocket(t, target)

	hello := receiveBotWebSocketFrame(t, conn)
	if hello["type"] != "hello" || hello["platform"] != "qq" {
		t.Fatalf("unexpected hello: %v", hello)
	}
	if err := websocket.JSON.Send(conn, map[string]any{"type": "ping", "id": "heartbeat-1"}); err != nil {
		t.Fatalf("send ping: %v", err)
	}
	if pong := receiveBotWebSocketFrame(t, conn); pong["type"] != "pong" || pong["id"] != "heartbeat-1" {
		t.Fatalf("unexpected pong: %v", pong)
	}
	if err := websocket.JSON.Send(conn, map[string]any{
		"type": "request", "id": "request-1", "method": http.MethodGet,
		"path": "/v1/bot/projects", "subject": "123456789", "scene": "group", "groupId": "10001",
		"query": map[string]string{"search": "GitHub"},
	}); err != nil {
		t.Fatalf("send request: %v", err)
	}
	response := receiveBotWebSocketFrame(t, conn)
	if response["type"] != "response" || response["id"] != "request-1" || response["status"] != float64(http.StatusOK) || calls.Load() != 1 {
		t.Fatalf("unexpected response: %v, calls=%d", response, calls.Load())
	}
	body, ok := response["body"].(map[string]any)
	if !ok || body["total"] != float64(0) {
		t.Fatalf("unexpected body: %T %v", response["body"], response["body"])
	}
}

func TestBotWebSocketProjectLaunchSubscription(t *testing.T) {
	eventAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	eventID, _ := botWebSocketEncodeSourceID(botWebSocketTopicProjectLaunched, 7)
	source := botWebSocketEventSourceFunc(func(_ context.Context, topics []string, cursor botWebSocketCursor, _ time.Time, _ int) ([]botWebSocketEvent, error) {
		if len(topics) != 1 || topics[0] != botWebSocketTopicProjectLaunched {
			return nil, fmt.Errorf("unexpected topics: %v", topics)
		}
		event := botWebSocketEvent{
			Topic:  botWebSocketTopicProjectLaunched,
			Cursor: botWebSocketCursor{After: eventAt, AfterID: eventID},
			Data: botProjectLaunchedData{Project: botProjectLaunchedProject{
				ID: 7, Name: "Launch", Description: "Public",
			}},
		}
		if !botWebSocketCursorBefore(cursor, event.Cursor) {
			return nil, nil
		}
		return []botWebSocketEvent{event}, nil
	})
	_, target := botWebSocketTestServer(t, http.NotFoundHandler(), source)
	conn := dialBotWebSocket(t, target)
	_ = receiveBotWebSocketFrame(t, conn)

	if err := websocket.JSON.Send(conn, map[string]any{
		"type": "subscribe", "id": "subscription-1", "topic": "project.launched",
		"after": "2026-08-30T11:00:00Z", "afterId": 0,
	}); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}
	if subscribed := receiveBotWebSocketFrame(t, conn); subscribed["type"] != "subscribed" {
		t.Fatalf("unexpected subscribed frame: %v", subscribed)
	}
	event := receiveBotWebSocketFrame(t, conn)
	if event["type"] != "event" || event["topic"] != "project.launched" {
		t.Fatalf("unexpected event: %v", event)
	}
	cursor := event["cursor"].(map[string]any)
	if cursor["afterId"] != strconv.FormatUint(eventID, 10) {
		t.Fatalf("unexpected cursor: %v", cursor)
	}
	data, _ := json.Marshal(event["data"])
	if !strings.Contains(string(data), `"name":"Launch"`) || strings.Contains(string(data), "reviewReason") || strings.Contains(string(data), "codePrice") {
		t.Fatalf("unexpected event data: %s", data)
	}
}

func TestBotWebSocketMultiTopicSubscriptionUsesOneCursor(t *testing.T) {
	eventAt := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	eventID, _ := botWebSocketEncodeSourceID(botWebSocketTopicSystemNoticeUpdated, 9)
	source := botWebSocketEventSourceFunc(func(_ context.Context, topics []string, cursor botWebSocketCursor, _ time.Time, _ int) ([]botWebSocketEvent, error) {
		if len(topics) != 2 || topics[0] != botWebSocketTopicSystemNoticeUpdated || topics[1] != botWebSocketTopicLeaderboardSettled {
			return nil, fmt.Errorf("unexpected topics: %v", topics)
		}
		event := botWebSocketEvent{
			Topic:  botWebSocketTopicSystemNoticeUpdated,
			Cursor: botWebSocketCursor{After: eventAt, AfterID: eventID},
			Data:   botSystemNoticeData{Notice: "维护通知"},
		}
		if !botWebSocketCursorBefore(cursor, event.Cursor) {
			return nil, nil
		}
		return []botWebSocketEvent{event}, nil
	})
	_, target := botWebSocketTestServer(t, http.NotFoundHandler(), source)
	conn := dialBotWebSocket(t, target)
	_ = receiveBotWebSocketFrame(t, conn)

	if err := websocket.JSON.Send(conn, map[string]any{
		"type": "subscribe", "id": "subscription-many", "topic": "ignored.invalid",
		"topics": []string{botWebSocketTopicSystemNoticeUpdated, botWebSocketTopicLeaderboardSettled},
		"after":  eventAt.Add(-time.Second).Format(time.RFC3339Nano), "afterId": 0,
	}); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}
	subscribed := receiveBotWebSocketFrame(t, conn)
	if subscribed["type"] != "subscribed" || subscribed["topic"] != nil {
		t.Fatalf("unexpected subscribed frame: %v", subscribed)
	}
	topics, ok := subscribed["topics"].([]any)
	if !ok || len(topics) != 2 {
		t.Fatalf("unexpected subscribed topics: %v", subscribed)
	}
	event := receiveBotWebSocketFrame(t, conn)
	if event["type"] != "event" || event["topic"] != botWebSocketTopicSystemNoticeUpdated {
		t.Fatalf("unexpected event: %v", event)
	}
	if cursor, ok := event["cursor"].(map[string]any); !ok || cursor["afterId"] != strconv.FormatUint(eventID, 10) {
		t.Fatalf("unexpected global cursor: %v", event["cursor"])
	}
}

func TestAllowedBotWebSocketRequestRejectsArbitraryRoutes(t *testing.T) {
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/v1/admin/users"},
		{http.MethodGet, "/v1/bot/ws"},
		{http.MethodPost, "/v1/bot/projects/1"},
		{http.MethodGet, "/v1/bot/projects/1/anything"},
		{http.MethodPost, "/v1/bot/recharges"},
		{http.MethodPost, "/v1/recharges"},
	} {
		if allowedBotWebSocketRequest(test.method, test.path) {
			t.Fatalf("unexpectedly allowed %s %s", test.method, test.path)
		}
	}
	if !allowedBotWebSocketRequest(http.MethodGet, "/v1/bot/projects/42/inventory") {
		t.Fatal("project inventory route was rejected")
	}
	if !allowedBotWebSocketRequest(http.MethodGet, "/v1/bot/context") {
		t.Fatal("bot context route was rejected")
	}
	if !allowedBotWebSocketRequest(http.MethodGet, "/v1/bot/profile") {
		t.Fatal("bot profile route was rejected")
	}
	if !allowedBotWebSocketRequest(http.MethodGet, "/v1/bot/recharges/config") {
		t.Fatal("bot recharge config route was rejected")
	}
	if !allowedBotWebSocketRequest(http.MethodPost, "/v1/bot/recharges/quote") {
		t.Fatal("bot recharge quote route was rejected")
	}
	recorder := newBotWebSocketResponseRecorder()
	payload := make([]byte, botWebSocketMaxResponseBytes+1)
	if written, err := recorder.Write(payload); err != nil || written != len(payload) || !recorder.tooLarge || recorder.body.Len() != botWebSocketMaxResponseBytes {
		t.Fatalf("response bound failed: written=%d error=%v tooLarge=%v buffered=%d", written, err, recorder.tooLarge, recorder.body.Len())
	}
	limits := &botWebSocketConnectionLimiter{}
	now := time.Now()
	for range botWebSocketFrameBurst {
		if !limits.takeFrame(9, now) {
			t.Fatal("frame budget ended early")
		}
	}
	if limits.takeFrame(9, now) {
		t.Fatal("frame budget allowed an excess frame")
	}
	if !limits.takeFrame(9, now.Add(100*time.Millisecond)) {
		t.Fatal("frame budget did not refill")
	}
}

func TestBotWebSocketReusesRegisteredBotMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	router := gin.New()
	router.Use(middleware.RequestID())
	auth := botRouterAuthenticator(func(context.Context, string) (*settingsdomain.SystemKey, error) {
		return &settingsdomain.SystemKey{
			ID: 9, Purpose: settingsdomain.SystemKeyPurposeBot,
			Platform: "qq", SubjectNamespace: "qq:main", AllowedGroupIDs: []string{"10001"},
		}, nil
	})
	registerBotRoutes(router.Group("/v1"), router, auth, nil, nil, nil, nil, nil, nil, rdb)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	conn := dialBotWebSocket(t, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/bot/ws")
	_ = receiveBotWebSocketFrame(t, conn)

	requireSend := map[string]any{
		"type": "request", "id": "request-middleware", "method": http.MethodGet,
		"path": "/v1/bot/context", "subject": "123456789", "scene": "group", "groupId": "10001",
	}
	if err := websocket.JSON.Send(conn, requireSend); err != nil {
		t.Fatalf("send request: %v", err)
	}
	response := receiveBotWebSocketFrame(t, conn)
	if response["type"] != "response" || response["status"] != float64(http.StatusServiceUnavailable) {
		t.Fatalf("unexpected middleware response: %v", response)
	}
	body, ok := response["body"].(map[string]any)
	if !ok || body["authorized"] != nil || body["message"] != "Service is temporarily unavailable." {
		t.Fatalf("unsafe context response: %v", response)
	}

	rejected := map[string]any{
		"type": "request", "id": "request-rejected-group", "method": http.MethodGet,
		"path": "/v1/bot/context", "subject": "123456789", "scene": "group", "groupId": "99999",
	}
	if err := websocket.JSON.Send(conn, rejected); err != nil {
		t.Fatalf("send rejected group request: %v", err)
	}
	response = receiveBotWebSocketFrame(t, conn)
	if response["type"] != "response" || response["status"] != float64(http.StatusUnauthorized) {
		t.Fatalf("unexpected rejected group response: %v", response)
	}
	encoded, _ := json.Marshal(response["body"])
	if strings.Contains(string(encoded), "10001") || strings.Contains(string(encoded), "99999") {
		t.Fatalf("group whitelist leaked: %s", encoded)
	}
}
