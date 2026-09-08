package infra

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/platform"
	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/stretchr/testify/require"
)

type historyTestProject struct {
	ID         uint `gorm:"primaryKey"`
	Status     string
	LooseMatch bool
}

func (historyTestProject) TableName() string { return "projects" }

type historyTestProduct struct {
	ID                      uint `gorm:"primaryKey"`
	ProjectID               uint
	Type                    string
	Status                  string
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
}

func (historyTestProduct) TableName() string { return "project_products" }

type historyTestRule struct {
	ID        uint `gorm:"primaryKey"`
	ProjectID uint
	RuleType  string
	Pattern   string
	Enabled   bool
}

func (historyTestRule) TableName() string { return "project_mail_rules" }

type historyTestUsage struct {
	ID         uint `gorm:"primaryKey"`
	ResourceID uint
	ProjectID  uint
}

func newHistoryTestService(t *testing.T) *Service {
	t.Helper()
	s, _ := newProtoAsyncTestService(t)
	require.NoError(t, s.DB.AutoMigrate(&historyTestProject{}, &historyTestProduct{}, &historyTestRule{}, &historyTestUsage{}, &ProjectHistoryState{}))
	require.NoError(t, s.DB.Create(&historyTestProject{ID: 10, Status: "listed"}).Error)
	require.NoError(t, s.DB.Create(&historyTestProduct{ID: 20, ProjectID: 10, Type: "proto", Status: "enabled", CodeWindowMinutes: 10, ActivationWindowMinutes: 30, WarrantyMinutes: 60}).Error)
	require.NoError(t, s.DB.Create(&[]historyTestRule{
		{ProjectID: 10, RuleType: "recipient", Pattern: "exact", Enabled: true},
		{ProjectID: 10, RuleType: "sender", Pattern: `^sender@example\.com$`, Enabled: true},
		{ProjectID: 10, RuleType: "subject", Pattern: "Welcome", Enabled: true},
		{ProjectID: 10, RuleType: "body", Pattern: "registered", Enabled: true},
	}).Error)
	s.HistoricalUsage = func(ctx context.Context, matches []HistoricalUsage) error {
		tx, ok := platform.GormTxFromContext(ctx)
		require.True(t, ok, "historical facts must join the resource/checkpoint transaction")
		for _, match := range matches {
			if err := tx.Create(&historyTestUsage{ResourceID: match.ResourceID, ProjectID: match.ProjectID}).Error; err != nil {
				return err
			}
		}
		return nil
	}
	return s
}

func historyTestResource(t *testing.T, s *Service, email, status string) (Resource, protoapp.HistoryTaskPayload) {
	t.Helper()
	id, _, err := s.ImportLine(context.Background(), 7, domain.ImportLine{Email: email, Password: "test-password"})
	require.NoError(t, err)
	require.NoError(t, s.DB.Model(&Resource{}).Where("id = ?", id).Updates(map[string]any{"status": status, "for_sale": true}).Error)
	resource, err := s.GetResource(context.Background(), id, nil)
	require.NoError(t, err)
	return *resource, protoapp.HistoryTaskPayload{ResourceID: id, OwnerUserID: 7, CredentialRevision: resource.CredentialRevision, ValidationGeneration: resource.ValidationGeneration}
}

func historyMessage(id, email string, receivedAt time.Time) proton.Message {
	return proton.Message{ID: id, Sender: proton.Address{Address: "sender@example.com"}, Subject: "Welcome", Body: "Your account is registered.",
		ToList: []proton.Address{{Address: email}}, OriginalToCount: 1, ReceivedAt: receivedAt}
}

