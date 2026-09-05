package gmail

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
)

const localGmailVariantCooldownKeyPrefix = "gmail:variant_cooldown:"

func localGmailVariantCooldownKey(resourceID, projectID uint) string {
	return localGmailVariantCooldownKeyPrefix + strconv.FormatUint(uint64(projectID), 10) + ":" + strconv.FormatUint(uint64(resourceID), 10)
}

// The caller holds the allocation resource lock and shares the order rollback scope.
func (s *Service) StartVariantCooldown(ctx context.Context, resourceID, projectID uint) (bool, error) {
	if resourceID == 0 || projectID == 0 {
		return false, ErrInvalidLocalResource
	}
	duration := runtimeconfig.GmailVariantCooldown()
	if duration <= 0 {
		return true, nil
	}
	if s == nil || s.redis == nil || !platform.HasGormRollback(ctx) {
		return false, ErrLocalCooldownDependency
	}
	key := localGmailVariantCooldownKey(resourceID, projectID)
	token := platform.NewUUIDV7String()
	platform.RegisterGormRollback(ctx, func(rollbackCtx context.Context) error {
		cleanupCtx, cancel := context.WithTimeout(rollbackCtx, 2*time.Second)
		defer cancel()
		return gmailVariantCooldownReleaseScript.Run(cleanupCtx, s.redis, []string{key}, token).Err()
	})
	started, err := s.redis.SetNX(ctx, key, token, duration).Result()
	if err != nil {
		return false, fmt.Errorf("set Gmail project variant cooldown TTL: %w", err)
	}
	return started, nil
}

var gmailVariantCooldownReleaseScript = redis.NewScript(
	"if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end return 0",
)

// CoolingResourceIDs checks only the supplied candidates, never the Redis keyspace.
func (s *Service) CoolingResourceIDs(ctx context.Context, projectID uint, resourceIDs []uint) ([]uint, error) {
	if projectID == 0 {
		return nil, ErrInvalidLocalResource
	}
	if runtimeconfig.GmailVariantCooldown() <= 0 || len(resourceIDs) == 0 {
		return nil, nil
	}
	if s == nil || s.redis == nil {
		return nil, ErrLocalCooldownDependency
	}
	commands := make(map[uint]*redis.DurationCmd, len(resourceIDs))
	_, err := s.redis.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, resourceID := range resourceIDs {
			if resourceID == 0 {
				return ErrInvalidLocalResource
			}
			if _, exists := commands[resourceID]; !exists {
				commands[resourceID] = pipe.PTTL(ctx, localGmailVariantCooldownKey(resourceID, projectID))
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read Gmail candidate variant cooldown TTLs: %w", err)
	}
	ids := make([]uint, 0, len(commands))
	for resourceID, command := range commands {
		if command.Val() > 0 {
			ids = append(ids, resourceID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// variantCooldowns reads all projects only for administrator filtering and display.
func (s *Service) variantCooldowns(ctx context.Context) (map[uint][]LocalProjectCooldown, error) {
	result := make(map[uint][]LocalProjectCooldown)
	if runtimeconfig.GmailVariantCooldown() <= 0 || s == nil || s.redis == nil {
		return result, nil
	}
	pattern := localGmailVariantCooldownKeyPrefix + "*"
	// ponytail: admin-wide filtering still scans Redis; add a cooldown index if
	// measured admin latency needs it. Allocation and inventory use exact keys.
	seen := make(map[string]bool)
	var cursor uint64
	for {
		keys, next, err := s.redis.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return nil, fmt.Errorf("list Gmail project variant cooldowns: %w", err)
		}
		commands := make(map[string]*redis.DurationCmd, len(keys))
		observedAt := s.now().UTC()
		_, err = s.redis.Pipelined(ctx, func(pipe redis.Pipeliner) error {
			for _, key := range keys {
				if !seen[key] {
					seen[key] = true
					commands[key] = pipe.PTTL(ctx, key)
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read Gmail project variant cooldown TTLs: %w", err)
		}
		for key, command := range commands {
			project, resource, ok := strings.Cut(strings.TrimPrefix(key, localGmailVariantCooldownKeyPrefix), ":")
			if !ok {
				continue // The retired global key has no project component.
			}
			projectID, projectErr := strconv.ParseUint(project, 10, strconv.IntSize)
			resourceID, resourceErr := strconv.ParseUint(resource, 10, strconv.IntSize)
			if projectErr != nil || resourceErr != nil || projectID == 0 || resourceID == 0 {
				continue
			}
			if ttl := command.Val(); ttl > 0 {
				until := observedAt.Add(ttl)
				result[uint(resourceID)] = append(result[uint(resourceID)], LocalProjectCooldown{
					ProjectID: uint(projectID), CooldownUntil: &until,
				})
			}
		}
		if next == 0 {
			return result, nil
		}
		cursor = next
	}
}

func (s *Service) enrichVariantCooldowns(ctx context.Context, items []LocalResourceItem, cooldowns map[uint][]LocalProjectCooldown) error {
	projectIDs := make([]uint, 0)
	for i := range items {
		if items[i].Status != LocalResourceNormal || len(cooldowns[items[i].ID]) == 0 {
			continue
		}
		items[i].Status = LocalResourceCooldown
		items[i].ProjectCooldowns = append([]LocalProjectCooldown(nil), cooldowns[items[i].ID]...)
		sort.Slice(items[i].ProjectCooldowns, func(left, right int) bool {
			return items[i].ProjectCooldowns[left].ProjectID < items[i].ProjectCooldowns[right].ProjectID
		})
		for _, cooldown := range items[i].ProjectCooldowns {
			projectIDs = append(projectIDs, cooldown.ProjectID)
			if items[i].CooldownUntil == nil || cooldown.CooldownUntil.After(*items[i].CooldownUntil) {
				items[i].CooldownUntil = cooldown.CooldownUntil
			}
		}
	}
	if len(projectIDs) == 0 {
		return nil
	}
	var projects []struct {
		ID   uint
		Name string
	}
	if err := s.dbFor(ctx).Table("projects").Select("id, name").Where("id IN ?", projectIDs).Scan(&projects).Error; err != nil {
		return fmt.Errorf("load cooling Gmail project names: %w", err)
	}
	names := make(map[uint]string, len(projects))
	for _, project := range projects {
		names[project.ID] = project.Name
	}
	for i := range items {
		for j := range items[i].ProjectCooldowns {
			items[i].ProjectCooldowns[j].ProjectName = names[items[i].ProjectCooldowns[j].ProjectID]
		}
	}
	return nil
}
