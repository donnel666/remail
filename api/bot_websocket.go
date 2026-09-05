package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/websocket"
)

const (
	botWebSocketMaxPayloadBytes   = 16 << 10
	botWebSocketMaxResponseBytes  = 2 << 20
	botWebSocketIdleTimeout       = 60 * time.Second
	botWebSocketWriteTimeout      = 10 * time.Second
	botWebSocketEventPollInterval = 2 * time.Second
	botWebSocketEventReadyDelay   = 2 * time.Second
	botWebSocketConnectionsPerKey = 4
	botWebSocketHeartbeatSeconds  = 20
	botWebSocketFramesPerMinute   = 600
	botWebSocketFrameBurst        = 30
	botWebSocketMaxInFlight       = 4
	botWebSocketReauthInterval    = time.Minute
)

type botWebSocketInbound struct {
	Type    string               `json:"type"`
	ID      string               `json:"id,omitempty"`
	Method  string               `json:"method,omitempty"`
	Path    string               `json:"path,omitempty"`
	Subject string               `json:"subject,omitempty"`
	Scene   string               `json:"scene,omitempty"`
	GroupID string               `json:"groupId,omitempty"`
	Query   map[string]string    `json:"query,omitempty"`
	Body    json.RawMessage      `json:"body,omitempty"`
	Topic   string               `json:"topic,omitempty"`
	Topics  []string             `json:"topics,omitempty"`
	After   string               `json:"after,omitempty"`
	AfterID botWebSocketCursorID `json:"afterId,omitempty"`
}

type botWebSocketOutbound struct {
	Type             string              `json:"type"`
	ID               string              `json:"id,omitempty"`
	Status           int                 `json:"status,omitempty"`
	Body             json.RawMessage     `json:"body,omitempty"`
	RetryAfter       string              `json:"retryAfter,omitempty"`
	Code             string              `json:"code,omitempty"`
	Message          string              `json:"message,omitempty"`
	Topic            string              `json:"topic,omitempty"`
	Topics           []string            `json:"topics,omitempty"`
	Cursor           *botWebSocketCursor `json:"cursor,omitempty"`
	Data             any                 `json:"data,omitempty"`
	HeartbeatSeconds int                 `json:"heartbeatSeconds,omitempty"`
	Platform         string              `json:"platform,omitempty"`
	SubjectNamespace string              `json:"subjectNamespace,omitempty"`
}

type botWebSocketConnectionLimiter struct {
	mu      sync.Mutex
	byKeyID map[uint]int
	frames  map[uint]botWebSocketFrameBudget
}

type botWebSocketFrameBudget struct {
	tokens float64
	last   time.Time
}

func (l *botWebSocketConnectionLimiter) acquire(keyID uint) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byKeyID == nil {
		l.byKeyID = make(map[uint]int)
	}
	if l.byKeyID[keyID] >= botWebSocketConnectionsPerKey {
		return false
	}
	l.byKeyID[keyID]++
	return true
}

func (l *botWebSocketConnectionLimiter) release(keyID uint) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byKeyID[keyID] <= 1 {
		delete(l.byKeyID, keyID)
		return
	}
	l.byKeyID[keyID]--
}

func (l *botWebSocketConnectionLimiter) takeFrame(keyID uint, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.frames == nil {
		l.frames = make(map[uint]botWebSocketFrameBudget)
	}
	budget, exists := l.frames[keyID]
	if !exists {
		budget = botWebSocketFrameBudget{tokens: botWebSocketFrameBurst, last: now}
	}
	elapsed := now.Sub(budget.last).Seconds()
	budget.tokens = min(float64(botWebSocketFrameBurst), budget.tokens+elapsed*float64(botWebSocketFramesPerMinute)/60)
	budget.last = now
	if budget.tokens < 1 {
		l.frames[keyID] = budget
		return false
	}
	budget.tokens--
	l.frames[keyID] = budget
	return true
}

