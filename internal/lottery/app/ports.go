package app

import (
	"context"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	lotterydomain "github.com/donnel666/remail/internal/lottery/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
)

type User struct {
	ID        uint
	Email     string
	Status    string
	Role      string
	CreatedAt time.Time
}

type UserDirectory interface {
	FindLotteryUser(ctx context.Context, userID uint) (*User, error)
	LookupLotteryUsers(ctx context.Context, userIDs []uint) (map[uint]User, error)
}

type Repository interface {
	Create(ctx context.Context, lottery *lotterydomain.Lottery) error
	FindByIdempotency(ctx context.Context, userID uint, key string) (*lotterydomain.Lottery, error)

	GetByID(ctx context.Context, id uint) (*lotterydomain.Lottery, error)
	GetByToken(ctx context.Context, token string) (*lotterydomain.Lottery, error)
	List(ctx context.Context, filter ListFilter) (items []*lotterydomain.Lottery, total int64, err error)
	ListEntries(ctx context.Context, lotteryID uint, offset, limit int) ([]lotterydomain.Entry, int64, error)
	ListPayouts(ctx context.Context, lotteryID uint, offset, limit int) ([]lotterydomain.Payout, int64, error)

	AddEntry(ctx context.Context, lotteryID, userID uint, registeredAt time.Time, now func() time.Time) (*EntryResult, error)
	ListAllEntries(ctx context.Context, lotteryID uint) ([]lotterydomain.Entry, error)
	ClaimSettlement(ctx context.Context, lotteryID uint, now time.Time) (*lotterydomain.Lottery, error)
	GetPayouts(ctx context.Context, lotteryID uint) ([]lotterydomain.Payout, error)
	SavePayouts(ctx context.Context, lotteryID uint, payouts []lotterydomain.Payout) error
	RecordBillingTransactions(ctx context.Context, lotteryID uint, transactions map[uint]string, unusedAmount string) error
	Complete(ctx context.Context, lotteryID uint, status lotterydomain.Status, unusedAmount string, settledAt time.Time) error
}

type Queue interface {
	EnqueueDraw(ctx context.Context, lotteryID uint, at *time.Time) error
}

type ListFilter struct {
	Status string
	Offset int
	Limit  int
}

type EntryResult struct {
	Lottery       *lotterydomain.Lottery
	Entry         *lotterydomain.Entry
	AlreadyExists bool
}

type CreateRequest struct {
	CreatedByUserID   uint
	Title             string
	TotalAmount       string
	MinPayout         string
	MaxPayout         string
	TierWeights       lotterydomain.TierWeights
	MinAccountAgeDays int
	DrawAt            *time.Time
	ParticipantTarget *int
	IdempotencyKey    string
	RequestID         string
}

type CreateResult struct {
	Lottery  *lotterydomain.Lottery
	Replayed bool
}

type Service struct {
	repo     Repository
	billing  BillingPort
	users    UserDirectory
	delivery mailapp.DeliveryPort
	queue    Queue
	now      func() time.Time
}

type BillingPort interface {
	SettleLotteryPool(ctx context.Context, req billingapp.LotterySettlementRequest) (*billingapp.LotterySettlementResult, error)
}

func NewService(repo Repository, billing BillingPort, users UserDirectory, delivery mailapp.DeliveryPort, queue Queue) *Service {
	return &Service{repo: repo, billing: billing, users: users, delivery: delivery, queue: queue, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) SetNow(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}
