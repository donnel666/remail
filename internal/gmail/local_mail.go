package gmail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	stdmail "net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	localGmailFetchTimeout   = 15 * time.Second
	localGmailFetchLimit     = 100
	localGmailDefaultBodyMax = 1 << 20
	localGmailBatchBodyMax   = 100 << 20
	localGmailInboxFolder    = "Inbox"
	localGmailSpamFolder     = "Spam"
)

type MailIngestPort interface {
	IngestGmailMail(ctx context.Context, resourceID uint, recipient string, raw []byte, receivedAt time.Time, providerMessageID, folder string, fence func(context.Context) error) (stored int, matched int, err error)
}

type localGmailFetchedMessage struct {
	UID               uint64
	Folder            string
	Recipient         string
	ProviderMessageID string
	Raw               []byte
	ReceivedAt        time.Time
}

type localGmailFolderCursors struct {
	Inbox uint64
	Spam  uint64
}

type localGmailFetchFunc func(
	ctx context.Context,
	email, appPassword string,
	cursors localGmailFolderCursors,
	since time.Time,
	fullHistory bool,
) ([]localGmailFetchedMessage, localGmailFolderCursors, error)

type localGmailFolder struct {
	ID    string
	Label string
}

func fetchLocalGmailMessagesThrough(
	ctx context.Context,
	email, appPassword string,
	cursors localGmailFolderCursors,
	since time.Time,
	proxyURL string,
	fingerprint localGmailClientFingerprint,
	fullHistory bool,
) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
	client, closeClient, err := openLocalGmailPickupIMAP(ctx, email, appPassword, proxyURL, fingerprint)
	if err != nil {
		return nil, cursors, err
	}
	defer closeClient()
	messages := make([]localGmailFetchedMessage, 0)
	nextCursors := cursors
	completed := map[string]bool{}
	for _, folder := range localGmailCandidateFolders(client) {
		if completed[folder.Label] {
			continue
		}
		selected, selectErr := client.Select(folder.ID, &imap.SelectOptions{ReadOnly: true}).Wait()
		if selectErr != nil {
			continue
		}
		cursor := cursors.Inbox
		if folder.Label == localGmailSpamFolder {
			cursor = cursors.Spam
		}
		fetched, nextCursor, fetchErr := fetchSelectedLocalGmailFolder(
			client, email, folder, selected.UIDValidity, cursor, since, fullHistory,
		)
		if fetchErr != nil {
			return nil, cursors, fetchErr
		}
		messages = append(messages, fetched...)
		if folder.Label == localGmailSpamFolder {
			nextCursors.Spam = nextCursor
		} else {
			nextCursors.Inbox = nextCursor
		}
		completed[folder.Label] = true
	}
	_ = client.Logout().Wait()
	if !completed[localGmailInboxFolder] || !completed[localGmailSpamFolder] {
		return nil, cursors, errors.New("gmail: Inbox and Spam must both be read completely")
	}
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].ReceivedAt.Equal(messages[j].ReceivedAt) {
			if messages[i].Folder != messages[j].Folder {
				return messages[i].Folder < messages[j].Folder
			}
			return messages[i].UID < messages[j].UID
		}
		return messages[i].ReceivedAt.Before(messages[j].ReceivedAt)
	})
	return messages, nextCursors, nil
}

