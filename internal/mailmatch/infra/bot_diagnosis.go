package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const (
	codeDiagnosisMessagePageSize  = 100
	codeDiagnosisMessageScanLimit = 1000
)

type codeDiagnosisOrderRow struct {
	OrderNo                  string
	ProjectID                uint
	ProjectName              string
	ServiceMode              string
	Status                   string
	EmailResourceID          uint
	DeliveryStoredAt         *time.Time
	ResourceAbnormalRefunded bool
}

type codeDiagnosisProjectRuleRow struct {
	ProjectID  uint
	LooseMatch bool
	RuleType   string
	Pattern    string
}

func (r *Repo) LookupCodeDiagnosis(ctx context.Context, userID uint, email string) (app.CodeDiagnosisLookup, error) {
	var rows []codeDiagnosisOrderRow
	if err := r.dbFor(ctx).Raw(`
SELECT
    o.order_no,
	o.project_id,
	project.name AS project_name,
    o.service_mode,
    o.status,
    COALESCE(ma.resource_id, da.resource_id, ga.resource_id, ia.resource_id, 0) AS email_resource_id,
	message.created_at AS delivery_stored_at,
    (
        o.status IN ('refunded', 'failed')
        AND o.refund_tx_id IS NOT NULL
		AND EXISTS (
			SELECT 1
			FROM order_events refund_event
			WHERE refund_event.order_no = o.order_no
			  AND refund_event.event_type = 'order.refunded'
			  AND refund_event.operator_type = 'system'
		)
    ) AS resource_abnormal_refunded
FROM orders o
JOIN projects project ON project.id = o.project_id
LEFT JOIN microsoft_allocations ma
    ON ma.order_no = o.order_no AND o.allocation_type = 'microsoft'
LEFT JOIN domain_allocations da
    ON da.order_no = o.order_no AND o.allocation_type = 'domain'
LEFT JOIN gmail_allocations ga
    ON ga.order_no = o.order_no AND o.allocation_type = 'gmail'
LEFT JOIN icloud_allocations ia
    ON ia.order_no = o.order_no AND o.allocation_type = 'icloud'
LEFT JOIN mailmatch_order_delivery_heads h ON h.order_id = o.id
LEFT JOIN mailmatch_messages message ON message.id = h.message_id
WHERE o.user_id = ?
  AND o.delivery_email = ?
  AND o.order_no NOT LIKE 'HIST-%'
ORDER BY
    CASE WHEN o.status IN ('pending_payment', 'paid', 'active') THEN 0 ELSE 1 END ASC,
    o.created_at DESC,
    o.id DESC
LIMIT 2`, userID, email).Scan(&rows).Error; err != nil {
		return app.CodeDiagnosisLookup{}, fmt.Errorf("lookup code diagnosis orders: %w", err)
	}
	needsProjectRules := false
	for _, row := range rows {
		if row.DeliveryStoredAt == nil && row.EmailResourceID > 0 && (row.Status == "active" || row.Status == "completed") {
			needsProjectRules = true
			break
		}
	}
	var projectRules []codeDiagnosisProjectRuleRow
	if needsProjectRules {
		var err error
		projectRules, err = r.codeDiagnosisAlternativeProjectRules(ctx)
		if err != nil {
			return app.CodeDiagnosisLookup{}, err
		}
	}

	lookup := app.CodeDiagnosisLookup{Orders: make([]app.CodeDiagnosisOrderFact, len(rows))}
	for i, row := range rows {
		fact := app.CodeDiagnosisOrderFact{
			OrderNo: row.OrderNo, ProjectID: row.ProjectID, ProjectName: row.ProjectName,
			ServiceMode: row.ServiceMode, Status: row.Status,
			EmailResourceID: row.EmailResourceID, DeliveryStoredAt: row.DeliveryStoredAt,
			ResourceAbnormalRefunded: row.ResourceAbnormalRefunded,
		}
		if fact.DeliveryStoredAt == nil && fact.EmailResourceID > 0 && (fact.Status == "active" || fact.Status == "completed") {
			mismatch, err := r.codeDiagnosisProjectMismatch(ctx, userID, email, fact, projectRules)
			if err != nil {
				return app.CodeDiagnosisLookup{}, err
			}
			fact.ProjectMismatch = mismatch
		}
		lookup.Orders[i] = fact
	}
	return lookup, nil
}

