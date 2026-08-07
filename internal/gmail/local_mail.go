package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	stdmail "net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
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
	IngestGmailMail(ctx context.Context, resourceID uint, recipient string, raw []byte, receivedAt time.Time, providerMessageID, folder string) error
	IngestICloudMail(ctx context.Context, resourceID uint, recipient string, raw []byte, receivedAt time.Time, providerMessageID, folder string) error
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
	if !since.IsZero() {
		criteria.Since = since.UTC().Add(-2 * time.Minute)
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
		if !since.IsZero() && receivedAt.Before(since.UTC().Add(-2*time.Minute)) {
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

func localICloudOriginalRecipient(raw []byte, activeAliases map[string]struct{}) string {
	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil || len(activeAliases) == 0 {
		return ""
	}
	matches := make(map[string]struct{}, 1)
	for _, header := range []string{"X-Original-To", "Envelope-To", "Original-Recipient", "X-Envelope-To", "To", "Cc"} {
		for _, candidate := range localGmailHistoryAddressCandidates(message.Header.Get(header)) {
			candidate = strings.ToLower(strings.TrimSpace(candidate))
			if _, active := activeAliases[candidate]; active {
				matches[candidate] = struct{}{}
			}
		}
	}
	if len(matches) != 1 {
		return ""
	}
	for recipient := range matches {
		return recipient
	}
	return ""
}

func localGmailRecipientBelongsTo(rootEmail, candidate string) bool {
	_, _, rootDots, rootOK := localGmailHistoryAliasForms(rootEmail)
	_, _, candidateDots, candidateOK := localGmailHistoryAliasForms(candidate)
	return rootOK && candidateOK && rootDots == candidateDots
}

func (s *Service) RecordMatchedCode(ctx context.Context, orderNo, value string, receivedAt time.Time) error {
	orderNo = strings.TrimSpace(orderNo)
	value = strings.TrimSpace(value)
	if orderNo == "" || value == "" || len(value) > 4096 {
		return ErrInvalidRoute
	}
	if receivedAt.IsZero() {
		receivedAt = s.now()
	}
	receivedAt = receivedAt.UTC()
	var session sessionModel
	completed := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).Take(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSessionMissing
			}
			return err
		}
		if session.Source != SourceLocal || session.ServiceMode != string(tradedomain.ServiceModeCode) {
			return ErrInvalidRoute
		}
		if session.Status != SessionActive || session.ReceivedCount >= MaxCodes {
			return nil
		}
		if session.StartedAt != nil && receivedAt.Before(session.StartedAt.UTC()) ||
			session.ExpiresAt != nil && !receivedAt.Before(session.ExpiresAt.UTC()) {
			return nil
		}
		codes, err := decodeCodes(session.CodesJSON)
		if err != nil || len(codes) != int(session.ReceivedCount) {
			return errors.New("gmail: code session count mismatch")
		}
		for _, code := range codes {
			if code.Code == value && code.ReceivedAt.UTC().Equal(receivedAt) {
				return nil
			}
		}
		count := int(session.ReceivedCount) + 1
		codes = append(codes, Code{Seq: count, Code: value, ReceivedAt: receivedAt})
		payload, err := json.Marshal(codes)
		if err != nil {
			return err
		}
		now := s.now()
		updates := map[string]any{
			"codes_json": string(payload), "received_count": count, "last_safe_error": "",
			"next_poll_at": now.Add(gmailPollInterval), "version": gorm.Expr("version + 1"),
		}
		if count >= MaxCodes {
			updates["status"] = SessionCompleted
			updates["completed_at"] = now
			updates["next_poll_at"] = now
			completed = true
		}
		if err := tx.Model(&sessionModel{}).Where("id = ? AND status = ?", session.ID, SessionActive).Updates(updates).Error; err != nil {
			return err
		}
		session.CodesJSON = string(payload)
		session.ReceivedCount = uint8(count)
		if completed {
			session.Status = SessionCompleted
			session.CompletedAt = &now
		}
		return nil
	})
	if err != nil || !completed {
		return err
	}
	return s.finishLocalSession(ctx, session)
}

