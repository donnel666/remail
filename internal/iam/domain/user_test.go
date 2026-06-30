package domain

import (
	"testing"
)

func TestRoleLevel_IsAtLeast(t *testing.T) {
	tests := []struct {
		name  string
		level RoleLevel
		min   RoleLevel
		want  bool
	}{
		{"user >= user", RoleUser, RoleUser, true},
		{"user >= supplier", RoleUser, RoleSupplier, false},
		{"user >= admin", RoleUser, RoleAdmin, false},
		{"user >= super_admin", RoleUser, RoleSuperAdmin, false},
		{"supplier >= user", RoleSupplier, RoleUser, true},
		{"supplier >= supplier", RoleSupplier, RoleSupplier, true},
		{"supplier >= admin", RoleSupplier, RoleAdmin, false},
		{"admin >= user", RoleAdmin, RoleUser, true},
		{"admin >= supplier", RoleAdmin, RoleSupplier, true},
		{"admin >= admin", RoleAdmin, RoleAdmin, true},
		{"admin >= super_admin", RoleAdmin, RoleSuperAdmin, false},
		{"super_admin >= user", RoleSuperAdmin, RoleUser, true},
		{"super_admin >= admin", RoleSuperAdmin, RoleAdmin, true},
		{"super_admin >= super_admin", RoleSuperAdmin, RoleSuperAdmin, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.level.IsAtLeast(tt.min); got != tt.want {
				t.Errorf("RoleLevel(%d).IsAtLeast(%d) = %v, want %v", tt.level, tt.min, got, tt.want)
			}
		})
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
