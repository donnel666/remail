package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

type adminDomainReadStub struct {
	filter  AdminDomainListFilter
	records []AdminDomainRecord
	facets  AdminDomainFacets
}

func (s *adminDomainReadStub) ListAdminDomains(_ context.Context, filter AdminDomainListFilter, _, _ int, _ uint) ([]AdminDomainRecord, int64, error) {
	s.filter = filter
	return s.records, int64(len(s.records)), nil
}

func (s *adminDomainReadStub) AdminDomainFacets(_ context.Context, filter AdminDomainListFilter) (*AdminDomainFacets, error) {
	s.filter = filter
	return &s.facets, nil
}

func (s *adminDomainReadStub) FindAdminDomain(_ context.Context, resourceID uint) (*AdminDomainRecord, error) {
	for i := range s.records {
		if s.records[i].ID == resourceID {
			return &s.records[i], nil
		}
	}
	return nil, domain.ErrResourceNotFound
}

type adminDomainOwnersStub struct {
	owners map[uint]AdminOwnerSummary
	err    error
}

func (s *adminDomainOwnersStub) GetByIDs(_ context.Context, ids []uint) (map[uint]AdminOwnerSummary, error) {
	result := make(map[uint]AdminOwnerSummary, len(ids))
	for _, id := range ids {
		if owner, ok := s.owners[id]; ok {
			result[id] = owner
		}
	}
	return result, nil
}

func (s *adminDomainOwnersStub) SearchAdminOwners(_ context.Context, search string, _ int) ([]AdminOwnerSummary, error) {
	if search == "owner@example.com" {
		return []AdminOwnerSummary{s.owners[9]}, nil
	}
	return []AdminOwnerSummary{}, nil
}

func (s *adminDomainOwnersStub) ValidateTargetOwner(_ context.Context, id uint) (*AdminOwnerSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	owner, ok := s.owners[id]
	if !ok {
		return nil, nil
	}
	return &owner, nil
}

type adminDomainCommandRepoStub struct {
	root     *domain.EmailResource
	resource *domain.MailDomainResource
}

func (s *adminDomainCommandRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*adminDomainCommandRepoStub) ReserveAdminCommand(context.Context, AdminResourceCommandReceipt) ([]byte, bool, error) {
	return nil, false, nil
}

func (*adminDomainCommandRepoStub) CompleteAdminCommand(context.Context, uint, string, []byte) error {
	return nil
}

func (s *adminDomainCommandRepoStub) LockAdminDomain(context.Context, uint) (*domain.EmailResource, *domain.MailDomainResource, error) {
	return s.root, s.resource, nil
}

func (*adminDomainCommandRepoStub) LockAdminDomainMailServer(context.Context, uint) (*domain.MailServer, error) {
	return nil, domain.ErrMailServerNotFound
}

func (*adminDomainCommandRepoStub) CreateAdminDomain(context.Context, *domain.EmailResource, *domain.MailDomainResource) error {
	return nil
}

func (*adminDomainCommandRepoStub) SaveAdminDomain(context.Context, *domain.EmailResource, *domain.MailDomainResource, uint64, uint) error {
	return nil
}

func (*adminDomainCommandRepoStub) ListAdminDomainIDs(context.Context, AdminDomainListFilter, int) ([]uint, error) {
	return nil, nil
}

type adminDomainLogStub struct{}

func (*adminDomainLogStub) Create(context.Context, *governancedomain.OperationLog) error { return nil }

func TestAdminDomainQueryUsesOwnerSearchDeletedViewAndCursor(t *testing.T) {
	now := time.Now().UTC()
	repo := &adminDomainReadStub{
		records: []AdminDomainRecord{
			{ID: 42, Version: 3, OwnerUserID: 9, Domain: "one.example.com", DomainTLD: "com", Purpose: domain.PurposeNotSale, Status: domain.DomainStatusDeleted, CreatedAt: now, UpdatedAt: now},
			{ID: 41, Version: 2, OwnerUserID: 9, Domain: "two.example.com", DomainTLD: "com", Purpose: domain.PurposeSale, Status: domain.DomainStatusDeleted, CreatedAt: now, UpdatedAt: now},
		},
		facets: AdminDomainFacets{Status: AdminDomainStatusFacets{Deleted: 2}},
	}
	owners := &adminDomainOwnersStub{owners: map[uint]AdminOwnerSummary{
		9: {ID: 9, Email: "owner@example.com", Nickname: "Owner", Role: "supplier", Enabled: true},
	}}
	query := NewAdminDomainQuery(repo)
	query.SetOwnerQuery(owners)

	result, err := query.List(context.Background(), AdminDomainListFilter{
		Search: " owner@example.com ", Status: domain.DomainStatusDeleted,
	}, 0, 1, 0)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(repo.filter.OwnerIDs) != 1 || repo.filter.OwnerIDs[0] != 9 {
		t.Fatalf("resolved owner IDs = %v, want [9]", repo.filter.OwnerIDs)
	}
	if repo.filter.Status != domain.DomainStatusDeleted {
		t.Fatalf("status = %q, want deleted", repo.filter.Status)
	}
	if len(result.Items) != 1 || result.Items[0].Owner.Email != "owner@example.com" {
		t.Fatalf("items = %+v", result.Items)
	}
	if result.NextAfterID == nil || *result.NextAfterID != 42 {
		t.Fatalf("nextAfterId = %v, want 42", result.NextAfterID)
	}
}

func TestAdminDomainBulkPublishPropagatesOwnerDependencyError(t *testing.T) {
	ownerDependencyErr := errors.New("iam unavailable")
	repo := &adminDomainCommandRepoStub{
		root:     &domain.EmailResource{ID: 42, Type: domain.ResourceTypeDomain, OwnerUserID: 9, Version: 1},
		resource: &domain.MailDomainResource{ID: 42, Purpose: domain.PurposeNotSale, Status: domain.DomainStatusNormal},
	}
	service := NewAdminDomainCommandService(repo, nil, nil, &adminDomainLogStub{})
	service.SetPorts(&adminDomainOwnersStub{err: ownerDependencyErr}, nil)

	_, err := service.ApplyBulk(context.Background(), "publish", AdminDomainBulkSelection{
		Mode: AdminDomainBulkIDs, ResourceIDs: []uint{42},
	}, 1, "domain-bulk-owner-dependency", "req-domain-bulk-owner-dependency", "/v1/admin/domains/bulk")
	if !errors.Is(err, ownerDependencyErr) {
		t.Fatalf("ApplyBulk() error = %v, want wrapped owner dependency error", err)
	}
}
