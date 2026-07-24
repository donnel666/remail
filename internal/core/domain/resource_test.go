package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
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
		{"identifying + forSale", MicrosoftStatusIdentifying, true, false},
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
		{MicrosoftStatusPending, MicrosoftStatusValidating},
		{MicrosoftStatusPending, MicrosoftStatusDisabled},
		{MicrosoftStatusValidating, MicrosoftStatusPending},
		{MicrosoftStatusValidating, MicrosoftStatusIdentifying},
		{MicrosoftStatusValidating, MicrosoftStatusAbnormal},
		{MicrosoftStatusValidating, MicrosoftStatusDisabled},
		{MicrosoftStatusIdentifying, MicrosoftStatusPending},
		{MicrosoftStatusIdentifying, MicrosoftStatusNormal},
		{MicrosoftStatusIdentifying, MicrosoftStatusAbnormal},
		{MicrosoftStatusIdentifying, MicrosoftStatusDisabled},
		{MicrosoftStatusNormal, MicrosoftStatusPending},
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

	err := r.TransitionStatus(MicrosoftStatusValidating)

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
		{DomainStatusPending, DomainStatusValidating},
		{DomainStatusPending, DomainStatusDisabled},
		{DomainStatusValidating, DomainStatusPending},
		{DomainStatusValidating, DomainStatusNormal},
		{DomainStatusValidating, DomainStatusAbnormal},
		{DomainStatusValidating, DomainStatusDisabled},
		{DomainStatusAbnormal, DomainStatusPending},
		{DomainStatusAbnormal, DomainStatusNormal},
		{DomainStatusAbnormal, DomainStatusDisabled},
		{DomainStatusNormal, DomainStatusAbnormal},
		{DomainStatusNormal, DomainStatusPending},
		{DomainStatusNormal, DomainStatusDisabled},
		{DomainStatusDisabled, DomainStatusPending},
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

func TestDomainResource_DNSStatusCannotEnableDisabledOrDeleted(t *testing.T) {
	for _, status := range []MailDomainStatus{DomainStatusDisabled, DomainStatusDeleted} {
		r := &MailDomainResource{Status: status, LastSafeError: "keep diagnostic"}
		err := r.MarkDNSStatusAdmin(true)
		if status == DomainStatusDisabled && !errors.Is(err, ErrInvalidResourceStatus) {
			t.Fatalf("disabled MarkDNSStatusAdmin() = %v, want ErrInvalidResourceStatus", err)
		}
		if status == DomainStatusDeleted && !errors.Is(err, ErrResourceNotFound) {
			t.Fatalf("deleted MarkDNSStatusAdmin() = %v, want ErrResourceNotFound", err)
		}
		if r.Status != status {
			t.Fatalf("status changed from %q to %q", status, r.Status)
		}
	}

	r := &MailDomainResource{Status: DomainStatusDisabled, LastSafeError: "old diagnostic"}
	if err := r.EnableAdmin(); err != nil {
		t.Fatalf("EnableAdmin() unexpected error: %v", err)
	}
	if r.Status != DomainStatusPending || r.LastSafeError != "" {
		t.Fatalf("EnableAdmin() status/error = %q/%q", r.Status, r.LastSafeError)
	}
}

func TestValidationRetryStartsNewGenerationAndClearsBusinessFailures(t *testing.T) {
	microsoft := &MicrosoftResource{
		Status: MicrosoftStatusValidating, ValidationGeneration: 7, ValidationFailures: 2, LastSafeError: "old",
	}
	changed, err := microsoft.QueueValidationAdmin()
	if err != nil || !changed {
		t.Fatalf("Microsoft QueueValidationAdmin() = %v, %v", changed, err)
	}
	if microsoft.Status != MicrosoftStatusPending || microsoft.ValidationGeneration != 8 || microsoft.ValidationFailures != 0 || microsoft.LastSafeError != "" {
		t.Fatalf("Microsoft retry state = %#v", microsoft)
	}

	domainResource := &MailDomainResource{
		Status: DomainStatusValidating, ValidationGeneration: 4, ValidationFailures: 2, LastSafeError: "old",
	}
	changed, err = domainResource.QueueValidationAdmin()
	if err != nil || !changed {
		t.Fatalf("Domain QueueValidationAdmin() = %v, %v", changed, err)
	}
	if domainResource.Status != DomainStatusPending || domainResource.ValidationGeneration != 5 || domainResource.ValidationFailures != 0 || domainResource.LastSafeError != "" {
		t.Fatalf("Domain retry state = %#v", domainResource)
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

func TestTLD(t *testing.T) {
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
		if got := TLD(tt.domain); got != tt.want {
			t.Fatalf("TLD(%q) = %q, want %q", tt.domain, got, tt.want)
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

func TestIsMicrosoftEmailDomain(t *testing.T) {
	cases := map[string]bool{
		"a@outlook.com":        true,
		"a@hotmail.com":        true,
		"a@outlook.fr":         true, // country variant in list
		"a@outlook.com.br":     true,
		"a@outlook.co.th":      true,
		"A@OutLook.CoM":        true,  // case-insensitive
		"a@outlook.com.":       true,  // trailing dot
		"a@outlook.co.uk":      false, // real MS variant, but not in the 32-list
		"a@hotmail.co.uk":      false, // excluded: only hotmail.com
		"a@live.com":           false, // excluded per policy
		"a@msn.com":            false, // excluded per policy
		"a@passport.com":       false,
		"a@icloud.com":         false,
		"a@gmail.com":          false,
		"a@alumni.sysu.edu.cn": false,
		"a@outlookx.com":       false, // outlook must be a whole first label
		"a@notoutlook.com":     false,
		"a@outlook":            false, // no TLD
		"noatsign":             false,
		"a@":                   false,
	}
	for email, want := range cases {
		if got := IsMicrosoftEmailDomain(email); got != want {
			t.Errorf("IsMicrosoftEmailDomain(%q) = %v, want %v", email, got, want)
		}
	}
}

func TestRuntimeDailyLimitsClampToStorageCapacity(t *testing.T) {
	runtimeconfig.Set("default_plus_daily_limit", "9223372036854775807")
	runtimeconfig.Set("default_mailbox_daily_limit", "9223372036854775807")
	defer runtimeconfig.Delete("default_plus_daily_limit")
	defer runtimeconfig.Delete("default_mailbox_daily_limit")

	if DefaultPlusDailyLimitValue() != maxConfiguredDailyLimit || DefaultMailboxDailyLimitValue() != maxConfiguredDailyLimit {
		t.Fatal("daily limits were not clamped")
	}
}
