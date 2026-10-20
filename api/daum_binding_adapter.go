package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	dauminfra "github.com/donnel666/remail/internal/daum/infra"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
)

type daumPickupScopeReader interface {
	LoadPickupScope(context.Context, string, string) (*mailmatchapp.OrderScope, error)
}

type daumBindingResolver struct {
	db     *gorm.DB
	pickup daumPickupScopeReader
}

func (r daumBindingResolver) Resolve(ctx context.Context, input dauminfra.BindingInput) (dauminfra.BindingResolution, error) {
	result := dauminfra.BindingResolution{Status: "unlinked", LastSafeError: "The system auxiliary mailbox was not found."}
	if r.db == nil {
		return result, errors.New("daum binding database is unavailable")
	}
	db := r.db.WithContext(ctx)
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx.WithContext(ctx)
	}
	address := strings.ToLower(strings.TrimSpace(input.BindingAddress))
	_, suffix, ok := strings.Cut(address, "@")
	if !ok || suffix == "" {
		result.Status, result.LastSafeError = "conflict", "The auxiliary mailbox address is invalid."
		return result, nil
	}
	if input.PickupURL != "" {
		email, token, err := dauminfra.ParsePickupURL(input.PickupURL)
		if err != nil || email != address {
			result.Status, result.LastSafeError = "conflict", "The remail pickup link does not match the auxiliary mailbox."
			return result, nil
		}
		if r.pickup == nil {
			return result, errors.New("daum auxiliary pickup lookup is unavailable")
		}
		scope, err := r.pickup.LoadPickupScope(ctx, token, address)
		if errors.Is(err, mailmatchdomain.ErrPickupCredentialInvalid) {
			result.Status, result.LastSafeError = "unavailable", "The remail pickup credential is invalid or expired."
			return result, nil
		}
		if err != nil {
			return result, fmt.Errorf("resolve Daum auxiliary pickup: %w", err)
		}
		if scope == nil || scope.EmailResourceID == 0 || scope.OrderNo == "" || !strings.EqualFold(scope.Recipient, address) {
			result.Status, result.LastSafeError = "conflict", "The remail pickup link does not reference the requested system mailbox."
			return result, nil
		}
		if input.OwnerUserID == 0 || scope.UserID != input.OwnerUserID {
			result.Status, result.LastSafeError = "conflict", "The auxiliary mailbox order belongs to another user."
			return result, nil
		}
		if (scope.OrderStatus != "active" && scope.OrderStatus != "completed") ||
			(scope.ServiceMode != "purchase" && scope.ReceiveUntil != nil && !scope.ReceiveUntil.After(time.Now())) {
			result.Status, result.LastSafeError = "unavailable", "The auxiliary mailbox order is no longer readable."
			return result, nil
		}
		tables := map[mailmatchdomain.ResourceType]string{
			mailmatchdomain.ResourceTypeMicrosoft: "microsoft_resources", mailmatchdomain.ResourceTypeMicrosoftEnterprise: "microsoft_enterprise_resources", mailmatchdomain.ResourceTypeDomain: "domain_resources", mailmatchdomain.ResourceTypeGmail: "gmail_resources", mailmatchdomain.ResourceTypeICloud: "icloud_resources", mailmatchdomain.ResourceTypeProto: "proto_resources", mailmatchdomain.ResourceTypeDaum: "daum_resources",
		}
		table, supported := tables[scope.AllocationType]
		if !supported {
			result.Status, result.LastSafeError = "conflict", "Unsupported auxiliary mailbox resource type."
			return result, nil
		}
		var count int64
		source := db.Table(table+" source").Joins("JOIN email_resources root ON root.id=source.id AND root.type=?", string(scope.AllocationType)).Where("source.id=? AND source.status<>'deleted'", scope.EmailResourceID)
		if scope.AllocationType == mailmatchdomain.ResourceTypeDomain {
			source = source.Where("source.status='normal' AND LOWER(source.domain)=?", suffix)
		}
		allocationTable := strings.TrimSuffix(table, "_resources") + "_allocations"
		allocation := db.Table(allocationTable+" allocation").Select("1").Where("allocation.resource_id=source.id AND allocation.order_no=? AND LOWER(allocation.email)=? AND allocation.status='allocated'", scope.OrderNo, address)
		if scope.AllocationType == mailmatchdomain.ResourceTypeGmail {
			allocation = allocation.Where("allocation.source='local'")
		}
		source = source.Where("EXISTS (?)", allocation)

		if err := source.Count(&count).Error; err != nil {
			return result, fmt.Errorf("resolve Daum auxiliary resource: %w", err)
		}
		if count == 0 {
			return result, nil
		}
		result.Status, result.LastSafeError, result.OrderNo = "linked", "", scope.OrderNo
		id := scope.EmailResourceID
		result.MailboxResourceID = &id
		if scope.AllocationType == mailmatchdomain.ResourceTypeDomain {
			result.DomainResourceID = &id
		}
		return result, nil
	}
	query := db.Table("domain_resources source").Select("source.id").Joins("JOIN email_resources root ON root.id=source.id AND root.type='domain' AND root.owner_user_id=source.owner_user_id").Where("LOWER(source.domain)=? AND source.status='normal' AND source.owner_user_id=?", suffix, input.OwnerUserID)
	var resource struct{ ID uint }
	if err := query.Limit(1).Scan(&resource).Error; err != nil {
		return result, fmt.Errorf("resolve Daum auxiliary domain: %w", err)
	}
	if resource.ID == 0 {
		return result, nil
	}
	result.Status, result.LastSafeError, result.DomainResourceID = "linked", "", &resource.ID
	return result, nil
}

// Microsoft binding readers keep their contract; domain usage includes Daum.
type daumBindingQuery struct {
	coreapp.BindingQueryPort
	db *gorm.DB
}

func (q daumBindingQuery) CountActiveByDomains(ctx context.Context, domains []string) (map[string]int64, error) {
	counts, err := q.BindingQueryPort.CountActiveByDomains(ctx, domains)
	if err != nil || len(domains) == 0 {
		return counts, err
	}
	if counts == nil {
		counts = map[string]int64{}
	}
	db := q.db.WithContext(ctx)
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx.WithContext(ctx)
	}
	var rows []struct {
		Domain string
		Count  int64
	}
	if err := db.Table("daum_binding_mailboxes AS binding").
		Select("LOWER(SUBSTRING_INDEX(binding.binding_address, '@', -1)) AS domain, COUNT(*) AS count").
		Joins("JOIN daum_resources AS resource ON resource.id = binding.resource_id AND resource.status <> 'deleted'").
		Where("LOWER(SUBSTRING_INDEX(binding.binding_address, '@', -1)) IN ?", domains).
		Group("LOWER(SUBSTRING_INDEX(binding.binding_address, '@', -1))").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count Daum auxiliary domain usage: %w", err)
	}
	for _, row := range rows {
		counts[row.Domain] += row.Count
	}
	return counts, nil
}
