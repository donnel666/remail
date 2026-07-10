package domain

import (
	"strings"
	"time"

	moneyfmt "github.com/donnel666/remail/internal/money"
)

// ProjectStatus represents the Core project lifecycle.
type ProjectStatus string

const (
	ProjectStatusReviewing ProjectStatus = "reviewing"
	ProjectStatusListed    ProjectStatus = "listed"
	ProjectStatusDelisted  ProjectStatus = "delisted"
)

// ProjectAccessType controls project visibility and ordering access.
type ProjectAccessType string

const (
	ProjectAccessPublic  ProjectAccessType = "public"
	ProjectAccessPrivate ProjectAccessType = "private"
)

// ProductStatus represents whether a project product can be ordered.
type ProductStatus string

const (
	ProductStatusEnabled  ProductStatus = "enabled"
	ProductStatusDisabled ProductStatus = "disabled"
)

// ProductType is the resource type a project product allocates from.
type ProductType string

const (
	ProductTypeMicrosoft ProductType = "microsoft"
	ProductTypeDomain    ProductType = "domain"
)

// MailRuleType identifies which part of a message a rule matches.
type MailRuleType string

const (
	MailRuleSender    MailRuleType = "sender"
	MailRuleRecipient MailRuleType = "recipient"
	MailRuleSubject   MailRuleType = "subject"
	MailRuleBody      MailRuleType = "body"
)

// Project is the Core aggregate root for project rules and saleable products.
type Project struct {
	ID              uint
	Name            string
	TargetPlatform  string
	LogoURL         string
	Description     string
	Status          ProjectStatus
	AccessType      ProjectAccessType
	ApplicantUserID *uint
	ReviewReason    string
	LooseMatch      bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Product is a saleable service configuration under a Project.
type Product struct {
	ID                      uint
	ProjectID               uint
	Type                    ProductType
	Status                  ProductStatus
	CodeEnabled             bool
	PurchaseEnabled         bool
	CodePrice               string
	PurchasePrice           string
	CodeSupplierPrice       string
	PurchaseSupplierPrice   string
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	MainWeight              int
	DotWeight               int
	PlusWeight              int
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// MailRule is a project mail matching rule.
type MailRule struct {
	ID        uint
	ProjectID uint
	RuleType  MailRuleType
	Pattern   string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProjectAccess grants a user access to a private project.
type ProjectAccess struct {
	ID        uint
	ProjectID uint
	UserID    uint
	GrantedBy uint
	CreatedAt time.Time
}

// ProjectDetail is the aggregate view returned to API/application consumers.
type ProjectDetail struct {
	Project   Project
	Products  []Product
	MailRules []MailRule
	Accesses  []ProjectAccess
}

func IsValidProjectStatus(status ProjectStatus) bool {
	switch status {
	case ProjectStatusReviewing, ProjectStatusListed, ProjectStatusDelisted:
		return true
	default:
		return false
	}
}

func IsValidProjectAccessType(accessType ProjectAccessType) bool {
	switch accessType {
	case ProjectAccessPublic, ProjectAccessPrivate:
		return true
	default:
		return false
	}
}

func IsValidProductStatus(status ProductStatus) bool {
	switch status {
	case ProductStatusEnabled, ProductStatusDisabled:
		return true
	default:
		return false
	}
}

func IsValidProductType(productType ProductType) bool {
	switch productType {
	case ProductTypeMicrosoft, ProductTypeDomain:
		return true
	default:
		return false
	}
}

func IsValidMailRuleType(ruleType MailRuleType) bool {
	switch ruleType {
	case MailRuleSender, MailRuleRecipient, MailRuleSubject, MailRuleBody:
		return true
	default:
		return false
	}
}

func NormalizeProjectStatus(status string) (ProjectStatus, bool) {
	switch ProjectStatus(strings.ToLower(strings.TrimSpace(status))) {
	case ProjectStatusReviewing:
		return ProjectStatusReviewing, true
	case ProjectStatusListed:
		return ProjectStatusListed, true
	case ProjectStatusDelisted:
		return ProjectStatusDelisted, true
	default:
		return "", false
	}
}

func NormalizeProjectAccessType(accessType string) (ProjectAccessType, bool) {
	switch ProjectAccessType(strings.ToLower(strings.TrimSpace(accessType))) {
	case ProjectAccessPublic:
		return ProjectAccessPublic, true
	case ProjectAccessPrivate:
		return ProjectAccessPrivate, true
	default:
		return "", false
	}
}

func NormalizeProductStatus(status string) (ProductStatus, bool) {
	switch ProductStatus(strings.ToLower(strings.TrimSpace(status))) {
	case ProductStatusEnabled:
		return ProductStatusEnabled, true
	case ProductStatusDisabled:
		return ProductStatusDisabled, true
	default:
		return "", false
	}
}

func NormalizeProductType(productType string) (ProductType, bool) {
	switch ProductType(strings.ToLower(strings.TrimSpace(productType))) {
	case ProductTypeMicrosoft:
		return ProductTypeMicrosoft, true
	case ProductTypeDomain:
		return ProductTypeDomain, true
	default:
		return "", false
	}
}

func NormalizeMailRuleType(ruleType string) (MailRuleType, bool) {
	switch strings.ToLower(strings.TrimSpace(ruleType)) {
	case "sender":
		return MailRuleSender, true
	case "recipient":
		return MailRuleRecipient, true
	case "subject":
		return MailRuleSubject, true
	case "body":
		return MailRuleBody, true
	default:
		return "", false
	}
}

func NormalizeMoney(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		trimmed = "0"
	}
	amount, err := moneyfmt.Parse(trimmed)
	if err != nil || amount.IsNegative() {
		return "", false
	}
	return amount.StringFixedBank(moneyfmt.Scale), true
}
