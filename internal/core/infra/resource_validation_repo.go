package infra

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
)

type ResourceValidationRepo struct {
	db                     *gorm.DB
	operationLogs          *governanceinfra.OperationLogRepo
	microsoftBindingCommit coreapp.MicrosoftValidationBindingCommitPort
}

const resourceValidationInsertSize = 1000

func NewResourceValidationRepo(db *gorm.DB) *ResourceValidationRepo {
	return &ResourceValidationRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
	}
}

// SetMicrosoftValidationBindingCommitPort installs the MailTransport-owned
// writer used to commit recovery-mailbox facts. The writer is called only from
// caller-owned, fenced validation progress/result transactions.
func (r *ResourceValidationRepo) SetMicrosoftValidationBindingCommitPort(port coreapp.MicrosoftValidationBindingCommitPort) {
	if r != nil {
		r.microsoftBindingCommit = port
	}
}

func validationBatchIDPage(values []uint, afterID uint, limit int) []uint {
	ids := append([]uint(nil), values...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make([]uint, 0, min(len(ids), limit))
	var previous uint
	for _, id := range ids {
		if id == 0 || id == previous || id <= afterID {
			continue
		}
		previous = id
		result = append(result, id)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}

func (r *ResourceValidationRepo) commitMicrosoftValidationBindingTx(
	ctx context.Context,
	tx *gorm.DB,
	root *EmailResourceModel,
	ms *MicrosoftResourceModel,
	result coreapp.MicrosoftValidationResult,
) (bool, error) {
	if result.RecoveredBinding == nil && result.BindingObservation == nil {
		return false, nil
	}
	if r.microsoftBindingCommit == nil || root == nil || ms == nil {
		return false, domain.ErrResourceDependency
	}
	changed, err := r.microsoftBindingCommit.CommitValidationBinding(
		platform.WithGormTx(ctx, tx.WithContext(ctx)),
		coreapp.MicrosoftValidationBindingCommand{
			ResourceID:         root.ID,
			OwnerUserID:        root.OwnerUserID,
			AccountEmail:       ms.EmailAddress,
			RecoveredBinding:   result.RecoveredBinding,
			BindingObservation: result.BindingObservation,
		},
	)
	if err != nil {
		return false, fmt.Errorf("commit microsoft validation binding: %w", err)
	}
	return changed, nil
}

func (r *ResourceValidationRepo) commitMicrosoftValidationBindingWithSavepointTx(
	ctx context.Context,
	tx *gorm.DB,
	root *EmailResourceModel,
	ms *MicrosoftResourceModel,
	result coreapp.MicrosoftValidationResult,
) (bool, error) {
	if result.RecoveredBinding == nil && result.BindingObservation == nil {
		return false, nil
	}
	const savepoint = "microsoft_validation_binding"
	if err := tx.SavePoint(savepoint).Error; err != nil {
		return false, fmt.Errorf("create microsoft validation binding savepoint: %w", err)
	}
	changed, err := r.commitMicrosoftValidationBindingTx(ctx, tx, root, ms, result)
	if err == nil {
		return changed, nil
	}
	if rollbackErr := tx.RollbackTo(savepoint).Error; rollbackErr != nil {
		return false, errors.Join(err, fmt.Errorf("rollback microsoft validation binding savepoint: %w", rollbackErr))
	}
	if errors.Is(err, coreapp.ErrValidationResultStale) {
		return false, err
	}
	// Auxiliary-mailbox persistence is supplementary once the current OAuth
	// credential is authoritative.  A clean savepoint rollback prevents a
	// partial Binding write while allowing validation progress to commit.
	return false, nil
}

func bumpResourceVersionTx(tx *gorm.DB, resourceID uint, now time.Time) error {
	result := tx.Model(&EmailResourceModel{}).
		Where("id = ?", resourceID).
		Updates(map[string]any{
			"version":    gorm.Expr("version + 1"),
			"updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("advance resource version: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrResourceNotFound
	}
	return nil
}

type validationCandidateRow struct {
	ID                 uint
	ResourceType       string `gorm:"column:resource_type"`
	OwnerUserID        uint   `gorm:"column:owner_user_id"`
	CredentialRevision uint64 `gorm:"column:credential_revision"`
	MicrosoftStatus    string `gorm:"column:microsoft_status"`
	DomainStatus       string `gorm:"column:domain_status"`
}

// Explicit selections skip resources that are unavailable or outside the
// caller's ownership instead of letting one stale ID poison the whole batch.
func selectAvailableValidationCandidatesByIDs(ctx context.Context, tx *gorm.DB, ownerUserID uint, ids []uint, expectedType domain.ResourceType, allowBinding bool, adminScope bool) ([]validationCandidateRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []validationCandidateRow
	query := tx.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id, er.type AS resource_type, er.owner_user_id, COALESCE(ms.credential_revision, 0) AS credential_revision, COALESCE(ms.status, '') AS microsoft_status, COALESCE(dr.status, '') AS domain_status").
		Joins("LEFT JOIN microsoft_resources AS ms ON ms.id = er.id AND er.type = ?", string(domain.ResourceTypeMicrosoft)).
		Joins("LEFT JOIN domain_resources AS dr ON dr.id = er.id AND er.type = ?", string(domain.ResourceTypeDomain)).
		Where("er.id IN ?", ids).
		Order("er.id ASC")
	if !adminScope {
		query = query.Where("er.owner_user_id = ?", ownerUserID)
	} else if domain.IsValidResourceType(expectedType) {
		query = query.Where("er.type = ?", string(expectedType))
	}
	if !allowBinding {
		query = query.Where("er.type <> ? OR dr.purpose <> ?", string(domain.ResourceTypeDomain), string(domain.PurposeBinding))
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select validation resources: %w", err)
	}
	result := make([]validationCandidateRow, 0, len(rows))
	for _, row := range rows {
		switch domain.ResourceType(row.ResourceType) {
		case domain.ResourceTypeMicrosoft:
			status := domain.MicrosoftResourceStatus(row.MicrosoftStatus)
			if status == "" || status == domain.MicrosoftStatusDeleted || status == domain.MicrosoftStatusDisabled || status == domain.MicrosoftStatusValidating {
				continue
			}
		case domain.ResourceTypeDomain:
			status := domain.MailDomainStatus(row.DomainStatus)
			if status == "" || status == domain.DomainStatusDeleted || status == domain.DomainStatusDisabled || status == domain.DomainStatusValidating {
				continue
			}
		default:
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

func selectValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, selection coreapp.ResourceBulkSelection, afterID uint, throughID uint, limit int) ([]validationCandidateRow, error) {
	switch selection.Filter.ResourceType {
	case domain.ResourceTypeMicrosoft:
		return selectMicrosoftValidationCandidatesByFilter(ctx, tx, ownerUserID, selection, afterID, throughID, limit)
	case domain.ResourceTypeDomain:
		return selectDomainValidationCandidatesByFilter(ctx, tx, ownerUserID, selection, afterID, throughID, limit)
	default:
		return nil, domain.ErrInvalidResourceType
	}
}

func selectMicrosoftValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, selection coreapp.ResourceBulkSelection, afterID uint, throughID uint, limit int) ([]validationCandidateRow, error) {
	q := microsoftValidationCandidateQuery(ctx, tx, ownerUserID, selection, afterID, throughID).
		Select("er.id, er.type AS resource_type, er.owner_user_id, ms.credential_revision AS credential_revision, ms.status AS microsoft_status, '' AS domain_status")

	var rows []validationCandidateRow
	q = q.Order("er.id ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select microsoft validation resources: %w", err)
	}
	return validateValidationCandidateRows(rows)
}

func microsoftValidationCandidateQuery(ctx context.Context, tx *gorm.DB, ownerUserID uint, selection coreapp.ResourceBulkSelection, afterID uint, throughID uint) *gorm.DB {
	filter := selection.Filter
	q := tx.WithContext(ctx).
		Table("email_resources AS er").
		Joins("JOIN microsoft_resources AS ms ON ms.id = er.id").
		Where("er.type = ?", string(domain.ResourceTypeMicrosoft)).
		Where("er.id <= ?", throughID).
		Where("ms.status NOT IN ?", []string{string(domain.MicrosoftStatusDeleted), string(domain.MicrosoftStatusDisabled), string(domain.MicrosoftStatusValidating)})
	if !selection.AdminScope {
		q = q.Where("er.owner_user_id = ?", ownerUserID)
	} else if filter.OwnerID > 0 {
		q = q.Where("er.owner_user_id = ?", filter.OwnerID)
	}

	if afterID > 0 {
		q = q.Where("er.id > ?", afterID)
	}
	if filter.Status != "" {
		q = q.Where("ms.status = ?", filter.Status)
	}
	if filter.ForSale != nil {
		q = q.Where("ms.for_sale = ?", *filter.ForSale)
	}
	if filter.LongLived != nil {
		q = q.Where("ms.long_lived = ?", *filter.LongLived)
	}
	if filter.GraphAvailable != nil {
		q = q.Where("ms.graph_available = ?", *filter.GraphAvailable)
	}
	if filter.CreatedFrom != nil {
		q = q.Where("er.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		op := "<="
		if filter.AdminSearch {
			op = "<"
		}
		q = q.Where("er.created_at "+op+" ?", *filter.CreatedTo)
	}
	if filter.Suffix != "" {
		suffix := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(filter.Suffix)), "@")
		if suffix != "" {
			q = q.Where("ms.email_domain = ?", suffix)
		}
	}
	if filter.Search != "" {
		if filter.AdminSearch {
			escaped := escapeAdminLike(filter.Search)
			conditions := []string{"LOWER(ms.email_address) LIKE ? ESCAPE '\\\\'"}
			args := []any{"%" + strings.ToLower(escaped) + "%"}
			if id, err := strconv.ParseUint(filter.Search, 10, 64); err == nil && id > 0 {
				conditions = append(conditions, "er.id = ?")
				args = append(args, id)
			}
			if len(filter.OwnerIDs) > 0 {
				conditions = append(conditions, "er.owner_user_id IN ?")
				args = append(args, filter.OwnerIDs)
			}
			q = q.Where("("+strings.Join(conditions, " OR ")+")", args...)
		} else {
			prefix := strings.ToLower(strings.TrimSpace(filter.Search)) + "%"
			q = q.Where("(ms.email_address LIKE ? OR ms.email_domain LIKE ?)", prefix, prefix)
		}
	}
	if filter.TokenHealth != "" {
		q = applyAdminTokenHealth(q, filter.TokenHealth, time.Now().UTC())
	}
	return q
}

func selectDomainValidationCandidatesByFilter(ctx context.Context, tx *gorm.DB, ownerUserID uint, selection coreapp.ResourceBulkSelection, afterID uint, throughID uint, limit int) ([]validationCandidateRow, error) {
	q := domainValidationCandidateQuery(ctx, tx, ownerUserID, selection, afterID, throughID).
		Select("er.id, er.type AS resource_type, er.owner_user_id, 0 AS credential_revision, '' AS microsoft_status, dr.status AS domain_status")

	var rows []validationCandidateRow
	q = q.Order("er.id ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select domain validation resources: %w", err)
	}
	return validateValidationCandidateRows(rows)
}

func domainValidationCandidateQuery(ctx context.Context, tx *gorm.DB, ownerUserID uint, selection coreapp.ResourceBulkSelection, afterID uint, throughID uint) *gorm.DB {
	filter := selection.Filter
	q := tx.WithContext(ctx).
		Table("email_resources AS er").
		Joins("JOIN domain_resources AS dr ON dr.id = er.id").
		Where("er.type = ?", string(domain.ResourceTypeDomain)).
		Where("er.id <= ?", throughID).
		Where("dr.owner_user_id = er.owner_user_id").
		Where("dr.status NOT IN ?", []string{string(domain.DomainStatusDeleted), string(domain.DomainStatusDisabled), string(domain.DomainStatusValidating)})
	if !selection.AdminScope {
		q = q.Where("er.owner_user_id = ?", ownerUserID)
	} else if filter.OwnerID > 0 {
		q = q.Where("er.owner_user_id = ?", filter.OwnerID)
	}
	if filter.ExcludeBinding {
		q = q.Where("dr.purpose <> ?", string(domain.PurposeBinding))
	}

	if afterID > 0 {
		q = q.Where("er.id > ?", afterID)
	}
	if filter.Status != "" {
		q = q.Where("dr.status = ?", filter.Status)
	}
	if filter.Purpose != "" {
		q = q.Where("dr.purpose = ?", filter.Purpose)
	}
	if filter.MailServerID > 0 {
		q = q.Where("dr.mail_server_id = ?", filter.MailServerID)
	}
	if filter.CreatedFrom != nil {
		q = q.Where("er.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		op := "<="
		if filter.AdminSearch {
			op = "<"
		}
		q = q.Where("er.created_at "+op+" ?", *filter.CreatedTo)
	}
	if filter.TLD != "" {
		q = q.Where("dr.domain_tld = ?", filter.TLD)
	}
	if filter.Search != "" {
		if filter.AdminSearch {
			escaped := strings.ToLower(escapeAdminLike(filter.Search))
			conditions := []string{"LOWER(dr.domain) LIKE ? ESCAPE '\\\\'"}
			args := []any{"%" + escaped + "%"}
			if id, err := strconv.ParseUint(filter.Search, 10, 64); err == nil && id > 0 {
				conditions = append(conditions, "er.id = ?", "er.owner_user_id = ?")
				args = append(args, id, id)
			}
			if len(filter.OwnerIDs) > 0 {
				conditions = append(conditions, "er.owner_user_id IN ?")
				args = append(args, filter.OwnerIDs)
			}
			q = q.Where("("+strings.Join(conditions, " OR ")+")", args...)
		} else {
			q = q.Where("dr.domain LIKE ?", strings.ToLower(strings.TrimSpace(filter.Search))+"%")
		}
	}
	return q
}

func validateValidationCandidateRows(rows []validationCandidateRow) ([]validationCandidateRow, error) {
	for _, row := range rows {
		switch domain.ResourceType(row.ResourceType) {
		case domain.ResourceTypeMicrosoft:
			switch domain.MicrosoftResourceStatus(row.MicrosoftStatus) {
			case "":
				return nil, domain.ErrResourceNotFound
			case domain.MicrosoftStatusDeleted:
				return nil, domain.ErrResourceNotFound
			case domain.MicrosoftStatusDisabled:
				return nil, domain.ErrInvalidResourceStatus
			}
		case domain.ResourceTypeDomain:
			switch domain.MailDomainStatus(row.DomainStatus) {
			case "":
				return nil, domain.ErrResourceNotFound
			case domain.DomainStatusDeleted:
				return nil, domain.ErrResourceNotFound
			case domain.DomainStatusDisabled:
				return nil, domain.ErrInvalidResourceStatus
			}
		default:
			return nil, domain.ErrInvalidResourceType
		}
	}
	return rows, nil
}

func markValidationCandidatesPendingTx(ctx context.Context, tx *gorm.DB, candidates []validationCandidateRow) error {
	microsoftIDs := make([]uint, 0, len(candidates))
	domainIDs := make([]uint, 0, len(candidates))
	for _, candidate := range candidates {
		switch domain.ResourceType(candidate.ResourceType) {
		case domain.ResourceTypeMicrosoft:
			microsoftIDs = append(microsoftIDs, candidate.ID)
		case domain.ResourceTypeDomain:
			domainIDs = append(domainIDs, candidate.ID)
		}
	}
	now := time.Now().UTC()
	for start := 0; start < len(microsoftIDs); start += resourceValidationInsertSize {
		end := start + resourceValidationInsertSize
		if end > len(microsoftIDs) {
			end = len(microsoftIDs)
		}
		updated := tx.WithContext(ctx).
			Model(&MicrosoftResourceModel{}).
			Where("id IN ? AND status NOT IN ?", microsoftIDs[start:end], []string{
				string(domain.MicrosoftStatusDeleted),
				string(domain.MicrosoftStatusDisabled),
				string(domain.MicrosoftStatusValidating),
			}).
			Updates(map[string]interface{}{
				"status":          string(domain.MicrosoftStatusPending),
				"last_safe_error": "",
				"updated_at":      now,
			})
		if updated.Error != nil {
			return fmt.Errorf("mark microsoft resources pending for validation: %w", updated.Error)
		}
	}
	for start := 0; start < len(domainIDs); start += resourceValidationInsertSize {
		end := min(start+resourceValidationInsertSize, len(domainIDs))
		updated := tx.WithContext(ctx).
			Model(&DomainResourceModel{}).
			Where("id IN ? AND status NOT IN ?", domainIDs[start:end], []string{
				string(domain.DomainStatusDeleted),
				string(domain.DomainStatusDisabled),
				string(domain.DomainStatusValidating),
			}).
			Updates(map[string]interface{}{
				"status":          string(domain.DomainStatusPending),
				"last_safe_error": "",
				"updated_at":      now,
			})
		if updated.Error != nil {
			return fmt.Errorf("mark domain resources pending for validation: %w", updated.Error)
		}
	}
	return nil
}

func markValidationCandidatesValidatingTx(ctx context.Context, tx *gorm.DB, candidates []validationCandidateRow) error {
	microsoftIDs := make([]uint, 0, len(candidates))
	domainIDs := make([]uint, 0, len(candidates))
	for _, candidate := range candidates {
		switch domain.ResourceType(candidate.ResourceType) {
		case domain.ResourceTypeMicrosoft:
			microsoftIDs = append(microsoftIDs, candidate.ID)
		case domain.ResourceTypeDomain:
			domainIDs = append(domainIDs, candidate.ID)
		}
	}
	now := time.Now().UTC()
	if len(microsoftIDs) > 0 {
		if err := tx.WithContext(ctx).Model(&MicrosoftResourceModel{}).
			Where("id IN ? AND status = ?", microsoftIDs, string(domain.MicrosoftStatusPending)).
			Updates(map[string]any{
				"status":     string(domain.MicrosoftStatusValidating),
				"updated_at": now,
			}).Error; err != nil {
			return fmt.Errorf("mark microsoft resources validating: %w", err)
		}
	}
	if len(domainIDs) > 0 {
		if err := tx.WithContext(ctx).Model(&DomainResourceModel{}).
			Where("id IN ? AND status = ?", domainIDs, string(domain.DomainStatusPending)).
			Updates(map[string]any{
				"status":     string(domain.DomainStatusValidating),
				"updated_at": now,
			}).Error; err != nil {
			return fmt.Errorf("mark domain resources validating: %w", err)
		}
	}
	return nil
}

func createSystemLogInTx(ctx context.Context, tx *gorm.DB, log *governancedomain.SystemLog) error {
	if log == nil {
		return nil
	}
	model := &governanceinfra.SystemLogModel{
		Level:     log.Level,
		Module:    log.Module,
		EventType: log.EventType,
		RequestID: log.RequestID,
		BizType:   log.BizType,
		BizID:     log.BizID,
		Message:   safeValidationMessage(log.Message),
		Detail:    safeValidationMessage(log.Detail),
	}
	if err := tx.WithContext(ctx).Create(model).Error; err != nil {
		return fmt.Errorf("create system log: %w", err)
	}
	return nil
}

func validationQualityScore(valid bool) int {
	if valid {
		return 100
	}
	return 0
}

func safeValidationMessage(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	const maxLen = 500
	if len(value) > maxLen {
		return value[:maxLen]
	}
	return value
}