func fetchSelectedLocalGmailFolder(
	client *imapclient.Client,
	rootEmail string,
	folder localGmailFolder,
	uidValidity uint32,
	cursor uint64,
	since time.Time,
	fullHistory bool,
) ([]localGmailFetchedMessage, uint64, error) {
	cursorUID := localGmailCursorUID(cursor, uidValidity)
	if cursorUID >= math.MaxUint32 {
		return nil, joinLocalGmailCursor(uidValidity, cursorUID), nil
	}
	criteria := &imap.SearchCriteria{}
	effectiveSince := localGmailSearchSince(cursorUID, since)
	if !effectiveSince.IsZero() {
		criteria.Since = effectiveSince
	}
	if cursorUID > 0 {
		uidSet := imap.UIDSet{}
		uidSet.AddRange(imap.UID(cursorUID+1), 0)
		criteria.UID = []imap.UIDSet{uidSet}
	}
	search, err := client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, cursor, err
	}
	uids := search.AllUIDs()
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	messageLimit, bodySection := localGmailFetchOptions(fullHistory)
	uids = oldestLocalGmailUIDs(uids, messageLimit)
	if len(uids) == 0 {
		return nil, joinLocalGmailCursor(uidValidity, cursorUID), nil
	}
	command := client.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{bodySection},
	})
	messages := make([]localGmailFetchedMessage, 0, len(uids))
	nextCursorUID := cursorUID
	for data := command.Next(); data != nil; data = command.Next() {
		row, collectErr := data.Collect()
		if collectErr != nil {
			_ = command.Close()
			return nil, cursor, collectErr
		}
		uid := uint64(row.UID)
		if uid > nextCursorUID {
			nextCursorUID = uid
		}
		raw := row.FindBodySection(bodySection)
		if len(raw) == 0 {
			continue
		}
		receivedAt := row.InternalDate.UTC()
		if receivedAt.IsZero() {
			receivedAt = time.Now().UTC()
		}
		if !effectiveSince.IsZero() && receivedAt.Before(effectiveSince) {
			continue
		}
		recipient := localGmailOriginalRecipient(rootEmail, raw)
		if recipient == "" {
			continue
		}
		messages = append(messages, localGmailFetchedMessage{
			UID: uid, Folder: folder.Label, Recipient: recipient,
			ProviderMessageID: localGmailProviderMessageID(folder.Label, uidValidity, uid),
			Raw:               raw, ReceivedAt: receivedAt,
		})
	}
	if err := command.Close(); err != nil {
		return nil, cursor, err
	}
	return messages, joinLocalGmailCursor(uidValidity, nextCursorUID), nil
}

func localGmailSearchSince(cursorUID uint64, since time.Time) time.Time {
	if cursorUID > 0 || since.IsZero() {
		return time.Time{}
	}
	return since.UTC().Add(-2 * time.Minute)
}

func oldestLocalGmailUIDs(uids []imap.UID, limit int) []imap.UID {
	if len(uids) > limit {
		return uids[:limit]
	}
	return uids
}

func splitLocalGmailCursor(cursor uint64) (uint32, uint64) {
	return uint32(cursor >> 32), cursor & math.MaxUint32
}

func localGmailCursorUID(cursor uint64, uidValidity uint32) uint64 {
	storedValidity, uid := splitLocalGmailCursor(cursor)
	if uidValidity != 0 && storedValidity != uidValidity {
		return 0
	}
	return uid
}

func joinLocalGmailCursor(uidValidity uint32, uid uint64) uint64 {
	return uint64(uidValidity)<<32 | uid&math.MaxUint32
}

func localGmailProviderMessageID(folder string, uidValidity uint32, uid uint64) string {
	return strings.ToLower(strings.TrimSpace(folder)) + ":" +
		strconv.FormatUint(uint64(uidValidity), 10) + ":" + strconv.FormatUint(uid, 10)
}

func localGmailFetchOptions(fullHistory bool) (int, *imap.FetchItemBodySection) {
	if fullHistory {
		return min(runtimeconfig.Int("mail_stream_batch_size", localGmailFetchLimit, 1), localGmailFetchLimit),
			&imap.FetchItemBodySection{Peek: true}
	}
	bodyLimit := runtimeconfig.Int("max_inbound_body_bytes", localGmailDefaultBodyMax, 1)
	if bodyLimit > localGmailBatchBodyMax {
		bodyLimit = localGmailBatchBodyMax
	}
	messageLimit := localGmailBatchBodyMax / bodyLimit
	if messageLimit > localGmailFetchLimit {
		messageLimit = localGmailFetchLimit
	}
	return messageLimit, &imap.FetchItemBodySection{
		Peek: true,
		Partial: &imap.SectionPartial{
			Size: int64(bodyLimit),
		},
	}
}

