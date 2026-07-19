package domain

import (
	"testing"
)

func TestRoleHelpers(t *testing.T) {
	if !RoleAdmin.HasAdminAccess() || !RoleSuperAdmin.HasAdminAccess() {
		t.Fatal("admin roles must have admin access")
	}
	if RoleUser.HasAdminAccess() || RoleSupplier.HasAdminAccess() {
		t.Fatal("user and supplier must not have admin access")
	}
	if !RoleSupplier.HasSupplierAccess() || !RoleAdmin.HasSupplierAccess() || !RoleSuperAdmin.HasSupplierAccess() {
		t.Fatal("supplier, admin, and super_admin must have supplier access")
	}
	if RoleUser.HasSupplierAccess() {
		t.Fatal("user must not have supplier access")
	}
}

func TestRoleValidation(t *testing.T) {
	for _, role := range []Role{RoleUser, RoleSupplier, RoleAdmin, RoleSuperAdmin} {
		if !role.IsValid() {
			t.Fatalf("role %q should be valid", role)
		}
	}
	if Role("owner").IsValid() {
		t.Fatal("unknown role should be invalid")
	}
}

func TestUserStatusLifecycle(t *testing.T) {
	for _, status := range []UserStatus{UserStatusActive, UserStatusDisabled, UserStatusDeleted} {
		if !status.IsValid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if !UserStatusActive.IsActive() || UserStatusDisabled.IsActive() || UserStatusDeleted.IsActive() {
		t.Fatal("only active users may authenticate")
	}
	if !UserStatusDeleted.IsDeleted() || UserStatusActive.IsDeleted() || UserStatusDisabled.IsDeleted() {
		t.Fatal("only deleted status is logically deleted")
	}
	if UserStatus("unknown").IsValid() {
		t.Fatal("unknown user status should be invalid")
	}
}

func TestIsActivationNeeded(t *testing.T) {
	tests := []struct {
		name  string
		count int64
		want  bool
	}{
		{"no users => needed", 0, true},
		{"one user => not needed", 1, false},
		{"many users => not needed", 100, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsActivationNeeded(tt.count); got != tt.want {
				t.Errorf("IsActivationNeeded(%d) = %v, want %v", tt.count, got, tt.want)
			}
		})
	}
}

func TestCaptcha_IsExpired(t *testing.T) {
	c := &Captcha{
		ID:     "test",
		Answer: "1234",
	}
	// Without setting ExpiresAt, IsExpired should be true (zero time)
	if !c.IsExpired() {
		t.Error("captcha with zero ExpiresAt should be expired")
	}
}
