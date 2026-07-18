package infra

import (
	"context"
	"fmt"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"gorm.io/gorm"
)

// AdminResourceBulkRepo performs only bounded Microsoft candidate queries.
// Redis owns the temporary batch cursor; no task model is persisted in MySQL.
type AdminResourceBulkRepo struct {
	db   *gorm.DB
	read *AdminResourceRepo
}

func NewAdminResourceBulkRepo(db *gorm.DB) *AdminResourceBulkRepo {
	return &AdminResourceBulkRepo{db: db, read: NewAdminResourceRepo(db)}
}

func (r *AdminResourceBulkRepo) MaxCandidateID(ctx context.Context, filter coreapp.AdminResourceBulkFilterValue, now time.Time) (uint, error) {
	if r == nil || r.db == nil || r.read == nil {
		return 0, nil
	}
	var maxID uint
	if err := r.read.adminMicrosoftFilterQuery(ctx, filter.ListFilter(), now, "").
		Select("COALESCE(MAX(er.id), 0)").Scan(&maxID).Error; err != nil {
		return 0, fmt.Errorf("capture admin resource bulk high-water mark: %w", err)
	}
	return maxID, nil
}

func (r *AdminResourceBulkRepo) ListCandidateIDs(ctx context.Context, filter coreapp.AdminResourceBulkFilterValue, afterID, throughID uint, limit int, now time.Time) ([]uint, error) {
	if r == nil || r.db == nil || r.read == nil || throughID == 0 || limit <= 0 {
		return nil, nil
	}
	var ids []uint
	if err := r.read.adminMicrosoftFilterQuery(ctx, filter.ListFilter(), now, "").
		Select("er.id").Where("er.id > ? AND er.id <= ?", afterID, throughID).
		Order("er.id ASC").Limit(limit).Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("list admin resource bulk candidates: %w", err)
	}
	return ids, nil
}

var _ coreapp.AdminResourceBulkRepository = (*AdminResourceBulkRepo)(nil)