func localGmailCandidateFolders(client *imapclient.Client) []localGmailFolder {
	if client == nil {
		return localGmailCandidateFoldersFromList(nil)
	}
	listed, err := client.List("", "*", nil).Collect()
	if err != nil {
		return localGmailCandidateFoldersFromList(nil)
	}
	return localGmailCandidateFoldersFromList(listed)
}

func localGmailCandidateFoldersFromList(listed []*imap.ListData) []localGmailFolder {
	fallback := []localGmailFolder{
		{ID: "INBOX", Label: localGmailInboxFolder},
		{ID: "[Gmail]/Spam", Label: localGmailSpamFolder},
		{ID: "[Google Mail]/Spam", Label: localGmailSpamFolder},
		{ID: "Spam", Label: localGmailSpamFolder},
	}
	folders := make([]localGmailFolder, 0, len(listed)+len(fallback))
	specialSpam := make([]localGmailFolder, 0, 1)
	namedSpam := make([]localGmailFolder, 0, 2)
	for _, item := range listed {
		if item == nil || strings.TrimSpace(item.Mailbox) == "" {
			continue
		}
		name := strings.TrimSpace(item.Mailbox)
		if strings.EqualFold(name, "INBOX") {
			folders = append(folders, localGmailFolder{ID: name, Label: localGmailInboxFolder})
			continue
		}
		isSpam := false
		for _, attr := range item.Attrs {
			if attr == imap.MailboxAttrJunk {
				isSpam = true
				break
			}
		}
		folder := localGmailFolder{ID: name, Label: localGmailSpamFolder}
		if isSpam {
			specialSpam = append(specialSpam, folder)
		} else if strings.Contains(strings.ToLower(name), "spam") || strings.Contains(strings.ToLower(name), "junk") {
			namedSpam = append(namedSpam, folder)
		}
	}
	folders = append(folders, specialSpam...)
	folders = append(folders, namedSpam...)
	folders = append(folders, fallback...)
	return uniqueLocalGmailFolders(folders)
}

func uniqueLocalGmailFolders(folders []localGmailFolder) []localGmailFolder {
	out := make([]localGmailFolder, 0, len(folders))
	seen := make(map[string]struct{}, len(folders))
	for _, folder := range folders {
		key := strings.ToLower(strings.TrimSpace(folder.ID))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, folder)
	}
	return out
}

func localGmailOriginalRecipient(rootEmail string, raw []byte) string {
	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	for _, header := range []string{"Delivered-To", "X-Original-To", "Envelope-To", "To", "Cc"} {
		matches := make([]string, 0, 1)
		seen := map[string]struct{}{}
		for _, candidate := range localGmailHistoryAddressCandidates(message.Header.Get(header)) {
			candidate = strings.ToLower(strings.TrimSpace(candidate))
			if !localGmailRecipientBelongsTo(rootEmail, candidate) {
				continue
			}
			if _, exists := seen[candidate]; exists {
				continue
			}
			seen[candidate] = struct{}{}
			matches = append(matches, candidate)
		}
		if len(matches) == 1 {
			return matches[0]
		}
		if len(matches) > 1 {
			return ""
		}
	}
	return ""
}

func localGmailRecipientBelongsTo(rootEmail, candidate string) bool {
	_, _, rootDots, rootOK := localGmailHistoryAliasForms(rootEmail)
	_, _, candidateDots, candidateOK := localGmailHistoryAliasForms(candidate)
	return rootOK && candidateOK && rootDots == candidateDots
}

type localOrderMailFetch struct {
	ResourceID uint
	Cursors    localGmailFolderCursors
	Fetched    int
	Stored     int
	Matched    int
}

