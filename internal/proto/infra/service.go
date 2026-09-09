package infra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/donnel666/remail/internal/platform"
	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type resourceRoot struct {
	ID          uint `gorm:"primaryKey;autoIncrement"`
	Type        string
	OwnerUserID uint
	Version     uint64 `gorm:"default:1"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (resourceRoot) TableName() string { return "email_resources" }

type Resource struct {
	ID                   uint `gorm:"primaryKey;autoIncrement:false"`
	ResourceType         string
	OwnerUserID          uint
	EmailAddress         string `gorm:"uniqueIndex"`
	EmailDomain          string
	Password             string
	PasswordConfigured   bool `gorm:"->"`
	ForSale              bool
	LongLived            bool
	QualityScore         int
	AllocBucket          uint16
	Status               string
	Version              uint64
	ValidationGeneration uint64
	CredentialRevision   uint64
	CredentialUpdatedAt  time.Time
	ValidationRequestID  string
	ValidationFailures   int
	LastSafeError        string
	LastCheckedAt        *time.Time
	LastAllocatedAt      *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (Resource) TableName() string { return "proto_resources" }

type BackgroundExecutionGate interface {
	TryAcquire() (func(), bool)
	Snapshot() platform.BackgroundLoadSnapshot
}

type Service struct {
	DB                    *gorm.DB
	Files                 governanceapp.FilePort
	Queue                 protoapp.Queue
	Redis                 redis.UniversalClient
	OperationLogs         governanceapp.OperationLogPort
	SystemLogs            governanceapp.SystemLogPort
	BackgroundExecution   BackgroundExecutionGate
	Protocol              ProtocolClient
	Proxies               ProxyProvider
	HistoricalUsage       func(context.Context, []HistoricalUsage) error
	ValidateOwner         func(context.Context, uint) (bool, error)
	ValidateSupplierOwner func(context.Context, uint) (bool, error)
	Now                   func() time.Time
}

func NewService(db *gorm.DB, files ...governanceapp.FilePort) *Service {
	s := &Service{DB: db, Protocol: proton.NewPKLClient(), Now: func() time.Time { return time.Now().UTC() }}
	if len(files) > 0 {
		s.Files = files[0]
	}
	return s
}
func (s *Service) SetFileStore(files governanceapp.FilePort) { s.Files = files }
func (s *Service) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return s.DB.WithContext(ctx)
}
func (s *Service) transaction(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
	if s == nil || s.DB == nil {
		return domain.ErrDependency
	}
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return fn(ctx, tx)
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return fn(platform.WithGormTx(ctx, tx), tx) })
	var mysqlError *mysql.MySQLError
	if errors.Is(err, gorm.ErrDuplicatedKey) || (errors.As(err, &mysqlError) && mysqlError.Number == 1062) {
		return domain.ErrResourceConflict
	}
	return err
}
func bumpRoot(tx *gorm.DB, id uint, now time.Time) error {
	result := tx.Model(&resourceRoot{}).Where("id = ? AND type = ?", id, domain.ResourceType).Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domain.ErrResourceMissing
	}
	return nil
}
func emailDomain(email string) string {
	_, suffix, _ := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@")
	return suffix
}

func (s *Service) ImportLine(ctx context.Context, owner uint, line domain.ImportLine) (uint, string, error) {
	line.Email = normalizeImportEmail(line.Email)
	if owner == 0 || !validEmail(line.Email) || !validPassword(line.Password) || (line.PKLBase64 != "" && !validImportPKL(line.PKLBase64)) {
		return 0, "", domain.ErrInvalidResource
	}
	var id uint
	outcome := "imported"
	err := s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return s.importLineTx(ctx, tx, owner, line, &id, &outcome)
	})
	return id, outcome, err
}
func (s *Service) importLineTx(_ context.Context, tx *gorm.DB, owner uint, line domain.ImportLine, id *uint, outcome *string) error {
	var existing Resource
	err := tx.Where("email_address = ?", line.Email).Take(&existing).Error
	now := s.Now().UTC()
	if err == nil {
		row, err := lockResource(tx, existing.ID, nil)
		if err != nil {
			return err
		}
		if row.Status != domain.StatusDeleted {
			*outcome = "skipped"
			return nil
		}
		if err := assertNoAllocations(tx, row.ID); err != nil {
			return err
		}
		if err := cancelMaintenanceRunsTx(tx, row.ID, "Superseded by a new import.", now); err != nil {
			return err
		}
		if err := deleteSessionTx(tx, row.ID); err != nil {
			return err
		}
		if err := tx.Model(&resourceRoot{}).Where("id = ? AND type = ?", row.ID, domain.ResourceType).Updates(map[string]any{"owner_user_id": owner, "version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&Resource{}).Where("id = ?", row.ID).Updates(map[string]any{"owner_user_id": owner, "password": line.Password, "status": domain.StatusPending, "for_sale": false, "email_domain": emailDomain(line.Email), "quality_score": 0, "alloc_bucket": row.ID % 2048, "version": row.Version + 1, "credential_revision": row.CredentialRevision + 1, "credential_updated_at": now, "validation_generation": row.ValidationGeneration + 1, "validation_failures": 0, "validation_request_id": "", "last_safe_error": "", "last_checked_at": nil, "last_allocated_at": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := s.storeImportedPKLTx(tx, row.ID, row.CredentialRevision+1, line.PKLBase64, now); err != nil {
			return err
		}
		*id = row.ID
		*outcome = "restored"
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	root := resourceRoot{Type: domain.ResourceType, OwnerUserID: owner, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&root).Error; err != nil {
		return err
	}
	row := Resource{ID: root.ID, ResourceType: domain.ResourceType, OwnerUserID: owner, EmailAddress: line.Email, EmailDomain: emailDomain(line.Email), Password: line.Password, Status: domain.StatusPending, Version: 1, ValidationGeneration: 1, CredentialRevision: 1, CredentialUpdatedAt: now, AllocBucket: uint16(root.ID % 2048), CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	if err := s.storeImportedPKLTx(tx, row.ID, row.CredentialRevision, line.PKLBase64, now); err != nil {
		return err
	}
	*id = root.ID
	return nil
}

func (s *Service) CompleteHistorySuccess(ctx context.Context, id uint, generation uint64) error {
	return s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, id, nil)
		if err != nil {
			return err
		}
		if row.Status != domain.StatusIdentifying || row.ValidationGeneration != generation {
			return domain.ErrInvalidClaim
		}
		now := s.Now().UTC()
		updates := map[string]any{"status": domain.StatusNormal, "validation_failures": 0, "last_safe_error": "", "last_checked_at": now, "version": row.Version + 1, "updated_at": now}
		if err := tx.Model(&Resource{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Model(&MaintenanceRun{}).Where("resource_id = ? AND validation_generation = ? AND kind = ? AND status IN ?", id, generation, maintenanceKindHistory, []string{maintenanceQueued, maintenanceRunning}).Updates(map[string]any{"status": maintenanceSucceeded, "last_safe_error": "", "finished_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return bumpRoot(tx, id, now)
	})
}
func (s *Service) BeginValidation(ctx context.Context, id uint) (uint64, error) {
	return s.ClaimForValidation(ctx, id, nil)
}
func Fingerprint(owner uint, strategy string, content []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|", owner, strings.TrimSpace(strategy))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

func lockResource(tx *gorm.DB, id uint, owner *uint) (*Resource, error) {
	var root resourceRoot
	q := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", id, domain.ResourceType)
	if owner != nil {
		q = q.Where("owner_user_id = ?", *owner)
	}
	if err := q.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrResourceMissing
		}
		return nil, err
	}
	var row Resource
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND resource_type = ? AND owner_user_id = ?", id, domain.ResourceType, root.OwnerUserID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrResourceMissing
		}
		return nil, err
	}
	row.Version = root.Version
	return &row, nil
}
func assertNoAllocations(tx *gorm.DB, id uint) error {
	var count int64
	if err := tx.Table("proto_allocations").Where("resource_id = ? AND status = 'allocated'", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return domain.ErrResourceBusy
	}
	return nil
}

// MarkPermanentFetchFailure fences the provider failure before Trade refunds
// affected orders. Administrative disable/delete always remain authoritative.
func (s *Service) MarkPermanentFetchFailure(ctx context.Context, id uint, revision uint64, safeMessage string) (bool, error) {
	if id == 0 || revision == 0 || strings.TrimSpace(safeMessage) == "" {
		return false, domain.ErrInvalidResource
	}
	applied := false
	err := s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, id, nil)
		if errors.Is(err, domain.ErrResourceMissing) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.CredentialRevision != revision || row.Status == domain.StatusDisabled || row.Status == domain.StatusDeleted {
			return nil
		}
		if row.Status == domain.StatusAbnormal {
			applied = true
			return nil
		}
		now := s.Now().UTC()
		if err := cancelMaintenanceRunsTx(tx, id, "Superseded by a permanent mail fetch failure.", now); err != nil {
			return err
		}
		if err := tx.Model(&Resource{}).Where("id = ?", id).Updates(map[string]any{"status": domain.StatusAbnormal, "quality_score": 0, "validation_generation": row.ValidationGeneration + 1, "last_safe_error": strings.TrimSpace(safeMessage), "version": row.Version + 1, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := bumpRoot(tx, id, now); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}
