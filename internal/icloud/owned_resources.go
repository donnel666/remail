package icloud

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	userICloudImportMaxAccounts = 100
	userICloudImportMaxBytes    = 1 << 20
	userICloudResourceLifetime  = 30 * 24 * time.Hour
)

type OwnedICloudResourceView struct {
	ID                uint      `json:"id"`
	Version           uint64    `json:"version"`
	Email             string    `json:"email"`
	Status            string    `json:"status"`
	ForSale           bool      `json:"forSale"`
	AliasCount        uint      `json:"aliasCount"`
	SelectedForwardTo string    `json:"selectedForwardTo"`
	ExpireAt          time.Time `json:"expireAt"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	LastSafeError     *string   `json:"lastSafeError"`
}

type OwnedICloudResourceList struct {
	Items  []OwnedICloudResourceView `json:"items"`
	Total  int64                     `json:"total"`
	Offset int                       `json:"offset"`
	Limit  int                       `json:"limit"`
	Facets OwnedICloudResourceFacets `json:"facets"`
}

type OwnedICloudResourceFacets struct {
	AdminICloudResourceFacets
	Suffixes []OwnedICloudSuffixFacet `json:"suffixes"`
}

type OwnedICloudSuffixFacet struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type OwnedICloudImportTask struct {
	ID            uint   `json:"id"`
	Email         string `json:"email"`
	Status        string `json:"status"`
	Stage         string `json:"stage"`
	LastSafeError string `json:"lastSafeError"`
}

type OwnedICloudImportView struct {
	ImportID  uint                    `json:"importId"`
	Status    string                  `json:"status"`
	Accepted  int                     `json:"accepted"`
	Completed int                     `json:"completed"`
	Failed    int                     `json:"failed"`
	Waiting   int                     `json:"waiting"`
	Tasks     []OwnedICloudImportTask `json:"tasks"`
}

func ownedICloudImportView(view *OnboardingImportView) *OwnedICloudImportView {
	result := &OwnedICloudImportView{ImportID: view.ImportID, Status: view.Status, Accepted: view.Accepted,
		Completed: view.Completed, Failed: view.Failed, Waiting: view.Waiting, Tasks: make([]OwnedICloudImportTask, len(view.Tasks))}
	for index, task := range view.Tasks {
		result.Tasks[index] = OwnedICloudImportTask{ID: task.ID, Email: task.PrimaryEmail, Status: task.Status, Stage: task.Stage, LastSafeError: task.LastSafeError}
	}
	return result
}

func (s *Service) ListOwnedICloudResources(ctx context.Context, userID uint, filter AdminICloudResourceListFilter) (*OwnedICloudResourceList, error) {
	if userID == 0 {
		return nil, ErrICloudResourceQuery
	}
	filter.OwnerUserID = userID
	filter, err := normalizeAdminICloudResourceFilter(filter)
	if err != nil {
		return nil, err
	}
	list, err := s.ListAdminICloudResources(ctx, filter)
	if err != nil {
		return nil, err
	}
	items := make([]OwnedICloudResourceView, len(list.Items))
	for i, resource := range list.Items {
		items[i] = OwnedICloudResourceView{ID: resource.ID, Version: resource.Version, Email: resource.PrimaryEmail,
			Status: resource.Status, ForSale: resource.ForSale, AliasCount: resource.AliasCount,
			SelectedForwardTo: resource.SelectedForwardTo, ExpireAt: resource.ExpireAt,
			CreatedAt: resource.CreatedAt, UpdatedAt: resource.UpdatedAt, LastSafeError: resource.LastSafeError}
	}
	filter.Suffix = ""
	suffixes := []OwnedICloudSuffixFacet{}
	suffix := "LOWER(SUBSTR(ir.primary_email, INSTR(ir.primary_email, '@') + 1))"
	if err := s.applyAdminICloudResourceFilter(s.adminICloudResourceQuery(ctx), filter, adminICloudFilterIgnore{}).
		Select(suffix + " AS `key`, COUNT(*) AS count").Group(suffix).Order("count DESC, `key` ASC").Scan(&suffixes).Error; err != nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	return &OwnedICloudResourceList{Items: items, Total: list.Total, Offset: list.Offset, Limit: list.Limit,
		Facets: OwnedICloudResourceFacets{AdminICloudResourceFacets: list.Facets, Suffixes: suffixes}}, nil
}

func (s *Service) AcceptOwnedICloudImport(ctx context.Context, userID uint, content []byte, key, requestID, path string) (*OwnedICloudImportView, bool, error) {
	if s == nil || s.db == nil || userID == 0 || len(content) == 0 || len(content) > userICloudImportMaxBytes {
		return nil, false, ErrICloudOnboardingInvalid
	}
	content = []byte(strings.TrimPrefix(string(content), "\uFEFF"))
	view, reused, err := s.acceptICloudOnboardingImport(ctx, userID, userID, content, s.now().UTC().Add(userICloudResourceLifetime), key, requestID, path, true)
	if err != nil {
		return nil, false, err
	}
	return ownedICloudImportView(view), reused, nil
}

func (s *Service) GetOwnedICloudImport(ctx context.Context, userID, importID uint) (*OwnedICloudImportView, error) {
	if userID == 0 {
		return nil, ErrICloudOnboardingNotFound
	}
	view, err := s.getICloudOnboardingImport(ctx, importID, userID)
	if err != nil {
		return nil, err
	}
	return ownedICloudImportView(view), nil
}

func (s *Service) ListOwnedICloudAliases(ctx context.Context, userID, resourceID uint, offset, limit int) (*AdminICloudAliasList, error) {
	if s == nil || s.db == nil || userID == 0 || resourceID == 0 {
		return nil, ErrICloudResourceNotFound
	}
	var result *AdminICloudAliasList
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND owner_user_id = ? AND type = ?", resourceID, userID, "icloud").Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrICloudResourceNotFound
			}
			return err
		}
		scoped := *s
		scoped.db = tx
		var err error
		result, err = scoped.ListAdminICloudAliases(ctx, resourceID, offset, limit)
		return err
	})
	return result, err
}

func (s *Service) ApplyOwnedICloudCommand(ctx context.Context, userID uint, command AdminICloudCommand, resourceID uint, version uint64, key, requestID, path string) (*AdminICloudMutationResult, error) {
	if userID == 0 || (command != AdminICloudValidate && command != AdminICloudPublish && command != AdminICloudDelete) {
		return nil, ErrICloudResourceQuery
	}
	return s.applyICloudCommand(ctx, command, resourceID, version, userID, key, requestID, path, userID)
}

type OwnedICloudCommandItem struct {
	ID      uint   `json:"id"`
	Version uint64 `json:"version"`
}

func (s *Service) ApplyOwnedICloudBatchByFilter(ctx context.Context, userID uint, command AdminICloudCommand, filter AdminICloudResourceListFilter, key, requestID, path string) (*AdminICloudBulkResult, error) {
	filter.OwnerUserID = userID
	filter, err := normalizeAdminICloudResourceFilter(filter)
	if err != nil {
		return nil, err
	}
	return s.applyOwnedICloudBatch(ctx, userID, command, nil, &filter, key, requestID, path)
}

func (s *Service) ApplyOwnedICloudBatch(ctx context.Context, userID uint, command AdminICloudCommand, items []OwnedICloudCommandItem, key, requestID, path string) (*AdminICloudBulkResult, error) {
	if len(items) == 0 || len(items) > adminICloudBatchMax {
		return nil, ErrICloudResourceQuery
	}
	seen := make(map[uint]bool, len(items))
	for _, item := range items {
		if item.ID == 0 || item.Version == 0 || seen[item.ID] {
			return nil, ErrICloudResourceSelection
		}
		seen[item.ID] = true
	}
	items = append([]OwnedICloudCommandItem(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return s.applyOwnedICloudBatch(ctx, userID, command, items, nil, key, requestID, path)
}

func (s *Service) applyOwnedICloudBatch(ctx context.Context, userID uint, command AdminICloudCommand, items []OwnedICloudCommandItem, filter *AdminICloudResourceListFilter, key, requestID, path string) (*AdminICloudBulkResult, error) {
	key = strings.TrimSpace(key)
	if s == nil || s.db == nil || s.operationLogs == nil || userID == 0 || key == "" || len(key) > 80 ||
		(command != AdminICloudValidate && command != AdminICloudPublish && command != AdminICloudDelete) {
		return nil, ErrICloudResourceQuery
	}
	fingerprintValue := any(items)
	if filter != nil {
		fingerprintValue = struct {
			AdminICloudResourceListFilter
			Suffix string `json:"suffix"`
		}{*filter, filter.Suffix}
	}
	fingerprint, err := adminICloudCommandFingerprint(fingerprintValue)
	if err != nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	receipt := coreapp.AdminResourceCommandReceipt{
		OperatorUserID: userID, IdempotencyKey: key,
		Operation: "icloud.resource." + string(command) + "_batch", Subject: "icloud_resources:" + fingerprint,
		RequestFingerprint: fingerprint,
	}
	result := &AdminICloudBulkResult{AffectedResourceIDs: []uint{}, SkippedResourceIDs: []uint{}, ReasonCounts: []AdminICloudReasonCount{}}
	replayed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		replayed, err = s.reserveAdminICloudCommand(ctx, tx, receipt, result)
		if err != nil || replayed {
			return err
		}
		scoped := *s
		scoped.db = tx
		// Dispatch only after the outer batch transaction commits.
		scoped.queue = nil
		if filter != nil {
			if err := scoped.applyAdminICloudResourceFilter(scoped.adminICloudResourceQuery(ctx), *filter, adminICloudFilterIgnore{}).
				Select("ir.id, er.version").Order("ir.id ASC").Limit(adminICloudBatchMax + 1).Scan(&items).Error; err != nil {
				return err
			}
			if len(items) > adminICloudBatchMax {
				return ErrICloudResourceSelection
			}
		}
		result.Requested = len(items)
		reasons := make(map[string]int64)
		for _, item := range items {
			changed, err := scoped.ApplyOwnedICloudCommand(ctx, userID, command, item.ID, item.Version, fmt.Sprintf("%s:%d", key, item.ID), requestID, path)
			if err != nil {
				reason := adminICloudSkipReason(err)
				if errors.Is(err, ErrICloudResourceVersion) {
					reason = "version_conflict"
				}
				if reason == "" {
					return err
				}
				appendAdminICloudSkip(result, reasons, item.ID, reason)
			} else if !changed.Changed {
				appendAdminICloudSkip(result, reasons, item.ID, "already_target")
			} else {
				result.AffectedResourceIDs = append(result.AffectedResourceIDs, item.ID)
			}
		}
		result.Affected, result.Skipped = len(result.AffectedResourceIDs), len(result.SkippedResourceIDs)
		result.ReasonCounts = adminICloudReasonCounts(reasons)
		return s.completeAdminICloudCommand(ctx, tx, userID, key, result)
	})
	if errors.Is(err, ErrICloudImportConflict) {
		return nil, err
	}
	if err != nil {
		return nil, normalizeAdminICloudCommandError(err)
	}
	if !replayed && result.Affected > 0 && commandQueuesICloudValidation(command) {
		_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return result, nil
}
