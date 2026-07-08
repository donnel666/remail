package api

import (
	"context"
	stdmail "net/mail"
	"regexp"
	"strings"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

const maxFetchProxyAttempts = 3

type MicrosoftFetchAdapter struct {
	client  *mailinfra.MicrosoftMailFetchClient
	proxies *proxyapp.ProxyUseCase
}

func NewMicrosoftFetchAdapter(proxies *proxyapp.ProxyUseCase) *MicrosoftFetchAdapter {
	return &MicrosoftFetchAdapter{
		client:  mailinfra.NewMicrosoftMailFetchClient(),
		proxies: proxies,
	}
}

func (a *MicrosoftFetchAdapter) FetchMicrosoftMessages(ctx context.Context, req mailmatchapp.FetchMessagesRequest) (*mailmatchapp.FetchMessagesResult, error) {
	if a == nil || a.client == nil {
		return nil, domain.ErrMailServiceUnavailable
	}
	var lastErr error
	for attempt := 0; attempt < maxFetchProxyAttempts; attempt++ {
		proxyConfig, err := a.acquireProxy(ctx, req.Scope, attempt)
		if err != nil {
			return nil, err
		}
		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}
		result, err := a.client.FetchAll(ctx, mailinfra.MicrosoftMailFetchRequest{
			EmailAddress: req.Scope.MicrosoftEmail,
			ClientID:     req.Scope.MicrosoftClientID,
			RefreshToken: req.Scope.MicrosoftRT,
			ProxyURL:     proxyURL,
			SinceAt:      req.SinceAt,
			UntilAt:      req.UntilAt,
			MaxMessages:  30,
		})
		if err != nil {
			lastErr = err
			_ = a.reportProxyFailure(ctx, proxyID, "Microsoft mail fetch failed.")
			continue
		}
		if result.Valid {
			_ = a.reportProxySuccess(ctx, proxyID)
			return &mailmatchapp.FetchMessagesResult{
				Messages:     microsoftMessagesToMailmatch(req.Scope, result.Messages),
				RefreshToken: strings.TrimSpace(result.RefreshToken),
			}, nil
		}
		lastErr = domain.ErrMailServiceUnavailable
		if result.ProxyFailure || proxyID != 0 {
			_ = a.reportProxyFailure(ctx, proxyID, result.SafeMessage)
			continue
		}
		if proxyID != 0 {
			_ = a.reportProxySuccess(ctx, proxyID)
		}
		return nil, domain.ErrMailServiceUnavailable
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, domain.ErrMailServiceUnavailable
}

func (a *MicrosoftFetchAdapter) acquireProxy(ctx context.Context, scope mailmatchapp.OrderScope, attempt int) (*proxyapp.ProxyConfig, error) {
	if a == nil || a.proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	return a.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 strings.ToLower(strings.TrimSpace(scope.MicrosoftEmail)),
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             proxydomain.ProxyPurposeFetch,
		AllowSystemFallback: true,
		Attempt:             attempt,
		RequestID:           scope.OrderNo,
	})
}

func (a *MicrosoftFetchAdapter) reportProxySuccess(ctx context.Context, proxyID uint) error {
	if a == nil || a.proxies == nil || proxyID == 0 {
		return nil
	}
	return a.proxies.ReportSuccess(ctx, proxyID)
}

func (a *MicrosoftFetchAdapter) reportProxyFailure(ctx context.Context, proxyID uint, safeError string) error {
	if a == nil || a.proxies == nil || proxyID == 0 {
		return nil
	}
	return a.proxies.ReportFailure(ctx, proxyID, safeError)
}

func microsoftMessagesToMailmatch(scope mailmatchapp.OrderScope, messages []mailinfra.MicrosoftFetchedMessage) []mailmatchapp.FetchedMessage {
	items := make([]mailmatchapp.FetchedMessage, 0, len(messages))
	for _, message := range messages {
		body := strings.TrimSpace(message.Body)
		if body == "" {
			body = strings.TrimSpace(message.Preview)
		}
		recipients := recipientCandidates(message.To)
		primaryRecipient := firstNonEmpty(recipients...)
		if primaryRecipient == "" && strings.TrimSpace(message.To) == "" {
			primaryRecipient = strings.ToLower(strings.TrimSpace(scope.Recipient))
			recipients = []string{primaryRecipient}
		}
		items = append(items, mailmatchapp.FetchedMessage{
			EmailResourceID:   scope.EmailResourceID,
			ResourceType:      domain.ResourceTypeMicrosoft,
			Recipient:         primaryRecipient,
			Recipients:        recipients,
			Sender:            strings.TrimSpace(message.From),
			Subject:           strings.TrimSpace(message.Subject),
			Body:              body,
			RawSource:         strings.TrimSpace(message.RawSource),
			ProviderPayload:   strings.TrimSpace(message.ProviderPayload),
			BodyPreview:       strings.TrimSpace(message.Preview),
			MessageIDHeader:   strings.TrimSpace(message.InternetMessageID),
			ProviderMessageID: strings.TrimSpace(message.ID),
			Protocol:          strings.TrimSpace(message.Protocol),
			Folder:            strings.TrimSpace(firstNonEmpty(message.FolderLabel, message.FolderID)),
			ReceivedAt:        message.ReceivedAt,
		})
	}
	return items
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

var mailAddressCandidateRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

func recipientCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	values := make([]string, 0)
	if list, err := stdmail.ParseAddressList(raw); err == nil {
		for _, address := range list {
			values = append(values, address.Address)
		}
	} else {
		values = append(values, mailAddressCandidateRe.FindAllString(raw, -1)...)
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}