func (s *Service) pollLocalSession(ctx context.Context, session sessionModel) error {
	switch session.Status {
	case SessionCompleted, SessionCancelled, SessionFailed:
		return s.finishLocalSession(ctx, session)
	case SessionActive:
	default:
		return nil
	}
	if err := s.ensureTradeActivation(ctx, session); err != nil {
		return err
	}
	now := s.now()
	if session.ExpiresAt != nil && !now.Before(session.ExpiresAt.UTC()) {
		return s.expireLocalSession(ctx, session.ID)
	}
	resource, err := s.loadLocalCodeResource(ctx, session.OrderNo)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return s.failLocalSession(ctx, session, "自有 Gmail 资源不可用，订单已退款。")
		}
		return s.deferPoll(ctx, session.ID, "自有 Gmail 资源暂时不可用", err)
	}
	if s.fetch == nil || s.mail == nil {
		return s.deferPoll(ctx, session.ID, "自有 Gmail 收件服务不可用", errors.New("gmail: local mail fetch unavailable"))
	}
	since := session.CreatedAt
	if session.StartedAt != nil {
		since = session.StartedAt.UTC()
	}
	messages, cursors, err := s.fetch(ctx, resource.LoginEmail, resource.AppPassword, localGmailFolderCursors{
		Inbox: session.ProviderCursor, Spam: session.ProviderSpamCursor,
	}, since, false)
	if err != nil {
		if errors.Is(err, errLocalGmailAuthentication) {
			if markErr := s.markLocalResourceAbnormal(ctx, resource.ID, "Gmail IMAP authentication failed. Check the app password."); markErr != nil {
				return s.deferPoll(ctx, session.ID, "Gmail 凭据失效，资源状态更新失败", errors.Join(err, markErr))
			}
			return s.failLocalSession(ctx, session, "自有 Gmail 凭据失效，订单已退款。")
		}
		return s.deferPoll(ctx, session.ID, "Gmail IMAP 暂时不可用", err)
	}
	for _, message := range messages {
		if err := s.mail.IngestGmailMail(
			ctx, resource.ID, message.Recipient, message.Raw, message.ReceivedAt, message.ProviderMessageID, message.Folder,
		); err != nil {
			return s.deferPoll(ctx, session.ID, "Gmail 邮件匹配暂时失败", err)
		}
	}
	return s.dbFor(ctx).Model(&sessionModel{}).Where("id = ? AND status = ?", session.ID, SessionActive).Updates(map[string]any{
		"provider_cursor": cursors.Inbox, "provider_spam_cursor": cursors.Spam,
		"next_poll_at": s.now().Add(gmailPollInterval), "last_safe_error": "",
	}).Error
}

type localCodeResource struct {
	ID          uint   `gorm:"column:id"`
	LoginEmail  string `gorm:"column:login_email"`
	Recipient   string `gorm:"column:recipient"`
	AppPassword string `gorm:"column:app_password"`
}

func (s *Service) FetchLocalPurchaseMail(ctx context.Context, orderNo string) error {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return ErrLocalResourceMissing
	}
	if s.fetch == nil || s.mail == nil {
		return errors.New("gmail: local mail fetch unavailable")
	}
	var resource struct {
		ID                 uint      `gorm:"column:id"`
		AllocationID       uint      `gorm:"column:allocation_id"`
		LoginEmail         string    `gorm:"column:login_email"`
		Recipient          string    `gorm:"column:recipient"`
		AppPassword        string    `gorm:"column:app_password"`
		ProviderCursor     uint64    `gorm:"column:provider_cursor"`
		ProviderSpamCursor uint64    `gorm:"column:provider_spam_cursor"`
		AllocatedAt        time.Time `gorm:"column:allocated_at"`
	}
	err := s.dbFor(ctx).Table("gmail_allocations AS a").
		Select("r.id, a.id AS allocation_id, r.email AS login_email, a.email AS recipient, r.app_password, a.provider_cursor, a.provider_spam_cursor, a.created_at AS allocated_at").
		Joins("JOIN gmail_resources AS r ON r.id = a.resource_id").
		Where("a.order_no = ? AND a.source = ? AND a.service_mode = ? AND a.status = ?",
			orderNo, SourceLocal, string(tradedomain.ServiceModePurchase), AllocationStatusAllocated).
		Take(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrLocalResourceMissing
	}
	if err != nil {
		return fmt.Errorf("load local Gmail purchase mail resource: %w", err)
	}
	if resource.ID == 0 || strings.TrimSpace(resource.LoginEmail) == "" || strings.TrimSpace(resource.Recipient) == "" || resource.AppPassword == "" {
		return ErrLocalResourceMissing
	}
	messages, cursors, err := s.fetch(ctx, resource.LoginEmail, resource.AppPassword, localGmailFolderCursors{
		Inbox: resource.ProviderCursor, Spam: resource.ProviderSpamCursor,
	}, resource.AllocatedAt.UTC(), false)
	if err != nil {
		return fmt.Errorf("fetch local Gmail purchase mail: %w", err)
	}
	for _, message := range messages {
		if err := s.mail.IngestGmailMail(
			ctx, resource.ID, message.Recipient, message.Raw, message.ReceivedAt, message.ProviderMessageID, message.Folder,
		); err != nil {
			return fmt.Errorf("ingest local Gmail purchase mail: %w", err)
		}
	}
	return s.dbFor(ctx).Model(&allocationModel{}).
		Where("id = ? AND status = ?", resource.AllocationID, AllocationStatusAllocated).
		Updates(map[string]any{"provider_cursor": cursors.Inbox, "provider_spam_cursor": cursors.Spam}).Error
}

