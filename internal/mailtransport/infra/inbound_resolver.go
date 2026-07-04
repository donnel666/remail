package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"gorm.io/gorm"
)

type InboundResourceResolver struct {
	db *gorm.DB
}

func NewInboundResourceResolver(db *gorm.DB) *InboundResourceResolver {
	return &InboundResourceResolver{db: db}
}

func (r *InboundResourceResolver) ResolveInboundRecipient(ctx context.Context, email string) (*domain.InboundRecipient, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, domain.ErrInboundRecipientRejected
	}
	domainPart := recipientDomain(email)
	if domainPart == "" {
		return nil, domain.ErrInboundRecipientRejected
	}

	var resolved domain.InboundRecipient
	err := r.db.WithContext(ctx).Raw(`
SELECT gm.email AS email, gm.resource_id AS resource_id, gm.owner_user_id AS owner_user_id
FROM generated_mailboxes AS gm
JOIN domain_resources AS dr
  ON dr.id = gm.resource_id AND dr.owner_user_id = gm.owner_user_id
WHERE gm.email = ?
  AND gm.status = 'normal'
  AND dr.status NOT IN ('deleted', 'disabled')
LIMIT 1`, email).Scan(&resolved).Error
	if err != nil {
		return nil, fmt.Errorf("resolve generated mailbox: %w", err)
	}
	if resolved.ResourceID != 0 {
		return &resolved, nil
	}

	err = r.db.WithContext(ctx).Raw(`
SELECT ? AS email, dr.id AS resource_id, dr.owner_user_id AS owner_user_id
FROM domain_resources AS dr
WHERE dr.domain = ?
  AND dr.status NOT IN ('deleted', 'disabled')
LIMIT 1`, email, domainPart).Scan(&resolved).Error
	if err != nil {
		return nil, fmt.Errorf("resolve domain mailbox: %w", err)
	}
	if resolved.ResourceID == 0 {
		return nil, domain.ErrInboundRecipientRejected
	}
	return &resolved, nil
}

func recipientDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(email[at+1:]))
}

func isInboundRecipientRejected(err error) bool {
	return errors.Is(err, domain.ErrInboundRecipientRejected)
}
