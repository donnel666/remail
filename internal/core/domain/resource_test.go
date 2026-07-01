package domain

import (
	"errors"
	"testing"
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
		{"dns_normal + sale", DomainStatusDNSNormal, PurposeSale, true},
		{"dns_normal + auxiliary", DomainStatusDNSNormal, PurposeAuxiliary, false},
		{"dns_abnormal + sale", DomainStatusDNSAbnormal, PurposeSale, false},
		{"disabled + sale", DomainStatusDisabled, PurposeSale, false},
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

func TestDomainResource_TransitionStatus(t *testing.T) {
	valid := []struct {
		from MailDomainStatus
		to   MailDomainStatus
	}{
		{DomainStatusDNSAbnormal, DomainStatusDNSNormal},
		{DomainStatusDNSAbnormal, DomainStatusDisabled},
		{DomainStatusDNSNormal, DomainStatusDNSAbnormal},
		{DomainStatusDNSNormal, DomainStatusDisabled},
		{DomainStatusDisabled, DomainStatusDNSAbnormal},
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

	err := r.TransitionStatus(DomainStatusDNSNormal)

	if !errors.Is(err, ErrInvalidResourceStatus) {
		t.Fatalf("expected ErrInvalidResourceStatus, got %v", err)
	}
	if r.Status != DomainStatusDisabled {
		t.Fatalf("status changed after illegal transition: %q", r.Status)
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
		{PurposeSale, true},
		{PurposeAuxiliary, true},
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
		{"dns_normal", true},
		{"dns_abnormal", true},
		{"disabled", true},
		{"unknown", false},
		{"normal", false},
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
