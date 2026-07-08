package api

import "time"

type MailContentResponse struct {
	Sender           string    `json:"sender"`
	Recipient        string    `json:"recipient"`
	ReceivedAt       time.Time `json:"receivedAt"`
	Subject          string    `json:"subject"`
	Body             string    `json:"body"`
	VerificationCode string    `json:"verificationCode,omitempty"`
}

type OrderMailResponse struct {
	Items []MailContentResponse `json:"items"`
	Fetch *FetchStateResponse   `json:"fetch,omitempty"`
}

type FetchStateResponse struct {
	LastJobID          *uint      `json:"lastJobId,omitempty"`
	LastStatus         string     `json:"lastStatus"`
	LastSubmittedAt    *time.Time `json:"lastSubmittedAt,omitempty"`
	LastSuccessAt      *time.Time `json:"lastSuccessAt,omitempty"`
	LastReceivedAt     *time.Time `json:"lastReceivedAt,omitempty"`
	NextFetchAllowedAt *time.Time `json:"nextFetchAllowedAt,omitempty"`
	LastSafeError      string     `json:"lastSafeError,omitempty"`
}
