package icloud

import (
	"context"
	"errors"
	"fmt"
	stdmail "net/mail"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

type iCloudMailAliasScope struct {
	ResourceID      uint   `gorm:"column:resource_id"`
	AliasID         uint   `gorm:"column:alias_id"`
	AliasEmail      string `gorm:"column:alias_email"`
	AnonymousID     string `gorm:"column:anonymous_id"`
	ForwardToEmail  string `gorm:"column:forward_to_email"`
	RecipientMailID string `gorm:"column:recipient_mail_id"`
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

type iCloudScopedMailRoute struct {
	scope           iCloudMailAliasScope
	recipientMailID string
	since           time.Time
}

type iCloudMailCandidate struct {
	scope  iCloudMailAliasScope
	row    iCloudForwardedMailRow
	sender string
}

type MailFetchRequest struct {
	ResourceID      uint
	Recipient       string
	SinceAt         time.Time
	UntilAt         time.Time
	MaxMessages     int
	FullHistory     bool
	KnownMessageIDs []string
}

type MailFetchedMessage struct {
	Recipient         string
	Sender            string
	Raw               []byte
	ReceivedAt        time.Time
	ProviderMessageID string
}

type MailFetchResult struct {
	Messages []MailFetchedMessage
}

// FetchMail reads Apple-forwarded HME messages from the local domain inbox.
// Relay routing is resolved before the private RFC822 object is read.
func (s *Service) FetchMail(ctx context.Context, request MailFetchRequest) (*MailFetchResult, error) {
	if s == nil || s.db == nil || s.files == nil || request.ResourceID == 0 {
		return nil, ErrICloudMailUnavailable
	}
	scopes, err := s.loadICloudMailAliases(ctx, request)
	if err != nil {
		return nil, err
	}
	if len(scopes) == 0 {
		return &MailFetchResult{}, nil
	}
	readLimit := request.MaxMessages
	if readLimit <= 0 {
		if request.FullHistory {
			readLimit = runtimeconfig.Int(runtimeconfig.ICloudAdminReadLimitKey, runtimeconfig.DefaultICloudAdminReadLimit, 1)
		} else {
			readLimit = runtimeconfig.Int("purchase_read_limit", 30, 1)
		}
	}
	knownRowIDs := iCloudKnownInboundRowIDs(request.KnownMessageIDs)
	candidates, err := s.fetchICloudMailCandidates(ctx, scopes, request.SinceAt, request.UntilAt, readLimit, knownRowIDs)
	if err != nil {
		return nil, err
	}
	result := &MailFetchResult{Messages: make([]MailFetchedMessage, 0, len(candidates))}
	for _, candidate := range candidates {
		object, readErr := s.files.ReadPrivate(ctx, candidate.row.SourceObjectKey)
		if readErr != nil || object == nil {
			if readErr == nil {
				readErr = errors.New("empty inbound object")
			}
			return nil, fmt.Errorf("%w: read inbound message %d: %v", ErrICloudMailUnavailable, candidate.row.ID, readErr)
		}
		result.Messages = append(result.Messages, MailFetchedMessage{
			Recipient:         strings.ToLower(strings.TrimSpace(candidate.scope.AliasEmail)),
			Sender:            candidate.sender,
			Raw:               object.ContentBytes,
			ReceivedAt:        candidate.row.CreatedAt.UTC(),
			ProviderMessageID: fmt.Sprintf("inbound:%d", candidate.row.ID),
		})
	}
	return result, nil
}

func (s *Service) loadICloudMailAliases(ctx context.Context, request MailFetchRequest) ([]iCloudMailAliasScope, error) {
	query := s.db.WithContext(ctx).Table("icloud_aliases AS alias").
		Select(`alias.resource_id, alias.id AS alias_id, alias.email AS alias_email,
			alias.anonymous_id, alias.forward_to_email, alias.recipient_mail_id`).
		Where("alias.resource_id = ? AND alias.status <> ?", request.ResourceID, iCloudResourceDeleted).
		Order("alias.id ASC")
	if !request.FullHistory {
		recipient := strings.ToLower(strings.TrimSpace(request.Recipient))
		if recipient == "" {
			return nil, ErrICloudMailUnavailable
		}
		query = query.Where("LOWER(alias.email) = ?", recipient)
	}
	var scopes []iCloudMailAliasScope
	if err := query.Find(&scopes).Error; err != nil {
		return nil, fmt.Errorf("%w: load aliases", ErrICloudMailUnavailable)
	}
	return scopes, nil
}

func (s *Service) fetchICloudMailCandidates(
	ctx context.Context,
	scopes []iCloudMailAliasScope,
	sinceAt time.Time,
	untilAt time.Time,
	readLimit int,
	knownRowIDs []uint,
) ([]iCloudMailCandidate, error) {
	if readLimit <= 0 {
		return nil, ErrICloudMailUnavailable
	}
	readSkew := runtimeconfig.Duration("read_window_skew_minutes", 2*time.Minute, time.Minute, 0)
	scanLimit := max(runtimeconfig.Int(runtimeconfig.ICloudMailmatchScanLimitKey, runtimeconfig.DefaultICloudMailmatchScanLimit, 1), readLimit)
	routesByMailbox := make(map[string][]iCloudScopedMailRoute)
	mailboxes := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if scope.ResourceID == 0 || strings.TrimSpace(scope.AliasEmail) == "" ||
			!validICloudRelayID(scope.AnonymousID, iCloudHMEAnonymousIDMaxLength) {
			continue
		}
		routes, err := s.loadICloudAliasRoutes(ctx, scope.AliasID, scope.ForwardToEmail, scope.RecipientMailID, sinceAt, readSkew)
		if err != nil {
			return nil, err
		}
		for _, route := range routes {
			forwardToEmail := strings.ToLower(strings.TrimSpace(route.ForwardToEmail))
			if forwardToEmail == "" {
				continue
			}
			if _, exists := routesByMailbox[forwardToEmail]; !exists {
				mailboxes = append(mailboxes, forwardToEmail)
			}
			routesByMailbox[forwardToEmail] = append(routesByMailbox[forwardToEmail], iCloudScopedMailRoute{
				scope: scope, recipientMailID: strings.TrimSpace(route.RecipientMailID), since: route.Since,
			})
		}
	}
	if len(mailboxes) == 0 {
		return nil, nil
	}
	resolverRoutesByMailbox := make(map[string][]iCloudScopedMailRoute, len(mailboxes))
	for _, forwardToEmail := range mailboxes {
		resolverRoutes, err := s.loadICloudMailboxResolverRoutes(ctx, forwardToEmail)
		if err != nil {
			return nil, err
		}
		resolverRoutesByMailbox[forwardToEmail] = resolverRoutes
	}
	seenRows := make(map[uint]struct{})
	candidates := make([]iCloudMailCandidate, 0, readLimit)
	for _, forwardToEmail := range mailboxes {
		targetRoutes := routesByMailbox[forwardToEmail]
		resolverRoutes := resolverRoutesByMailbox[forwardToEmail]
		mailboxSince := earliestICloudRouteSince(targetRoutes)
		err := s.scanICloudMailboxRows(ctx, forwardToEmail, mailboxSince, untilAt, scanLimit, knownRowIDs, func(row iCloudForwardedMailRow) bool {
			if _, exists := seenRows[row.ID]; exists {
				return true
			}
			resolved, sender, ok := resolveICloudMailboxAlias(row.EnvelopeFrom, resolverRoutes)
			if !ok {
				return true
			}
			target, ok := activeICloudTargetScope(targetRoutes, resolved.AliasID, row.CreatedAt)
			if ok {
				seenRows[row.ID] = struct{}{}
				candidates = append(candidates, iCloudMailCandidate{scope: target, row: row, sender: sender})
			}
			return true
		})
		if err != nil {
			return nil, err
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
	slices.Reverse(candidates)
	return candidates, nil
}

func (s *Service) loadICloudMailboxResolverRoutes(ctx context.Context, forwardToEmail string) ([]iCloudScopedMailRoute, error) {
	forwardToEmail = strings.ToLower(strings.TrimSpace(forwardToEmail))
	if s == nil || s.db == nil || forwardToEmail == "" {
		return nil, ErrICloudMailUnavailable
	}
	var current []iCloudMailAliasScope
	if err := s.db.WithContext(ctx).Table("icloud_aliases AS alias").
		Select(`alias.resource_id, alias.id AS alias_id, alias.email AS alias_email,
			alias.anonymous_id, alias.forward_to_email, alias.recipient_mail_id`).
		Where("LOWER(alias.forward_to_email) = ? AND alias.status <> ?", forwardToEmail, iCloudResourceDeleted).
		Find(&current).Error; err != nil {
		return nil, fmt.Errorf("%w: load mailbox aliases", ErrICloudMailUnavailable)
	}
	routes := make([]iCloudScopedMailRoute, 0, len(current))
	seen := make(map[string]struct{}, len(current))
	appendRoute := func(scope iCloudMailAliasScope) {
		recipientMailID := strings.ToLower(strings.TrimSpace(scope.RecipientMailID))
		key := fmt.Sprintf("%d\x00%s", scope.AliasID, recipientMailID)
		if scope.AliasID == 0 {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		routes = append(routes, iCloudScopedMailRoute{scope: scope, recipientMailID: recipientMailID})
	}
	for _, scope := range current {
		appendRoute(scope)
	}
	var persisted []iCloudMailAliasScope
	if err := s.db.WithContext(ctx).Table("icloud_alias_routes AS route").
		Select(`alias.resource_id, alias.id AS alias_id, alias.email AS alias_email,
			alias.anonymous_id, route.forward_to_email, route.recipient_mail_id`).
		Joins("JOIN icloud_aliases AS alias ON alias.id = route.alias_id AND alias.resource_id = route.resource_id").
		Where("LOWER(route.forward_to_email) = ? AND alias.status <> ?", forwardToEmail, iCloudResourceDeleted).
		Find(&persisted).Error; err != nil {
		return nil, fmt.Errorf("%w: load mailbox alias routes", ErrICloudMailUnavailable)
	}
	for _, scope := range persisted {
		appendRoute(scope)
	}
	return routes, nil
}

func resolveICloudMailboxAlias(envelopeFrom string, routes []iCloudScopedMailRoute) (iCloudMailAliasScope, string, bool) {
	var exactScope iCloudMailAliasScope
	exactSender := ""
	exactLength := -1
	for _, route := range routes {
		recipientMailID := strings.TrimSpace(route.recipientMailID)
		if recipientMailID == "" {
			continue
		}
		sender, ok := decodeICloudRelaySenderForRoute(envelopeFrom, route.scope.AnonymousID, recipientMailID)
		if !ok || len(recipientMailID) < exactLength {
			continue
		}
		if len(recipientMailID) == exactLength && exactScope.AliasID != 0 && exactScope.AliasID != route.scope.AliasID {
			return iCloudMailAliasScope{}, "", false
		}
		exactScope, exactSender, exactLength = route.scope, sender, len(recipientMailID)
	}
	if exactScope.AliasID != 0 {
		return exactScope, exactSender, true
	}

	var fallbackScope iCloudMailAliasScope
	fallbackSender := ""
	for _, route := range routes {
		if strings.TrimSpace(route.recipientMailID) != "" {
			continue
		}
		sender, ok := decodeICloudRelaySenderForRoute(envelopeFrom, route.scope.AnonymousID, "")
		if !ok {
			continue
		}
		if fallbackScope.AliasID != 0 && fallbackScope.AliasID != route.scope.AliasID {
			return iCloudMailAliasScope{}, "", false
		}
		fallbackScope, fallbackSender = route.scope, sender
	}
	if fallbackScope.AliasID == 0 {
		return iCloudMailAliasScope{}, "", false
	}
	return fallbackScope, fallbackSender, true
}

func activeICloudTargetScope(routes []iCloudScopedMailRoute, aliasID uint, receivedAt time.Time) (iCloudMailAliasScope, bool) {
	for _, route := range routes {
		if route.scope.AliasID != aliasID || (!route.since.IsZero() && receivedAt.Before(route.since)) {
			continue
		}
		return route.scope, true
	}
	return iCloudMailAliasScope{}, false
}

func earliestICloudRouteSince(routes []iCloudScopedMailRoute) time.Time {
	var earliest time.Time
	for _, route := range routes {
		if route.since.IsZero() {
			return time.Time{}
		}
		if earliest.IsZero() || route.since.Before(earliest) {
			earliest = route.since
		}
	}
	return earliest
}

func (s *Service) loadICloudAliasRoutes(ctx context.Context, aliasID uint, currentForward, currentRecipient string, since time.Time, readSkew time.Duration) ([]iCloudMailRoute, error) {
	if s == nil || s.db == nil || aliasID == 0 {
		return nil, ErrICloudMailUnavailable
	}
	routes := make([]iCloudMailRoute, 0, 2)
	byPair := make(map[string]int)
	var persisted []iCloudAliasRouteModel
	if err := s.db.WithContext(ctx).Where("alias_id = ?", aliasID).Order("first_seen_at ASC, id ASC").Find(&persisted).Error; err != nil {
		return nil, fmt.Errorf("%w: load alias routes", ErrICloudMailUnavailable)
	}
	for _, route := range persisted {
		appendICloudMailRoute(&routes, byPair, route.ForwardToEmail, route.RecipientMailID, route.FirstSeenAt, since, readSkew)
	}
	appendICloudMailRoute(&routes, byPair, currentForward, currentRecipient, time.Time{}, since, readSkew)
	return routes, nil
}

func appendICloudMailRoute(routes *[]iCloudMailRoute, byPair map[string]int, forwardToEmail, recipientMailID string, firstSeenAt, since time.Time, readSkew time.Duration) {
	forwardToEmail = strings.ToLower(strings.TrimSpace(forwardToEmail))
	recipientMailID = strings.ToLower(strings.TrimSpace(recipientMailID))
	if forwardToEmail == "" {
		return
	}
	routeSince := since
	if !since.IsZero() && firstSeenAt.After(routeSince) {
		routeSince = firstSeenAt.Add(-readSkew)
		if routeSince.Before(since) {
			routeSince = since
		}
	}
	key := forwardToEmail + "\x00" + recipientMailID
	if index, exists := byPair[key]; exists {
		if routeSince.IsZero() || (!(*routes)[index].Since.IsZero() && routeSince.Before((*routes)[index].Since)) {
			(*routes)[index].Since = routeSince
		}
		return
	}
	byPair[key] = len(*routes)
	*routes = append(*routes, iCloudMailRoute{ForwardToEmail: forwardToEmail, RecipientMailID: recipientMailID, Since: routeSince})
}

func (s *Service) scanICloudMailboxRows(
	ctx context.Context,
	forwardToEmail string,
	sinceAt time.Time,
	untilAt time.Time,
	limit int,
	knownRowIDs []uint,
	visit func(iCloudForwardedMailRow) bool,
) error {
	mailboxKey := mailbox.Normalize(forwardToEmail)
	if s == nil || s.db == nil || mailboxKey == "" || limit <= 0 || visit == nil {
		return ErrICloudMailUnavailable
	}
	query := s.db.WithContext(ctx).Table("inbound_mails").
		Select("id, envelope_from, source_object_key, created_at").
		Where("resource_type = ? AND mailbox_key = ? AND status = ?", "domain", mailboxKey, "stored").
		Order("created_at DESC, id DESC").Limit(limit)
	if !sinceAt.IsZero() {
		query = query.Where("created_at >= ?", sinceAt.UTC())
	}
	if !untilAt.IsZero() {
		query = query.Where("created_at <= ?", untilAt.UTC())
	}
	if len(knownRowIDs) > 0 {
		query = query.Where("id NOT IN ?", knownRowIDs)
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

func decodeICloudRelaySender(envelopeFrom string, anonymousID string) (string, bool) {
	return decodeICloudRelaySenderForRoute(envelopeFrom, anonymousID, "")
}

func decodeICloudRelaySenderForRoute(envelopeFrom, anonymousID, recipientMailID string) (string, bool) {
	parsed, err := stdmail.ParseAddress(strings.TrimSpace(envelopeFrom))
	if err != nil {
		return "", false
	}
	address := strings.ToLower(strings.TrimSpace(parsed.Address))
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || address[at+1:] != "icloud.com" {
		return "", false
	}
	local := address[:at]
	var encodedSender string
	recipientMailID = strings.ToLower(strings.TrimSpace(recipientMailID))
	if recipientMailID != "" {
		if !validICloudRelayID(recipientMailID, iCloudHMERecipientMailIDMaxLength) {
			return "", false
		}
		suffix := "_" + recipientMailID
		if !strings.HasSuffix(local, suffix) {
			return "", false
		}
		encodedSender = strings.TrimSuffix(local, suffix)
	} else {
		var ok bool
		encodedSender, ok = splitICloudRelaySender(local, anonymousID)
		if !ok {
			return "", false
		}
	}
	separator := strings.LastIndex(encodedSender, "_at_")
	if separator <= 0 || separator+len("_at_") >= len(encodedSender) {
		return "", false
	}
	restored := strings.ReplaceAll(encodedSender[:separator], "_", ".") + "@" +
		strings.ReplaceAll(encodedSender[separator+len("_at_"):], "_", ".")
	restoredAddress, err := stdmail.ParseAddress(restored)
	if err != nil || !strings.EqualFold(strings.TrimSpace(restoredAddress.Address), restored) {
		return "", false
	}
	return restored, true
}

func iCloudKnownInboundRowIDs(values []string) []uint {
	const providerPrefix = "provider:smtp:inbound:inbound:"
	seen := make(map[uint]struct{}, len(values))
	result := make([]uint, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		switch {
		case strings.HasPrefix(value, providerPrefix):
			value = strings.TrimPrefix(value, providerPrefix)
		case strings.HasPrefix(value, "inbound:"):
			value = strings.TrimPrefix(value, "inbound:")
		default:
			continue
		}
		parsed, err := strconv.ParseUint(value, 10, 64)
		rowID := uint(parsed)
		if err != nil || parsed == 0 || uint64(rowID) != parsed {
			continue
		}
		if _, exists := seen[rowID]; exists {
			continue
		}
		seen[rowID] = struct{}{}
		result = append(result, rowID)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func splitICloudRelaySender(local, anonymousID string) (string, bool) {
	for separator := strings.LastIndexByte(local, '_'); separator >= 0; separator = strings.LastIndexByte(local[:separator], '_') {
		if iCloudAnonymousIDMatchesRelayTail(anonymousID, local[separator+1:]) {
			return local[:separator], true
		}
	}
	return "", false
}

func iCloudAnonymousIDMatchesRelayTail(anonymousID, relayTail string) bool {
	anonymousID = strings.ToLower(strings.TrimSpace(anonymousID))
	relayTail = strings.ToLower(strings.TrimSpace(relayTail))
	if !validICloudRelayID(anonymousID, iCloudHMEAnonymousIDMaxLength) || relayTail == "" {
		return false
	}
	for index := 0; index < len(relayTail); index++ {
		if len(anonymousID) > 0 && relayTail[index] == anonymousID[0] {
			anonymousID = anonymousID[1:]
		}
	}
	return anonymousID == ""
}

func validICloudRelayID(value string, maxLength int) bool {
	value = strings.TrimSpace(value)
	return validICloudHMEText(value, maxLength, false) && !strings.Contains(value, "@")
}
