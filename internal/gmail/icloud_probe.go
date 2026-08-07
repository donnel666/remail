package gmail

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ProbeICloudDelivery reuses the existing local Gmail IMAP reader. It is a
// read-only, narrow port for iCloud validation; Gmail credentials never leave
// this package and the probe token is not persisted by Gmail.
func (s *Service) ProbeICloudDelivery(ctx context.Context, resourceID uint, recipient, token string, since time.Time) (bool, error) {
	if s == nil || s.db == nil || s.fetch == nil || resourceID == 0 || strings.TrimSpace(recipient) == "" || strings.TrimSpace(token) == "" {
		return false, errors.New("gmail: iCloud delivery probe unavailable")
	}
	var resource struct {
		ID          uint   `gorm:"column:id"`
		Email       string `gorm:"column:email"`
		AppPassword string `gorm:"column:app_password"`
		Status      string `gorm:"column:status"`
	}
	err := s.dbFor(ctx).Table("gmail_resources").
		Select("id, email, app_password, status").Where("id = ?", resourceID).Take(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, errors.New("gmail: linked resource not found")
	}
	if err != nil {
		return false, err
	}
	if resource.Status != LocalResourceNormal && resource.Status != localResourceRollbackNormal ||
		strings.TrimSpace(resource.Email) == "" || strings.TrimSpace(resource.AppPassword) == "" {
		return false, errors.New("gmail: linked resource is unavailable")
	}
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	recipientBytes := []byte(recipient)
	tokenBytes := []byte(strings.ToLower(strings.TrimSpace(token)))
	cursors := localGmailFolderCursors{}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		messages, nextCursors, err := s.fetch(ctx, resource.Email, resource.AppPassword, cursors, since, false)
		if err != nil {
			return false, err
		}
		for _, message := range messages {
			raw := bytes.ToLower(message.Raw)
			if bytes.Contains(raw, recipientBytes) && bytes.Contains(raw, tokenBytes) {
				return true, nil
			}
		}
		if nextCursors == cursors {
			return false, nil
		}
		cursors = nextCursors
	}
}
