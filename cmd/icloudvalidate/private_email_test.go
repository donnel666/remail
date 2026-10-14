package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/icloud"
	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func privateEmailFixture(email, id string, active bool) icloud.ApplePrivateEmail {
	return icloud.ApplePrivateEmail{EmailAddress: email, ID: id, Active: &active, ForwardToEmail: "relay@example.com"}
}

func privateEmailDebugger(t *testing.T, provider *scriptedAppleProvider) *debugger {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec("CREATE TABLE icloud_resources (id INTEGER PRIMARY KEY, primary_email TEXT)").Error)
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cp := accountCheckpoint{Fingerprint: "fixture", ManageAuthenticated: true, ManageSessionExpiresAt: time.Now().Add(time.Hour), Session: json.RawMessage(`{"mode":"manage"}`), Committed: true, ResourceID: 42}
	state := checkpointFile{Version: 1, Accounts: map[string]accountCheckpoint{"owner@example.com": cp}}
	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, saveCheckpoint(path, state))
	return &debugger{ctx: context.Background(), input: accountInput{Email: "owner@example.com"},
		runtime: &runtime{apple: provider, icloud: icloud.NewService(db, nil, nil, rdb)}, stdout: &bytes.Buffer{},
		checkpoint: &cp, state: &state, statePath: path, stateKey: "owner@example.com", session: cp.Session}
}

func replacementSteps() []appleProviderStep {
	other := privateEmailFixture("other@icloud.com", "other-id", true)
	return []appleProviderStep{
		{response: icloud.AppleOnboardingResponse{Next: "ready"}},
		{response: icloud.AppleOnboardingResponse{PrivateEmails: []icloud.ApplePrivateEmail{privateEmailFixture("old@icloud.com", "old-id", true), other}}},
		{response: icloud.AppleOnboardingResponse{Next: "stop"}},
		{response: icloud.AppleOnboardingResponse{PrivateEmails: []icloud.ApplePrivateEmail{privateEmailFixture("old@icloud.com", "old-id", false), other}}},
		{response: icloud.AppleOnboardingResponse{Next: "remove"}},
		{response: icloud.AppleOnboardingResponse{PrivateEmails: []icloud.ApplePrivateEmail{other}}},
		{response: icloud.AppleOnboardingResponse{PrivateEmailAddress: "new@icloud.com"}},
		{response: icloud.AppleOnboardingResponse{PrivateEmailAddress: "new@icloud.com"}},
		{response: icloud.AppleOnboardingResponse{PrivateEmails: []icloud.ApplePrivateEmail{privateEmailFixture("new@icloud.com", "new-id", true), other}}},
	}
}

func TestPrivateEmailReplacementDeletesOnlyTargetAndCreatesOnce(t *testing.T) {
	provider := &scriptedAppleProvider{steps: replacementSteps()}
	d := privateEmailDebugger(t, provider)
	config := options{replacePrivateEmail: "old@icloud.com", ownerUserID: 1}
	require.NoError(t, d.run(nil, config))
	want := []string{icloud.AppleOnboardingFetchManage, icloud.AppleOnboardingListPrivateEmails, icloud.AppleOnboardingStopPrivateEmail,
		icloud.AppleOnboardingListPrivateEmails, icloud.AppleOnboardingRemovePrivateEmail, icloud.AppleOnboardingListPrivateEmails,
		icloud.AppleOnboardingGeneratePrivateEmail, icloud.AppleOnboardingCompletePrivateEmail, icloud.AppleOnboardingListPrivateEmails}
	require.Len(t, provider.requests, len(want))
	for i, operation := range want {
		require.Equal(t, operation, provider.requests[i].Operation)
	}
	require.True(t, provider.requests[0].SkipPrivateAlias)
	for _, i := range []int{2, 4} {
		require.Equal(t, "old-id", provider.requests[i].PrivateEmailID)
		require.Equal(t, "old@icloud.com", provider.requests[i].PrivateEmailAddress)
	}
	require.Equal(t, "new@icloud.com", provider.requests[7].PrivateEmailAddress)
	state, err := loadCheckpoint(d.statePath)
	require.NoError(t, err)
	require.True(t, state.Accounts[d.stateKey].PrivateEmailReplacement.Completed)
	require.NoError(t, d.run(nil, config))
	require.Len(t, provider.requests, len(want), "a completed checkpoint must not create again")
}

func TestPrivateEmailReplacementRecoversLostMutationResponses(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		failedAt, remoteAfter int
	}{
		{"stop", 2, 3}, {"remove", 4, 5}, {"create", 7, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := replacementSteps()
			lost := errors.New("response lost after sending request")
			firstSteps := append([]appleProviderStep(nil), steps[:tc.failedAt]...)
			first := &scriptedAppleProvider{steps: append(firstSteps, appleProviderStep{err: lost})}
			d := privateEmailDebugger(t, first)
			config := options{replacePrivateEmail: "old@icloud.com", ownerUserID: 1}
			require.ErrorIs(t, d.run(nil, config), lost)
			state, err := loadCheckpoint(d.statePath)
			require.NoError(t, err)
			cp := state.Accounts[d.stateKey]
			d.state, d.checkpoint, d.session = &state, &cp, cp.Session
			second := &scriptedAppleProvider{steps: append([]appleProviderStep{{response: icloud.AppleOnboardingResponse{Next: "ready"}}}, steps[tc.remoteAfter:]...)}
			d.runtime.apple = second
			require.NoError(t, d.run(nil, config))
			for _, request := range second.requests {
				require.NotEqual(t, first.requests[tc.failedAt].Operation, request.Operation, "an applied mutation must not be replayed")
			}
		})
	}
}

