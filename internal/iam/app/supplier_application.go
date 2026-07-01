package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/domain"
)

const (
	supplierApplicationReasonMax       = 1000
	supplierApplicationReviewReasonMax = 500
)

// SupplierApplicationUseCase handles supplier permission applications.
type SupplierApplicationUseCase struct {
	applications SupplierApplicationRepository
	users        UserRepository
}

// NewSupplierApplicationUseCase creates a new SupplierApplicationUseCase.
func NewSupplierApplicationUseCase(applications SupplierApplicationRepository, users UserRepository) *SupplierApplicationUseCase {
	return &SupplierApplicationUseCase{applications: applications, users: users}
}

// SupplierApplicationListResult contains paginated supplier applications.
type SupplierApplicationListResult struct {
	Applications []domain.SupplierApplication
	Total        int64
	Offset       int
	Limit        int
}

// Submit creates a reviewing supplier application for the current user.
func (uc *SupplierApplicationUseCase) Submit(ctx context.Context, userID uint, reason string) (*domain.SupplierApplication, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > supplierApplicationReasonMax {
		return nil, domain.ErrInvalidSupplierApplication
	}

	user, err := uc.users.FindByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("supplier application find user: %w", err)
	}
	if user == nil {
		return nil, domain.ErrUserNotFound
	}
	if user.RoleLevel.IsAtLeast(domain.RoleSupplier) {
		return nil, domain.ErrInvalidSupplierApplicationStatus
	}

	application := &domain.SupplierApplication{
		ApplicantUserID: userID,
		Reason:          reason,
		Status:          domain.SupplierApplicationReviewing,
	}
	if err := uc.applications.CreateSupplierApplicationReviewing(ctx, application); err != nil {
		return nil, err
	}
	return application, nil
}

// Current returns the latest supplier application for the current user.
func (uc *SupplierApplicationUseCase) Current(ctx context.Context, userID uint) (*domain.SupplierApplication, error) {
	return uc.applications.FindLatestSupplierApplicationByApplicantUserID(ctx, userID)
}

// List returns paginated supplier applications for admin review.
func (uc *SupplierApplicationUseCase) List(ctx context.Context, status string, offset, limit int) (*SupplierApplicationListResult, error) {
	status = strings.TrimSpace(status)
	if status == "all" {
		status = ""
	}
	if status != "" && !domain.IsValidSupplierApplicationStatus(domain.SupplierApplicationStatus(status)) {
		return nil, domain.ErrInvalidSupplierApplicationStatus
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	total, err := uc.applications.CountSupplierApplications(ctx, status)
	if err != nil {
		return nil, err
	}
	items, err := uc.applications.ListSupplierApplications(ctx, status, offset, limit)
	if err != nil {
		return nil, err
	}
	return &SupplierApplicationListResult{Applications: items, Total: total, Offset: offset, Limit: limit}, nil
}

// Approve approves a reviewing supplier application and promotes the applicant to supplier.
func (uc *SupplierApplicationUseCase) Approve(ctx context.Context, operatorUserID uint, requestID, path string, applicationID uint) (*domain.SupplierApplication, error) {
	application, err := uc.applications.FindSupplierApplicationByID(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if application == nil {
		return nil, domain.ErrSupplierApplicationNotFound
	}
	if application.Status != domain.SupplierApplicationReviewing {
		return nil, domain.ErrInvalidSupplierApplicationStatus
	}

	user, err := uc.users.FindByID(ctx, application.ApplicantUserID)
	if err != nil {
		return nil, fmt.Errorf("supplier application approve find user: %w", err)
	}
	if user == nil {
		return nil, domain.ErrUserNotFound
	}
	if !user.RoleLevel.IsAtLeast(domain.RoleSupplier) {
		user.RoleLevel = domain.RoleSupplier
	}

	now := time.Now().UTC()
	application.Status = domain.SupplierApplicationApproved
	application.ReviewedBy = &operatorUserID
	application.ReviewedAt = &now
	application.ReviewReason = ""

	if err := uc.applications.ApproveSupplierApplicationWithUserAndLog(ctx, application, user, supplierApplicationOperationLog(
		operatorUserID,
		requestID,
		path,
		"iam.supplier_application.approve",
		application.ID,
		"success",
		"Supplier application approved.",
	)); err != nil {
		return nil, err
	}
	return application, nil
}

// Reject rejects a reviewing supplier application with a safe review reason.
func (uc *SupplierApplicationUseCase) Reject(ctx context.Context, operatorUserID uint, requestID, path string, applicationID uint, reviewReason string) (*domain.SupplierApplication, error) {
	reviewReason = strings.TrimSpace(reviewReason)
	if reviewReason == "" || len([]rune(reviewReason)) > supplierApplicationReviewReasonMax {
		return nil, domain.ErrInvalidSupplierApplication
	}

	application, err := uc.applications.FindSupplierApplicationByID(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if application == nil {
		return nil, domain.ErrSupplierApplicationNotFound
	}
	if application.Status != domain.SupplierApplicationReviewing {
		return nil, domain.ErrInvalidSupplierApplicationStatus
	}

	now := time.Now().UTC()
	application.Status = domain.SupplierApplicationRejected
	application.ReviewReason = reviewReason
	application.ReviewedBy = &operatorUserID
	application.ReviewedAt = &now

	if err := uc.applications.RejectSupplierApplicationWithLog(ctx, application, supplierApplicationOperationLog(
		operatorUserID,
		requestID,
		path,
		"iam.supplier_application.reject",
		application.ID,
		"success",
		"Supplier application rejected.",
	)); err != nil {
		return nil, err
	}
	return application, nil
}

func supplierApplicationOperationLog(operatorUserID uint, requestID, path, operationType string, applicationID uint, result, summary string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  operationType,
		ResourceType:   "supplier_application",
		ResourceID:     fmt.Sprintf("%d", applicationID),
		Path:           path,
		Result:         result,
		SafeSummary:    summary,
		RequestID:      requestID,
	}
}
