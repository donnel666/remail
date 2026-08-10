package icloud

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	stdmail "net/mail"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
)

const iCloudDeliveryProbeReadLimit = 100

type iCloudPurchaseMailScope struct {
	ResourceID      uint      `gorm:"column:resource_id"`
	AliasID         uint      `gorm:"column:alias_id"`
	AliasEmail      string    `gorm:"column:alias_email"`
	RecipientMailID string    `gorm:"column:recipient_mail_id"`
	ForwardToEmail  string    `gorm:"column:forward_to_email"`
	AllocatedAt     time.Time `gorm:"column:allocated_at"`
}

type iCloudMailRoute struct {
	ForwardToEmail  string
	RecipientMailID string
	Since           time.Time
}

type iCloudForwardedMailRow struct {
	ID              uint      `gorm:"column:id"`
	EnvelopeFrom    string    `gorm:"column:envelope_from"`
	SourceObjectKey string    `gorm:"column:source_object_key"`
	CreatedAt       time.Time `gorm:"column:created_at"`
}

type iCloudMailCandidate struct {
	scope  iCloudPurchaseMailScope
	row    iCloudForwardedMailRow
	sender string
}

type iCloudScopedMailRoute struct {
	scope           iCloudPurchaseMailScope
	recipientMailID string
	since           time.Time
	suffix          string
}

// FetchICloudMail reads one allocated alias from its persisted domain inbox.
// The Apple relay recipient ID is authoritative; the encoded prefix is only
// decoded after that suffix has selected the alias and resource.
func (s *Service) FetchICloudMail(ctx context.Context, orderNo string) error {
	orderNo = strings.TrimSpace(orderNo)
	if s == nil || s.db == nil || s.files == nil || s.mailIngest == nil || orderNo == "" {
		return ErrICloudMailUnavailable
	}
	scope, err := s.loadICloudPurchaseMailScope(ctx, orderNo)
	if err != nil {
		return err
	}
	_, _, _, err = s.fetchICloudMailScopes(ctx, []iCloudPurchaseMailScope{*scope}, runtimeconfig.Int("purchase_read_limit", 30, 1), nil)
	return err
}

// FetchICloudResourceMail is retained for order/API callers that do not own a
// resource-fetch generation. The administrator worker uses the fenced variant.
func (s *Service) FetchICloudResourceMail(ctx context.Context, resourceID uint) error {
	_, _, _, err := s.FetchICloudResourceMailWithFence(ctx, resourceID, nil)
	return err
}

// FetchICloudResourceMailWithFence reads every persisted alias route, including
// released and never-allocated aliases. The read budget is deliberately
// separate from purchase_read_limit so an administrator scan cannot starve or
// silently truncate customer pickup behavior.
func (s *Service) FetchICloudResourceMailWithFence(ctx context.Context, resourceID uint, fence func(context.Context) error) (int, int, int, error) {
	if s == nil || s.db == nil || s.files == nil || s.mailIngest == nil || resourceID == 0 {
		return 0, 0, 0, ErrICloudMailUnavailable
	}
	var scopes []iCloudPurchaseMailScope
	if err := s.db.WithContext(ctx).Table("icloud_aliases AS alias").
		Select(`alias.resource_id, alias.id AS alias_id, alias.email AS alias_email,
			alias.recipient_mail_id, alias.forward_to_email`).
		Where("alias.resource_id = ? AND alias.status <> ?", resourceID, iCloudResourceDeleted).
		Order("alias.id ASC").
		Find(&scopes).Error; err != nil {
		return 0, 0, 0, fmt.Errorf("%w: load aliases", ErrICloudMailUnavailable)
	}
	if len(scopes) == 0 {
		return 0, 0, 0, fmt.Errorf("%w: no persisted iCloud aliases", ErrICloudMailUnavailable)
	}
	return s.fetchICloudMailScopes(ctx, scopes, runtimeconfig.Int(runtimeconfig.ICloudAdminReadLimitKey, 5000, 1), fence)
}

