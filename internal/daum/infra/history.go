package infra

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	daumapp "github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
)

func historyMailboxTimeout() time.Duration {
	return min(runtimeconfig.Duration("imap_full_history_timeout_minutes", 15*time.Minute, time.Minute, 1), 2*time.Hour)
}
func historyRunLease() time.Duration { return daumapp.HistoryTaskTimeout() + time.Minute }

var errHistoryScopeChanged = errors.New("daum: project history scope changed")

// HistoricalUsage crosses only the existing Trade boundary. History does not
// create orders, wallet entries, allocations, or provider aliases itself.
type HistoricalUsage struct {
	ResourceID              uint
	ProjectID               uint
	ProductID               uint
	Email                   string
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	FirstMatchedAt          time.Time
	LastMatchedAt           time.Time
	EvidenceCount           int
}

type historyRule struct {
	Type    string
	Pattern string
}

type historyProjectScope struct {
	ProjectID               uint
	ProductID               uint
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	LooseMatch              bool
	Rules                   []historyRule
}

type historyMailboxFetch func(context.Context, uint, uint64, protocol.FetchRequest) (protocol.FetchResult, error)

// These project and rule queries are an independent copy of the Microsoft
// history scope: delisted projects and disabled products still retain usage.
func (s *Service) historyProjectScopes(ctx context.Context, projectID uint) ([]historyProjectScope, error) {
	var rows []struct {
		ProjectID               uint
		ProductID               uint
		CodeWindowMinutes       int
		ActivationWindowMinutes int
		WarrantyMinutes         int
		LooseMatch              bool
		RuleType                string
		Pattern                 string
	}
	query := s.dbFor(ctx).Table("projects AS p").
		Select("p.id AS project_id, pp.id AS product_id, pp.code_window_minutes, pp.activation_window_minutes, pp.warranty_minutes, p.loose_match, pmr.rule_type, pmr.pattern").
		Joins(`JOIN project_products AS pp ON pp.project_id = p.id AND pp.type = 'daum'
			AND pp.id = (SELECT MIN(candidate.id) FROM project_products AS candidate WHERE candidate.project_id = p.id AND candidate.type = 'daum')`).
		Joins("JOIN project_mail_rules AS pmr ON pmr.project_id = p.id AND pmr.enabled = 1").
		Where("p.status IN ?", []string{"listed", "delisted"})
	if projectID != 0 {
		query = query.Where("p.id = ?", projectID)
	}
	if err := query.Order("p.id ASC, pmr.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	scopes := make([]historyProjectScope, 0)
	for _, row := range rows {
		if len(scopes) == 0 || scopes[len(scopes)-1].ProjectID != row.ProjectID {
			scopes = append(scopes, historyProjectScope{
				ProjectID: row.ProjectID, ProductID: row.ProductID, LooseMatch: row.LooseMatch,
				CodeWindowMinutes: row.CodeWindowMinutes, ActivationWindowMinutes: row.ActivationWindowMinutes,
				WarrantyMinutes: row.WarrantyMinutes,
			})
		}
		last := &scopes[len(scopes)-1]
		last.Rules = append(last.Rules, historyRule{Type: row.RuleType, Pattern: row.Pattern})
	}
	return scopes, nil
}

func (s *Service) ProcessHistory(ctx context.Context, task daumapp.HistoryTaskPayload) error {
	return s.processHistory(ctx, task, s.FetchMailbox)
}

func (s *Service) processHistory(ctx context.Context, task daumapp.HistoryTaskPayload, fetch historyMailboxFetch) error {
	if task.ResourceID == 0 || task.OwnerUserID == 0 || task.ValidationGeneration == 0 || task.CredentialRevision == 0 {
		return domain.ErrInvalidResource
	}
	if task.RequestID != "" {
		ctx = context.WithValue(ctx, platform.RequestIDKey, task.RequestID)
	}
	var resource Resource
	var runID uint64
	var attempt int
	err := s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, task.ResourceID, &task.OwnerUserID)
		if err != nil {
			return err
		}
		if !historyTaskMatches(row, task) {
			return domain.ErrInvalidClaim
		}
		run, err := ensureMaintenanceRunTx(ctx, tx, row.ID, row.ValidationGeneration, row.CredentialRevision, maintenanceKindHistory, task.RequestID, s.Now().UTC())
		if err != nil {
			return err
		}
		if (task.MaintenanceRunID != 0 && run.ID != task.MaintenanceRunID) || (run.Status != maintenanceQueued && run.Status != maintenanceRunning) {
			return domain.ErrInvalidClaim
		}
		now := s.Now().UTC()
		if run.Status == maintenanceRunning && run.StartedAt != nil && run.StartedAt.Add(historyRunLease()).After(now) {
			return domain.ErrInvalidClaim
		}
		resource, runID = *row, run.ID
		attempt = run.Attempts + 1
		return tx.Model(&MaintenanceRun{}).Where("id = ?", run.ID).Updates(map[string]any{
			"status": maintenanceRunning, "attempts": attempt, "started_at": now, "updated_at": now,
		}).Error
	})
	if err != nil {
		return err
	}
	scopes, err := s.historyProjectScopes(ctx, 0)
	var matches []HistoricalUsage
	if err == nil {
		matches, err = s.fetchHistoryMatches(ctx, resource, scopes, fetch)
	}
	if err == nil {
		err = s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
			row, err := lockResource(tx, task.ResourceID, &task.OwnerUserID)
			if err != nil {
				return err
			}
			if !historyTaskMatches(row, task) || row.EmailAddress != resource.EmailAddress {
				return domain.ErrInvalidClaim
			}
			var active int64
			if err := tx.Model(&MaintenanceRun{}).Where("id = ? AND status = ? AND attempts = ?", runID, maintenanceRunning, attempt).Count(&active).Error; err != nil {
				return err
			}
			if active != 1 {
				return domain.ErrInvalidClaim
			}
			current, err := s.historyProjectScopes(ctx, 0)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(scopes, current) {
				return errHistoryScopeChanged
			}
			if err := s.commitHistoricalUsage(ctx, matches); err != nil {
				return err
			}
			return s.CompleteHistorySuccess(ctx, row.ID, row.ValidationGeneration)
		})
	}
	if err == nil || errors.Is(err, domain.ErrInvalidClaim) || errors.Is(err, domain.ErrResourceMissing) {
		return err
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if maintenanceDeferred(err) {
		return s.deferBusyMaintenance(cleanup, task.ResourceID, task.ValidationGeneration, task.CredentialRevision, runID, attempt, maintenanceKindHistory)
	}
	terminal, recordErr := s.recordHistoryFailure(cleanup, task, runID, attempt, err)
	if recordErr != nil {
		return errors.Join(err, recordErr)
	}
	if terminal {
		return nil
	}
	return err
}

