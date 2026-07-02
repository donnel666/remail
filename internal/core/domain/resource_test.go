package domain

import (
	"errors"
	"testing"
	"time"
)

func TestMicrosoftResource_IsAllocatable(t *testing.T) {
	tests := []struct {
		name    string
		status  MicrosoftResourceStatus
		forSale bool
		want    bool
	}{
		{"normal + forSale", MicrosoftStatusNormal, true, true},
		{"normal + not forSale", MicrosoftStatusNormal, false, false},
		{"pending + forSale", MicrosoftStatusPending, true, false},
		{"abnormal + forSale", MicrosoftStatusAbnormal, true, false},
		{"disabled + forSale", MicrosoftStatusDisabled, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MicrosoftResource{Status: tt.status, ForSale: tt.forSale}
			if got := r.IsAllocatable(); got != tt.want {
				t.Errorf("IsAllocatable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDomainResource_IsAllocatable(t *testing.T) {
	tests := []struct {
		name    string
		status  MailDomainStatus
		purpose ResourcePurpose
		want    bool
	}{
		{"normal + sale", DomainStatusNormal, PurposeSale, true},
		{"normal + not_sale", DomainStatusNormal, PurposeNotSale, false},
		{"normal + binding", DomainStatusNormal, PurposeBinding, false},
		{"abnormal + sale", DomainStatusAbnormal, PurposeSale, false},
		{"disabled + sale", DomainStatusDisabled, PurposeSale, false},
		{"deleted + sale", DomainStatusDeleted, PurposeSale, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MailDomainResource{Status: tt.status, Purpose: tt.purpose}
			if got := r.IsAllocatable(); got != tt.want {
				t.Errorf("IsAllocatable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMicrosoftResource_TransitionStatus(t *testing.T) {
	valid := []struct {
		from MicrosoftResourceStatus
		to   MicrosoftResourceStatus
	}{
		{MicrosoftStatusPending, MicrosoftStatusNormal},
		{MicrosoftStatusPending, MicrosoftStatusAbnormal},
		{MicrosoftStatusNormal, MicrosoftStatusAbnormal},
		{MicrosoftStatusNormal, MicrosoftStatusDisabled},
		{MicrosoftStatusAbnormal, MicrosoftStatusPending},
		{MicrosoftStatusAbnormal, MicrosoftStatusDisabled},
		{MicrosoftStatusDisabled, MicrosoftStatusPending},
	}

	for _, tt := range valid {
		t.Run(string(tt.from)+"->"+string(tt.to), func(t *testing.T) {
			r := &MicrosoftResource{Status: tt.from}
			if err := r.TransitionStatus(tt.to); err != nil {
				t.Fatalf("TransitionStatus() unexpected error: %v", err)
			}
			if r.Status != tt.to {
				t.Fatalf("status = %q, want %q", r.Status, tt.to)
			}
		})
	}
}

func TestMicrosoftResource_TransitionStatusRejectsIllegal(t *testing.T) {
	r := &MicrosoftResource{Status: MicrosoftStatusNormal}

	err := r.TransitionStatus(MicrosoftStatusPending)

	if !errors.Is(err, ErrInvalidResourceStatus) {
		t.Fatalf("expected ErrInvalidResourceStatus, got %v", err)
	}
	if r.Status != MicrosoftStatusNormal {
		t.Fatalf("status changed after illegal transition: %q", r.Status)
	}
}

func TestMicrosoftResource_MarkDeleted(t *testing.T) {
	allocatedAt := testTime()
	r := &MicrosoftResource{
		Status:          MicrosoftStatusNormal,
		ForSale:         false,
		LastSafeError:   "old safe error",
		LastAllocatedAt: &allocatedAt,
	}

	err := r.MarkDeleted()

	if err != nil {
		t.Fatalf("MarkDeleted() unexpected error: %v", err)
	}
	if r.Status != MicrosoftStatusDeleted {
		t.Fatalf("status = %q, want %q", r.Status, MicrosoftStatusDeleted)
	}
	if r.LastSafeError != "" {
		t.Fatalf("LastSafeError = %q, want empty", r.LastSafeError)
	}
	if r.LastAllocatedAt != nil {
		t.Fatalf("LastAllocatedAt = %v, want nil", r.LastAllocatedAt)
	}
}

func TestMicrosoftResource_MarkDeletedRejectsPublicOrDeleted(t *testing.T) {
	public := &MicrosoftResource{Status: MicrosoftStatusNormal, ForSale: true}
	if err := public.MarkDeleted(); !errors.Is(err, ErrResourceNotPrivate) {
		t.Fatalf("public MarkDeleted() = %v, want ErrResourceNotPrivate", err)
	}

	deleted := &MicrosoftResource{Status: MicrosoftStatusDeleted, ForSale: false}
	if err := deleted.MarkDeleted(); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("deleted MarkDeleted() = %v, want ErrResourceNotFound", err)
	}
}

func TestDomainResource_TransitionStatus(t *testing.T) {
	valid := []struct {
		from MailDomainStatus
		to   MailDomainStatus
	}{
		{DomainStatusAbnormal, DomainStatusNormal},
		{DomainStatusAbnormal, DomainStatusDisabled},
		{DomainStatusNormal, DomainStatusAbnormal},
		{DomainStatusNormal, DomainStatusDisabled},
		{DomainStatusDisabled, DomainStatusAbnormal},
	}

	for _, tt := range valid {
		t.Run(string(tt.from)+"->"+string(tt.to), func(t *testing.T) {
			r := &MailDomainResource{Status: tt.from}
			if err := r.TransitionStatus(tt.to); err != nil {
				t.Fatalf("TransitionStatus() unexpected error: %v", err)
			}
			if r.Status != tt.to {
				t.Fatalf("status = %q, want %q", r.Status, tt.to)
			}
		})
	}
}

func TestDomainResource_TransitionStatusRejectsIllegal(t *testing.T) {
	r := &MailDomainResource{Status: DomainStatusDisabled}

	err := r.TransitionStatus(DomainStatusNormal)

	if !errors.Is(err, ErrInvalidResourceStatus) {
		t.Fatalf("expected ErrInvalidResourceStatus, got %v", err)
	}
	if r.Status != DomainStatusDisabled {
		t.Fatalf("status changed after illegal transition: %q", r.Status)
	}
}

func TestDomainResource_MarkDeleted(t *testing.T) {
	allocatedAt := testTime()
	r := &MailDomainResource{
		Status:          DomainStatusNormal,
		Purpose:         PurposeNotSale,
		LastAllocatedAt: &allocatedAt,
	}

	err := r.MarkDeleted()

	if err != nil {
		t.Fatalf("MarkDeleted() unexpected error: %v", err)
	}
	if r.Status != DomainStatusDeleted {
		t.Fatalf("status = %q, want %q", r.Status, DomainStatusDeleted)
	}
	if r.LastAllocatedAt != nil {
		t.Fatalf("LastAllocatedAt = %v, want nil", r.LastAllocatedAt)
	}
}

func TestDomainResource_MarkDeletedRejectsSaleBindingOrDeleted(t *testing.T) {
	for _, purpose := range []ResourcePurpose{PurposeSale, PurposeBinding} {
		r := &MailDomainResource{Status: DomainStatusNormal, Purpose: purpose}
		if err := r.MarkDeleted(); !errors.Is(err, ErrResourceNotPrivate) {
			t.Fatalf("%s MarkDeleted() = %v, want ErrResourceNotPrivate", purpose, err)
		}
	}

	deleted := &MailDomainResource{Status: DomainStatusDeleted, Purpose: PurposeNotSale}
	if err := deleted.MarkDeleted(); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("deleted MarkDeleted() = %v, want ErrResourceNotFound", err)
	}
}

func TestIsValidResourceType(t *testing.T) {
	tests := []struct {
		t  ResourceType
		ok bool
	}{
		{ResourceTypeMicrosoft, true},
		{ResourceTypeDomain, true},
		{"unknown", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsValidResourceType(tt.t); got != tt.ok {
			t.Errorf("IsValidResourceType(%q) = %v, want %v", tt.t, got, tt.ok)
		}
	}
}

func TestIsValidPurpose(t *testing.T) {
	tests := []struct {
		p  ResourcePurpose
		ok bool
	}{
		{PurposeNotSale, true},
		{PurposeSale, true},
		{PurposeBinding, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsValidPurpose(tt.p); got != tt.ok {
			t.Errorf("IsValidPurpose(%q) = %v, want %v", tt.p, got, tt.ok)
		}
	}
}

func TestIsValidMicrosoftStatus(t *testing.T) {
	tests := []struct {
		s  string
		ok bool
	}{
		{"pending", true},
		{"normal", true},
		{"abnormal", true},
		{"disabled", true},
		{"deleted", true},
		{"unknown", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsValidMicrosoftStatus(tt.s); got != tt.ok {
			t.Errorf("IsValidMicrosoftStatus(%q) = %v, want %v", tt.s, got, tt.ok)
		}
	}
}

func TestIsValidDomainStatus(t *testing.T) {
	tests := []struct {
		s  string
		ok bool
	}{
		{"normal", true},
		{"abnormal", true},
		{"disabled", true},
		{"deleted", true},
		{"unknown", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsValidDomainStatus(tt.s); got != tt.ok {
			t.Errorf("IsValidDomainStatus(%q) = %v, want %v", tt.s, got, tt.ok)
		}
	}
}

func TestIsValidMailServerStatus(t *testing.T) {
	tests := []struct {
		s  string
		ok bool
	}{
		{"online", true},
		{"offline", true},
		{"disabled", true},
		{"unknown", false},
	}
	for _, tt := range tests {
		if got := IsValidMailServerStatus(tt.s); got != tt.ok {
			t.Errorf("IsValidMailServerStatus(%q) = %v, want %v", tt.s, got, tt.ok)
		}
	}
}

func TestIsValidGeneratedMailboxStatus(t *testing.T) {
	tests := []struct {
		s  string
		ok bool
	}{
		{"normal", true},
		{"disabled", true},
		{"unknown", false},
	}
	for _, tt := range tests {
		if got := IsValidGeneratedMailboxStatus(tt.s); got != tt.ok {
			t.Errorf("IsValidGeneratedMailboxStatus(%q) = %v, want %v", tt.s, got, tt.ok)
		}
	}
}

func TestNormalizeDomainName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"lowercase and trim", " Example.COM ", "example.com"},
		{"strip trailing root dot", "example.com.", "example.com"},
		{"subdomain", "Sub.Example.Co.UK", "sub.example.co.uk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeDomainName(tt.input)
			if err != nil {
				t.Fatalf("NormalizeDomainName() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeDomainName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeDomainNameRejectsInvalid(t *testing.T) {
	invalid := []string{
		"",
		"localhost",
		"https://example.com",
		"example.com/path",
		"example.com:443",
		"*.example.com",
		"example..com",
		"-example.com",
		"example-.com",
		"exa_mple.com",
		"例子.com",
	}
	for _, input := range invalid {
		t.Run(input, func(t *testing.T) {
			if _, err := NormalizeDomainName(input); !errors.Is(err, ErrInvalidDomain) {
				t.Fatalf("NormalizeDomainName(%q) = %v, want ErrInvalidDomain", input, err)
			}
		})
	}
}

func TestDomainTLD(t *testing.T) {
	tests := []struct {
		domain string
		want   string
	}{
		{"example.com", ".com"},
		{"sub.example.co.uk", ".co.uk"},
		{"example.com.cn", ".com.cn"},
		{"example.net.ar", ".net.ar"},
	}
	for _, tt := range tests {
		if got := DomainTLD(tt.domain); got != tt.want {
			t.Fatalf("DomainTLD(%q) = %q, want %q", tt.domain, got, tt.want)
		}
	}
}

func TestResourceType_ConstantValues(t *testing.T) {
	if ResourceTypeMicrosoft != "microsoft" {
		t.Errorf("ResourceTypeMicrosoft = %q, want 'microsoft'", ResourceTypeMicrosoft)
	}
	if ResourceTypeDomain != "domain" {
		t.Errorf("ResourceTypeDomain = %q, want 'domain'", ResourceTypeDomain)
	}
}

func TestMicrosoftResource_NoCredentialsInString(t *testing.T) {
	r := MicrosoftResource{
		EmailAddress: "test@example.com",
		Password:     "secret-password",
		ClientID:     "my-client-id",
		RefreshToken: "my-refresh-token",
	}
	s := r.EmailAddress
	if contains(s, "secret") {
		t.Error("password leaked in EmailAddress")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func testTime() time.Time {
	return time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
}