func TestPrivateEmailReplacementDoesNotResendUnconfirmedCreate(t *testing.T) {
	steps := replacementSteps()
	retryAt := time.Now().Add(time.Minute)
	first := &scriptedAppleProvider{steps: append(steps[:7], appleProviderStep{err: &icloud.AppleOnboardingError{
		Category: "apple_unavailable", HTTPStatus: 500, ProxyRetryPending: true, RetryAt: &retryAt,
	}})}
	d := privateEmailDebugger(t, first)
	config := options{replacePrivateEmail: "old@icloud.com", ownerUserID: 1}
	require.Error(t, d.run(nil, config))
	second := &scriptedAppleProvider{steps: []appleProviderStep{steps[0], steps[5]}}
	d.runtime.apple = second
	require.ErrorContains(t, d.run(nil, config), "creation is unconfirmed")
	require.Len(t, second.requests, 2)
	require.Equal(t, icloud.AppleOnboardingListPrivateEmails, second.requests[1].Operation)
}

func TestPrivateEmailReplacementRetriesRejectedCandidateWithoutGeneratingAnother(t *testing.T) {
	steps := replacementSteps()
	firstSteps := append([]appleProviderStep(nil), steps[:7]...)
	provider := &scriptedAppleProvider{steps: append(firstSteps, appleProviderStep{err: &icloud.AppleOnboardingError{HTTPStatus: 400, Category: "alias_create_failed"}})}
	d := privateEmailDebugger(t, provider)
	config := options{replacePrivateEmail: "old@icloud.com", ownerUserID: 1}
	require.Error(t, d.run(nil, config))
	require.False(t, d.checkpoint.PrivateEmailReplacement.CreateStarted)
	second := &scriptedAppleProvider{steps: []appleProviderStep{steps[0], steps[5], steps[7], steps[8]}}
	d.runtime.apple = second
	require.NoError(t, d.run(nil, config))
	require.Len(t, second.requests, 4)
	require.Equal(t, icloud.AppleOnboardingCompletePrivateEmail, second.requests[2].Operation)
	require.Equal(t, "new@icloud.com", second.requests[2].PrivateEmailAddress)
}

func TestPrivateEmailReplacementWaitsForConfirmedDeletion(t *testing.T) {
	for _, tc := range []struct {
		name                string
		listAt, unchangedAt int
	}{
		{"stop unconfirmed", 3, 1}, {"remove unconfirmed", 5, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := replacementSteps()
			steps[tc.listAt] = steps[tc.unchangedAt]
			provider := &scriptedAppleProvider{steps: steps[:tc.listAt+1]}
			d := privateEmailDebugger(t, provider)
			require.ErrorContains(t, d.run(nil, options{replacePrivateEmail: "old@icloud.com", ownerUserID: 1}), "not confirmed")
			require.Len(t, provider.requests, tc.listAt+1)
			require.Empty(t, d.checkpoint.PrivateEmailReplacement.Candidate)
		})
	}
}

func TestPrivateEmailReplacementRejectsMissingTargetBeforeCreation(t *testing.T) {
	steps := replacementSteps()
	provider := &scriptedAppleProvider{steps: []appleProviderStep{steps[0], steps[5]}}
	d := privateEmailDebugger(t, provider)
	require.ErrorContains(t, d.run(nil, options{replacePrivateEmail: "old@icloud.com", ownerUserID: 1}), "does not belong")
	require.Len(t, provider.requests, 2)
}

func TestPrivateEmailReplacementOptions(t *testing.T) {
	for _, args := range [][]string{
		{"-replace-private-email", "old@icloud.com"},
		{"-replace-private-email", " OLD@ICLOUD.COM "},
	} {
		config, err := parseOptions(args, &bytes.Buffer{})
		require.NoError(t, err)
		require.Equal(t, "old@icloud.com", config.replacePrivateEmail)
	}
	for _, args := range [][]string{
		{"-replace-private-email", "*"}, {"-replace-private-email", "old@example.com"},
		{"-replace-private-email", "old@icloud.com,new@icloud.com"},
		{"-replace-private-email", "old@icloud.com", "-forward-to", "another@example.com"},
		{"-replace-private-email", "old@icloud.com", "-legacy-commit"},
		{"-replace-private-email", "old@icloud.com", "-reset-state"},
	} {
		_, err := parseOptions(args, &bytes.Buffer{})
		require.Error(t, err)
	}
}