func (r *Repo) codeDiagnosisProjectMismatch(
	ctx context.Context,
	userID uint,
	email string,
	order app.CodeDiagnosisOrderFact,
	projectRules []codeDiagnosisProjectRuleRow,
) (bool, error) {
	scope, err := r.LoadOrderScope(ctx, order.OrderNo, userID, false)
	if err != nil {
		return false, fmt.Errorf("load owned code diagnosis scope: %w", err)
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if scope == nil || scope.EmailResourceID != order.EmailResourceID || !strings.EqualFold(scope.Recipient, email) {
		return false, nil
	}
	start, end := app.OrderReadWindow(*scope, time.Now().UTC())
	if end.Before(start) {
		return false, nil
	}
	alternatives := codeDiagnosisAlternativeProjectScopes(*scope, projectRules)
	if len(alternatives) == 0 {
		return false, nil
	}
	allocationTable := codeDiagnosisAllocationTable(string(scope.AllocationType))
	if allocationTable == "" {
		return false, nil
	}
	recipientPredicate, recipientArgs, ok := codeDiagnosisOverlapRecipient(scope.AllocationType, email)
	if !ok {
		return false, nil
	}
	skewMinutes := max(2, int(min(
		runtimeconfig.Duration("read_window_skew_minutes", 2*time.Minute, time.Minute, 1),
		24*time.Hour,
	)/time.Minute))
	overlapGuard := fmt.Sprintf(`NOT EXISTS (
	SELECT 1
	FROM %s AS other_allocation
	JOIN orders AS other_order ON other_order.order_no = other_allocation.order_no
	WHERE other_order.id <> ?
	  AND other_allocation.resource_id = m.email_resource_id
	  AND (%s)
	  AND other_allocation.created_at <= TIMESTAMPADD(MINUTE, ?, m.received_at)
	  AND (other_allocation.released_at IS NULL OR other_allocation.released_at >= m.received_at)
	  AND (other_order.receive_started_at IS NULL OR other_order.receive_started_at <= TIMESTAMPADD(MINUTE, ?, m.received_at))
	  AND (other_order.service_mode = 'purchase' OR other_order.receive_until IS NULL OR other_order.receive_until >= m.received_at)
)`, allocationTable, recipientPredicate)
	overlapArgs := []any{scope.OrderID}
	overlapArgs = append(overlapArgs, recipientArgs...)
	overlapArgs = append(overlapArgs, skewMinutes, skewMinutes)

	base := r.dbFor(ctx).
		Table("mailmatch_messages AS m").
		Select("m.id, m.email_resource_id, m.resource_type, m.recipient, m.recipients_json, m.sender, m.subject, m.raw_body, m.body_preview, m.received_at").
		Joins("LEFT JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id").
		Where("m.email_resource_id = ? AND m.resource_type = ?", scope.EmailResourceID, scope.AllocationType).
		Where("m.received_at BETWEEN ? AND ?", start, end).
		Where("(m.recipient = ? OR JSON_CONTAINS(m.recipients_json, JSON_QUOTE(?)))", email, email).
		Where("("+effectiveMessageOwnerSQL+") IS NULL").
		Where("(CASE WHEN mp.message_id IS NULL THEN m.status ELSE mp.status END) = ?", "ignored").
		Where(overlapGuard, overlapArgs...)

	var cursorAt time.Time
	var cursorID uint
	scanned := 0
	foundMismatch := false
	for scanned < codeDiagnosisMessageScanLimit {
		remaining := codeDiagnosisMessageScanLimit - scanned
		pageLimit := codeDiagnosisMessagePageSize
		if remaining <= codeDiagnosisMessagePageSize {
			pageLimit = remaining + 1
		}
		query := base
		if !cursorAt.IsZero() {
			query = query.Where("m.received_at < ? OR (m.received_at = ? AND m.id < ?)", cursorAt, cursorAt, cursorID)
		}
		var messages []MessageModel
		if err := query.Order("m.received_at DESC, m.id DESC").Limit(pageLimit).Find(&messages).Error; err != nil {
			return false, fmt.Errorf("lookup code diagnosis messages: %w", err)
		}
		if len(messages) == 0 {
			return foundMismatch, nil
		}
		processCount := min(len(messages), remaining)
		for _, row := range messages[:processCount] {
			message := messageModelToDomain(row)
			if app.CodeDiagnosisMessageMatchesProject(message, *scope) {
				continue
			}
			matchesAlternative := false
			for _, alternative := range alternatives {
				if app.CodeDiagnosisMessageMatchesProject(message, alternative) {
					matchesAlternative = true
				}
			}
			if matchesAlternative {
				foundMismatch = true
			}
		}
		scanned += processCount
		if len(messages) > processCount {
			// ponytail: keep diagnosis work bounded; fail closed instead of
			// turning an exhausted scan into a false "no mismatch" result.
			return false, fmt.Errorf("lookup code diagnosis messages: scan limit %d exceeded", codeDiagnosisMessageScanLimit)
		}
		if len(messages) < pageLimit {
			return foundMismatch, nil
		}
		last := messages[len(messages)-1]
		cursorAt, cursorID = last.ReceivedAt, last.ID
	}
	return foundMismatch, nil
}

func codeDiagnosisAllocationTable(resourceType string) string {
	switch resourceType {
	case "microsoft":
		return "microsoft_allocations"
	case "domain":
		return "domain_allocations"
	case "gmail":
		return "gmail_allocations"
	case "icloud":
		return "icloud_allocations"
	}
	return ""
}

func codeDiagnosisOverlapRecipient(resourceType domain.ResourceType, email string) (string, []any, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	switch resourceType {
	case domain.ResourceTypeMicrosoft:
		_, _, canonical, ok := domain.RecipientAliasForms(email)
		if !ok {
			return "", nil, false
		}
		local, host, _ := strings.Cut(canonical, "@")
		return `other_allocation.email = ? OR (
			other_allocation.mailbox IN ('main', 'alias')
			AND LOWER(SUBSTRING_INDEX(other_allocation.email, '@', -1)) = ?
			AND REPLACE(LOWER(SUBSTRING_INDEX(other_allocation.email, '@', 1)), '.', '') = ?
		)`, []any{email, host, local}, true
	case domain.ResourceTypeGmail:
		return "other_allocation.email = ?", []any{email}, email != ""
	case domain.ResourceTypeDomain:
		canonical := mailbox.Normalize(email)
		if canonical == "" {
			return "", nil, false
		}
		local, host, _ := strings.Cut(canonical, "@")
		return `LOWER(SUBSTRING_INDEX(other_allocation.email, '@', -1)) = ?
			AND REPLACE(
				SUBSTRING_INDEX(LOWER(SUBSTRING_INDEX(other_allocation.email, '@', 1)), '+', 1),
				'.',
				''
			) = ?`, []any{host, local}, true
	case domain.ResourceTypeICloud:
		_, plusBase, _, ok := domain.RecipientAliasForms(email)
		if !ok {
			return "", nil, false
		}
		return `other_allocation.email = ?
			OR EXISTS (
				SELECT 1 FROM icloud_plus_aliases ipa
				WHERE ipa.resource_id = other_allocation.resource_id
				  AND ipa.alias_id = other_allocation.alias_id
				  AND ipa.email = ? AND ipa.status = 'normal'
			)
			OR EXISTS (
				SELECT 1 FROM icloud_dot_aliases ida
				WHERE ida.resource_id = other_allocation.resource_id
				  AND ida.alias_id = other_allocation.alias_id
				  AND ida.email = ? AND ida.status = 'normal'
			)`, []any{email, email, plusBase}, true
	}
	return "", nil, false
}

func (r *Repo) codeDiagnosisAlternativeProjectRules(ctx context.Context) ([]codeDiagnosisProjectRuleRow, error) {
	var rows []codeDiagnosisProjectRuleRow
	if err := r.dbFor(ctx).
		Table("projects AS project").
		Select("project.id AS project_id, project.loose_match, rule.rule_type, rule.pattern").
		Joins("JOIN project_mail_rules AS rule ON rule.project_id = project.id AND rule.enabled = 1").
		Where("project.status IN ?", []string{"listed", "delisted"}).
		Order("project.id ASC, rule.id ASC").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("lookup code diagnosis project rules: %w", err)
	}
	return rows, nil
}

func codeDiagnosisAlternativeProjectScopes(owned app.OrderScope, rows []codeDiagnosisProjectRuleRow) []app.OrderScope {
	scopes := make([]app.OrderScope, 0)
	indexByProject := make(map[uint]int)
	for _, row := range rows {
		if row.ProjectID == owned.ProjectID {
			continue
		}
		index, ok := indexByProject[row.ProjectID]
		if !ok {
			index = len(scopes)
			indexByProject[row.ProjectID] = index
			scopes = append(scopes, app.OrderScope{
				ProjectID: row.ProjectID, ServiceMode: owned.ServiceMode,
				AllocationType: owned.AllocationType, EmailResourceID: owned.EmailResourceID,
				Recipient: owned.Recipient, RecipientKind: owned.RecipientKind, LooseMatch: row.LooseMatch,
			})
		}
		scopes[index].Rules = append(scopes[index].Rules, app.MailRule{
			Type: app.MailRuleType(row.RuleType), Pattern: row.Pattern, Enabled: true,
		})
	}
	return scopes
}
