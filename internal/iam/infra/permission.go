package infra

import (
	"context"
	"fmt"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"github.com/donnel666/remail/internal/iam/domain"
	"gorm.io/gorm"
)

const casbinModel = `
[request_definition]
r = sub, role, obj, act

[policy_definition]
p = sub, obj, act, eft

[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))

[matchers]
m = (r.sub == p.sub || r.role == p.sub) && keyMatch2(r.obj, p.obj) && r.act == p.act
`

// PermissionService checks IAM permissions with Casbin policies.
type PermissionService struct {
	enforcer *casbin.SyncedEnforcer
	db       *gorm.DB
}

// NewPermissionService creates a Casbin-backed permission checker.
func NewPermissionService(db *gorm.DB) (*PermissionService, error) {
	m, err := model.NewModelFromString(casbinModel)
	if err != nil {
		return nil, fmt.Errorf("casbin model: %w", err)
	}

	adapter, err := gormadapter.NewAdapterByDB(db)
	if err != nil {
		return nil, fmt.Errorf("casbin adapter: %w", err)
	}

	enforcer, err := casbin.NewSyncedEnforcer(m, adapter)
	if err != nil {
		return nil, fmt.Errorf("casbin enforcer: %w", err)
	}
	if err := enforcer.LoadPolicy(); err != nil {
		return nil, fmt.Errorf("casbin load policy: %w", err)
	}

	return &PermissionService{enforcer: enforcer, db: db}, nil
}

// Check returns whether a user has the requested resource/action permission.
func (s *PermissionService) Check(ctx context.Context, userID uint, roleLevel domain.RoleLevel, resource, action string) (bool, error) {
	userSub := fmt.Sprintf("user:%d", userID)
	roleSub := "role:" + roleLevel.Name()
	allowed, err := s.enforcer.Enforce(userSub, roleSub, resource, action)
	if err != nil {
		return false, fmt.Errorf("casbin enforce: %w", err)
	}
	return allowed, nil
}

// Reload refreshes policies from storage.
func (s *PermissionService) Reload(ctx context.Context) error {
	if err := s.enforcer.LoadPolicy(); err != nil {
		return fmt.Errorf("casbin reload policy: %w", err)
	}
	return nil
}

func (s *PermissionService) ListUserPermissionPolicies(ctx context.Context, userID uint) ([]domain.PermissionPolicy, error) {
	var models []CasbinRuleModel
	sub := fmt.Sprintf("user:%d", userID)
	if err := s.db.WithContext(ctx).Where("ptype = ? AND v0 = ?", "p", sub).Order("v1 ASC, v2 ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list user permissions: %w", err)
	}
	policies := make([]domain.PermissionPolicy, len(models))
	for i, m := range models {
		policies[i] = domain.PermissionPolicy{Resource: m.V1, Action: m.V2, Effect: m.V3}
	}
	return policies, nil
}

func (s *PermissionService) ReplaceUserPermissionPolicies(ctx context.Context, userID uint, policies []domain.PermissionPolicy) error {
	sub := fmt.Sprintf("user:%d", userID)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("ptype = ? AND v0 = ?", "p", sub).Delete(&CasbinRuleModel{}).Error; err != nil {
			return fmt.Errorf("delete user permissions: %w", err)
		}
		for _, policy := range policies {
			model := &CasbinRuleModel{
				Ptype: "p",
				V0:    sub,
				V1:    policy.Resource,
				V2:    policy.Action,
				V3:    policy.Effect,
			}
			if err := tx.Create(model).Error; err != nil {
				return fmt.Errorf("create user permission: %w", err)
			}
		}
		return nil
	})
}
