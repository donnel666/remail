package api

import (
	"context"
	"errors"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/iam/infra"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// UserFinder is the subset of UserRepository needed by middleware.
type UserFinder interface {
	FindByID(ctx context.Context, id uint) (*domain.User, error)
}

// IAMModule holds all wired dependencies for the IAM module.
type IAMModule struct {
	ActivationUseCase     *app.ActivationUseCase
	RegistrationUseCase   *app.RegistrationUseCase
	LoginUseCase          *app.LoginUseCase
	SessionUseCase        *app.SessionUseCase
	ChangePasswordUseCase *app.ChangePasswordUseCase
	PasswordResetUseCase  *app.PasswordResetUseCase
	AdminUseCase          *app.AdminUseCase
	CaptchaUseCase        *app.CaptchaUseCase
	EmailCodeUseCase      *app.EmailCodeUseCase
	PermissionChecker     app.PermissionChecker
	Hasher                *infra.Hasher
	UserRepo              UserFinder
	SessionStore          app.SessionStore
	CaptchaStore          app.CaptchaStore
	EmailCodeStore        app.EmailCodeStore
}

// NewIAMModule wires up all IAM dependencies.
func NewIAMModule(db *gorm.DB, rdb redis.UniversalClient, emailCodeSender app.EmailCodeSender) (*IAMModule, error) {
	if emailCodeSender == nil {
		return nil, errors.New("email code sender is required")
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

	emailCodeUseCase := app.NewEmailCodeUseCase(emailCodeStore, emailCodeSender, captchaStore)

	return &IAMModule{
		ActivationUseCase:     app.NewActivationUseCase(userRepo, hasher),
		RegistrationUseCase:   app.NewRegistrationUseCase(userRepo, hasher, emailCodeStore),
		LoginUseCase:          app.NewLoginUseCase(userRepo, hasher, sessionStore, captchaStore),
		SessionUseCase:        app.NewSessionUseCase(sessionStore, userRepo),
		ChangePasswordUseCase: app.NewChangePasswordUseCase(userRepo, hasher, sessionStore),
		PasswordResetUseCase:  app.NewPasswordResetUseCase(userRepo, hasher, sessionStore, emailCodeStore, emailCodeUseCase),
		AdminUseCase:          app.NewAdminUseCase(userRepo, sessionStore, userRepo, permissionService, operationLogRepo),
		CaptchaUseCase:        app.NewCaptchaUseCase(captchaStore),
		EmailCodeUseCase:      emailCodeUseCase,
		PermissionChecker:     permissionService,
		Hasher:                hasher,
		UserRepo:              userRepo,
		SessionStore:          sessionStore,
		CaptchaStore:          captchaStore,
		EmailCodeStore:        emailCodeStore,
	}, nil
}
