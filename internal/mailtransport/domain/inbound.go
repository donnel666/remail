package domain

import (
	"errors"
	"time"
)

var (
	ErrInboundRecipientRejected  = errors.New("inbound recipient is not accepted")
	ErrInboundStorageUnavailable = errors.New("inbound mail storage is temporarily unavailable")
	ErrInvalidAuxiliaryMailQuery = errors.New("invalid auxiliary mail query")
	ErrAuxiliaryResourceNotFound = errors.New("auxiliary mail resource not found")
	ErrAuxiliaryMessageNotFound  = errors.New("auxiliary mail message not found")
	ErrAuxiliaryMailUnavailable  = errors.New("auxiliary mail is temporarily unavailable")
)

type InboundStatus string

const (
	InboundStatusPending    InboundStatus = "pending"
	InboundStatusProcessing InboundStatus = "processing"
	InboundStatusStored     InboundStatus = "stored"
	InboundStatusFailed     InboundStatus = "failed"
)

type InboundResourceType string

const (
	InboundResourceMicrosoft InboundResourceType = "microsoft"
	InboundResourceDomain    InboundResourceType = "domain"
)

func IsValidInboundResourceType(value InboundResourceType) bool {
	return value == InboundResourceMicrosoft || value == InboundResourceDomain
}

type InboundRecipient struct {
	Email        string
	ResourceID   uint
	ResourceType InboundResourceType
	OwnerUserID  uint
}

type InboundMail struct {
	ID               uint                `json:"id"`
	EnvelopeFrom     string              `json:"envelopeFrom"`
	HeaderFrom       string              `json:"headerFrom"`
	Recipient        string              `json:"recipient"`
	Subject          string              `json:"subject"`
	BodyPreview      string              `json:"bodyPreview"`
	VerificationCode string              `json:"verificationCode"`
	MessageIDHeader  string              `json:"messageIdHeader"`
	ResourceID       uint                `json:"resourceId"`
	ResourceType     InboundResourceType `json:"resourceType"`
	OwnerUserID      uint                `json:"ownerUserId"`
	SourceObjectKey  string              `json:"sourceObjectKey"`
	Status           InboundStatus       `json:"status"`
	FailureReason    string              `json:"failureReason"`
	ReceivedAt       *time.Time          `json:"receivedAt"`
	ParsedAt         *time.Time          `json:"parsedAt"`
	CreatedAt        time.Time           `json:"createdAt"`
	UpdatedAt        time.Time           `json:"updatedAt"`
}

// InboundMailSummary is the bounded, safe summary extracted from an RFC822
// object during inbound processing. It deliberately excludes the raw message,
// private object key, transport envelope, and any dispatch/claim secret.
type InboundMailSummary struct {
	HeaderFrom       string
	Subject          string
	BodyPreview      string
	VerificationCode string
	MessageIDHeader  string
	ReceivedAt       time.Time
	ParsedAt         time.Time
}

func NewInboundMail(envelopeFrom string, recipient InboundRecipient, sourceObjectKey string, now time.Time) *InboundMail {
	return &InboundMail{
		EnvelopeFrom:    envelopeFrom,
		Recipient:       recipient.Email,
		ResourceID:      recipient.ResourceID,
		ResourceType:    recipient.ResourceType,
		OwnerUserID:     recipient.OwnerUserID,
		SourceObjectKey: sourceObjectKey,
		Status:          InboundStatusPending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func (m *InboundMail) MarkProcessing(now time.Time) {
	m.Status = InboundStatusProcessing
	m.FailureReason = ""
	m.UpdatedAt = now
}

func (m *InboundMail) MarkStored(now time.Time) {
	m.Status = InboundStatusStored
	m.FailureReason = ""
	m.UpdatedAt = now
}

func (m *InboundMail) MarkFailed(now time.Time, reason string) {
	m.Status = InboundStatusFailed
	m.FailureReason = reason
	m.UpdatedAt = now
}
