package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
)

type generatedMailboxRetryRepo struct {
	Repository
	candidate DomainCandidate
	calls     int
}

func (*generatedMailboxRetryRepo) LockResourceRoot(context.Context, uint, domain.AllocationType) (bool, error) {
	return true, nil
}

func (r *generatedMailboxRetryRepo) LockDomainCandidate(context.Context, uint, uint, domain.SupplyScope, string) (*DomainCandidate, error) {
	return &r.candidate, nil
}

func (*generatedMailboxRetryRepo) EnsureDailyUsageAvailable(context.Context, string, domain.AllocationType, uint, domain.DailyUsageKind, int) error {
	return nil
}

func (*generatedMailboxRetryRepo) FindReusableGeneratedMailbox(context.Context, uint, uint) (*GeneratedMailboxCandidate, error) {
	return nil, nil
}

func (r *generatedMailboxRetryRepo) FindOrCreateGeneratedMailbox(_ context.Context, _ uint, _ uint, email string) (*GeneratedMailboxCandidate, error) {
	r.calls++
	if r.calls == 1 {
		return nil, domain.ErrAllocationConflict
	}
	return &GeneratedMailboxCandidate{ID: 7, Email: email}, nil
}

func (*generatedMailboxRetryRepo) CreateDomainAllocation(_ context.Context, allocation *domain.GeneratedMailboxAllocation) error {
	allocation.ID = 8
	allocation.CreatedAt = time.Now().UTC()
	return nil
}

func (*generatedMailboxRetryRepo) ConsumeDailyUsage(context.Context, string, domain.AllocationType, uint, domain.DailyUsageKind, int) error {
	return nil
}

func (*generatedMailboxRetryRepo) TouchDomainAllocated(context.Context, uint, uint, time.Time) error {
	return nil
}

func TestGeneratedMailboxVariantsUseHumanNamesAndUpToSixDigits(t *testing.T) {
	if len(biblicalMailboxNames) < 1_000 {
		t.Fatalf("got %d biblical names, want at least 1000", len(biblicalMailboxNames))
	}
	names := make(map[string]struct{}, generatedMailboxNameCount())
	for i := 0; i < generatedMailboxNameCount(); i++ {
		name := generatedMailboxName(i)
		if strings.Contains(name, ".") {
			t.Fatalf("generated mailbox base name contains a dot: %q", name)
		}
		names[name] = struct{}{}
	}
	if len(names) < 10_000 {
		t.Fatalf("got %d unique base names, want at least 10000", len(names))
	}
	variants := generatedMailboxVariants("Example.COM")
	if len(variants) != aliasGenerationWindow {
		t.Fatalf("got %d variants, want %d", len(variants), aliasGenerationWindow)
	}
	for _, email := range variants {
		local, domain, ok := splitEmail(email)
		name := strings.TrimRight(local, "0123456789")
		digits := strings.TrimPrefix(local, name)
		if _, known := names[name]; !ok || !known || strings.Contains(name, ".") || domain != "example.com" || len(digits) > 6 {
			t.Fatalf("unexpected generated mailbox %q", email)
		}
	}
}

func TestDomainAllocationTriesAnotherAddressAfterDisabledMailboxConflict(t *testing.T) {
	repo := &generatedMailboxRetryRepo{candidate: DomainCandidate{
		ResourceID: 1, OwnerUserID: 2, Domain: "example.com", MailboxDailyLimit: 10,
	}}
	result, err := NewUseCase(repo).tryDomainCandidate(
		context.Background(),
		AllocateCommand{OrderNo: "order-1", BuyerUserID: 3, SupplyScope: domain.SupplyScopeOwned},
		ProductAllocationConfig{ProjectID: 4, ProductID: 5},
		repo.candidate,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("tryDomainCandidate() unexpected error: %v", err)
	}
	if repo.calls != 2 || result == nil {
		t.Fatalf("generated mailbox attempts = %d, result = %#v; want two attempts and an allocation", repo.calls, result)
	}
}
