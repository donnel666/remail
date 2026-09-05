package gmail

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const localGmailVariantCooldownKeyPrefix = "gmail:variant_cooldown:"

func localGmailVariantCooldownKey(resourceID uint) string {
	return localGmailVariantCooldownKeyPrefix + strconv.FormatUint(uint64(resourceID), 10)
}

func (s *Service) StartVariantCooldown(ctx context.Context, resourceID uint) error {
	duration := runtimeconfig.GmailVariantCooldown()
	if duration <= 0 {
		return nil
	}
	if s == nil || s.db == nil || s.redis == nil || resourceID == 0 {
		return ErrLocalCooldownDependency
	}
	if err := s.redis.Set(ctx, localGmailVariantCooldownKey(resourceID), "1", duration).Err(); err != nil {
		return fmt.Errorf("set Gmail variant cooldown TTL: %w", err)
	}
	result := s.dbFor(ctx).Model(&localResourceModel{}).
		Where("id = ? AND status IN ?", resourceID, []string{LocalResourceNormal, localResourceRollbackNormal}).
		Updates(map[string]any{"status": LocalResourceCooldown, "updated_at": s.now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("mark Gmail resource cooling down: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrInvalidLocalResource
	}
	return nil
}

func (s *Service) RestoreExpiredVariantCooldowns(ctx context.Context) error {
	if s == nil || s.db == nil || s.redis == nil {
		return nil
	}
	var resourceIDs []uint
	if err := s.dbFor(ctx).Model(&localResourceModel{}).
		Where("status = ?", LocalResourceCooldown).Order("id").Pluck("id", &resourceIDs).Error; err != nil {
		return fmt.Errorf("list cooling Gmail resources: %w", err)
	}
	if len(resourceIDs) == 0 {
		return nil
	}
	// ponytail: a pipelined scan is enough for the current Gmail pool; move due
	// IDs to a Redis sorted set only if active cooldown cardinality becomes large.
	ttls, err := s.variantCooldownTTLs(ctx, resourceIDs)
	if err != nil {
		return err
	}
	expired := make([]uint, 0, len(resourceIDs))
	for _, resourceID := range resourceIDs {
		if ttls[resourceID] <= 0 {
			expired = append(expired, resourceID)
		}
	}
	for _, resourceID := range expired {
		if err := s.restoreExpiredVariantCooldown(ctx, resourceID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) restoreExpiredVariantCooldown(ctx context.Context, resourceID uint) error {
	return s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var resource localResourceModel
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
			Where("id = ? AND status = ?", resourceID, LocalResourceCooldown).Limit(1).Find(&resource)
		if result.Error != nil {
			return fmt.Errorf("lock cooling Gmail resource: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
		ttl, err := s.redis.PTTL(ctx, localGmailVariantCooldownKey(resourceID)).Result()
		if err != nil {
			return fmt.Errorf("recheck Gmail variant cooldown TTL: %w", err)
		}
		if ttl > 0 {
			return nil
		}
		if err := s.redis.Del(ctx, localGmailVariantCooldownKey(resourceID)).Err(); err != nil {
			return fmt.Errorf("clear expired Gmail variant cooldown: %w", err)
		}
		if err := tx.Model(&localResourceModel{}).Where("id = ? AND status = ?", resourceID, LocalResourceCooldown).
			Updates(map[string]any{"status": localResourceRollbackNormal, "updated_at": s.now().UTC()}).Error; err != nil {
			return fmt.Errorf("restore cooled Gmail resource: %w", err)
		}
		return nil
	})
}

func (s *Service) variantCooldownTTLs(ctx context.Context, resourceIDs []uint) (map[uint]time.Duration, error) {
	commands := make(map[uint]*redis.DurationCmd, len(resourceIDs))
	_, err := s.redis.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, resourceID := range resourceIDs {
			commands[resourceID] = pipe.PTTL(ctx, localGmailVariantCooldownKey(resourceID))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read Gmail variant cooldown TTLs: %w", err)
	}
	ttls := make(map[uint]time.Duration, len(commands))
	for resourceID, command := range commands {
		ttl, err := command.Result()
		if err != nil {
			return nil, fmt.Errorf("read Gmail resource %d cooldown TTL: %w", resourceID, err)
		}
		ttls[resourceID] = ttl
	}
	return ttls, nil
}

func (s *Service) enrichVariantCooldowns(ctx context.Context, items []LocalResourceItem) error {
	if s == nil || s.redis == nil {
		return nil
	}
	resourceIDs := make([]uint, 0, len(items))
	for i := range items {
		if items[i].Status == LocalResourceCooldown {
			resourceIDs = append(resourceIDs, items[i].ID)
		}
	}
	if len(resourceIDs) == 0 {
		return nil
	}
	ttls, err := s.variantCooldownTTLs(ctx, resourceIDs)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for i := range items {
		if ttl := ttls[items[i].ID]; items[i].Status == LocalResourceCooldown && ttl > 0 {
			until := now.Add(ttl)
			items[i].CooldownUntil = &until
		}
	}
	return nil
}
