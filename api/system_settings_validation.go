package api

import (
	"context"
	"strings"
	"time"

	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
)

func validateICloudForwardingSuffixes(ctx context.Context, db *gorm.DB, settings []settingsdomain.Setting) error {
	for _, setting := range settings {
		if !strings.EqualFold(strings.TrimSpace(setting.Key), runtimeconfig.ICloudForwardingSuffixesKey) {
			continue
		}

		domains := runtimeconfig.ICloudForwardingSuffixes(setting.Value)
		if len(domains) == 0 {
			continue
		}
		var count int64
		if err := db.WithContext(ctx).Table("domain_resources").
			Where("purpose = ? AND status NOT IN ? AND LOWER(domain) IN ?", "binding", []string{"disabled", "deleted"}, domains).
			Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(domains)) {
			return &settingsdomain.InvalidValueFieldsError{Fields: map[string]string{
				runtimeconfig.ICloudForwardingSuffixesKey: "Must contain only receivable auxiliary mailbox domains.",
			}}
		}
	}
	return nil
}

func applyICloudForwardingSuffixesUpdate(ctx context.Context, db *gorm.DB, settings []settingsdomain.Setting) error {
	value, changed := "", false
	for _, setting := range settings {
		if strings.EqualFold(strings.TrimSpace(setting.Key), runtimeconfig.ICloudForwardingSuffixesKey) {
			value, changed = setting.Value, true
		}
	}
	if !changed {
		return nil
	}
	domains := runtimeconfig.ICloudForwardingSuffixes(value)
	now := time.Now().UTC()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		resourceQuery := unauthorizedICloudForwardingQuery(
			tx.Table("icloud_resources").Where("status NOT IN ? AND selected_forward_to <> ''", []string{"disabled", "deleted"}),
			"selected_forward_to", domains,
		)
		var resourceIDs []uint
		if err := resourceQuery.Pluck("id", &resourceIDs).Error; err != nil {
			return err
		}
		if len(resourceIDs) > 0 {
			message := "iCloud forwarding domain is no longer authorized."
			if err := tx.Table("icloud_resources").Where("id IN ?", resourceIDs).Updates(map[string]any{
				"status": "abnormal", "next_validation_at": nil, "next_provision_at": nil,
				"validation_generation": gorm.Expr("validation_generation + 1"),
				"last_safe_error":       message, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			if err := tx.Table("icloud_maintenance_runs").
				Where("resource_id IN ? AND status IN ?", resourceIDs, []string{"queued", "running"}).
				Updates(map[string]any{"status": "canceled", "finished_at": now, "last_safe_error": message, "updated_at": now}).Error; err != nil {
				return err
			}
			if err := tx.Table("email_resources").Where("id IN ? AND type = ?", resourceIDs, "icloud").
				Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return unauthorizedICloudForwardingQuery(
			tx.Table("icloud_aliases").Where("status = ?", "normal"), "forward_to_email", domains,
		).Updates(map[string]any{"status": "disabled", "updated_at": now}).Error
	})
}

func unauthorizedICloudForwardingQuery(query *gorm.DB, column string, domains []string) *gorm.DB {
	if len(domains) == 0 {
		return query
	}
	return query.Where("LOWER(SUBSTR("+column+", INSTR("+column+", '@') + 1)) NOT IN ?", domains)
}
