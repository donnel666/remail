package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type OutboundPurpose string

const (
	PurposeVerificationCode OutboundPurpose = "verification_code"
	PurposeSystemNotice     OutboundPurpose = "system_notification"
	PurposeSecurityNotice   OutboundPurpose = "security_notice"
)

var (
	ErrDeliveryUnavailable         = errors.New("mail delivery is temporarily unavailable")
	ErrOutboundIdempotencyConflict = errors.New("outbound mail idempotency key conflicts")
)

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
	From           string
	To             string
	ReplyTo        string
	Subject        string
	TextBody       string
	HTMLBody       string
}

type OutboundMail struct {
	ID             uint            `json:"id"`
	IdempotencyKey string          `json:"idempotencyKey"`
	RequestHash    string          `json:"requestHash"`
	Purpose        OutboundPurpose `json:"purpose"`
	Sender         string          `json:"sender"`
	Recipient      string          `json:"recipient"`
	ReplyTo        string          `json:"replyTo"`
	Subject        string          `json:"subject"`
	TextBody       string          `json:"textBody"`
	HTMLBody       string          `json:"htmlBody"`
	Status         OutboundStatus  `json:"status"`
	SendGeneration uint64          `json:"-"`
	Retries        int             `json:"retries"`
	FailureReason  string          `json:"failureReason"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	SentAt         *time.Time      `json:"sentAt,omitempty"`
}

func NewOutboundMail(message OutboundMessage, now time.Time) *OutboundMail {
	return &OutboundMail{
		IdempotencyKey: message.IdempotencyKey,
		RequestHash:    message.RequestHash(),
		Purpose:        message.Purpose,
		Sender:         message.From,
		Recipient:      message.To,
		ReplyTo:        message.ReplyTo,
		Subject:        message.Subject,
		TextBody:       message.TextBody,
		HTMLBody:       message.HTMLBody,
		Status:         OutboundStatusPending,
		SendGeneration: 1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func (m OutboundMessage) RequestHash() string {
	h := sha256.New()
	for _, part := range []any{m.Purpose, m.From, m.To, m.Subject, m.TextBody, m.HTMLBody} {
		_, _ = fmt.Fprint(h, part)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (m *OutboundMail) MarkSending(now time.Time) {
	m.Status = OutboundStatusSending
	m.FailureReason = ""
	m.UpdatedAt = now
}

func (m *OutboundMail) MarkPending(now time.Time, reason string) {
	m.Status = OutboundStatusPending
	m.SendGeneration++
	m.FailureReason = reason
	m.UpdatedAt = now
}

func (m *OutboundMail) ResetForRetry(now time.Time, reason string) {
	m.Retries = 0
	m.MarkPending(now, reason)
}

func (m *OutboundMail) RecordSendFailure(now time.Time, reason string, retryable bool) bool {
	if m.Retries < 3 {
		m.Retries++
	}
	if !retryable || m.Retries >= 3 {
		m.MarkFailed(now, reason)
		return true
	}
	m.MarkPending(now, reason)
	return false
}

func (m *OutboundMail) MarkSent(now time.Time) {
	m.Status = OutboundStatusSent
	m.Retries = 0
	m.FailureReason = ""
	m.UpdatedAt = now
	m.SentAt = &now
}

func (m *OutboundMail) MarkFailed(now time.Time, reason string) {
	m.Status = OutboundStatusFailed
	m.FailureReason = reason
	m.UpdatedAt = now
}
