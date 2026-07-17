package api

import (
	"context"
	"errors"

	coreapp "github.com/donnel666/remail/internal/core/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/iam/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// UserFinder is the subset of UserRepository needed by middleware.
type UserFinder interface {
	FindByID(ctx context.Context, id uint) (*domain.User, error)
}

// IAMModule holds all wired dependencies for the IAM module.
type IAMModule struct {
	ActivationUseCase          *app.ActivationUseCase
	RegistrationUseCase        *app.RegistrationUseCase
	LoginUseCase               *app.LoginUseCase
	SessionUseCase             *app.SessionUseCase
	ChangePasswordUseCase      *app.ChangePasswordUseCase
	PasswordResetUseCase       *app.PasswordResetUseCase
	AdminUseCase               *app.AdminUseCase
	InviteUseCase              *app.InviteUseCase
	SupplierApplicationUseCase *app.SupplierApplicationUseCase
	CaptchaUseCase             *app.CaptchaUseCase
	EmailCodeUseCase           *app.EmailCodeUseCase
	PermissionChecker          app.PermissionChecker
	Hasher                     *infra.Hasher
	UserRepo                   UserFinder
	// Users is the concrete repo, exposed for cross-context wiring that needs
	// the batch user-summary lookups (e.g. billing's wallet directory).
	Users                      *infra.UserRepo
	SessionStore               app.SessionStore
	CaptchaStore               app.CaptchaStore
	EmailCodeStore             app.EmailCodeStore
	AdminResourceOwners        coreapp.OwnerQueryPort
	AdminUserSelectionResolver *AdminUserSelectionResolver
}

// NewIAMModule wires up all IAM dependencies.
func NewIAMModule(db *gorm.DB, rdb redis.UniversalClient, mailDelivery mailapp.DeliveryPort) (*IAMModule, error) {
	if mailDelivery == nil {
		return nil, errors.New("mail delivery is required")
	}

	hasher := infra.NewHasher()
	userRepo := infra.NewUserRepo(db)
	sessionStore := infra.NewSessionStore(rdb)
	captchaStore := infra.NewCaptchaStore(rdb)
	emailCodeStore := infra.NewEmailCodeStore(rdb)
	operationLogRepo := governanceinfra.NewOperationLogRepo(db)
	permissionService, err := infra.NewPermissionService(db)
	if err != nil {
		return nil, err
	}
	supplierApplicationRepo := infra.NewSupplierApplicationRepo(db)

	emailCodeUseCase := app.NewEmailCodeUseCase(emailCodeStore, mailDelivery, captchaStore)

	return &IAMModule{
		ActivationUseCase:          app.NewActivationUseCase(userRepo, hasher),
		RegistrationUseCase:        app.NewRegistrationUseCase(userRepo, hasher, emailCodeStore),
		LoginUseCase:               app.NewLoginUseCase(userRepo, hasher, sessionStore, captchaStore),
		SessionUseCase:             app.NewSessionUseCase(sessionStore, userRepo),
		ChangePasswordUseCase:      app.NewChangePasswordUseCase(userRepo, hasher, sessionStore),
		PasswordResetUseCase:       app.NewPasswordResetUseCase(userRepo, hasher, sessionStore, emailCodeStore, emailCodeUseCase),
		AdminUseCase:               app.NewAdminUseCase(userRepo, sessionStore, userRepo, permissionService, hasher, operationLogRepo),
		InviteUseCase:              app.NewInviteUseCase(userRepo),
		SupplierApplicationUseCase: app.NewSupplierApplicationUseCase(supplierApplicationRepo, userRepo),
		CaptchaUseCase:             app.NewCaptchaUseCase(captchaStore),
		EmailCodeUseCase:           emailCodeUseCase,
		PermissionChecker:          permissionService,
		Hasher:                     hasher,
		UserRepo:                   userRepo,
		Users:                      userRepo,
		SessionStore:               sessionStore,
		CaptchaStore:               captchaStore,
		EmailCodeStore:             emailCodeStore,
		AdminResourceOwners:        NewAdminResourceOwnerAdapter(userRepo),
		AdminUserSelectionResolver: NewAdminUserSelectionResolver(userRepo),
	}, nil
}
