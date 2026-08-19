package api

import (
	"context"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	iamapp "github.com/donnel666/remail/internal/iam/app"
	lotteryapp "github.com/donnel666/remail/internal/lottery/app"
	lotteryinfra "github.com/donnel666/remail/internal/lottery/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type Module struct {
	Service *lotteryapp.Service
	Repo    *lotteryinfra.Repo
	Queue   *lotteryinfra.Queue
}

func NewModule(db *gorm.DB, client *asynq.Client, billing *billingapp.WalletUseCase, users iamapp.UserRepository, delivery mailapp.DeliveryPort) *Module {
	repo := lotteryinfra.NewRepo(db)
	queue := lotteryinfra.NewQueue(client)
	return &Module{
		Service: lotteryapp.NewService(repo, billing, iamUserDirectory{users: users}, delivery, queue),
		Repo:    repo,
		Queue:   queue,
	}
}

type iamUserDirectory struct{ users iamapp.UserRepository }

func (d iamUserDirectory) FindLotteryUser(ctx context.Context, userID uint) (*lotteryapp.User, error) {
	if d.users == nil {
		return nil, nil
	}
	user, err := d.users.FindByID(ctx, userID)
	if err != nil || user == nil {
		return nil, err
	}
	return &lotteryapp.User{ID: user.ID, Email: user.Email, Status: user.Status.String(), Role: user.Role.String(), CreatedAt: user.CreatedAt}, nil
}

func (d iamUserDirectory) LookupLotteryUsers(ctx context.Context, userIDs []uint) (map[uint]lotteryapp.User, error) {
	result := make(map[uint]lotteryapp.User, len(userIDs))
	if d.users == nil || len(userIDs) == 0 {
		return result, nil
	}
	users, err := d.users.FindByIDs(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	for _, user := range users {
		result[user.ID] = lotteryapp.User{ID: user.ID, Email: user.Email, Status: user.Status.String(), Role: user.Role.String(), CreatedAt: user.CreatedAt}
	}
	return result, nil
}
