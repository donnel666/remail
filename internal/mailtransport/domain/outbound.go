package domain

import (
	"errors"
	"time"
)

type OutboundPurpose string

const (
	PurposeVerificationCode OutboundPurpose = "verification_code"
	PurposeSystemNotice     OutboundPurpose = "system_notification"
	PurposeSecurityNotice   OutboundPurpose = "security_notice"
)

var ErrDeliveryUnavailable = errors.New("mail delivery is temporarily unavailable")

type OutboundStatus string

const (
	OutboundStatusPending OutboundStatus = "pending"
	OutboundStatusSending OutboundStatus = "sending"
	OutboundStatusSent    OutboundStatus = "sent"
	OutboundStatusFailed  OutboundStatus = "failed"
)

type OutboundMessage struct {
	IdempotencyKey string
	Purpose        OutboundPurpose
	To             string
	Subject        string
	TextBody       string
	HTMLBody       string
}

type OutboundMail struct {
	IdempotencyKey string          `json:"idempotencyKey"`
	Purpose        OutboundPurpose `json:"purpose"`
	Recipient      string          `json:"recipient"`
	Subject        string          `json:"subject"`
	Status         OutboundStatus  `json:"status"`
	Retries        int             `json:"retries"`
	FailureReason  string          `json:"failureReason"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	SentAt         *time.Time      `json:"sentAt,omitempty"`
}

func NewOutboundMail(message OutboundMessage, now time.Time) *OutboundMail {
	return &OutboundMail{
		IdempotencyKey: message.IdempotencyKey,
		Purpose:        message.Purpose,
		Recipient:      message.To,
		Subject:        message.Subject,
		Status:         OutboundStatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func (m *OutboundMail) MarkSending(now time.Time) {
	m.Status = OutboundStatusSending
	m.Retries++
	m.FailureReason = ""
	m.UpdatedAt = now
}

func (m *OutboundMail) MarkSent(now time.Time) {
	m.Status = OutboundStatusSent
	m.FailureReason = ""
	m.UpdatedAt = now
	m.SentAt = &now
}

func (m *OutboundMail) MarkFailed(now time.Time, reason string) {
	m.Status = OutboundStatusFailed
	m.FailureReason = reason
	m.UpdatedAt = now
}
