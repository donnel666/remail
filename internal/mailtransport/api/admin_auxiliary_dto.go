package api

import "time"

type AdminBindingSummaryResponse struct {
	ID           uint      `json:"id"`
	EmailAddress string    `json:"emailAddress"`
	Status       string    `json:"status"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type AdminAuxiliaryMessageSummaryResponse struct {
	ID               uint      `json:"id"`
	Recipient        string    `json:"recipient"`
	Sender           string    `json:"sender"`
	Subject          string    `json:"subject"`
	Preview          string    `json:"preview"`
	Status           string    `json:"status"`
	VerificationCode *string   `json:"verificationCode"`
	OrderNo          *string   `json:"orderNo"`
	ReceivedAt       time.Time `json:"receivedAt"`
}

type AdminAuxiliaryMessageDetailResponse struct {
	AdminAuxiliaryMessageSummaryResponse
	Body            string  `json:"body"`
	MatchDiagnostic *string `json:"matchDiagnostic"`
}

type AdminBindingMessageListResponse struct {
	Binding              *AdminBindingSummaryResponse           `json:"binding"`
	Items                []AdminAuxiliaryMessageSummaryResponse `json:"items"`
	Total                *int64                                 `json:"total,omitempty"`
	Offset               int                                    `json:"offset"`
	Limit                int                                    `json:"limit"`
	HasMore              bool                                   `json:"hasMore"`
	NextBeforeReceivedAt *time.Time                             `json:"nextBeforeReceivedAt,omitempty"`
	NextBeforeID         *uint                                  `json:"nextBeforeId,omitempty"`
}