func registerBotWebSocketRoute(
	rg *gin.RouterGroup,
	dispatch http.Handler,
	authenticator middleware.BotSystemKeyAuthenticator,
	eventSources ...botWebSocketEventSource,
) {
	connections := &botWebSocketConnectionLimiter{}
	var events botWebSocketEventSource
	if len(eventSources) > 0 {
		events = eventSources[0]
	}
	rg.GET("/ws", func(c *gin.Context) {
		integration, ok := middleware.GetCurrentBotIntegration(c)
		plainKey := strings.TrimSpace(c.GetHeader(middleware.SystemKeyHeaderName))
		if !ok || plainKey == "" || dispatch == nil || authenticator == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"message": "Service is temporarily unavailable.", "requestId": middleware.GetRequestID(c),
			})
			return
		}
		if !connections.acquire(integration.SystemKeyID) {
			c.Header("Retry-After", "30")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"message": "Too many WebSocket connections.", "requestId": middleware.GetRequestID(c),
			})
			return
		}
		defer connections.release(integration.SystemKeyID)

		server := websocket.Server{
			// Authentication uses a non-browser X-System-Key header; browser clients
			// cannot supply it through the WebSocket API, so Origin is not an auth input.
			Handshake: func(_ *websocket.Config, _ *http.Request) error { return nil },
			Handler: func(conn *websocket.Conn) {
				session := newBotWebSocketSession(conn, dispatch, plainKey, integration, connections, authenticator, events)
				session.serve()
			},
		}
		server.ServeHTTP(c.Writer, c.Request)
	})
}

type botWebSocketSession struct {
	conn          *websocket.Conn
	dispatch      http.Handler
	plainKey      string
	integration   middleware.BotIntegration
	ctx           context.Context
	cancel        context.CancelFunc
	sendMu        sync.Mutex
	subMu         sync.Mutex
	subCancel     context.CancelFunc
	subGeneration uint64
	requestSlots  chan struct{}
	limits        *botWebSocketConnectionLimiter
	authenticator middleware.BotSystemKeyAuthenticator
	authenticated time.Time
	events        botWebSocketEventSource
}

func newBotWebSocketSession(
	conn *websocket.Conn,
	dispatch http.Handler,
	plainKey string,
	integration middleware.BotIntegration,
	limits *botWebSocketConnectionLimiter,
	authenticator middleware.BotSystemKeyAuthenticator,
	eventSources ...botWebSocketEventSource,
) *botWebSocketSession {
	ctx, cancel := context.WithCancel(conn.Request().Context())
	var events botWebSocketEventSource
	if len(eventSources) > 0 {
		events = eventSources[0]
	}
	return &botWebSocketSession{
		conn: conn, dispatch: dispatch, plainKey: plainKey, integration: integration,
		ctx: ctx, cancel: cancel, requestSlots: make(chan struct{}, botWebSocketMaxInFlight),
		limits: limits, authenticator: authenticator, authenticated: time.Now(), events: events,
	}
}

func (s *botWebSocketSession) serve() {
	defer s.close()
	s.conn.MaxPayloadBytes = botWebSocketMaxPayloadBytes
	if err := s.send(botWebSocketOutbound{
		Type: "hello", HeartbeatSeconds: botWebSocketHeartbeatSeconds,
		Platform: s.integration.Platform, SubjectNamespace: s.integration.SubjectNamespace,
	}); err != nil {
		return
	}
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(botWebSocketIdleTimeout))
		var frame botWebSocketInbound
		if err := websocket.JSON.Receive(s.conn, &frame); err != nil {
			return
		}
		if !s.reauthenticate(time.Now()) {
			return
		}
		if s.limits == nil || !s.limits.takeFrame(s.integration.SystemKeyID, time.Now()) {
			s.sendProtocolError(frame.ID, "rate_limit_exceeded", "WebSocket frame rate exceeded.")
			return
		}
		switch strings.ToLower(strings.TrimSpace(frame.Type)) {
		case "ping":
			if err := s.send(botWebSocketOutbound{Type: "pong", ID: frame.ID}); err != nil {
				return
			}
		case "request":
			s.startRequest(frame)
		case "subscribe":
			s.handleSubscribe(frame)
		default:
			s.sendProtocolError(frame.ID, "invalid_frame", "Unsupported WebSocket frame type.")
		}
	}
}

