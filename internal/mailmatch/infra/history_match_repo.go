package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type microsoftResourceProjectMatchModel struct {
	ResourceID     uint      `gorm:"primaryKey;column:resource_id"`
	ProjectID      uint      `gorm:"primaryKey;column:project_id"`
	FirstMatchedAt time.Time `gorm:"column:first_matched_at"`
	LastMatchedAt  time.Time `gorm:"column:last_matched_at"`
	EvidenceCount  int       `gorm:"column:evidence_count"`
	LastScannedAt  time.Time `gorm:"column:last_scanned_at"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (microsoftResourceProjectMatchModel) TableName() string {
	return "microsoft_resource_project_matches"
}

func (r *Repo) ListHistoricalProjectScopes(ctx context.Context) ([]app.HistoricalProjectScope, error) {
	var rows []struct {
		ProjectID  uint   `gorm:"column:project_id"`
		LooseMatch bool   `gorm:"column:loose_match"`
		RuleType   string `gorm:"column:rule_type"`
		Pattern    string
		Enabled    bool
	}
	if err := r.dbFor(ctx).
		Table("projects AS p").
		Select("p.id AS project_id, p.loose_match, pmr.rule_type, pmr.pattern, pmr.enabled").
		Joins("JOIN project_mail_rules AS pmr ON pmr.project_id = p.id AND pmr.enabled = 1").
		Where("p.status IN ?", []string{"listed", "delisted"}).
		Where(`EXISTS (
			SELECT 1
			FROM project_products pp
			WHERE pp.project_id = p.id
			  AND pp.type = 'microsoft'
		)`).
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
				ProjectID:  row.ProjectID,
				LooseMatch: row.LooseMatch,
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

func (r *Repo) UpsertMicrosoftProjectMatches(ctx context.Context, matches []app.HistoricalProjectMatch) error {
	if len(matches) == 0 {
		return nil
	}
	models := make([]microsoftResourceProjectMatchModel, len(matches))
	for i := range matches {
		models[i] = microsoftResourceProjectMatchModel{
			ResourceID:     matches[i].ResourceID,
			ProjectID:      matches[i].ProjectID,
			FirstMatchedAt: matches[i].FirstMatchedAt.UTC(),
			LastMatchedAt:  matches[i].LastMatchedAt.UTC(),
			EvidenceCount:  matches[i].EvidenceCount,
			LastScannedAt:  matches[i].ScannedAt.UTC(),
		}
	}
	err := r.dbFor(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "resource_id"}, {Name: "project_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"first_matched_at": gorm.Expr("LEAST(first_matched_at, VALUES(first_matched_at))"),
			"last_matched_at":  gorm.Expr("GREATEST(last_matched_at, VALUES(last_matched_at))"),
			"evidence_count":   gorm.Expr("GREATEST(evidence_count, VALUES(evidence_count))"),
			"last_scanned_at":  gorm.Expr("GREATEST(last_scanned_at, VALUES(last_scanned_at))"),
		}),
	}).CreateInBatches(models, 100).Error
	if err != nil {
		return fmt.Errorf("upsert microsoft resource project matches: %w", err)
	}
	return nil
}