func (s *Service) fetchICloudMailScopes(ctx context.Context, scopes []iCloudPurchaseMailScope, readLimit int, fence func(context.Context) error) (int, int, int, error) {
	if s == nil || s.files == nil || s.mailIngest == nil {
		return 0, 0, 0, ErrICloudMailUnavailable
	}
	readSkew := runtimeconfig.Duration("read_window_skew_minutes", 2*time.Minute, time.Minute, 0)
	scanLimit := runtimeconfig.Int(runtimeconfig.ICloudMailmatchScanLimitKey, runtimeconfig.DefaultICloudMailmatchScanLimit, 1)
	if readLimit <= 0 {
		return 0, 0, 0, ErrICloudMailUnavailable
	}
	fetched, storedCount, matchedCount := 0, 0, 0
	seenRows := make(map[uint]struct{})
	candidates := make([]iCloudMailCandidate, 0, readLimit)
	routesByMailbox := make(map[string][]iCloudScopedMailRoute)
	mailboxes := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if scope.ResourceID == 0 || strings.TrimSpace(scope.AliasEmail) == "" {
			continue
		}
		routes, err := s.loadICloudAliasRoutes(ctx, scope.AliasID, scope.ForwardToEmail, scope.RecipientMailID, scope.AllocatedAt)
		if err != nil {
			return fetched, storedCount, matchedCount, err
		}
		if len(routes) == 0 {
			continue
		}
		for _, route := range routes {
			forwardToEmail := strings.ToLower(strings.TrimSpace(route.ForwardToEmail))
			suffix, validSuffix := iCloudRelaySuffix(route.RecipientMailID)
			if forwardToEmail == "" || !validSuffix {
				continue
			}
			routeSince := route.Since
			if !routeSince.IsZero() {
				routeSince = routeSince.Add(-readSkew)
			}
			if _, exists := routesByMailbox[forwardToEmail]; !exists {
				mailboxes = append(mailboxes, forwardToEmail)
			}
			routesByMailbox[forwardToEmail] = append(routesByMailbox[forwardToEmail], iCloudScopedMailRoute{
				scope: scope, recipientMailID: strings.ToLower(strings.TrimSpace(route.RecipientMailID)), since: routeSince, suffix: suffix,
			})
		}
	}
	if len(mailboxes) == 0 {
		return fetched, storedCount, matchedCount, fmt.Errorf("%w: no readable iCloud alias route", ErrICloudMailUnavailable)
	}
	for _, forwardToEmail := range mailboxes {
		routes := routesByMailbox[forwardToEmail]
		// Prefer the longest recipient suffix when IDs overlap by an underscore.
		// This keeps an exact Apple recipient ID from being captured by a shorter one.
		sort.SliceStable(routes, func(i, j int) bool {
			return len(routes[i].suffix) > len(routes[j].suffix)
		})
		var mailboxSince time.Time
		unbounded := false
		for _, route := range routes {
			if route.since.IsZero() {
				unbounded = true
				break
			}
			if mailboxSince.IsZero() || route.since.Before(mailboxSince) {
				mailboxSince = route.since
			}
		}
		if unbounded {
			mailboxSince = time.Time{}
		}
		matchedInMailbox := 0
		scanErr := s.scanICloudMailboxRows(ctx, forwardToEmail, mailboxSince, scanLimit, func(row iCloudForwardedMailRow) bool {
			if _, exists := seenRows[row.ID]; exists {
				return true
			}
			for _, route := range routes {
				if !route.since.IsZero() && row.CreatedAt.Before(route.since) {
					continue
				}
				sender, ok := decodeICloudRelaySender(row.EnvelopeFrom, route.recipientMailID)
				if !ok {
					continue
				}
				seenRows[row.ID] = struct{}{}
				candidates = append(candidates, iCloudMailCandidate{scope: route.scope, row: row, sender: sender})
				matchedInMailbox++
				return matchedInMailbox < readLimit
			}
			return true
		})
		if scanErr != nil {
			return fetched, storedCount, matchedCount, scanErr
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].row.CreatedAt.Equal(candidates[j].row.CreatedAt) {
			return candidates[i].row.CreatedAt.After(candidates[j].row.CreatedAt)
		}
		return candidates[i].row.ID > candidates[j].row.ID
	})
	if len(candidates) > readLimit {
		candidates = candidates[:readLimit]
	}
	// The query bounds newest-first; ingestion remains chronological so an
	// older matching code cannot overwrite a newer delivery decision.
	slices.Reverse(candidates)
	for _, candidate := range candidates {
		object, readErr := s.files.ReadPrivate(ctx, candidate.row.SourceObjectKey)
		if readErr != nil || object == nil {
			if readErr == nil {
				readErr = errors.New("empty inbound object")
			}
			return fetched, storedCount, matchedCount, fmt.Errorf("%w: read inbound message %d: %v", ErrICloudMailUnavailable, candidate.row.ID, readErr)
		}
		fetched++
		result, ingestErr := s.ingestICloudMail(ctx, candidate.scope.ResourceID, strings.ToLower(strings.TrimSpace(candidate.scope.AliasEmail)), candidate.sender, object.ContentBytes, candidate.row.CreatedAt.UTC(), fmt.Sprintf("inbound:%d", candidate.row.ID), fence)
		if ingestErr != nil {
			return fetched, storedCount, matchedCount, fmt.Errorf("%w: ingest inbound message: %w", ErrICloudMailUnavailable, ingestErr)
		}
		storedCount += result.Stored
		matchedCount += result.Matched
	}
	return fetched, storedCount, matchedCount, nil
}