func (s *botWebSocketSession) reauthenticate(now time.Time) bool {
	if s == nil || s.authenticator == nil {
		return false
	}
	if !s.authenticated.IsZero() && now.Sub(s.authenticated) < botWebSocketReauthInterval {
		return true
	}
	key, err := s.authenticator.AuthenticateBotSystemKey(s.ctx, s.plainKey)
	if err != nil || key == nil || key.ID != s.integration.SystemKeyID ||
		key.Platform != s.integration.Platform || key.SubjectNamespace != s.integration.SubjectNamespace {
		return false
	}
	s.authenticated = now
	return true
}

func (s *botWebSocketSession) startRequest(frame botWebSocketInbound) {
	select {
	case s.requestSlots <- struct{}{}:
		go func() {
			defer func() { <-s.requestSlots }()
			s.handleRequest(frame)
		}()
	default:
		s.sendProtocolError(frame.ID, "too_many_requests", "Too many in-flight WebSocket requests.")
	}
}

func (s *botWebSocketSession) close() {
	s.cancel()
	s.subMu.Lock()
	if s.subCancel != nil {
		s.subCancel()
	}
	s.subMu.Unlock()
	_ = s.conn.Close()
}

func (s *botWebSocketSession) send(frame botWebSocketOutbound) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(botWebSocketWriteTimeout))
	err := websocket.JSON.Send(s.conn, frame)
	if err != nil {
		s.cancel()
		_ = s.conn.Close()
	}
	return err
}

func (s *botWebSocketSession) sendProtocolError(id, code, message string) {
	_ = s.send(botWebSocketOutbound{Type: "error", ID: id, Code: code, Message: message})
}

func (s *botWebSocketSession) handleRequest(frame botWebSocketInbound) {
	if !validBotWebSocketRequestID(frame.ID) || !allowedBotWebSocketRequest(frame.Method, frame.Path) {
		s.sendProtocolError(frame.ID, "invalid_request", "Unsupported Bot API request.")
		return
	}
	status, body, retryAfter, err := dispatchBotWebSocketRequest(
		s.ctx, s.dispatch, s.plainKey, s.integration.Platform, frame,
	)
	if err != nil {
		s.sendProtocolError(frame.ID, "invalid_request", "Invalid Bot API request.")
		return
	}
	_ = s.send(botWebSocketOutbound{
		Type: "response", ID: frame.ID, Status: status, Body: body, RetryAfter: retryAfter,
	})
}