func TestProtoHistoryMatchesMainRecipientAndMicrosoftRuleSemantics(t *testing.T) {
	now := time.Now().UTC()
	strict := historyProjectScope{ProjectID: 1, ProductID: 11, Rules: []historyRule{
		{Type: "recipient", Pattern: "exact"}, {Type: "sender", Pattern: `^sender@example\.com$`}, {Type: "subject", Pattern: "Welcome"}, {Type: "body", Pattern: "registered"},
	}}
	loose := historyProjectScope{ProjectID: 2, ProductID: 22, LooseMatch: true, Rules: strict.Rules[:2]}
	a := newHistoryAccumulator(Resource{ID: 9, EmailAddress: "main@proton.me"}, []historyProjectScope{strict, loose}, now)
	first := historyMessage("one", "main@proton.me", now.Add(-time.Hour))
	last := historyMessage("two", "main@proton.me", now)
	last.Subject, last.Body = "Other subject", "No code"
	alias := historyMessage("alias", "main+tag@proton.me", now)
	ccOnly := historyMessage("cc", "elsewhere@proton.me", now)
	ccOnly.CCList = []proton.Address{{Address: "main@proton.me"}}
	duplicateTo := first
	duplicateTo.ID, duplicateTo.OriginalToCount = "multiple", 2
	duplicateTo.ToList = append(duplicateTo.ToList, duplicateTo.ToList[0])
	require.NoError(t, a.add([]proton.Message{first, alias, ccOnly, duplicateTo}))
	require.NoError(t, a.add([]proton.Message{first, last}))
	require.Len(t, a.matches, 2)
	require.Equal(t, 1, a.matches[0].EvidenceCount)
	require.Equal(t, 2, a.matches[1].EvidenceCount)
	require.Equal(t, first.ReceivedAt, a.matches[1].FirstMatchedAt)
	require.Equal(t, last.ReceivedAt, a.matches[1].LastMatchedAt)
	missingBodyRule := strict
	missingBodyRule.Rules = strict.Rules[:3]
	a = newHistoryAccumulator(Resource{ID: 9, EmailAddress: "main@proton.me"}, []historyProjectScope{missingBodyRule}, now)
	require.NoError(t, a.add([]proton.Message{first}))
	require.Empty(t, a.matches)
}

func TestProtoHistorySenderUsesTheSameDisplayFormatAsPickup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		matched bool
	}{
		{"", `^sender@example\.com$`, true},
		{"Security", `^Security <sender@example\.com>$`, true},
		{"Security", `^sender@example\.com$`, false},
		{"", `^<sender@example\.com>$`, false},
	} {
		t.Run(tc.name+tc.pattern, func(t *testing.T) {
			scope := historyProjectScope{ProjectID: 1, ProductID: 2, LooseMatch: true, Rules: []historyRule{
				{Type: "recipient", Pattern: "exact"}, {Type: "sender", Pattern: tc.pattern},
			}}
			now := time.Now().UTC()
			message := historyMessage("sender", "main@proton.me", now)
			message.Sender.Name = tc.name
			a := newHistoryAccumulator(Resource{ID: 3, EmailAddress: "main@proton.me"}, []historyProjectScope{scope}, now)
			require.NoError(t, a.add([]proton.Message{message}))
			require.Equal(t, tc.matched, len(a.matches) == 1)
		})
	}
}