func (s *Service) loadICloudPurchaseMailScope(ctx context.Context, orderNo string) (*iCloudPurchaseMailScope, error) {
	var scope iCloudPurchaseMailScope
	err := s.db.WithContext(ctx).Table("icloud_allocations AS allocation").
		Select(`allocation.resource_id, allocation.alias_id,
			allocation.email AS alias_email,
			alias.recipient_mail_id, alias.forward_to_email,
			allocation.created_at AS allocated_at`).
		Joins("JOIN icloud_aliases AS alias ON alias.id = allocation.alias_id AND alias.resource_id = allocation.resource_id").
		Where("allocation.order_no = ? AND allocation.status = ?", orderNo, "allocated").
		Take(&scope).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrICloudMailUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load allocation", ErrICloudMailUnavailable)
	}
	if scope.ResourceID == 0 || scope.AliasID == 0 || strings.TrimSpace(scope.AliasEmail) == "" || scope.AllocatedAt.IsZero() {
		return nil, ErrICloudMailUnavailable
	}
	return &scope, nil
}

func (s *Service) loadICloudAliasRoutes(ctx context.Context, aliasID uint, currentForward, currentRecipient string, since time.Time) ([]iCloudMailRoute, error) {
	routes := make([]iCloudMailRoute, 0, 2)
	if s == nil || s.db == nil || aliasID == 0 {
		return nil, ErrICloudMailUnavailable
	}
	if s.db.Migrator().HasTable("icloud_alias_routes") {
		var persisted []iCloudAliasRouteModel
		if err := s.db.WithContext(ctx).Where("alias_id = ?", aliasID).Order("first_seen_at ASC, id ASC").Find(&persisted).Error; err != nil {
			return nil, fmt.Errorf("%w: load alias routes", ErrICloudMailUnavailable)
		}
		for _, route := range persisted {
			if strings.TrimSpace(route.ForwardToEmail) == "" || strings.TrimSpace(route.RecipientMailID) == "" {
				continue
			}
			routeSince := since
			if !since.IsZero() {
				routeSince = maxICloudTime(route.FirstSeenAt, since)
			}
			routes = append(routes, iCloudMailRoute{ForwardToEmail: route.ForwardToEmail, RecipientMailID: route.RecipientMailID, Since: routeSince})
		}
	}
	currentForward = strings.ToLower(strings.TrimSpace(currentForward))
	currentRecipient = strings.ToLower(strings.TrimSpace(currentRecipient))
	if currentForward != "" && currentRecipient != "" {
		seen := false
		for _, route := range routes {
			if strings.EqualFold(route.ForwardToEmail, currentForward) && strings.EqualFold(route.RecipientMailID, currentRecipient) {
				seen = true
				break
			}
		}
		if !seen {
			routes = append(routes, iCloudMailRoute{ForwardToEmail: currentForward, RecipientMailID: currentRecipient, Since: since})
		}
	}
	return routes, nil
}

func maxICloudTime(first, second time.Time) time.Time {
	if first.IsZero() || second.After(first) {
		return second
	}
	return first
}

func (s *Service) ingestICloudMail(ctx context.Context, resourceID uint, recipient, sender string, raw []byte, receivedAt time.Time, providerMessageID string, fence func(context.Context) error) (MailIngestResult, error) {
	if fence != nil {
		fenced, ok := s.mailIngest.(MailIngestWithFencePort)
		if !ok {
			return MailIngestResult{}, ErrICloudMailUnavailable
		}
		return fenced.IngestICloudMailWithFence(ctx, resourceID, recipient, sender, raw, receivedAt, providerMessageID, fence)
	}
	if err := s.mailIngest.IngestICloudMail(ctx, resourceID, recipient, sender, raw, receivedAt, providerMessageID); err != nil {
		return MailIngestResult{}, err
	}
	return MailIngestResult{Stored: 1}, nil
}

