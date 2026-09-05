package api

import "time"

type AllocationItemResponse struct {
	Type        string     `json:"type"`
	ID          uint       `json:"id"`
	OrderNo     string     `json:"orderNo"`
	ProjectID   uint       `json:"projectId"`
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
	Gmail                      GmailInventoryResponse     `json:"gmail"`
	ICloud                     ICloudInventoryResponse    `json:"icloud"`
	TotalAvailable             int64                      `json:"totalAvailable"`
	ActiveMicrosoftAllocations int64                      `json:"activeMicrosoftAllocations"`
	ActiveDomainAllocations    int64                      `json:"activeDomainAllocations"`
	ActiveGmailAllocations     int64                      `json:"activeGmailAllocations"`
	ActiveICloudAllocations    int64                      `json:"activeICloudAllocations"`
}

type ProjectInventoryTotalResponse struct {
	ProjectID      uint                                   `json:"projectId"`
	TotalAvailable int64                                  `json:"totalAvailable"`
	Products       []ProjectProductInventoryTotalResponse `json:"products"`
	ObservedAt     *time.Time                             `json:"observedAt,omitempty"`
}

type ProjectProductInventoryTotalResponse struct {
	ProductType             string                                  `json:"productType"`
	TotalAvailable          int64                                   `json:"totalAvailable"`
	PublicAvailable         int64                                   `json:"publicAvailable"`
	CodeAvailable           *int64                                  `json:"codeAvailable,omitempty"`
	CodePublicAvailable     *int64                                  `json:"codePublicAvailable,omitempty"`
	PurchaseAvailable       *int64                                  `json:"purchaseAvailable,omitempty"`
	PurchasePublicAvailable *int64                                  `json:"purchasePublicAvailable,omitempty"`
	Suffixes                []ProjectProductSuffixInventoryResponse `json:"suffixes,omitempty"`
}

type ProjectProductSuffixInventoryResponse struct {
	Suffix          string `json:"suffix"`
	TotalAvailable  int64  `json:"totalAvailable"`
	PublicAvailable int64  `json:"publicAvailable"`
}

type InventoryRefreshRequest struct {
	ProjectID *uint `json:"projectId"`
}

type InventoryRefreshResponse struct {
	Items      []InventoryRefreshItemResponse `json:"items"`
	Parameters InventoryRefreshParameters     `json:"parameters"`
}

type InventoryRefreshItemResponse struct {
	ProjectID       uint       `json:"projectId"`
	ProjectName     string     `json:"projectName"`
	Status          string     `json:"status"`
	TotalAvailable  int64      `json:"totalAvailable"`
	LastRefreshedAt *time.Time `json:"lastRefreshedAt"`
	NextRefreshAt   *time.Time `json:"nextRefreshAt"`
	LastAttemptAt   *time.Time `json:"lastAttemptAt"`
	LastError       string     `json:"lastError"`
}

type InventoryRefreshParameters struct {
	RefreshIntervalMinutes int64 `json:"refreshIntervalMinutes"`
	CacheHardTTLHours      int64 `json:"cacheHardTtlHours"`
	BatchSize              int   `json:"batchSize"`
}

type InventoryRefreshAcceptedResponse struct {
	ProjectIDs []uint `json:"projectIds"`
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

type GmailInventoryResponse struct {
	Enabled                 bool  `json:"enabled"`
	CodeEnabled             bool  `json:"codeEnabled"`
	PurchaseEnabled         bool  `json:"purchaseEnabled"`
	MainEnabled             bool  `json:"mainEnabled"`
	DotEnabled              bool  `json:"dotEnabled"`
	PlusEnabled             bool  `json:"plusEnabled"`
	EligibleResources       int64 `json:"eligibleResources"`
	PublicEligibleResources int64 `json:"publicEligibleResources"`
	MainAvailable           int64 `json:"mainAvailable"`
	MainPublicAvailable     int64 `json:"mainPublicAvailable"`
	DotAvailable            int64 `json:"dotAvailable"`
	DotPublicAvailable      int64 `json:"dotPublicAvailable"`
	PlusAvailable           int64 `json:"plusAvailable"`
	PlusPublicAvailable     int64 `json:"plusPublicAvailable"`
	TotalAvailable          int64 `json:"totalAvailable"`
	PublicAvailable         int64 `json:"publicAvailable"`
}

type ICloudInventoryResponse struct {
	Enabled           bool  `json:"enabled"`
	EligibleResources int64 `json:"eligibleResources"`
	AliasAvailable    int64 `json:"aliasAvailable"`
	TotalAvailable    int64 `json:"totalAvailable"`
}
