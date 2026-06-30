package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/redis/go-redis/v9"
)

const (
	sessionKeyPrefix   = "session:"
	userSessionsPrefix = "user_sessions:"
)

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

// sessionData is the JSON structure stored in Redis.
type sessionData struct {
	UserID       uint   `json:"userId"`
	RoleLevel    int    `json:"roleLevel"`
	Email        string `json:"email"`
	TokenVersion int    `json:"tokenVersion"`
}

func (s *SessionStore) Create(ctx context.Context, session *domain.Session, ttlSeconds int) error {
	data := sessionData{
		UserID:       session.UserID,
		RoleLevel:    int(session.RoleLevel),
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
		RoleLevel:    domain.RoleLevel(data.RoleLevel),
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
