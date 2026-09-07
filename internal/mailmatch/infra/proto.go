package infra

import (
	"context"
	"errors"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const protoMatchingScopesSQL = `
SELECT o.id AS order_id, o.order_no, o.user_id, o.project_id,
       o.project_product_id AS product_id, o.service_mode, o.status AS order_status,
       o.allocation_type, pa.id AS allocation_id, 'exact' AS recipient_kind,
       pa.resource_id AS email_resource_id, pa.email AS recipient,
       o.receive_started_at, o.receive_until, o.activated_at, o.after_sale_until,
       p.loose_match, pr.credential_revision
FROM proto_allocations pa
JOIN orders o ON o.order_no = pa.order_no AND o.allocation_type = 'proto'
JOIN projects p ON p.id = o.project_id
JOIN proto_resources pr ON pr.id = pa.resource_id AND pr.resource_type = 'proto'
WHERE pa.resource_id = ? AND pa.email = ? AND pa.guard_type = 'proto'
  AND pa.status = 'allocated' AND pr.status <> 'deleted'
  AND (o.receive_started_at IS NULL OR ? >= DATE_SUB(o.receive_started_at, INTERVAL 2 MINUTE))
  AND ((o.service_mode = 'code' AND o.status = 'active' AND (o.receive_until IS NULL OR ? <= o.receive_until))
       OR (o.service_mode = 'purchase' AND o.status IN ('active', 'completed')))
ORDER BY o.created_at ASC, o.id ASC`

func (r *Repo) AssertProtoCredentialRevision(ctx context.Context, resourceID uint, revision uint64) error {
	if resourceID == 0 || revision == 0 {
		return domain.ErrResourceFetchCredentialChanged
	}
	return r.WithTx(ctx, func(txCtx context.Context) error {
		return assertProtoCredentialRevision(r.dbFor(txCtx), resourceID, revision)
	})
}

func assertProtoCredentialRevision(db *gorm.DB, resourceID uint, revision uint64) error {
	scope, err := loadProtoResourceFetchScope(db, resourceID)
	if err != nil {
		return err
	}
	return validateResourceFetchScope(scope, revision)
}

func loadProtoResourceFetchScope(db *gorm.DB, resourceID uint) (*domain.ResourceFetchScope, error) {
	var root struct{ ID uint }
	if err := db.Table("email_resources").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = 'proto'", resourceID).Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrResourceFetchNotFound
		}
		return nil, err
	}
	var resource domain.ResourceFetchScope
	if err := db.Table("proto_resources").Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id AS resource_id, status, email_address, credential_revision, password <> '' AS credentials_configured").
		Where("id = ? AND resource_type = 'proto'", resourceID).Take(&resource).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrResourceFetchNotFound
		}
		return nil, err
	}
	resource.ResourceType = domain.ResourceTypeProto
	return &resource, nil
}

func (r *ResourceFetchRepo) AssertProtoResourceFetchFence(ctx context.Context, resourceID uint, generation, revision uint64) error {
	return r.withTx(ctx, func(_ context.Context, tx *gorm.DB) error {
		if err := assertProtoCredentialRevision(tx, resourceID, revision); err != nil {
			return err
		}
		return r.lockResourceFetchState(tx, resourceID, generation)
	})
}

func (r *ResourceFetchRepo) CompleteProtoResourceFetch(ctx context.Context, resourceID uint, generation, revision uint64, fetched, stored, matched int, now time.Time, log *governancedomain.SystemLog) error {
	return r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		if err := assertProtoCredentialRevision(tx, resourceID, revision); err != nil {
			return err
		}
		if err := r.lockResourceFetchState(tx, resourceID, generation); err != nil {
			return err
		}
		return r.finishResourceFetchState(txCtx, tx, resourceID, generation, map[string]any{
			"status": string(domain.ResourceFetchJobSucceeded), "failures": 0,
			"fetched_count": max(fetched, 0), "stored_count": max(stored, 0), "matched_count": max(matched, 0),
			"last_safe_error": "", "finished_at": now,
		}, log)
	})
}
