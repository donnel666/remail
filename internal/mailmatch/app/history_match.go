package app

import (
	"context"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
)

type HistoricalProjectScope struct {
	ProjectID  uint
	LooseMatch bool
	Rules      []MailRule
}

type HistoricalProjectMessage struct {
	Recipients        []string
	Sender            string
	Subject           string
	Body              string
	BodyPreview       string
	MessageIDHeader   string
	ProviderMessageID string
	Protocol          string
	Folder            string
	ReceivedAt        time.Time
}

type HistoricalProjectMatchRequest struct {
	ResourceID   uint
	EmailAddress string
	Messages     []HistoricalProjectMessage
	ScannedAt    time.Time
}

type HistoricalProjectMatch struct {
	ResourceID     uint
	ProjectID      uint
	FirstMatchedAt time.Time
	LastMatchedAt  time.Time
	EvidenceCount  int
	ScannedAt      time.Time
}

func (uc *UseCase) MatchMicrosoftHistory(ctx context.Context, req HistoricalProjectMatchRequest) error {
	if req.ResourceID == 0 || normalizeEmail(req.EmailAddress) == "" {
		return domain.ErrInvalidRequest
	}
	if len(req.Messages) == 0 {
		return nil
	}
	scopes, err := uc.repo.ListHistoricalProjectScopes(ctx)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		return nil
	}
	scannedAt := req.ScannedAt.UTC()
	if scannedAt.IsZero() {
		scannedAt = uc.now()
	}
	matches := historicalProjectMatches(req, scopes, scannedAt)
	if len(matches) == 0 {
		return nil
	}
	return uc.repo.UpsertMicrosoftProjectMatches(ctx, matches)
}

func historicalProjectMatches(req HistoricalProjectMatchRequest, scopes []HistoricalProjectScope, scannedAt time.Time) []HistoricalProjectMatch {
	matches := make([]HistoricalProjectMatch, 0)
	for _, scope := range scopes {
		var match *HistoricalProjectMatch
		for _, item := range req.Messages {
			if !historicalMessageMatchesProject(item, req.EmailAddress, scope) {
				continue
			}
			receivedAt := item.ReceivedAt.UTC()
			if receivedAt.IsZero() {
				receivedAt = scannedAt
			}
			if match == nil {
				match = &HistoricalProjectMatch{
					ResourceID:     req.ResourceID,
					ProjectID:      scope.ProjectID,
					FirstMatchedAt: receivedAt,
					LastMatchedAt:  receivedAt,
					ScannedAt:      scannedAt,
				}
			}
			match.EvidenceCount++
			if receivedAt.Before(match.FirstMatchedAt) {
				match.FirstMatchedAt = receivedAt
			}
			if receivedAt.After(match.LastMatchedAt) {
				match.LastMatchedAt = receivedAt
			}
		}
		if match != nil {
			matches = append(matches, *match)
		}
	}
	return matches
}

func historicalMessageMatchesProject(message HistoricalProjectMessage, mainEmail string, scope HistoricalProjectScope) bool {
	fetched := FetchedMessage{
		ResourceType:      domain.ResourceTypeMicrosoft,
		Recipients:        normalizeRecipientCandidates(message.Recipients),
		Sender:            strings.TrimSpace(message.Sender),
		Subject:           strings.TrimSpace(message.Subject),
		Body:              strings.TrimSpace(message.Body),
		BodyPreview:       strings.TrimSpace(message.BodyPreview),
		MessageIDHeader:   strings.TrimSpace(message.MessageIDHeader),
		ProviderMessageID: strings.TrimSpace(message.ProviderMessageID),
		Protocol:          strings.TrimSpace(message.Protocol),
		Folder:            strings.TrimSpace(message.Folder),
		ReceivedAt:        message.ReceivedAt.UTC(),
	}
	for _, recipient := range fetched.Recipients {
		fetched.Recipient = recipient
		orderScope := OrderScope{
			ProjectID:     scope.ProjectID,
			LooseMatch:    scope.LooseMatch,
			Rules:         scope.Rules,
			Recipient:     recipient,
			RecipientKind: historicalRecipientKind(mainEmail, recipient),
		}
		enabled := enabledRules(orderScope.Rules)
		if !matchRequiredRule(MailRuleRecipient, enabled, fetched, orderScope) {
			continue
		}
		if orderScope.LooseMatch {
			if matchRequiredRule(MailRuleSender, enabled, fetched, orderScope) {
				return true
			}
			continue
		}
		if matchRequiredRule(MailRuleSender, enabled, fetched, orderScope) &&
			matchRequiredRule(MailRuleSubject, enabled, fetched, orderScope) &&
			matchRequiredRule(MailRuleBody, enabled, fetched, orderScope) {
			return true
		}
	}
	return false
}

func historicalRecipientKind(mainEmail string, recipient string) string {
	mainLocal, mainDomain, ok := splitEmail(mainEmail)
	if !ok {
		return "exact"
	}
	local, recipientDomain, ok := splitEmail(recipient)
	if !ok || recipientDomain != mainDomain {
		return "exact"
	}
	if plus := strings.IndexByte(local, '+'); plus > 0 && local[:plus] == mainLocal {
		return "plus"
	}
	if local != mainLocal && strings.ReplaceAll(local, ".", "") == strings.ReplaceAll(mainLocal, ".", "") {
		return "dot"
	}
	return "exact"
}

func splitEmail(value string) (string, string, bool) {
	parts := strings.Split(normalizeEmail(value), "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