func (s *botWebSocketSession) handleSubscribe(frame botWebSocketInbound) {
	if !validBotWebSocketRequestID(frame.ID) {
		s.sendProtocolError(frame.ID, "invalid_subscription", "Unsupported Bot event subscription.")
		return
	}
	topics, cursor, err := normalizeBotWebSocketSubscription(frame, time.Now().UTC())
	if err != nil || s.events == nil {
		s.sendProtocolError(frame.ID, "invalid_subscription", "Unsupported Bot event subscription.")
		return
	}

	s.subMu.Lock()
	if s.subCancel != nil {
		s.subCancel()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.subCancel = cancel
	s.subGeneration++
	generation := s.subGeneration
	s.subMu.Unlock()

	response := botWebSocketOutbound{Type: "subscribed", ID: frame.ID, Topics: topics, Cursor: &cursor}
	if len(topics) == 1 {
		response.Topic = topics[0]
	}
	if err := s.send(response); err != nil {
		cancel()
		return
	}
	go s.streamEvents(ctx, generation, topics, cursor)
}

func (s *botWebSocketSession) streamEvents(ctx context.Context, generation uint64, topics []string, cursor botWebSocketCursor) {
	// ponytail: one durable-fact poll per subscribing Bot connection; use a
	// shared broker if concurrent Bot subscribers grow beyond 10.
	for {
		if !s.currentSubscription(generation) {
			return
		}
		events, retryAfter, err := s.nextEvents(ctx, topics, cursor, 100)
		if err != nil {
			if ctx.Err() == nil {
				_ = s.send(botWebSocketOutbound{Type: "error", Code: "subscription_failed", Message: "Bot event subscription failed.", RetryAfter: retryAfter})
			}
			return
		}
		for _, event := range events {
			if !s.currentSubscription(generation) {
				return
			}
			if err := s.send(botWebSocketOutbound{
				Type: "event", Topic: event.Topic, Cursor: &event.Cursor, Data: event.Data,
			}); err != nil {
				return
			}
			cursor = event.Cursor
		}
		if len(events) == 100 {
			continue
		}
		timer := time.NewTimer(botWebSocketEventPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *botWebSocketSession) nextEvents(ctx context.Context, topics []string, cursor botWebSocketCursor, limit int) ([]botWebSocketEvent, string, error) {
	cutoff := time.Now().UTC().Add(-botWebSocketEventReadyDelay)
	events, err := s.events.List(ctx, topics, cursor, cutoff, limit)
	if err != nil {
		return nil, "", err
	}
	return events, "", nil
}

func (s *botWebSocketSession) currentSubscription(generation uint64) bool {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	return s.subGeneration == generation
}

func dispatchBotWebSocketRequest(
	ctx context.Context,
	dispatch http.Handler,
	plainKey string,
	channel string,
	frame botWebSocketInbound,
) (int, json.RawMessage, string, error) {
	method := strings.ToUpper(strings.TrimSpace(frame.Method))
	path := strings.TrimSpace(frame.Path)
	if !allowedBotWebSocketRequest(method, path) || len(frame.Query) > 32 {
		return 0, nil, "", errors.New("unsupported bot request")
	}
	values := make(url.Values, len(frame.Query))
	for key, value := range frame.Query {
		if key == "" || len(key) > 64 || len(value) > 512 {
			return 0, nil, "", errors.New("invalid bot query")
		}
		values.Set(key, value)
	}
	target := path
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(frame.Body))
	if err != nil {
		return 0, nil, "", err
	}
	request = middleware.WithTrustedBotDispatch(request)
	request.Header.Set(middleware.SystemKeyHeaderName, plainKey)
	request.Header.Set(middleware.BotChannelHeaderName, channel)
	if frame.Subject != "" {
		request.Header.Set(middleware.BotSubjectHeaderName, frame.Subject)
	}
	if frame.Scene != "" {
		request.Header.Set(middleware.BotSceneHeaderName, frame.Scene)
	}
	if frame.GroupID != "" {
		request.Header.Set(middleware.BotGroupHeaderName, frame.GroupID)
	}
	if len(frame.Body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := newBotWebSocketResponseRecorder()
	dispatch.ServeHTTP(recorder, request)
	if recorder.tooLarge {
		return 0, nil, "", errors.New("bot response is too large")
	}
	body := bytes.TrimSpace(recorder.body.Bytes())
	if len(body) > 0 && !json.Valid(body) {
		return 0, nil, "", errors.New("bot response is not JSON")
	}
	return recorder.statusCode(), json.RawMessage(bytes.Clone(body)), recorder.Header().Get("Retry-After"), nil
}

type botWebSocketResponseRecorder struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	tooLarge bool
}

func newBotWebSocketResponseRecorder() *botWebSocketResponseRecorder {
	return &botWebSocketResponseRecorder{header: make(http.Header)}
}

func (r *botWebSocketResponseRecorder) Header() http.Header { return r.header }

func (r *botWebSocketResponseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *botWebSocketResponseRecorder) Write(payload []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	remaining := botWebSocketMaxResponseBytes - r.body.Len()
	if len(payload) > remaining {
		r.tooLarge = true
		if remaining > 0 {
			_, _ = r.body.Write(payload[:remaining])
		}
		return len(payload), nil
	}
	_, _ = r.body.Write(payload)
	return len(payload), nil
}

func (r *botWebSocketResponseRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func allowedBotWebSocketRequest(method, path string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	path = strings.TrimSpace(path)
	switch method + " " + path {
	case "POST /v1/bot/bindings",
		"GET /v1/bot/binding",
		"DELETE /v1/bot/binding",
		"GET /v1/bot/context",
		"GET /v1/bot/profile",
		"GET /v1/bot/projects",
		"GET /v1/bot/recharges/config",
		"GET /v1/bot/rankings/orders",
		"GET /v1/bot/rankings/rewards/latest",
		"POST /v1/bot/diagnoses/code":
		return true
	}
	if method != http.MethodGet || !strings.HasPrefix(path, "/v1/bot/projects/") {
		return false
	}
	remainder := strings.TrimPrefix(path, "/v1/bot/projects/")
	remainder = strings.TrimSuffix(remainder, "/inventory")
	projectID, err := strconv.ParseUint(remainder, 10, 64)
	return err == nil && projectID > 0
}

func validBotWebSocketRequestID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
