package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/donnel666/remail/internal/core/domain"
	"gorm.io/gorm"
)

func microsoftResourceTable(resourceType domain.ResourceType) string {
	if resourceType == domain.ResourceTypeMicrosoftEnterprise {
		return "microsoft_enterprise_resources"
	}
	return "microsoft_resources"
}

func microsoftEmailsByType(emails []string) map[domain.ResourceType][]string {
	groups := make(map[domain.ResourceType][]string)
	for _, email := range emails {
		kind := domain.MicrosoftResourceType(domain.IsMicrosoftEnterpriseEmail(email))
		groups[kind] = append(groups[kind], email)
	}
	return groups
}

func microsoftResourceTypeByID(ctx context.Context, db *gorm.DB, resourceID uint) (domain.ResourceType, error) {
	var root EmailResourceModel
	if err := db.WithContext(ctx).Select("id", "type").First(&root, resourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", domain.ErrResourceNotFound
		}
		return "", fmt.Errorf("load Microsoft resource type: %w", err)
	}
	resourceType := domain.ResourceType(root.Type)
	if !domain.IsMicrosoftResourceType(resourceType) {
		return "", domain.ErrInvalidResourceType
	}
	return resourceType, nil
}