// FetchLocalOrderMail is the single IMAP path for local Gmail code and
// purchase orders. MailMatch applies each order's recipient/time filters.
func (s *Service) FetchLocalOrderMail(ctx context.Context, orderNo string) error {
	_, err := s.fetchLocalOrderMail(ctx, orderNo, nil)
	return err
}

func (s *Service) FetchLocalOrderMailWithFence(ctx context.Context, orderNo string, fence func(context.Context) error) error {
	_, err := s.fetchLocalOrderMail(ctx, orderNo, fence)
	return err
}

func (s *Service) fetchLocalOrderMail(ctx context.Context, orderNo string, fence func(context.Context) error) (localOrderMailFetch, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return localOrderMailFetch{}, ErrLocalResourceMissing
	}
	var resource struct {
		ID                 uint   `gorm:"column:id"`
		CredentialRevision uint64 `gorm:"column:credential_revision"`
	}
	err := s.dbFor(ctx).Table("gmail_allocations AS a").
		Select("r.id, r.credential_revision").
		Joins("JOIN gmail_resources AS r ON r.id = a.resource_id").
		Where("a.order_no = ? AND a.source = ? AND a.status = ?", orderNo, SourceLocal, AllocationStatusAllocated).
		Take(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return localOrderMailFetch{}, ErrLocalResourceMissing
	}
	if err != nil {
		return localOrderMailFetch{}, fmt.Errorf("load local Gmail order mail resource: %w", err)
	}
	return s.fetchLocalResourceMail(ctx, resource.ID, resource.CredentialRevision, fence)
}

// FetchLocalResourceMail pulls recent mail for an administrator-selected Gmail
// resource. It resumes from the resource cursor so manual fetches
// and order fetches do not repeatedly scan the same mailbox range.
func (s *Service) FetchLocalResourceMail(ctx context.Context, resourceID uint, expectedCredentialRevision uint64) (int, int, int, error) {
	result, err := s.fetchLocalResourceMail(ctx, resourceID, expectedCredentialRevision, nil)
	return result.Fetched, result.Stored, result.Matched, err
}

func (s *Service) FetchLocalResourceMailWithFence(ctx context.Context, resourceID uint, expectedCredentialRevision uint64, fence func(context.Context) error) (int, int, int, error) {
	result, err := s.fetchLocalResourceMail(ctx, resourceID, expectedCredentialRevision, fence)
	return result.Fetched, result.Stored, result.Matched, err
}

