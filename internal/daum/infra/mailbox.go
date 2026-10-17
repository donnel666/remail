package infra

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
)

type ProtocolClient = protocol.MailboxClient

// Old fetches can finish after credential replacement or revalidation.
func daumFetchFailureKey(resource Resource) string {
	return fmt.Sprintf("%s:%d:%d", mailbox.FetchFailureKey(domain.ResourceType, resource.ID), resource.CredentialRevision, resource.ValidationGeneration)
}

func FormatSender(sender protocol.Address) string {
	name, address := strings.TrimSpace(sender.Name), strings.TrimSpace(sender.Address)
	if name == "" {
		return address
	}
	if address == "" {
		return name
	}
	return name + " <" + address + ">"
}

func (s *Service) FetchMailbox(ctx context.Context, id uint, revision uint64, request protocol.FetchRequest) (result protocol.FetchResult, err error) {
	if s == nil || s.DB == nil || s.Protocol == nil {
		return result, domain.ErrDependency
	}
	if id == 0 || revision == 0 {
		return result, domain.ErrInvalidResource
	}
	if _, active := platform.GormTxFromContext(ctx); active {
		return result, domain.ErrDependency
	}
	err = func() error {
		var resource Resource
		if err := daumResourceQuery(s.dbFor(ctx)).Where("id = ? AND credential_revision = ? AND status NOT IN ?", id, revision, []string{domain.StatusDisabled, domain.StatusDeleted, domain.StatusAbnormal}).Take(&resource).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrInvalidClaim
			}
			return err
		}
		if request.Recipient != "" {
			canonical, ok := CanonicalEmail(request.Recipient)
			if !ok || canonical != resource.EmailAddress {
				return domain.ErrInvalidResource
			}
		}
		value, fetchErr := s.fetchProtocol(ctx, resource, request)
		if fetchErr != nil {
			current, err := s.GetResource(ctx, id, nil)
			if err != nil {
				return err
			}
			if current.CredentialRevision != revision || current.ValidationGeneration != resource.ValidationGeneration || current.Status != resource.Status {
				return domain.ErrInvalidClaim
			}
			var failure *protocol.Failure
			if errors.As(fetchErr, &failure) && !failure.Retryable {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer stop()
				failures, countErr := mailbox.IncrementFetchFailure(cleanup, s.Redis, daumFetchFailureKey(resource))
				if countErr != nil {
					slog.Warn("Daum fetch failure counter unavailable", "resource_id", id, "error", countErr)
					return fetchErr
				}
				if failures < mailbox.FetchFailureThreshold {
					return fetchErr
				}
				applied, commitErr := s.markPermanentFetchFailure(cleanup, id, revision, resource.ValidationGeneration, failure.Category, failure.SafeMessage)
				if commitErr != nil {
					return errors.Join(domain.ErrDependency, commitErr)
				}
				if !applied {
					return domain.ErrInvalidClaim
				}
			}
			return fetchErr
		}
		if request.FullHistory && !value.Complete {
			return &protocol.Failure{Category: "incomplete_history", SafeMessage: "Daum mailbox history was not completely read.", Retryable: true}
		}
		if clearErr := mailbox.ClearFetchFailures(ctx, s.Redis, daumFetchFailureKey(resource)); clearErr != nil {
			slog.Warn("Daum fetch failure counter clear failed", "resource_id", id, "error", clearErr)
		}
		current, err := s.GetResource(ctx, id, nil)
		if err != nil {
			return err
		}
		if current.CredentialRevision != revision || current.ValidationGeneration != resource.ValidationGeneration || current.Status != resource.Status {
			return domain.ErrInvalidClaim
		}
		result = value
		return nil
	}()
	if err != nil {
		return protocol.FetchResult{}, err
	}
	return result, nil
}
