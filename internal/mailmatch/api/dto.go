package api

import "time"

type MailContentResponse struct {
	ID               uint      `json:"id"`
	Sender           string    `json:"sender"`
	Recipient        string    `json:"recipient"`
	ReceivedAt       time.Time `json:"receivedAt"`
	Subject          string    `json:"subject"`
	BodyPreview      string    `json:"bodyPreview"`
	VerificationCode string    `json:"verificationCode,omitempty"`
}

type MailContentDetailResponse struct {
	MailContentResponse
	Body string `json:"body"`
}

type OrderMailResponse struct {
	Items []MailContentResponse `json:"items"`
	Fetch *FetchStateResponse   `json:"fetch,omitempty"`
}

type PickupCredentialRequest struct {
	Email string `json:"email"`
	Token string `json:"token"`
}

type PickupBatchRequest struct {
	Items []PickupCredentialRequest `json:"items"`
}

type PickupBatchItemErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PickupBatchItemResponse struct {
	Index  int                           `json:"index"`
	Status string                        `json:"status"`
	Data   *OrderMailResponse            `json:"data,omitempty"`
	Error  *PickupBatchItemErrorResponse `json:"error,omitempty"`
}

type PickupBatchResponse []PickupBatchItemResponse

type FetchStateResponse struct {
	LastJobID          *uint      `json:"lastJobId,omitempty"`
	LastStatus         string     `json:"lastStatus"`
	LastSubmittedAt    *time.Time `json:"lastSubmittedAt,omitempty"`
	LastSuccessAt      *time.Time `json:"lastSuccessAt,omitempty"`
	LastReceivedAt     *time.Time `json:"lastReceivedAt,omitempty"`
	NextFetchAllowedAt *time.Time `json:"nextFetchAllowedAt,omitempty"`
	LastSafeError      string     `json:"lastSafeError,omitempty"`
}
