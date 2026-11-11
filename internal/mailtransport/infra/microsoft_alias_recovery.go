package infra

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
)

func (s *MicrosoftAliasStore) recoverStaleDispatches(ctx context.Context, now time.Time) error {
	if s.redis == nil {
		return nil
	}
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	if now.Sub(s.lastRecoveryAt) < 30*time.Second {
		return nil
	}
	s.lastRecoveryAt = now
	before := now.Add(-microsoftAliasTimeout() - time.Minute)
	var pages [2][]MicrosoftAliasScheduleModel
	var rows []MicrosoftAliasScheduleModel
	for index, status := range []string{"queued", "running"} {
		query := s.db.WithContext(ctx).Model(&MicrosoftAliasScheduleModel{})
		if s.db.Name() == "mysql" {
			query = query.Table("microsoft_alias_schedules FORCE INDEX (idx_microsoft_alias_schedules_stale)")
		}
		query = query.Select("/*+ MAX_EXECUTION_TIME(1500) */ resource_id, generation, updated_at").
			Where("status = ? AND updated_at < ?", status, before)
		cursor := s.recoveryCursor[index]
		if cursor.resourceID > 0 {
			query = query.Where("updated_at > ? OR (updated_at = ? AND resource_id > ?)", cursor.updatedAt, cursor.updatedAt, cursor.resourceID)
		}
		if err := query.Order("updated_at ASC, resource_id ASC").Limit(50).Find(&pages[index]).Error; err != nil {
			return fmt.Errorf("list stale microsoft alias dispatches: %w", err)
		}
		rows = append(rows, pages[index]...)
	}
	if len(rows) == 0 {
		for index := range s.recoveryCursor {
			s.recoveryCursor[index].resourceID = 0
		}
		return nil
	}
	candidates := make(map[uint]struct{}, len(rows))
	for _, row := range rows {
		candidates[row.ResourceID] = struct{}{}
	}
	live, err := platform.LiveTaskResourceIDs(ctx, s.redis, MicrosoftAliasQueueName, TypeMicrosoftAlias, candidates)
	if err != nil {
		return err
	}
	recovered := 0
	for _, row := range rows {
		if _, ok := live[row.ResourceID]; ok {
			continue
		}
		changed, err := s.markDispatchFailed(ctx, mailapp.MicrosoftAliasTask{
			ResourceID: row.ResourceID, Generation: row.Generation,
		}, now.Add(time.Duration(row.ResourceID%30)*time.Second), "Recovered orphaned alias task; existing attempts require reconciliation.", before)
		if err != nil {
			return err
		}
		if changed {
			recovered++
		}
	}
	for index, page := range pages {
		if len(page) < 50 {
			s.recoveryCursor[index].resourceID = 0
		} else {
			last := page[len(page)-1]
			s.recoveryCursor[index].resourceID = last.ResourceID
			s.recoveryCursor[index].updatedAt = last.UpdatedAt
		}
	}
	if recovered > 0 {
		slog.InfoContext(ctx, "recovered orphaned microsoft alias dispatches", "recovered", recovered)
	}
	return nil
}