func (s *Service) FetchICloudMail(ctx context.Context, orderNo string) error {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return ErrLocalResourceMissing
	}
	if s.fetch == nil || s.mail == nil {
		return errors.New("gmail: local mail fetch unavailable")
	}
	var resource struct {
		ID                 uint   `gorm:"column:id"`
		GmailResourceID    uint   `gorm:"column:gmail_resource_id"`
		LoginEmail         string `gorm:"column:login_email"`
		AppPassword        string `gorm:"column:app_password"`
		ProviderCursor     uint64 `gorm:"column:provider_cursor"`
		ProviderSpamCursor uint64 `gorm:"column:provider_spam_cursor"`
	}
	err := s.dbFor(ctx).Table("icloud_allocations AS requested").
		Select("ir.id, ir.gmail_resource_id, gr.email AS login_email, gr.app_password, ir.provider_cursor, ir.provider_spam_cursor").
		Joins("JOIN icloud_resources AS ir ON ir.id = requested.resource_id").
		Joins("JOIN gmail_resources AS gr ON gr.id = ir.gmail_resource_id").
		Where("requested.order_no = ? AND requested.status = ?", orderNo, AllocationStatusAllocated).
		Where("gr.status IN ?", []string{LocalResourceNormal, localResourceRollbackNormal}).
		Take(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrLocalResourceMissing
	}
	if err != nil {
		return fmt.Errorf("load iCloud Gmail mail resource: %w", err)
	}
	if resource.ID == 0 || resource.GmailResourceID == 0 || strings.TrimSpace(resource.LoginEmail) == "" || resource.AppPassword == "" {
		return ErrLocalResourceMissing
	}
	var allocations []struct {
		Email     string    `gorm:"column:email"`
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	if err := s.dbFor(ctx).Table("icloud_allocations").
		Select("email, created_at").
		Where("resource_id = ? AND status = ?", resource.ID, AllocationStatusAllocated).
		Order("created_at ASC, id ASC").Find(&allocations).Error; err != nil {
		return fmt.Errorf("load active iCloud aliases: %w", err)
	}
	if len(allocations) == 0 {
		return ErrLocalResourceMissing
	}
	activeAliases := make(map[string]struct{}, len(allocations))
	for _, allocation := range allocations {
		if email := strings.ToLower(strings.TrimSpace(allocation.Email)); email != "" {
			activeAliases[email] = struct{}{}
		}
	}
	if len(activeAliases) == 0 {
		return ErrLocalResourceMissing
	}
	messages, cursors, err := s.fetch(ctx, resource.LoginEmail, resource.AppPassword, localGmailFolderCursors{
		Inbox: resource.ProviderCursor, Spam: resource.ProviderSpamCursor,
	}, allocations[0].CreatedAt.UTC(), false)
	if err != nil {
		return fmt.Errorf("fetch iCloud Gmail mail: %w", err)
	}
	for _, message := range messages {
		recipient := localICloudOriginalRecipient(message.Raw, activeAliases)
		if recipient == "" {
			continue
		}
		providerMessageID := strings.TrimSpace(message.ProviderMessageID)
		if providerMessageID != "" {
			providerMessageID = fmt.Sprintf("gmail:%d:%s", resource.GmailResourceID, providerMessageID)
		}
		if err := s.mail.IngestICloudMail(
			ctx, resource.ID, recipient, message.Raw, message.ReceivedAt, providerMessageID, message.Folder,
		); err != nil {
			return fmt.Errorf("ingest iCloud Gmail mail: %w", err)
		}
	}
	return s.dbFor(ctx).Table("icloud_resources").
		Where("id = ? AND gmail_resource_id = ?", resource.ID, resource.GmailResourceID).
		Updates(map[string]any{"provider_cursor": cursors.Inbox, "provider_spam_cursor": cursors.Spam}).Error
}