func historyTaskMatches(row *Resource, task daumapp.HistoryTaskPayload) bool {
	return row.Status == domain.StatusIdentifying && row.ValidationGeneration == task.ValidationGeneration && row.CredentialRevision == task.CredentialRevision
}

func (s *Service) commitHistoricalUsage(ctx context.Context, matches []HistoricalUsage) error {
	if len(matches) == 0 {
		return nil
	}
	if s.HistoricalUsage == nil {
		return domain.ErrDependency
	}
	return s.HistoricalUsage(ctx, matches)
}

func (s *Service) fetchHistoryMatches(ctx context.Context, resource Resource, scopes []historyProjectScope, fetch historyMailboxFetch) ([]HistoricalUsage, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	accumulator := newHistoryAccumulator(resource, scopes, s.Now().UTC())
	fetchCtx, cancel := context.WithTimeout(ctx, historyMailboxTimeout())
	defer cancel()
	result, err := fetch(fetchCtx, resource.ID, resource.CredentialRevision, protocol.FetchRequest{
		FullHistory: true, OnMessages: accumulator.add, OnReset: accumulator.reset,
	})
	if err != nil {
		return nil, err
	}
	if !result.Complete {
		return nil, &protocol.Failure{Category: "history_incomplete", SafeMessage: "Daum mailbox history scan did not complete.", Retryable: true}
	}
	if err := accumulator.add(result.Messages); err != nil {
		return nil, err
	}
	return accumulator.matches, nil
}

func (s *Service) recordHistoryFailure(ctx context.Context, task daumapp.HistoryTaskPayload, runID uint64, attempt int, cause error) (bool, error) {
	terminal := false
	err := s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, task.ResourceID, &task.OwnerUserID)
		if err != nil {
			return err
		}
		if !historyTaskMatches(row, task) {
			return domain.ErrInvalidClaim
		}
		var run MaintenanceRun
		if err := tx.Where("id = ? AND status = ? AND attempts = ?", runID, maintenanceRunning, attempt).Take(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrInvalidClaim
			}
			return err
		}
		var failure *protocol.Failure
		terminal = errors.As(cause, &failure) && (!failure.Retryable || run.Attempts >= run.MaxAttempts)
		now := s.Now().UTC()
		updates := map[string]any{"status": maintenanceQueued, "last_safe_error": safeHistoryError(cause), "updated_at": now}
		if terminal {
			updates["status"], updates["finished_at"] = maintenanceUncertain, now
		}
		if err := tx.Model(&MaintenanceRun{}).Where("id = ?", runID).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Model(&Resource{}).Where("id = ?", row.ID).Updates(map[string]any{
			"last_safe_error": safeHistoryError(cause), "version": row.Version + 1, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return bumpRoot(tx, row.ID, now)
	})
	return terminal, err
}