func TestProtoHistoryCommitsFactsAndNormalOnlyAfterCompleteFetch(t *testing.T) {
	s := newHistoryTestService(t)
	resource, task := historyTestResource(t, s, "complete@proton.me", domain.StatusIdentifying)
	fetchCalls := 0
	fetch := func(ctx context.Context, id uint, revision uint64, request proton.FetchRequest) (proton.FetchResult, error) {
		fetchCalls++
		_, inTx := platform.GormTxFromContext(ctx)
		require.False(t, inTx, "network must not hold the database transaction")
		require.True(t, request.FullHistory)
		require.Equal(t, resource.ID, id)
		require.Equal(t, resource.CredentialRevision, revision)
		require.NoError(t, request.OnMessages([]proton.Message{historyMessage("first", resource.EmailAddress, s.Now())}))
		return proton.FetchResult{Complete: true}, nil
	}
	require.NoError(t, s.processHistory(context.Background(), task, fetch))
	stored, err := s.GetResource(context.Background(), resource.ID, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusNormal, stored.Status)
	require.True(t, stored.ForSale)
	var count int64
	require.NoError(t, s.DB.Model(&historyTestUsage{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.ErrorIs(t, s.processHistory(context.Background(), task, fetch), domain.ErrInvalidClaim)
	require.Equal(t, 1, fetchCalls)
}

func TestProtoHistoryPartialFetchNeverWritesUsageOrPromotes(t *testing.T) {
	s := newHistoryTestService(t)
	resource, task := historyTestResource(t, s, "partial@proton.me", domain.StatusIdentifying)
	fetch := func(_ context.Context, _ uint, _ uint64, request proton.FetchRequest) (proton.FetchResult, error) {
		require.NoError(t, request.OnMessages([]proton.Message{historyMessage("one", resource.EmailAddress, s.Now())}))
		return proton.FetchResult{Complete: false}, nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := s.processHistory(context.Background(), task, fetch)
		if attempt < 2 {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
	stored, err := s.GetResource(context.Background(), resource.ID, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusIdentifying, stored.Status)
	require.True(t, stored.ForSale)
	run, err := s.FindMaintenanceRun(context.Background(), resource.ID, task.ValidationGeneration, maintenanceKindHistory)
	require.NoError(t, err)
	require.Equal(t, maintenanceUncertain, run.Status)
	require.Equal(t, 3, run.Attempts)
	var count int64
	require.NoError(t, s.DB.Model(&historyTestUsage{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProtoHistoryRejectsChangesDuringFetchAndRollsBackFailedTrade(t *testing.T) {
	for _, change := range []string{"rules", "credentials", "owner", "trade"} {
		t.Run(change, func(t *testing.T) {
			s := newHistoryTestService(t)
			resource, task := historyTestResource(t, s, "changed@proton.me", domain.StatusIdentifying)
			if change == "trade" {
				write := s.HistoricalUsage
				s.HistoricalUsage = func(ctx context.Context, matches []HistoricalUsage) error {
					require.NoError(t, write(ctx, matches))
					return errors.New("injected trade failure")
				}
			}
			err := s.processHistory(context.Background(), task, func(_ context.Context, _ uint, _ uint64, request proton.FetchRequest) (proton.FetchResult, error) {
				require.NoError(t, request.OnMessages([]proton.Message{historyMessage("one", resource.EmailAddress, s.Now())}))
				switch change {
				case "rules":
					require.NoError(t, s.DB.Model(&historyTestRule{}).Where("rule_type = 'body'").Update("pattern", "new-rules").Error)
				case "credentials":
					require.NoError(t, s.DB.Model(&Resource{}).Where("id = ?", resource.ID).Update("credential_revision", resource.CredentialRevision+1).Error)
				case "owner":
					require.NoError(t, s.DB.Model(&resourceRoot{}).Where("id = ?", resource.ID).Update("owner_user_id", 8).Error)
					require.NoError(t, s.DB.Model(&Resource{}).Where("id = ?", resource.ID).Update("owner_user_id", 8).Error)
				}
				return proton.FetchResult{Complete: true}, nil
			})
			require.Error(t, err)
			var count int64
			require.NoError(t, s.DB.Model(&historyTestUsage{}).Count(&count).Error)
			require.Zero(t, count)
			stored, err := s.GetResource(context.Background(), resource.ID, nil)
			require.NoError(t, err)
			require.Equal(t, domain.StatusIdentifying, stored.Status)
		})
	}
}

func TestProtoHistoryScopesIncludeDelistedProtoOnly(t *testing.T) {
	s := newHistoryTestService(t)
	require.NoError(t, s.DB.Model(&historyTestProject{}).Where("id = 10").Update("status", "delisted").Error)
	require.NoError(t, s.DB.Model(&historyTestProduct{}).Where("id = 20").Update("status", "disabled").Error)
	require.NoError(t, s.DB.Create(&historyTestProduct{ID: 19, ProjectID: 10, Type: "microsoft"}).Error)
	scopes, err := s.historyProjectScopes(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, scopes, 1)
	require.EqualValues(t, 20, scopes[0].ProductID)
	require.NoError(t, s.DB.Model(&historyTestProject{}).Where("id = 10").Update("status", "reviewing").Error)
	scopes, err = s.historyProjectScopes(context.Background(), 0)
	require.NoError(t, err)
	require.Empty(t, scopes)
}

func TestProtoProjectHistoryResumesDurableCursorAndIgnoresDuplicateTask(t *testing.T) {
	s := newHistoryTestService(t)
	first, _ := historyTestResource(t, s, "first@proton.me", domain.StatusNormal)
	second, _ := historyTestResource(t, s, "second@proton.me", domain.StatusDisabled)
	require.NoError(t, s.ScheduleProjectHistory(context.Background(), 10, "history-request"))
	q := &recoveringQueue{fail: true}
	s.Queue = q
	fetchCalls := 0
	fetch := func(_ context.Context, id uint, _ uint64, request proton.FetchRequest) (proton.FetchResult, error) {
		fetchCalls++
		require.Equal(t, first.ID, id)
		require.NoError(t, request.OnMessages([]proton.Message{historyMessage("one", first.EmailAddress, s.Now())}))
		return proton.FetchResult{Complete: true}, nil
	}
	initial := ProjectHistoryTask{ProjectID: 10, Generation: 1}
	require.Error(t, s.processProjectHistory(context.Background(), initial, fetch))
	q.fail = false
	require.NoError(t, s.DispatchProjectHistory(context.Background(), 10))
	var next ProjectHistoryTask
	require.NoError(t, json.Unmarshal(q.tasks[0].Payload(), &next))
	require.Equal(t, first.ID, next.AfterID)
	require.ErrorIs(t, s.processProjectHistory(context.Background(), initial, fetch), domain.ErrInvalidClaim)
	// An account imported later is handled by its own validation/history job,
	// not appended to an already-running project's frozen high-water range.
	_, _ = historyTestResource(t, s, "later@proton.me", domain.StatusNormal)
	require.NoError(t, s.processProjectHistory(context.Background(), next, fetch))
	require.NoError(t, json.Unmarshal(q.tasks[1].Payload(), &next))
	require.Equal(t, second.ID, next.AfterID)
	require.NoError(t, s.processProjectHistory(context.Background(), next, fetch))
	var state ProjectHistoryState
	require.NoError(t, s.DB.First(&state, 10).Error)
	require.Equal(t, "normal", state.Status)
	require.Equal(t, 2, state.ScannedCount)
	require.Equal(t, 1, state.MatchedCount)
	require.Equal(t, 1, state.SkippedCount)
	require.Equal(t, second.ID, state.ThroughID)
	require.Equal(t, 1, fetchCalls)
}

func TestProtoProjectHistoryFailureDoesNotAdvanceCursorOrWritePartialFacts(t *testing.T) {
	s := newHistoryTestService(t)
	resource, _ := historyTestResource(t, s, "partial-project@proton.me", domain.StatusNormal)
	require.NoError(t, s.ScheduleProjectHistory(context.Background(), 10, "history-request"))
	s.Queue = &protoQueueStub{}
	task := ProjectHistoryTask{ProjectID: 10, Generation: 1}
	err := s.processProjectHistory(context.Background(), task, func(_ context.Context, _ uint, _ uint64, request proton.FetchRequest) (proton.FetchResult, error) {
		require.NoError(t, request.OnMessages([]proton.Message{historyMessage("one", resource.EmailAddress, s.Now())}))
		return proton.FetchResult{}, &proton.Failure{SafeMessage: "Temporary failure.", Retryable: true}
	})
	require.Error(t, err)
	var state ProjectHistoryState
	require.NoError(t, s.DB.First(&state, 10).Error)
	require.Equal(t, "pending", state.Status)
	require.Zero(t, state.AfterID)
	require.Zero(t, state.ScannedCount)
	var count int64
	require.NoError(t, s.DB.Model(&historyTestUsage{}).Count(&count).Error)
	require.Zero(t, count)
	// Changing the project while the retry fetches prevents its old generation
	// from committing either facts or cursor progress.
	err = s.processProjectHistory(context.Background(), task, func(_ context.Context, _ uint, _ uint64, request proton.FetchRequest) (proton.FetchResult, error) {
		require.NoError(t, request.OnMessages([]proton.Message{historyMessage("two", resource.EmailAddress, s.Now())}))
		require.NoError(t, s.ScheduleProjectHistory(context.Background(), 10, "new-generation"))
		return proton.FetchResult{Complete: true}, nil
	})
	require.ErrorIs(t, err, domain.ErrInvalidClaim)
	require.NoError(t, s.DB.Model(&historyTestUsage{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProtoHistoryClaimsFenceDuplicateAndExpiredWorkers(t *testing.T) {
	for _, scanner := range []string{"resource", "project"} {
		for _, oldOutcome := range []string{"success", "failure"} {
			t.Run(scanner+"/"+oldOutcome, func(t *testing.T) {
				s := newHistoryTestService(t)
				resource, task := historyTestResource(t, s, "claims@proton.me", domain.StatusIdentifying)
				now := s.Now().Truncate(time.Millisecond)
				s.Now = func() time.Time { return now }
				run := func(fetch historyMailboxFetch) error { return s.processHistory(context.Background(), task, fetch) }
				if scanner == "project" {
					require.NoError(t, s.ScheduleProjectHistory(context.Background(), 10, "claim-test"))
					s.Queue = &protoQueueStub{}
					run = func(fetch historyMailboxFetch) error {
						return s.processProjectHistory(context.Background(), ProjectHistoryTask{ProjectID: 10, Generation: 1}, fetch)
					}
				}
				err := run(func(_ context.Context, _ uint, _ uint64, request proton.FetchRequest) (proton.FetchResult, error) {
					require.ErrorIs(t, run(func(context.Context, uint, uint64, proton.FetchRequest) (proton.FetchResult, error) {
						t.Fatal("active duplicate reached the mailbox")
						return proton.FetchResult{}, nil
					}), domain.ErrInvalidClaim)
					now = now.Add(17 * time.Minute)
					require.Error(t, run(func(context.Context, uint, uint64, proton.FetchRequest) (proton.FetchResult, error) {
						return proton.FetchResult{}, &proton.Failure{Category: "request", SafeMessage: "New attempt failed.", Retryable: true}
					}))
					if oldOutcome == "failure" {
						return proton.FetchResult{}, &proton.Failure{Category: "decryption", SafeMessage: "Old attempt failed."}
					}
					require.NoError(t, request.OnMessages([]proton.Message{historyMessage("old", resource.EmailAddress, now)}))
					return proton.FetchResult{Complete: true}, nil
				})
				require.ErrorIs(t, err, domain.ErrInvalidClaim)
				if scanner == "resource" {
					stored, err := s.FindMaintenanceRun(context.Background(), resource.ID, task.ValidationGeneration, maintenanceKindHistory)
					require.NoError(t, err)
					require.Equal(t, 2, stored.Attempts)
					require.Equal(t, maintenanceQueued, stored.Status)
					require.Equal(t, "New attempt failed.", stored.LastSafeError)
				} else {
					var state ProjectHistoryState
					require.NoError(t, s.DB.First(&state, 10).Error)
					require.Equal(t, "pending", state.Status)
					require.Equal(t, 1, state.Failures)
					require.Equal(t, "New attempt failed.", state.LastSafeError)
					require.Zero(t, state.AfterID)
				}
				var count int64
				require.NoError(t, s.DB.Model(&historyTestUsage{}).Count(&count).Error)
				require.Zero(t, count)
			})
		}
	}
}

func TestProtoProjectHistoryDoesNotSkipUndecryptedOrUnverifiedHistory(t *testing.T) {
	for _, category := range []string{"decryption", "action_required", "protocol", "session_revoked"} {
		t.Run(category, func(t *testing.T) {
			s := newHistoryTestService(t)
			resource, _ := historyTestResource(t, s, "unread@proton.me", domain.StatusNormal)
			require.NoError(t, s.ScheduleProjectHistory(context.Background(), 10, "history-request"))
			s.Queue = &protoQueueStub{}
			err := s.processProjectHistory(context.Background(), ProjectHistoryTask{ProjectID: 10, Generation: 1}, func(_ context.Context, _ uint, _ uint64, request proton.FetchRequest) (proton.FetchResult, error) {
				require.NoError(t, request.OnMessages([]proton.Message{historyMessage("partial", resource.EmailAddress, s.Now())}))
				return proton.FetchResult{}, &proton.Failure{Category: category, SafeMessage: "History could not be read."}
			})
			require.NoError(t, err)
			var state ProjectHistoryState
			require.NoError(t, s.DB.First(&state, 10).Error)
			if category == "session_revoked" {
				require.Equal(t, "pending", state.Status)
				require.Equal(t, resource.ID, state.AfterID)
				require.Equal(t, 1, state.SkippedCount)
			} else {
				require.Equal(t, "uncertain", state.Status)
				require.Zero(t, state.AfterID)
				require.Zero(t, state.ScannedCount)
				require.Zero(t, state.SkippedCount)
			}
			var count int64
			require.NoError(t, s.DB.Model(&historyTestUsage{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestProtoProjectHistoryResetsFailuresAfterEachCompletedResource(t *testing.T) {
	s := newHistoryTestService(t)
	for _, email := range []string{"one@proton.me", "two@proton.me", "three@proton.me"} {
		_, _ = historyTestResource(t, s, email, domain.StatusNormal)
	}
	require.NoError(t, s.ScheduleProjectHistory(context.Background(), 10, "history-request"))
	q := &protoQueueStub{}
	s.Queue = q
	task := ProjectHistoryTask{ProjectID: 10, Generation: 1}
	for resource := 0; resource < 3; resource++ {
		require.Error(t, s.processProjectHistory(context.Background(), task, func(context.Context, uint, uint64, proton.FetchRequest) (proton.FetchResult, error) {
			return proton.FetchResult{}, &proton.Failure{Category: "request", SafeMessage: "Temporary fetch error.", Retryable: true}
		}))
		require.NoError(t, s.processProjectHistory(context.Background(), task, func(context.Context, uint, uint64, proton.FetchRequest) (proton.FetchResult, error) {
			return proton.FetchResult{Complete: true}, nil
		}))
		var state ProjectHistoryState
		require.NoError(t, s.DB.First(&state, 10).Error)
		require.Equal(t, "pending", state.Status)
		require.Zero(t, state.Failures)
		require.NoError(t, json.Unmarshal(q.tasks[len(q.tasks)-1].Payload(), &task))
	}
}
