package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/redis/go-redis/v9"
)

const (
	sessionKeyPrefix     = "session:"
	userSessionsPrefix   = "user_sessions:"
	oauthFlowPrefix      = "oauth_flow:"
	linuxDOFlowPrefix    = "linuxdo_oauth_flow:"
	linuxDOPendingPrefix = "linuxdo_oauth_pending:"
	githubPendingPrefix  = "github_oauth_pending:"
	nodeLocPendingPrefix = "nodeloc_oauth_pending:"
)

var consumeOAuthFlowScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then
  return nil
end
redis.call('DEL', KEYS[1])
return value
`)

// SessionStore implements app.SessionStore using Redis.
type SessionStore struct {
	rdb redis.UniversalClient
}

// NewSessionStore creates a new Redis-backed session store.
func NewSessionStore(rdb redis.UniversalClient) *SessionStore {
	return &SessionStore{rdb: rdb}
}

func sessionKey(id string) string {
	return sessionKeyPrefix + id
}

func userSessionsKey(userID uint) string {
	return fmt.Sprintf("%s%d", userSessionsPrefix, userID)
}

func oauthFlowKey(state string) string {
	return oauthFlowPrefix + state
}

func linuxDOFlowKey(state string) string {
	return linuxDOFlowPrefix + state
}

func linuxDOPendingKey(token string) string {
	return linuxDOPendingPrefix + token
}

func githubPendingKey(token string) string {
	return githubPendingPrefix + token
}

func nodeLocPendingKey(token string) string {
	return nodeLocPendingPrefix + token
}

// sessionData is the JSON structure stored in Redis.
type sessionData struct {
	UserID       uint   `json:"userId"`
	Role         string `json:"role"`
	Email        string `json:"email"`
	TokenVersion int    `json:"tokenVersion"`
}

func (s *SessionStore) Create(ctx context.Context, session *domain.Session, ttlSeconds int) error {
	data := sessionData{
		UserID:       session.UserID,
		Role:         session.Role.String(),
		Email:        session.Email,
		TokenVersion: session.TokenVersion,
	}
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, sessionKey(session.ID), b, time.Duration(ttlSeconds)*time.Second)
	pipe.SAdd(ctx, userSessionsKey(session.UserID), session.ID)
	// Set TTL on the user sessions set too (clean up stale tracking)
	pipe.Expire(ctx, userSessionsKey(session.UserID), time.Duration(ttlSeconds)*time.Second)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis session create: %w", err)
	}
	return nil
}

func (s *SessionStore) Get(ctx context.Context, sessionID string) (*domain.Session, error) {
	b, err := s.rdb.Get(ctx, sessionKey(sessionID)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("redis session get: %w", err)
	}

	var data sessionData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}

	return &domain.Session{
		ID:           sessionID,
		UserID:       data.UserID,
		Role:         domain.Role(data.Role),
		Email:        data.Email,
		TokenVersion: data.TokenVersion,
	}, nil
}

func (s *SessionStore) Delete(ctx context.Context, sessionID string) error {
	// We need the userID to remove from the tracking set. Look up session first.
	sess, err := s.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess == nil {
		return nil
	}

	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, sessionKey(sessionID))
	pipe.SRem(ctx, userSessionsKey(sess.UserID), sessionID)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *SessionStore) DeleteByUserID(ctx context.Context, userID uint) error {
	// Get all session IDs for this user
	sessionIDs, err := s.rdb.SMembers(ctx, userSessionsKey(userID)).Result()
	if err != nil {
		return fmt.Errorf("redis get user sessions: %w", err)
	}

	if len(sessionIDs) == 0 {
		return nil
	}

	pipe := s.rdb.Pipeline()
	for _, sid := range sessionIDs {
		pipe.Del(ctx, sessionKey(sid))
	}
	pipe.Del(ctx, userSessionsKey(userID))
	_, err = pipe.Exec(ctx)
	return err
}

func (s *SessionStore) PutOAuthFlow(ctx context.Context, state string, flow app.OAuthFlow, ttl time.Duration) error {
	if strings.TrimSpace(state) == "" || strings.TrimSpace(flow.Provider) == "" || ttl <= 0 {
		return fmt.Errorf("invalid oauth flow")
	}
	data, err := json.Marshal(flow)
	if err != nil {
		return fmt.Errorf("marshal oauth flow: %w", err)
	}
	key := oauthFlowKey(state)
	if flow.Provider == "linuxdo" {
		// Keep LinuxDO on its original key while old and new instances overlap.
		key = linuxDOFlowKey(state)
	}
	if err := s.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("redis oauth flow put: %w", err)
	}
	return nil
}

func (s *SessionStore) ConsumeOAuthFlow(ctx context.Context, state string) (*app.OAuthFlow, error) {
	if strings.TrimSpace(state) == "" {
		return nil, nil
	}
	for _, key := range []string{oauthFlowKey(state), linuxDOFlowKey(state)} {
		value, err := consumeOAuthFlowScript.Run(ctx, s.rdb, []string{key}).Text()
		if err != nil {
			if err == redis.Nil {
				continue
			}
			return nil, fmt.Errorf("redis oauth flow consume: %w", err)
		}
		var flow app.OAuthFlow
		if err := json.Unmarshal([]byte(value), &flow); err != nil {
			return nil, fmt.Errorf("unmarshal oauth flow: %w", err)
		}
		if key == linuxDOFlowKey(state) && flow.Provider == "" {
			flow.Provider = "linuxdo"
		}
		return &flow, nil
	}
	return nil, nil
}

func (s *SessionStore) PutLinuxDOPending(ctx context.Context, token string, pending app.LinuxDOPending, ttl time.Duration) error {
	if strings.TrimSpace(token) == "" || ttl <= 0 {
		return fmt.Errorf("invalid linuxdo oauth pending setup")
	}
	data, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("marshal linuxdo oauth pending setup: %w", err)
	}
	if err := s.rdb.Set(ctx, linuxDOPendingKey(token), data, ttl).Err(); err != nil {
		return fmt.Errorf("redis linuxdo oauth pending setup put: %w", err)
	}
	return nil
}

func (s *SessionStore) GetLinuxDOPending(ctx context.Context, token string) (*app.LinuxDOPending, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil
	}
	data, err := s.rdb.Get(ctx, linuxDOPendingKey(token)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("redis linuxdo oauth pending setup get: %w", err)
	}
	var pending app.LinuxDOPending
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, fmt.Errorf("unmarshal linuxdo oauth pending setup: %w", err)
	}
	return &pending, nil
}

func (s *SessionStore) DeleteLinuxDOPending(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	if err := s.rdb.Del(ctx, linuxDOPendingKey(token)).Err(); err != nil {
		return fmt.Errorf("redis linuxdo oauth pending setup delete: %w", err)
	}
	return nil
}

func (s *SessionStore) PutGitHubPending(ctx context.Context, token string, pending app.GitHubPending, ttl time.Duration) error {
	if strings.TrimSpace(token) == "" || ttl <= 0 {
		return fmt.Errorf("invalid github oauth pending verification")
	}
	data, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("marshal github oauth pending verification: %w", err)
	}
	if err := s.rdb.Set(ctx, githubPendingKey(token), data, ttl).Err(); err != nil {
		return fmt.Errorf("redis github oauth pending verification put: %w", err)
	}
	return nil
}

func (s *SessionStore) GetGitHubPending(ctx context.Context, token string) (*app.GitHubPending, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil
	}
	data, err := s.rdb.Get(ctx, githubPendingKey(token)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("redis github oauth pending verification get: %w", err)
	}
	var pending app.GitHubPending
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, fmt.Errorf("unmarshal github oauth pending verification: %w", err)
	}
	return &pending, nil
}

func (s *SessionStore) DeleteGitHubPending(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	if err := s.rdb.Del(ctx, githubPendingKey(token)).Err(); err != nil {
		return fmt.Errorf("redis github oauth pending verification delete: %w", err)
	}
	return nil
}

func (s *SessionStore) PutNodeLocPending(ctx context.Context, token string, pending app.NodeLocPending, ttl time.Duration) error {
	if strings.TrimSpace(token) == "" || ttl <= 0 {
		return fmt.Errorf("invalid nodeloc oauth pending setup")
	}
	data, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("marshal nodeloc oauth pending setup: %w", err)
	}
	if err := s.rdb.Set(ctx, nodeLocPendingKey(token), data, ttl).Err(); err != nil {
		return fmt.Errorf("redis nodeloc oauth pending setup put: %w", err)
	}
	return nil
}

func (s *SessionStore) GetNodeLocPending(ctx context.Context, token string) (*app.NodeLocPending, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil
	}
	data, err := s.rdb.Get(ctx, nodeLocPendingKey(token)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("redis nodeloc oauth pending setup get: %w", err)
	}
	var pending app.NodeLocPending
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, fmt.Errorf("unmarshal nodeloc oauth pending setup: %w", err)
	}
	return &pending, nil
}

func (s *SessionStore) DeleteNodeLocPending(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	if err := s.rdb.Del(ctx, nodeLocPendingKey(token)).Err(); err != nil {
		return fmt.Errorf("redis nodeloc oauth pending setup delete: %w", err)
	}
	return nil
}