func safeHistoryError(err error) string {
	var failure *protocol.Failure
	if errors.As(err, &failure) && strings.TrimSpace(failure.SafeMessage) != "" {
		return string([]rune(failure.SafeMessage)[:min(len([]rune(failure.SafeMessage)), 500)])
	}
	if errors.Is(err, errHistoryScopeChanged) {
		return "Daum project history rules changed while mail was being fetched."
	}
	return "Daum mailbox history is temporarily unavailable."
}

type compiledHistoryScope struct {
	scope historyProjectScope
	exact bool
	rules map[string][]*regexp.Regexp
}

type historyAccumulator struct {
	resource Resource
	scopes   []compiledHistoryScope
	scanned  time.Time
	matches  []HistoricalUsage
	index    map[string]int
	seen     map[string]bool
}

func newHistoryAccumulator(resource Resource, scopes []historyProjectScope, scannedAt time.Time) *historyAccumulator {
	a := &historyAccumulator{resource: resource, scanned: scannedAt, index: make(map[string]int), seen: make(map[string]bool)}
	for _, scope := range scopes {
		compiled := compiledHistoryScope{scope: scope, rules: make(map[string][]*regexp.Regexp)}
		for _, rule := range scope.Rules {
			pattern := strings.TrimSpace(rule.Pattern)
			if rule.Type == "recipient" {
				compiled.exact = compiled.exact || strings.EqualFold(pattern, "exact")
				continue
			}
			if pattern != "" {
				if re, err := regexp.Compile(pattern); err == nil {
					compiled.rules[rule.Type] = append(compiled.rules[rule.Type], re)
				}
			}
		}
		a.scopes = append(a.scopes, compiled)
	}
	return a
}

func (a *historyAccumulator) reset() { a.matches = nil; clear(a.index); clear(a.seen) }

func (a *historyAccumulator) add(messages []protocol.Message) error {
	for _, message := range messages {
		if message.OriginalToCount != 1 || len(message.ToList) != 1 {
			continue
		}
		recipient := strings.ToLower(strings.TrimSpace(message.ToList[0].Address))
		if canonical, ok := CanonicalEmail(recipient); !ok || canonical != a.resource.EmailAddress {
			continue
		}
		if message.ID == "" {
			return &protocol.Failure{Category: "history_incomplete", SafeMessage: "Daum history message identity is missing.", Retryable: true}
		}
		if a.seen[message.ID] {
			continue
		}
		a.seen[message.ID] = true
		for _, compiled := range a.scopes {
			if !compiled.exact || !matchesHistoryRule(compiled.rules["sender"], FormatSender(message.Sender)) {
				continue
			}
			if !compiled.scope.LooseMatch && (!matchesHistoryRule(compiled.rules["subject"], strings.TrimSpace(message.Subject)) || !matchesHistoryRule(compiled.rules["body"], message.Body)) {
				continue
			}
			receivedAt := message.ReceivedAt.UTC()
			if receivedAt.IsZero() {
				receivedAt = a.scanned
			}
			key := fmt.Sprintf("%d:%s", compiled.scope.ProjectID, recipient)
			index, exists := a.index[key]
			if !exists {
				index = len(a.matches)
				a.index[key] = index
				a.matches = append(a.matches, HistoricalUsage{
					ResourceID: a.resource.ID, ProjectID: compiled.scope.ProjectID, ProductID: compiled.scope.ProductID, Email: recipient,
					CodeWindowMinutes: compiled.scope.CodeWindowMinutes, ActivationWindowMinutes: compiled.scope.ActivationWindowMinutes,
					WarrantyMinutes: compiled.scope.WarrantyMinutes, FirstMatchedAt: receivedAt, LastMatchedAt: receivedAt,
				})
			}
			match := &a.matches[index]
			match.EvidenceCount++
			if receivedAt.Before(match.FirstMatchedAt) {
				match.FirstMatchedAt = receivedAt
			}
			if receivedAt.After(match.LastMatchedAt) {
				match.LastMatchedAt = receivedAt
			}
		}
	}
	return nil
}

func matchesHistoryRule(rules []*regexp.Regexp, value string) bool {
	for _, rule := range rules {
		if rule.MatchString(value) {
			return true
		}
	}
	return false
}
