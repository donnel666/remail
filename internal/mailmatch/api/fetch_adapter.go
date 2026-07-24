package api

import (
	"context"
	"log/slog"
	stdmail "net/mail"
	"regexp"
	"strings"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

const (
	maxFetchProxyAttempts           = 2
	realtimeMicrosoftMessageMaximum = 30
)

type microsoftMessageFetchClient interface {
	FetchAll(context.Context, mailinfra.MicrosoftMailFetchRequest) (mailinfra.MicrosoftMailFetchResult, error)
}

type MicrosoftFetchAdapter struct {
	client  microsoftMessageFetchClient
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
		return nil, &mailmatchapp.MailFetchFailure{
			Category:    "request",
			SafeMessage: "Microsoft mail service is temporarily unavailable.",
			Retryable:   true,
			Cause:       domain.ErrMailServiceUnavailable,
		}
	}
	var lastFailure error
	maxMessages := 30
	sinceAt := req.SinceAt
	untilAt := req.UntilAt
	stopAfterLimit := false
	if req.Realtime {
		maxMessages = realtimeMicrosoftMessageMaximum
		sinceAt = time.Time{}
		untilAt = time.Time{}
		stopAfterLimit = true
	}
	if req.FullHistory {
		maxMessages = 0
		sinceAt = req.SinceAt
		untilAt = req.UntilAt
		stopAfterLimit = false
	}
	for attempt := 0; attempt < maxFetchProxyAttempts; attempt++ {
		streamed := false
		var onMessages func([]mailinfra.MicrosoftFetchedMessage)
		if req.OnMessages != nil {
			onMessages = func(messages []mailinfra.MicrosoftFetchedMessage) {
				streamed = true
				req.OnMessages(microsoftMessagesToMailmatch(req.Scope, messages))
			}
		}
		proxyConfig, err := a.acquireProxy(ctx, req.Scope, req.RequestID, attempt)
		if err != nil {
			return nil, &mailmatchapp.MailFetchFailure{
				Category:     "request",
				SafeMessage:  "Microsoft mail service is temporarily unavailable.",
				Retryable:    true,
				RefreshToken: strings.TrimSpace(req.Scope.MicrosoftRT),
				Cause:        err,
			}
		}
		proxyURL := ""
		proxyID := uint(0)
		if proxyConfig != nil && !proxyConfig.Direct {
			proxyURL = proxyConfig.URL
			proxyID = proxyConfig.ID
		}
		result, err := a.client.FetchAll(ctx, mailinfra.MicrosoftMailFetchRequest{
			EmailAddress:    req.Scope.MicrosoftEmail,
			ClientID:        req.Scope.MicrosoftClientID,
			RefreshToken:    req.Scope.MicrosoftRT,
			ProxyURL:        proxyURL,
			SinceAt:         sinceAt,
			UntilAt:         untilAt,
			MaxMessages:     maxMessages,
			StopAfterLimit:  stopAfterLimit,
			KnownMessageIDs: req.KnownMessageIDs,
			OnMessages:      onMessages,
			OnReset:         req.OnReset,
		})
		if rotated := strings.TrimSpace(result.RefreshToken); rotated != "" {
			// A mailbox operation can fail after Microsoft has already rotated the
			// RT. Reuse the newest credential for the next proxy attempt instead of
			// retrying with a superseded one.
			req.Scope.MicrosoftRT = rotated
		}
		if err != nil {
			lastFailure = &mailmatchapp.MailFetchFailure{
				Category:     "request",
				SafeMessage:  "Microsoft mail service is temporarily unavailable.",
				Retryable:    true,
				RefreshToken: strings.TrimSpace(req.Scope.MicrosoftRT),
				Cause:        err,
			}
			slog.Warn(
				"microsoft mail fetch attempt failed",
				"attempt", attempt+1,
				"proxy_id", proxyID,
				"direct", proxyConfig == nil || proxyConfig.Direct,
				"category", "request",
				"safe_message", "Microsoft mail service is temporarily unavailable.",
			)
			_ = a.reportProxyFailure(ctx, proxyID, "Microsoft mail fetch failed.")
			if streamed {
				if req.OnReset == nil {
					return nil, lastFailure
				}
				req.OnReset()
			}
			continue
		}
		if result.Valid {
			_ = a.reportProxySuccess(ctx, proxyID)
			return &mailmatchapp.FetchMessagesResult{
				Messages:     microsoftMessagesToMailmatch(req.Scope, result.Messages),
				RefreshToken: strings.TrimSpace(result.RefreshToken),
			}, nil
		}
		failure := microsoftFetchFailure(result.Category, result.SafeMessage, result.ProxyFailure)
		failure.RefreshToken = strings.TrimSpace(req.Scope.MicrosoftRT)
		lastFailure = failure
		slog.Warn(
			"microsoft mail fetch attempt rejected",
			"attempt", attempt+1,
			"proxy_id", proxyID,
			"direct", proxyConfig == nil || proxyConfig.Direct,
			"category", failure.Category,
			"proxy_failure", result.ProxyFailure,
			"safe_message", failure.SafeMessage,
		)
		if result.ProxyFailure {
			_ = a.reportProxyFailure(ctx, proxyID, result.SafeMessage)
			if streamed {
				if req.OnReset == nil {
					return nil, lastFailure
				}
				req.OnReset()
			}
			continue
		}
		if proxyID != 0 {
			_ = a.reportProxySuccess(ctx, proxyID)
		}
		return nil, lastFailure
	}
	if lastFailure != nil {
		return nil, lastFailure
	}
	return nil, &mailmatchapp.MailFetchFailure{
		Category:    "request",
		SafeMessage: "Microsoft mail service is temporarily unavailable.",
		Retryable:   true,
		Cause:       domain.ErrMailServiceUnavailable,
	}
}

