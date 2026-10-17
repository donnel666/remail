package infra

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	daumapp "github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestDaumFetchRequiresThreeFailedRoundsAndSuccessClearsCounter(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "counter@daum.net", AppPassword: "synthetic"})
	require.NoError(t, err)
	require.NoError(t, s.DB.Model(&Resource{}).Where("id=?", id).Update("status", domain.StatusNormal).Error)
	server := miniredis.RunT(t)
	s.Redis = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = s.Redis.Close() })
	calls, fail, complete := 0, true, true
	s.Protocol = daumValidationClientStub{fetch: func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
		calls++
		if fail {
			return protocol.FetchResult{}, &protocol.Failure{Category: "invalid_credentials", SafeMessage: "APP password rejected."}
		}
		return protocol.FetchResult{Complete: complete}, nil
	}}
	for round := 1; round <= 2; round++ {
		_, err = s.FetchMailbox(ctx, id, 1, protocol.FetchRequest{})
		require.Error(t, err)
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.Equal(t, domain.StatusNormal, row.Status)
		require.Equal(t, round*3, calls)
	}
	fail = false
	complete = false
	_, err = s.FetchMailbox(ctx, id, 1, protocol.FetchRequest{FullHistory: true})
	require.Error(t, err)
	current, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	count, err := server.Get(daumFetchFailureKey(*current))
	require.NoError(t, err)
	require.Equal(t, "2", count, "incomplete history must not clear prior failures")
	complete = true
	_, err = s.FetchMailbox(ctx, id, 1, protocol.FetchRequest{})
	require.NoError(t, err)
	current, err = s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.False(t, server.Exists(daumFetchFailureKey(*current)))
	fail = true
	for round := 1; round <= 3; round++ {
		_, err = s.FetchMailbox(ctx, id, 1, protocol.FetchRequest{})
		require.Error(t, err)
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		if round < 3 {
			require.Equal(t, domain.StatusNormal, row.Status)
		} else {
			require.Equal(t, domain.StatusAbnormal, row.Status)
			require.Equal(t, "invalid_credentials", row.FailureCategory)
		}
	}
}

func TestDaumFetchDoesNotConfirmFailureWithoutCounter(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "nocounter@daum.net", AppPassword: "synthetic"})
	require.NoError(t, err)
	require.NoError(t, s.DB.Model(&Resource{}).Where("id=?", id).Update("status", domain.StatusNormal).Error)
	s.Protocol = daumValidationClientStub{fetch: func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
		return protocol.FetchResult{}, &protocol.Failure{Category: "invalid_credentials"}
	}}
	for round := 0; round < 4; round++ {
		_, err = s.FetchMailbox(ctx, id, 1, protocol.FetchRequest{})
		require.Error(t, err)
	}
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusNormal, row.Status)
}

type fetchFailureRaceHook struct {
	before func(string)
}

func (h fetchFailureRaceHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h fetchFailureRaceHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.before(cmd.Name())
		return next(ctx, cmd)
	}
}
func (h fetchFailureRaceHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			h.before(cmd.Name())
		}
		return next(ctx, cmds)
	}
}

func TestDaumFetchCounterIsolatesSupersededCredentialsAndValidation(t *testing.T) {
	for _, change := range []string{"credentials", "validation"} {
		for _, race := range []string{"late_failure", "before_increment", "before_clear"} {
			t.Run(change+"/"+race, func(t *testing.T) {
				ctx := context.Background()
				s := newHistoryTestService(t)
				resource, _ := historyTestResource(t, s, "race@daum.net", domain.StatusNormal)
				server := miniredis.RunT(t)
				s.Redis = redis.NewClient(&redis.Options{Addr: server.Addr()})
				t.Cleanup(func() { _ = s.Redis.Close() })
				var current Resource
				changed, completingHistory := false, false
				changeIdentity := func() {
					changed, completingHistory = true, true
					if change == "credentials" {
						password := "new-synthetic-app-password"
						require.NoError(t, s.ReplaceCredentials(ctx, resource.ID, nil, nil, &password, nil))
					} else {
						_, err := s.ClaimForValidation(ctx, resource.ID, nil)
						require.NoError(t, err)
					}
					row, err := s.GetResource(ctx, resource.ID, nil)
					require.NoError(t, err)
					require.Greater(t, row.ValidationGeneration, resource.ValidationGeneration)
					if change == "validation" {
						require.Equal(t, resource.CredentialRevision, row.CredentialRevision)
					} else {
						require.Greater(t, row.CredentialRevision, resource.CredentialRevision)
					}
					require.NoError(t, s.ProcessValidation(ctx, daumapp.ValidationTaskPayload{
						ResourceID: row.ID, OwnerUserID: row.OwnerUserID, CredentialRevision: row.CredentialRevision, ValidationGeneration: row.ValidationGeneration,
					}))
					require.NoError(t, s.ProcessHistory(ctx, daumapp.HistoryTaskPayload{
						ResourceID: row.ID, OwnerUserID: row.OwnerUserID, CredentialRevision: row.CredentialRevision, ValidationGeneration: row.ValidationGeneration,
					}))
					row, err = s.GetResource(ctx, resource.ID, nil)
					require.NoError(t, err)
					require.Equal(t, domain.StatusNormal, row.Status)
					current = *row
					require.False(t, server.Exists(daumFetchFailureKey(current)))
					completingHistory = false
				}
				s.Protocol = daumValidationClientStub{fetch: func(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error) {
					if completingHistory || (race == "before_clear" && !changed) {
						return protocol.FetchResult{Complete: true}, nil
					}
					if race == "late_failure" && !changed {
						changeIdentity()
					}
					return protocol.FetchResult{}, &protocol.Failure{Category: "invalid_credentials", SafeMessage: "Synthetic rejection."}
				}}
				s.Redis.AddHook(fetchFailureRaceHook{before: func(command string) {
					triggerRace := (race == "before_increment" && command == "incr") || (race == "before_clear" && command == "del")
					if changed || !triggerRace {
						return
					}
					// Interleave after the database fence, immediately before the old Redis write.
					changeIdentity()
					if race == "before_clear" {
						for range 2 {
							_, err := s.FetchMailbox(ctx, current.ID, current.CredentialRevision, protocol.FetchRequest{})
							require.Error(t, err)
						}
					}
				}})
				_, err := s.FetchMailbox(ctx, resource.ID, resource.CredentialRevision, protocol.FetchRequest{})
				require.Error(t, err)
				require.True(t, changed)
				if race != "before_increment" {
					require.ErrorIs(t, err, domain.ErrInvalidClaim)
				}
				if race != "before_clear" {
					require.False(t, server.Exists(daumFetchFailureKey(current)), "old failures must not count against the current identity")
					for range 2 {
						_, err := s.FetchMailbox(ctx, current.ID, current.CredentialRevision, protocol.FetchRequest{})
						require.Error(t, err)
					}
				}
				count, err := server.Get(daumFetchFailureKey(current))
				require.NoError(t, err)
				require.Equal(t, "2", count, "old success must not clear current failures")
				row, err := s.GetResource(ctx, current.ID, nil)
				require.NoError(t, err)
				require.Equal(t, domain.StatusNormal, row.Status)
				_, err = s.FetchMailbox(ctx, current.ID, current.CredentialRevision, protocol.FetchRequest{})
				require.Error(t, err)
				row, err = s.GetResource(ctx, current.ID, nil)
				require.NoError(t, err)
				require.Equal(t, domain.StatusAbnormal, row.Status, "only the third current failure may mark the account abnormal")
			})
		}
	}
}
