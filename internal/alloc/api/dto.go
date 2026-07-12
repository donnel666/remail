package api

import "time"

type AllocationItemResponse struct {
	Type        string     `json:"type"`
	ID          uint       `json:"id"`
	OrderNo     string     `json:"orderNo"`
	ProjectID   uint       `json:"projectId"`
	ProductID   uint       `json:"productId"`
	ResourceID  uint       `json:"resourceId"`
	SupplyScope string     `json:"supplyScope"`
	Mailbox     string     `json:"mailbox"`
	Email       string     `json:"email"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	ReleasedAt  *time.Time `json:"releasedAt,omitempty"`
}

type AllocationListResponse struct {
	Items  []AllocationItemResponse `json:"items"`
	Total  int64                    `json:"total"`
	Offset int                      `json:"offset"`
	Limit  int                      `json:"limit"`
}

type AdminAllocationItemResponse struct {
	Type             string     `json:"type"`
	ID               uint       `json:"id"`
	OrderNo          string     `json:"orderNo"`
	ProjectID        uint       `json:"projectId"`
	ProjectName      string     `json:"projectName"`
	ProjectLogoURL   *string    `json:"projectLogoUrl"`
	ResourceID       uint       `json:"resourceId"`
	Mailbox          string     `json:"mailbox"`
	SupplyScope      string     `json:"supplyScope"`
	DeliveryEmail    string     `json:"deliveryEmail"`
	ServiceMode      string     `json:"serviceMode"`
	OrderStatus      string     `json:"orderStatus"`
	Status           string     `json:"status"`
	PayAmount        string     `json:"payAmount"`
	BuyerEmail       string     `json:"buyerEmail"`
	VerificationCode *string    `json:"verificationCode"`
	CreatedAt        time.Time  `json:"createdAt"`
	ReceiveUntil     *time.Time `json:"receiveUntil"`
}

type AdminAllocationListResponse struct {
	Items  []AdminAllocationItemResponse `json:"items"`
	Total  int64                         `json:"total"`
	Offset int                           `json:"offset"`
	Limit  int                           `json:"limit"`
}

type ProjectInventoryResponse struct {
	ProjectID                  uint                       `json:"projectId"`
	Microsoft                  MicrosoftInventoryResponse `json:"microsoft"`
	Domain                     DomainInventoryResponse    `json:"domain"`
	TotalAvailable             int64                      `json:"totalAvailable"`
	ActiveMicrosoftAllocations int64                      `json:"activeMicrosoftAllocations"`
	ActiveDomainAllocations    int64                      `json:"activeDomainAllocations"`
}

type ProjectInventoryTotalResponse struct {
	ProjectID      uint                                   `json:"projectId"`
	TotalAvailable int64                                  `json:"totalAvailable"`
	Products       []ProjectProductInventoryTotalResponse `json:"products"`
}

type ProjectProductInventoryTotalResponse struct {
	ProductID       uint                                    `json:"productId"`
	TotalAvailable  int64                                   `json:"totalAvailable"`
	PublicAvailable int64                                   `json:"publicAvailable"`
	Suffixes        []ProjectProductSuffixInventoryResponse `json:"suffixes,omitempty"`
}

type ProjectProductSuffixInventoryResponse struct {
	Suffix          string `json:"suffix"`
	TotalAvailable  int64  `json:"totalAvailable"`
	PublicAvailable int64  `json:"publicAvailable"`
}

type MicrosoftInventoryResponse struct {
	Enabled                bool  `json:"enabled"`
	MainEnabled            bool  `json:"mainEnabled"`
	DotEnabled             bool  `json:"dotEnabled"`
	PlusEnabled            bool  `json:"plusEnabled"`
	EligibleResources      int64 `json:"eligibleResources"`
	MainAvailable          int64 `json:"mainAvailable"`
	ExplicitAliasAvailable int64 `json:"explicitAliasAvailable"`
	DotCapacity            int64 `json:"dotCapacity"`
	ActiveDotAllocations   int64 `json:"activeDotAllocations"`
	DotAvailable           int64 `json:"dotAvailable"`
	PlusDailyLimit         int64 `json:"plusDailyLimit"`
	PlusDailyUsed          int64 `json:"plusDailyUsed"`
	PlusDailyAvailable     int64 `json:"plusDailyAvailable"`
	TotalAvailable         int64 `json:"totalAvailable"`
}

type DomainInventoryResponse struct {
	Enabled               bool  `json:"enabled"`
	EligibleResources     int64 `json:"eligibleResources"`
	MailboxDailyLimit     int64 `json:"mailboxDailyLimit"`
	MailboxDailyUsed      int64 `json:"mailboxDailyUsed"`
	MailboxDailyAvailable int64 `json:"mailboxDailyAvailable"`
	TotalAvailable        int64 `json:"totalAvailable"`
}

type RoutingCandidateResponse struct {
	ID              uint       `json:"id"`
	Type            string     `json:"type"`
	ProjectID       uint       `json:"projectId"`
	ResourceID      uint       `json:"resourceId"`
	Address         string     `json:"address"`
	DomainSuffix    string     `json:"domainSuffix"`
	ForSale         bool       `json:"forSale"`
	QualityScore    int        `json:"qualityScore"`
	Status          string     `json:"status"`
	Bucket          uint8      `json:"bucket"`
	LastAllocatedAt *time.Time `json:"lastAllocatedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type RoutingCandidateListResponse struct {
	Items  []RoutingCandidateResponse `json:"items"`
	Total  int64                      `json:"total"`
	Offset int                        `json:"offset"`
	Limit  int                        `json:"limit"`
}

type CandidateRefreshResponse struct {
	JobID     uint   `json:"jobId"`
	ProjectID uint   `json:"projectId"`
	Status    string `json:"status"`
	Created   bool   `json:"created"`
	Message   string `json:"message"`
}
