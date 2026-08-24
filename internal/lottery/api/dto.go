package api

import (
	"time"

	lotterydomain "github.com/donnel666/remail/internal/lottery/domain"
)

type CreateLotteryRequest struct {
	Title               string                    `json:"title"`
	LotteryType         lotterydomain.LotteryType `json:"lotteryType,omitempty"`
	TotalAmount         string                    `json:"totalAmount"`
	PoolIncrementAmount string                    `json:"poolIncrementAmount,omitempty"`
	MinPayout           string                    `json:"minPayout"`
	MaxPayout           string                    `json:"maxPayout"`
	TierWeights         lotterydomain.TierWeights `json:"tierWeights"`
	MinAccountAgeDays   int                       `json:"minAccountAgeDays"`
	DrawAt              *time.Time                `json:"drawAt,omitempty"`
	ParticipantTarget   *int                      `json:"participantTarget,omitempty"`
}

type LotteryResponse struct {
	ID                  uint                      `json:"id"`
	PublicToken         string                    `json:"publicToken"`
	PublicURL           string                    `json:"publicUrl"`
	Title               string                    `json:"title"`
	LotteryType         lotterydomain.LotteryType `json:"lotteryType"`
	StartingAmount      string                    `json:"startingAmount"`
	TotalAmount         string                    `json:"totalAmount"`
	PoolIncrementAmount string                    `json:"poolIncrementAmount"`
	MinPayout           string                    `json:"minPayout"`
	MaxPayout           string                    `json:"maxPayout"`
	TierWeights         lotterydomain.TierWeights `json:"tierWeights"`
	MinAccountAgeDays   int                       `json:"minAccountAgeDays"`
	DrawAt              *time.Time                `json:"drawAt,omitempty"`
	ParticipantTarget   *int                      `json:"participantTarget,omitempty"`
	ParticipantCount    int                       `json:"participantCount"`
	MaxParticipants     int                       `json:"maxParticipants"`
	AlgorithmVersion    string                    `json:"algorithmVersion"`
	Status              string                    `json:"status"`
	TriggeredBy         string                    `json:"triggeredBy,omitempty"`
	UnusedAmount        string                    `json:"unusedAmount"`
	CreatedAt           time.Time                 `json:"createdAt"`
	SettledAt           *time.Time                `json:"settledAt,omitempty"`
}

// PublicLotterySummary exposes only the campaign facts participants need to
// decide whether to join. ParticipantCount and the optional ParticipantTarget
// are display data; MaxParticipants and all allocation/account rules remain
// private.
type PublicLotterySummary struct {
	Title             string     `json:"title"`
	TotalAmount       string     `json:"totalAmount"`
	ParticipantCount  int        `json:"participantCount"`
	ParticipantTarget *int       `json:"participantTarget,omitempty"`
	DrawAt            *time.Time `json:"drawAt,omitempty"`
	Status            string     `json:"status"`
}

type PublicPayoutResponse struct {
	Amount string `json:"amount"`
}

type PublicLotteryResponse struct {
	Lottery    PublicLotterySummary  `json:"lottery"`
	HasEntered bool                  `json:"hasEntered"`
	MyPayout   *PublicPayoutResponse `json:"myPayout,omitempty"`
}

type PublicEntryResponse struct {
	Already bool `json:"already"`
}

type EntryResponse struct {
	ID           uint      `json:"id"`
	LotteryID    uint      `json:"lotteryId"`
	UserID       uint      `json:"userId"`
	RegisteredAt time.Time `json:"registeredAt"`
	Already      bool      `json:"already"`
}

type PayoutResponse struct {
	ID                   uint   `json:"id"`
	LotteryID            uint   `json:"lotteryId"`
	UserID               uint   `json:"userId"`
	Tier                 string `json:"tier"`
	Amount               string `json:"amount"`
	BillingTransactionNo string `json:"billingTransactionNo,omitempty"`
}

type LotteryListResponse struct {
	Items  []LotteryResponse `json:"items"`
	Total  int64             `json:"total"`
	Offset int               `json:"offset"`
	Limit  int               `json:"limit"`
}

type EntryListResponse struct {
	Items  []EntryResponse `json:"items"`
	Total  int64           `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}

type PayoutListResponse struct {
	Items  []PayoutResponse `json:"items"`
	Total  int64            `json:"total"`
	Offset int              `json:"offset"`
	Limit  int              `json:"limit"`
}

func lotteryResponse(item *lotterydomain.Lottery) LotteryResponse {
	lotteryType := item.LotteryType
	if !lotteryType.Valid() {
		lotteryType = lotterydomain.LotteryTypeFixed
	}
	startingAmount := item.StartingAmount
	if startingAmount == "" {
		startingAmount = item.TotalAmount
	}
	incrementAmount := item.PoolIncrementAmount
	if incrementAmount == "" {
		incrementAmount = "0.00"
	}
	return LotteryResponse{
		ID: item.ID, PublicToken: item.PublicToken, PublicURL: "/lottery/" + item.PublicToken,
		Title: item.Title, LotteryType: lotteryType, StartingAmount: startingAmount,
		TotalAmount: item.TotalAmount, PoolIncrementAmount: incrementAmount,
		MinPayout: item.MinPayout, MaxPayout: item.MaxPayout, TierWeights: item.TierWeights,
		MinAccountAgeDays: item.MinAccountAgeDays, DrawAt: item.DrawAt,
		ParticipantTarget: item.ParticipantTarget, ParticipantCount: item.ParticipantCount,
		MaxParticipants: item.MaxParticipants, AlgorithmVersion: item.AlgorithmVersion,
		Status: string(item.Status), TriggeredBy: string(item.TriggeredBy), UnusedAmount: item.UnusedAmount,
		CreatedAt: item.CreatedAt, SettledAt: item.SettledAt,
	}
}

func publicLotterySummary(item *lotterydomain.Lottery) PublicLotterySummary {
	return PublicLotterySummary{
		Title:             item.Title,
		TotalAmount:       item.TotalAmount,
		ParticipantCount:  item.ParticipantCount,
		ParticipantTarget: item.ParticipantTarget,
		DrawAt:            item.DrawAt,
		Status:            string(item.Status),
	}
}

func entryResponse(item lotterydomain.Entry, already bool) EntryResponse {
	return EntryResponse{ID: item.ID, LotteryID: item.LotteryID, UserID: item.UserID, RegisteredAt: item.RegisteredAt, Already: already}
}

func payoutResponse(item lotterydomain.Payout) PayoutResponse {
	return PayoutResponse{ID: item.ID, LotteryID: item.LotteryID, UserID: item.UserID, Tier: string(item.Tier), Amount: item.Amount, BillingTransactionNo: item.BillingTransactionNo}
}
