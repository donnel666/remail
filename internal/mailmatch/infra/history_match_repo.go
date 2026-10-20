package infra

import (
	"context"
	"fmt"

	"github.com/donnel666/remail/internal/mailmatch/app"
)

func (r *Repo) ListHistoricalProjectScopes(ctx context.Context, enterprise bool) ([]app.HistoricalProjectScope, error) {
	return r.listHistoricalProjectScopes(ctx, 0, enterprise)
}

func (r *Repo) FindHistoricalProjectScope(ctx context.Context, projectID uint, enterprise bool) (*app.HistoricalProjectScope, error) {
	scopes, err := r.listHistoricalProjectScopes(ctx, projectID, enterprise)
	if err != nil || len(scopes) == 0 {
		return nil, err
	}
	return &scopes[0], nil
}

func (r *Repo) listHistoricalProjectScopes(ctx context.Context, projectID uint, enterprise bool) ([]app.HistoricalProjectScope, error) {
	var rows []struct {
		ProjectID               uint   `gorm:"column:project_id"`
		ProductID               uint   `gorm:"column:product_id"`
		CodeWindowMinutes       int    `gorm:"column:code_window_minutes"`
		ActivationWindowMinutes int    `gorm:"column:activation_window_minutes"`
		WarrantyMinutes         int    `gorm:"column:warranty_minutes"`
		LooseMatch              bool   `gorm:"column:loose_match"`
		RuleType                string `gorm:"column:rule_type"`
		Pattern                 string
		Enabled                 bool
	}
	productType := "microsoft"
	if enterprise {
		productType = "microsoft_enterprise"
	}
	query := r.dbFor(ctx).
		Table("projects AS p").
		Select("p.id AS project_id, pp.id AS product_id, pp.code_window_minutes, pp.activation_window_minutes, pp.warranty_minutes, p.loose_match, pmr.rule_type, pmr.pattern, pmr.enabled").
		Joins(`JOIN project_products AS pp ON pp.project_id = p.id AND pp.type = ?
			AND pp.id = (SELECT MIN(candidate.id) FROM project_products AS candidate WHERE candidate.project_id = p.id AND candidate.type = ?)`, productType, productType).
		Joins("JOIN project_mail_rules AS pmr ON pmr.project_id = p.id AND pmr.enabled = 1").
		Where("p.status IN ?", []string{"listed", "delisted"})
	if projectID > 0 {
		query = query.Where("p.id = ?", projectID)
	}
	if err := query.
		Order("p.id ASC, pmr.id ASC").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list historical project scopes: %w", err)
	}
	scopes := make([]app.HistoricalProjectScope, 0)
	indexByProject := make(map[uint]int)
	for _, row := range rows {
		index, ok := indexByProject[row.ProjectID]
		if !ok {
			index = len(scopes)
			indexByProject[row.ProjectID] = index
			scopes = append(scopes, app.HistoricalProjectScope{
				ProjectID: row.ProjectID, ProductID: row.ProductID,
				CodeWindowMinutes: row.CodeWindowMinutes, ActivationWindowMinutes: row.ActivationWindowMinutes,
				WarrantyMinutes: row.WarrantyMinutes, LooseMatch: row.LooseMatch,
			})
		}
		scopes[index].Rules = append(scopes[index].Rules, app.MailRule{
			Type:    app.MailRuleType(row.RuleType),
			Pattern: row.Pattern,
			Enabled: row.Enabled,
		})
	}
	return scopes, nil
}

func (r *Repo) ClearLegacyMicrosoftProjectHistory(ctx context.Context, resourceID uint, projectID uint, enterprise bool) error {
	if r == nil || resourceID == 0 {
		return fmt.Errorf("clear legacy microsoft project history: invalid resource")
	}
	table := "microsoft_resource_project_matches"
	if enterprise {
		table = "microsoft_enterprise_project_matches"
	}
	deleteSQL := "DELETE FROM " + table + " WHERE resource_id = ?"
	args := []any{resourceID}
	if projectID > 0 {
		deleteSQL += " AND project_id = ?"
		args = append(args, projectID)
	}
	if err := r.dbFor(ctx).Exec(deleteSQL, args...).Error; err != nil {
		return fmt.Errorf("delete migrated microsoft project history: %w", err)
	}
	return nil
}