func (s *Service) listICloudForwardedMailRows(
	ctx context.Context,
	forwardToEmail string,
	recipientMailID string,
	since time.Time,
	limit int,
) ([]iCloudForwardedMailRow, error) {
	suffix, ok := iCloudRelaySuffix(recipientMailID)
	if s == nil || s.db == nil || !ok || limit <= 0 {
		return nil, ErrICloudMailUnavailable
	}
	rows := make([]iCloudForwardedMailRow, 0, limit)
	err := s.scanICloudMailboxRows(ctx, forwardToEmail, since, limit, func(row iCloudForwardedMailRow) bool {
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(row.EnvelopeFrom)), suffix) {
			rows = append(rows, row)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// scanICloudMailboxRows bounds the raw mailbox read before relay-suffix
// matching. The callback may stop the scan once the caller has enough matches.
func (s *Service) scanICloudMailboxRows(
	ctx context.Context,
	forwardToEmail string,
	since time.Time,
	limit int,
	visit func(iCloudForwardedMailRow) bool,
) error {
	forwardToEmail = strings.ToLower(strings.TrimSpace(forwardToEmail))
	mailboxKey := mailbox.Normalize(forwardToEmail)
	if s == nil || s.db == nil || mailboxKey == "" || limit <= 0 || visit == nil {
		return ErrICloudMailUnavailable
	}
	query := s.db.WithContext(ctx).Table("inbound_mails").
		Select("id, envelope_from, source_object_key, created_at").
		Where("resource_type = ? AND mailbox_key = ? AND status = ?", "domain", mailboxKey, "stored").
		Order("created_at DESC, id DESC").
		Limit(limit)
	if !since.IsZero() {
		query = query.Where("created_at >= ?", since.UTC())
	}
	rows, err := query.Rows()
	if err != nil {
		return fmt.Errorf("%w: list domain inbox", ErrICloudMailUnavailable)
	}
	defer rows.Close()
	for rows.Next() {
		var row iCloudForwardedMailRow
		if err := query.ScanRows(rows, &row); err != nil {
			return fmt.Errorf("%w: scan domain inbox", ErrICloudMailUnavailable)
		}
		if !visit(row) {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: iterate domain inbox", ErrICloudMailUnavailable)
	}
	return nil
}

func (s *Service) findICloudDeliveryProbe(
	ctx context.Context,
	forwardToEmail string,
	recipientMailID string,
	token string,
	since time.Time,
) (bool, error) {
	token = strings.TrimSpace(token)
	if s == nil || s.files == nil || token == "" {
		return false, ErrICloudMailUnavailable
	}
	rows, err := s.listICloudForwardedMailRows(
		ctx,
		forwardToEmail,
		recipientMailID,
		since,
		iCloudDeliveryProbeReadLimit,
	)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		stored, readErr := s.files.ReadPrivate(ctx, row.SourceObjectKey)
		if readErr != nil || stored == nil {
			return false, ErrICloudMailUnavailable
		}
		if bytes.Contains(stored.ContentBytes, []byte(token)) {
			return true, nil
		}
	}
	return false, nil
}

// findICloudRecipientProbes scans the configured domain mailbox only while an
// alias lacks its Apple recipientMailId. The probe token maps an otherwise
// opaque relay envelope back to the exact alias that received the message.
func (s *Service) findICloudRecipientProbes(
	ctx context.Context,
	forwardToEmail string,
	tokens map[string]time.Time,
) (map[string]string, error) {
	if s == nil || s.db == nil || s.files == nil || len(tokens) == 0 {
		return nil, ErrICloudMailUnavailable
	}
	forwardToEmail = strings.ToLower(strings.TrimSpace(forwardToEmail))
	mailboxKey := mailbox.Normalize(forwardToEmail)
	if mailboxKey == "" {
		return nil, ErrICloudMailUnavailable
	}
	var since time.Time
	for _, startedAt := range tokens {
		if startedAt.IsZero() {
			continue
		}
		if since.IsZero() || startedAt.Before(since) {
			since = startedAt
		}
	}
	if since.IsZero() {
		return map[string]string{}, nil
	}
	var rows []iCloudForwardedMailRow
	if err := s.db.WithContext(ctx).Table("inbound_mails").
		Select("id, envelope_from, source_object_key, created_at").
		Where("resource_type = ? AND mailbox_key = ? AND status = ?", "domain", mailboxKey, "stored").
		Where("created_at >= ?", since.Add(-time.Minute).UTC()).
		Order("created_at DESC, id DESC").
		Limit(iCloudRecipientProbeReadLimit).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("%w: list recipient discovery probes", ErrICloudMailUnavailable)
	}
	found := make(map[string]string)
	for _, row := range rows {
		stored, readErr := s.files.ReadPrivate(ctx, row.SourceObjectKey)
		if readErr != nil || stored == nil {
			return nil, ErrICloudMailUnavailable
		}
		for token, startedAt := range tokens {
			if _, exists := found[token]; exists || row.CreatedAt.Before(startedAt.Add(-time.Minute)) ||
				!bytes.Contains(stored.ContentBytes, []byte(token)) {
				continue
			}
			recipientMailID, ok := extractICloudRelayRecipientID(row.EnvelopeFrom, stored.ContentBytes)
			if ok {
				found[token] = recipientMailID
			}
		}
		if len(found) == len(tokens) {
			break
		}
	}
	return found, nil
}

func iCloudRelaySuffix(recipientMailID string) (string, bool) {
	recipientMailID = strings.ToLower(strings.TrimSpace(recipientMailID))
	if !validICloudHMEText(recipientMailID, iCloudHMERecipientIDMaxLength, false) ||
		strings.ContainsAny(recipientMailID, "@\r\n") {
		return "", false
	}
	return "_" + recipientMailID + "@icloud.com", true
}

func decodeICloudRelaySender(envelopeFrom string, recipientMailID string) (string, bool) {
	parsed, err := stdmail.ParseAddress(strings.TrimSpace(envelopeFrom))
	if err != nil {
		return "", false
	}
	address := strings.ToLower(strings.TrimSpace(parsed.Address))
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || address[at+1:] != "icloud.com" {
		return "", false
	}
	suffix, ok := iCloudRelaySuffix(recipientMailID)
	if !ok || !strings.HasSuffix(address, suffix) {
		return "", false
	}
	encodedSender := strings.TrimSuffix(address, suffix)
	separator := strings.LastIndex(encodedSender, "_at_")
	if separator <= 0 || separator+len("_at_") >= len(encodedSender) {
		return "", false
	}
	localPart := strings.ReplaceAll(encodedSender[:separator], "_", ".")
	domainPart := strings.ReplaceAll(encodedSender[separator+len("_at_"):], "_", ".")
	restored := localPart + "@" + domainPart
	restoredAddress, err := stdmail.ParseAddress(restored)
	if err != nil || !strings.EqualFold(strings.TrimSpace(restoredAddress.Address), restored) {
		return "", false
	}
	return restored, true
}

// extractICloudRelayRecipientID recovers the Apple recipient suffix from a
// probe message before the alias has a recipientMailId. The forwarded RFC822
// From header gives us the exact encoded sender prefix; taking an arbitrary
// suffix from MAIL FROM would be ambiguous because both sender domains and
// Apple IDs may contain underscores.
func extractICloudRelayRecipientID(envelopeFrom string, raw []byte) (string, bool) {
	parsedEnvelope, err := stdmail.ParseAddress(strings.TrimSpace(envelopeFrom))
	if err != nil {
		return "", false
	}
	address := strings.ToLower(strings.TrimSpace(parsedEnvelope.Address))
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || address[at+1:] != "icloud.com" {
		return "", false
	}
	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", false
	}
	from, err := stdmail.ParseAddress(strings.TrimSpace(message.Header.Get("From")))
	if err != nil {
		return "", false
	}
	sender := strings.ToLower(strings.TrimSpace(from.Address))
	senderAt := strings.LastIndexByte(sender, '@')
	if senderAt <= 0 || senderAt == len(sender)-1 {
		return "", false
	}
	encodedSender := strings.ReplaceAll(sender[:senderAt], ".", "_") + "_at_" +
		strings.ReplaceAll(sender[senderAt+1:], ".", "_")
	prefix := encodedSender + "_"
	if !strings.HasPrefix(address[:at], prefix) {
		return "", false
	}
	candidate := strings.TrimSpace(address[len(prefix):at])
	if !validICloudHMEText(candidate, iCloudHMERecipientIDMaxLength, false) || strings.ContainsAny(candidate, "@\r\n") {
		return "", false
	}
	return candidate, true
}