func (a *MicrosoftFetchAdapter) acquireProxy(ctx context.Context, scope mailmatchapp.OrderScope, requestID string, attempt int) (*proxyapp.ProxyConfig, error) {
	if a == nil || a.proxies == nil {
		return &proxyapp.ProxyConfig{Direct: true}, nil
	}
	return a.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 strings.ToLower(strings.TrimSpace(scope.MicrosoftEmail)),
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             proxydomain.ProxyPurposeFetch,
		AllowSystemFallback: true,
		Attempt:             attempt,
		RequestID:           firstNonEmpty(requestID, scope.OrderNo),
	})
}

func microsoftFetchFailure(category string, safeMessage string, proxyFailure bool) *mailmatchapp.MailFetchFailure {
	category = strings.ToLower(strings.TrimSpace(category))
	retryable := proxyFailure || category == "request" || category == "auth_timeout"
	if category == "" {
		category = "request"
		retryable = true
	}
	safeMessage = strings.TrimSpace(safeMessage)
	if safeMessage == "" {
		safeMessage = "Microsoft mail service is temporarily unavailable."
	}
	return &mailmatchapp.MailFetchFailure{
		Category:    category,
		SafeMessage: safeMessage,
		Retryable:   retryable,
		Cause:       domain.ErrMailServiceUnavailable,
	}
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
		recipients := recipientCandidates(strings.Join([]string{message.To, message.Cc, message.Bcc}, ","))
		primaryRecipient := firstNonEmpty(recipients...)
		items = append(items, mailmatchapp.FetchedMessage{
			EmailResourceID:   scope.EmailResourceID,
			ResourceType:      domain.ResourceTypeMicrosoft,
			Recipient:         primaryRecipient,
			Recipients:        recipients,
			Sender:            strings.TrimSpace(message.From),
			Subject:           strings.TrimSpace(message.Subject),
			Body:              body,
			RawSource:         message.RawSource,
			ProviderPayload:   message.ProviderPayload,
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
