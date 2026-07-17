package app

import (
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
)

type HistoricalProjectScope struct {
	ProjectID               uint
	ProductID               uint
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	LooseMatch              bool
	Rules                   []MailRule
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

type HistoricalMailboxType string

const (
	HistoricalMailboxMain  HistoricalMailboxType = "main"
	HistoricalMailboxAlias HistoricalMailboxType = "alias"
	HistoricalMailboxDot   HistoricalMailboxType = "dot"
	HistoricalMailboxPlus  HistoricalMailboxType = "plus"
)

type HistoricalProjectMatch struct {
	ResourceID              uint
	MailboxType             HistoricalMailboxType
	MailboxEmail            string
	ProjectID               uint
	ProductID               uint
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	FirstMatchedAt          time.Time
	LastMatchedAt           time.Time
	EvidenceCount           int
	ScannedAt               time.Time
}

func historicalProjectMatches(req HistoricalProjectMatchRequest, scopes []HistoricalProjectScope, scannedAt time.Time) []HistoricalProjectMatch {
	matches := make([]HistoricalProjectMatch, 0)
	index := make(map[struct {
		projectID uint
		mailbox   HistoricalMailboxType
		email     string
	}]int)
	for _, scope := range scopes {
		for _, item := range req.Messages {
			for _, recipient := range historicalRecipientCandidates(req.EmailAddress, item.Recipients) {
				if !historicalRecipientMatchesProject(item, req.EmailAddress, recipient, scope) {
					continue
				}
				receivedAt := item.ReceivedAt.UTC()
				if receivedAt.IsZero() {
					receivedAt = scannedAt
				}
				mailboxType := historicalMailboxType(req.EmailAddress, recipient)
				key := struct {
					projectID uint
					mailbox   HistoricalMailboxType
					email     string
				}{projectID: scope.ProjectID, mailbox: mailboxType, email: recipient}
				matchIndex, ok := index[key]
				if !ok {
					index[key] = len(matches)
					matches = append(matches, HistoricalProjectMatch{
						ResourceID: req.ResourceID, MailboxType: mailboxType, MailboxEmail: recipient,
						ProjectID: scope.ProjectID, ProductID: scope.ProductID,
						CodeWindowMinutes: scope.CodeWindowMinutes, ActivationWindowMinutes: scope.ActivationWindowMinutes,
						WarrantyMinutes: scope.WarrantyMinutes,
						FirstMatchedAt:  receivedAt, LastMatchedAt: receivedAt,
						ScannedAt: scannedAt,
					})
					matchIndex = len(matches) - 1
				}
				matches[matchIndex].EvidenceCount++
				if receivedAt.Before(matches[matchIndex].FirstMatchedAt) {
					matches[matchIndex].FirstMatchedAt = receivedAt
				}
				if receivedAt.After(matches[matchIndex].LastMatchedAt) {
					matches[matchIndex].LastMatchedAt = receivedAt
				}
			}
		}
	}
	return matches
}

func historicalMessageMatchesProject(message HistoricalProjectMessage, mainEmail string, scope HistoricalProjectScope) bool {
	for _, recipient := range historicalRecipientCandidates(mainEmail, message.Recipients) {
		if historicalRecipientMatchesProject(message, mainEmail, recipient, scope) {
			return true
		}
	}
	return false
}

func historicalRecipientCandidates(mainEmail string, recipients []string) []string {
	candidates := normalizeRecipientCandidates(recipients)
	if len(candidates) <= 1 {
		return candidates
	}
	filtered := candidates[:0]
	for _, recipient := range candidates {
		if historicalMailboxType(mainEmail, recipient) != HistoricalMailboxAlias {
			filtered = append(filtered, recipient)
		}
	}
	return filtered
}

func historicalRecipientMatchesProject(message HistoricalProjectMessage, mainEmail, recipient string, scope HistoricalProjectScope) bool {
	fetched := FetchedMessage{
		ResourceType:      domain.ResourceTypeMicrosoft,
		Recipient:         recipient,
		Recipients:        []string{recipient},
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
	orderScope := OrderScope{
		ProjectID:     scope.ProjectID,
		LooseMatch:    scope.LooseMatch,
		Rules:         scope.Rules,
		Recipient:     recipient,
		RecipientKind: historicalRecipientKind(mainEmail, recipient),
	}
	enabled := enabledRules(orderScope.Rules)
	if !matchRequiredRule(MailRuleRecipient, enabled, fetched, orderScope) {
		return false
	}
	if orderScope.LooseMatch {
		return matchRequiredRule(MailRuleSender, enabled, fetched, orderScope)
	}
	return matchRequiredRule(MailRuleSender, enabled, fetched, orderScope) &&
		matchRequiredRule(MailRuleSubject, enabled, fetched, orderScope) &&
		matchRequiredRule(MailRuleBody, enabled, fetched, orderScope)
}

func historicalMailboxType(mainEmail, recipient string) HistoricalMailboxType {
	if normalizeEmail(mainEmail) == normalizeEmail(recipient) {
		return HistoricalMailboxMain
	}
	switch historicalRecipientKind(mainEmail, recipient) {
	case "dot":
		return HistoricalMailboxDot
	case "plus":
		return HistoricalMailboxPlus
	default:
		return HistoricalMailboxAlias
	}
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