func (s *Service) fetchLocalResourceMail(ctx context.Context, resourceID uint, expectedCredentialRevision uint64, fence func(context.Context) error) (localOrderMailFetch, error) {
	if s == nil || s.db == nil || s.fetch == nil || s.mail == nil || resourceID == 0 {
		return localOrderMailFetch{}, ErrInvalidLocalResource
	}
	var resource struct {
		ID                 uint   `gorm:"column:id"`
		LoginEmail         string `gorm:"column:login_email"`
		AppPassword        string `gorm:"column:app_password"`
		Status             string `gorm:"column:status"`
		CredentialRevision uint64 `gorm:"column:credential_revision"`
		ProviderCursor     uint64 `gorm:"column:provider_cursor"`
		ProviderSpamCursor uint64 `gorm:"column:provider_spam_cursor"`
	}
	err := s.dbFor(ctx).Table("gmail_resources AS r").
		Select("r.id, r.email AS login_email, r.app_password, r.status, r.credential_revision, r.provider_cursor, r.provider_spam_cursor").
		Joins("JOIN email_resources AS root ON root.id = r.id AND root.type = ?", "gmail").
		Where("r.id = ?", resourceID).Take(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || resource.Status == LocalResourceDeleted {
		return localOrderMailFetch{}, ErrLocalResourceMissing
	}
	if err != nil {
		return localOrderMailFetch{}, fmt.Errorf("load local Gmail resource mail: %w", err)
	}
	if resource.CredentialRevision != expectedCredentialRevision {
		return localOrderMailFetch{ResourceID: resource.ID}, ErrLocalValidationConflict
	}
	if strings.TrimSpace(resource.LoginEmail) == "" || resource.AppPassword == "" {
		return localOrderMailFetch{ResourceID: resource.ID}, ErrInvalidLocalResource
	}
	if fence != nil {
		if err := fence(ctx); err != nil {
			return localOrderMailFetch{ResourceID: resource.ID}, err
		}
	}
	cursor := localGmailFolderCursors{Inbox: resource.ProviderCursor, Spam: resource.ProviderSpamCursor}
	since := s.now().UTC().Add(-boundedLocalGmailLookback())
	messages, cursors, err := s.fetch(
		ctx,
		resource.LoginEmail,
		resource.AppPassword,
		cursor,
		since,
		false,
	)
	if err != nil {
		if errors.Is(err, errLocalGmailAuthentication) {
			handleErr := s.handleLocalGmailAuthenticationFailure(ctx, resource.ID, expectedCredentialRevision, fence)
			return localOrderMailFetch{ResourceID: resource.ID}, errors.Join(err, handleErr)
		}
		return localOrderMailFetch{ResourceID: resource.ID}, fmt.Errorf("fetch local Gmail resource mail: %w", err)
	}
	result := localOrderMailFetch{ResourceID: resource.ID, Cursors: cursors, Fetched: len(messages)}
	for _, message := range messages {
		storedCount, matchedCount, err := s.mail.IngestGmailMail(
			ctx, resource.ID, message.Recipient, message.Raw, message.ReceivedAt, message.ProviderMessageID, message.Folder, fence,
		)
		if err != nil {
			return result, fmt.Errorf("ingest local Gmail resource mail: %w", err)
		}
		result.Stored += storedCount
		result.Matched += matchedCount
	}
	err = s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := platform.WithGormTx(ctx, tx)
		if fence != nil {
			if err := fence(txCtx); err != nil {
				return err
			}
		}
		var current localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status", "credential_revision", "provider_cursor", "provider_spam_cursor").Where("id = ?", resourceID).Take(&current).Error; err != nil {
			return err
		}
		if current.Status == LocalResourceDeleted || current.CredentialRevision != expectedCredentialRevision {
			return ErrLocalValidationConflict
		}
		if current.ProviderCursor != cursor.Inbox || current.ProviderSpamCursor != cursor.Spam {
			return nil
		}
		return tx.Model(&localResourceModel{}).Where("id = ?", resourceID).
			Updates(map[string]any{"provider_cursor": cursors.Inbox, "provider_spam_cursor": cursors.Spam}).Error
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func boundedLocalGmailLookback() time.Duration {
	days := runtimeconfig.Int("fetch_lookback_window_days", 90, 1)
	return time.Duration(min(days, 3650)) * 24 * time.Hour
}

func (s *Service) handleLocalGmailAuthenticationFailure(ctx context.Context, resourceID uint, expectedCredentialRevision uint64, fence func(context.Context) error) error {
	markedAbnormal := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := platform.WithGormTx(ctx, tx)
		if fence != nil {
			if err := fence(txCtx); err != nil {
				return err
			}
		}
		var resource localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status", "credential_revision").Where("id = ?", resourceID).Take(&resource).Error; err != nil {
			return err
		}
		if resource.Status == LocalResourceDeleted || resource.CredentialRevision != expectedCredentialRevision {
			return ErrLocalValidationConflict
		}
		if resource.Status == LocalResourceDisabled {
			return nil
		}
		result := tx.Model(&localResourceModel{}).Where("id = ?", resourceID).Updates(map[string]any{
			"status": LocalResourceAbnormal, "last_safe_error": "Gmail IMAP authentication failed. Check the app password.", "last_checked_at": s.now(),
		})
		markedAbnormal = result.Error == nil && result.RowsAffected == 1
		return result.Error
	})
	if err != nil || !markedAbnormal || s.trade == nil {
		return err
	}
	_, err = s.trade.RefundUnavailableGmailOrders(ctx, resourceID, "")
	return err
}