func (s *Service) loadLocalCodeResource(ctx context.Context, orderNo string) (*localCodeResource, error) {
	var resource localCodeResource
	err := s.dbFor(ctx).Table("gmail_allocations AS a").
		Select("r.id, r.email AS login_email, a.email AS recipient, r.app_password").
		Joins("JOIN gmail_resources AS r ON r.id = a.resource_id").
		Where("a.order_no = ? AND a.source = ? AND a.service_mode = ? AND a.status = ?",
			orderNo, SourceLocal, string(tradedomain.ServiceModeCode), AllocationStatusAllocated).
		Take(&resource).Error
	if err != nil {
		return nil, fmt.Errorf("load local Gmail code resource: %w", err)
	}
	return &resource, nil
}

func (s *Service) expireLocalSession(ctx context.Context, sessionID uint) error {
	var session sessionModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, sessionID).Error; err != nil {
			return err
		}
		if session.Status != SessionActive {
			return nil
		}
		now := s.now()
		status := SessionCompleted
		if session.ReceivedCount == 0 {
			status = SessionCancelled
		}
		if err := tx.Model(&sessionModel{}).Where("id = ? AND status = ?", session.ID, SessionActive).Updates(map[string]any{
			"status": status, "completed_at": now, "next_poll_at": now, "version": gorm.Expr("version + 1"),
		}).Error; err != nil {
			return err
		}
		session.Status = status
		session.CompletedAt = &now
		return nil
	})
	if err != nil {
		return err
	}
	return s.finishLocalSession(ctx, session)
}

func (s *Service) failLocalSession(ctx context.Context, session sessionModel, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "自有 Gmail 收件失败，订单已退款。"
	}
	finish := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var current sessionModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, session.ID).Error; err != nil {
			return err
		}
		session = current
		switch session.Status {
		case SessionPending, SessionProvisioning, SessionActive:
			now := s.now()
			if err := tx.Model(&sessionModel{}).Where("id = ? AND status = ?", session.ID, session.Status).Updates(map[string]any{
				"status": SessionFailed, "completed_at": now, "next_poll_at": now,
				"last_safe_error": reason, "version": gorm.Expr("version + 1"),
			}).Error; err != nil {
				return err
			}
			session.Status = SessionFailed
			session.CompletedAt = &now
			session.LastSafeError = reason
			finish = true
		case SessionCompleted, SessionCancelled, SessionFailed:
			finish = true
		}
		return nil
	})
	if err != nil || !finish {
		return err
	}
	return s.finishLocalSession(ctx, session)
}

func (s *Service) finishLocalSession(ctx context.Context, session sessionModel) error {
	if session.Status != SessionCompleted && session.Status != SessionCancelled && session.Status != SessionFailed {
		return errors.New("gmail: local session is not terminal")
	}
	if s.trade == nil {
		return errors.New("gmail: trade callback unavailable")
	}
	var err error
	switch session.Status {
	case SessionCompleted:
		err = s.trade.CompleteGmailOrder(ctx, session.OrderNo, gmailCompletionReason(session))
	case SessionCancelled:
		reason := strings.TrimSpace(session.LastSafeError)
		if reason == "" {
			reason = "Gmail 接码窗口结束，订单已退款。"
		}
		err = s.trade.FailGmailOrder(ctx, session.OrderNo, reason)
	case SessionFailed:
		reason := strings.TrimSpace(session.LastSafeError)
		if reason == "" {
			reason = "自有 Gmail 收件失败，订单已退款。"
		}
		err = s.trade.FailGmailOrder(ctx, session.OrderNo, reason)
	}
	if err != nil {
		return err
	}
	return s.clearNextPoll(ctx, session.ID)
}
func (s *Service) markLocalResourceAbnormal(ctx context.Context, resourceID uint, safeError string) error {
	return s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		resource, err := lockLocalResource(tx, resourceID)
		if err != nil {
			return err
		}
		if resource.Status == LocalResourceDisabled {
			return nil
		}
		return tx.Model(&localResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
			"status": LocalResourceAbnormal, "last_safe_error": safeError, "last_checked_at": s.now(),
		}).Error
	})
}
