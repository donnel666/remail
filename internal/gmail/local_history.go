package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	stdmail "net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/hibiken/asynq"
	htmlcharset "golang.org/x/net/html/charset"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	typeGmailValidatedHistoryScan    = "gmail:validated_history_scan"
	localGmailHistoryTaskMaxRetry    = 3
	localGmailHistoryTaskTimeout     = 20 * time.Minute
	localGmailHistoryMailboxTimeout  = 15 * time.Minute
	localGmailHistoryMaxTimeout      = 2 * time.Hour
	localGmailHistoryTaskSettleAfter = time.Second
)

var errLocalGmailHistoryScopeChanged = errors.New("gmail: history project scope changed")

type localGmailHistoryTask struct {
	ResourceID                 uint   `json:"resourceId"`
	OwnerUserID                uint   `json:"ownerUserId"`
	ValidationGeneration       uint64 `json:"validationGeneration"`
	ExpectedCredentialRevision uint64 `json:"expectedCredentialRevision"`
	MaintenanceRunID           uint64 `json:"maintenanceRunId,omitempty"`
	RequestID                  string `json:"requestId,omitempty"`
}

type localGmailHistoryRule struct {
	Type    string
	Pattern string
}

type localGmailHistoryScope struct {
	ProjectID               uint
	ProductID               uint
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	LooseMatch              bool
	Rules                   []localGmailHistoryRule
}

type localGmailHistoryMessage struct {
	Recipients []string
	Sender     string
	Subject    string
	Body       string
	ReceivedAt time.Time
}

type localGmailHistoryMatch struct {
	ResourceID              uint
	ProjectID               uint
	ProductID               uint
	CodeWindowMinutes       int
	ActivationWindowMinutes int
	WarrantyMinutes         int
	Mailbox                 string
	Email                   string
	FirstMatchedAt          time.Time
	LastMatchedAt           time.Time
	EvidenceCount           int
}

type localGmailHistoryMatchKey struct {
	ProjectID uint
	Mailbox   string
	Email     string
}

func (s *Service) enqueueValidatedLocalGmailHistory(ctx context.Context, task localGmailHistoryTask) error {
	if s == nil || s.queue == nil || task.ResourceID == 0 || task.OwnerUserID == 0 ||
		task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return ErrLocalValidationDependency
	}
	task.RequestID = strings.TrimSpace(task.RequestID)
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("encode validated Gmail history task: %w", err)
	}
	timeout := min(
		runtimeconfig.Duration("project_history_timeout_minutes", localGmailHistoryTaskTimeout, time.Minute, 1),
		localGmailHistoryMaxTimeout,
	)
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailValidatedHistoryScan, payload),
		asynq.Queue(platform.QueueBackgroundGmailIdentification),
		asynq.ProcessIn(localGmailHistoryTaskSettleAfter),
		asynq.Unique(timeout),
		asynq.MaxRetry(localGmailHistoryTaskMaxRetry),
		asynq.Timeout(timeout),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue validated Gmail history task: %w", err)
	}
	return nil
}

func decodeLocalGmailHistoryTask(task *asynq.Task) (localGmailHistoryTask, error) {
	var payload localGmailHistoryTask
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.ResourceID == 0 ||
		payload.OwnerUserID == 0 || payload.ValidationGeneration == 0 || payload.ExpectedCredentialRevision == 0 {
		return localGmailHistoryTask{}, fmt.Errorf("decode validated Gmail history task: %w", asynq.SkipRetry)
	}
	payload.RequestID = strings.TrimSpace(payload.RequestID)
	return payload, nil
}

func (s *Service) ProcessValidatedLocalGmailHistory(ctx context.Context, task localGmailHistoryTask) error {
	if s == nil || s.db == nil || s.fetch == nil || task.ResourceID == 0 || task.OwnerUserID == 0 ||
		task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return ErrLocalValidationConflict
	}
	resource, err := s.loadLocalGmailHistoryResource(ctx, task)
	if err != nil {
		return err
	}
	if resource == nil {
		_ = s.finishGmailMaintenanceRunForTask(
			context.WithoutCancel(ctx), task.MaintenanceRunID, task.ResourceID, task.ValidationGeneration,
			gmailMaintenanceHistory, gmailMaintenanceCanceled, "Gmail resource changed before history scanning started.",
		)
		return nil
	}
	switch resource.Status {
	case LocalResourcePending, LocalResourceValidating:
		return platform.ErrBackgroundExecutionDeferred
	case LocalResourceIdentifying, LocalResourceNormal, localResourceRollbackNormal:
	default:
		_ = s.finishGmailMaintenanceRunForTask(
			context.WithoutCancel(ctx), task.MaintenanceRunID, task.ResourceID, task.ValidationGeneration,
			gmailMaintenanceHistory, gmailMaintenanceCanceled, "Gmail history scanning was canceled before it started.",
		)
		return nil
	}
	runID, started, err := s.startGmailMaintenanceRun(
		ctx, task.MaintenanceRunID, task.ResourceID, task.ValidationGeneration,
		gmailMaintenanceHistory, task.ExpectedCredentialRevision,
	)
	if err != nil {
		return fmt.Errorf("start Gmail history maintenance run: %w", err)
	}
	if !started {
		return nil
	}
	task.MaintenanceRunID = runID

	scopes, err := s.listLocalGmailHistoryScopes(ctx)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		return s.commitLocalGmailHistory(ctx, task, scopes, nil)
	}
	matches := make([]localGmailHistoryMatch, 0)
	matchIndex := make(map[localGmailHistoryMatchKey]int)
	cursors := localGmailFolderCursors{}
	for {
		messages, nextCursors, fetchErr := s.fetch(ctx, resource.Email, resource.AppPassword, cursors, time.Time{}, true)
		if fetchErr != nil {
			return fmt.Errorf("fetch validated Gmail history: %w", fetchErr)
		}
		for _, fetched := range messages {
			message := parseLocalGmailHistoryMessage(fetched.Raw, fetched.ReceivedAt)
			mailbox, recipient, ok := localGmailHistoryRecipient(resource.Email, []string{fetched.Recipient})
			if !ok {
				continue
			}
			for _, scope := range scopes {
				if !localGmailHistoryMatchesScope(message, mailbox, scope) {
					continue
				}
				matchedAt := message.ReceivedAt.UTC()
				if matchedAt.IsZero() {
					matchedAt = s.now().UTC()
				}
				key := localGmailHistoryMatchKey{ProjectID: scope.ProjectID, Mailbox: mailbox, Email: recipient}
				index, exists := matchIndex[key]
				if !exists {
					matchIndex[key] = len(matches)
					matches = append(matches, localGmailHistoryMatch{
						ResourceID: task.ResourceID, ProjectID: scope.ProjectID, ProductID: scope.ProductID,
						CodeWindowMinutes: scope.CodeWindowMinutes, ActivationWindowMinutes: scope.ActivationWindowMinutes,
						WarrantyMinutes: scope.WarrantyMinutes,
						Mailbox:         mailbox, Email: recipient, FirstMatchedAt: matchedAt, LastMatchedAt: matchedAt,
						EvidenceCount: 1,
					})
					continue
				}
				if matchedAt.Before(matches[index].FirstMatchedAt) {
					matches[index].FirstMatchedAt = matchedAt
				}
				if matchedAt.After(matches[index].LastMatchedAt) {
					matches[index].LastMatchedAt = matchedAt
				}
				matches[index].EvidenceCount++
			}
		}
		if nextCursors == cursors {
			break
		}
		cursors = nextCursors
	}
	return s.commitLocalGmailHistory(ctx, task, scopes, matches)
}

func (s *Service) loadLocalGmailHistoryResource(ctx context.Context, task localGmailHistoryTask) (*localResourceModel, error) {
	var resource localResourceModel
	result := s.dbFor(ctx).Table("gmail_resources AS gr").Select("gr.*").
		Joins("JOIN email_resources AS er ON er.id = gr.id AND er.type = ? AND er.owner_user_id = ?", "gmail", task.OwnerUserID).
		Where("gr.id = ?", task.ResourceID).Limit(1).Scan(&resource)
	if result.Error != nil {
		return nil, fmt.Errorf("load validated Gmail history resource: %w", result.Error)
	}
	if resource.ID == 0 || resource.OwnerUserID != task.OwnerUserID ||
		resource.ValidationGeneration != task.ValidationGeneration ||
		resource.CredentialRevision != task.ExpectedCredentialRevision {
		return nil, nil
	}
	return &resource, nil
}

func (s *Service) listLocalGmailHistoryScopes(ctx context.Context) ([]localGmailHistoryScope, error) {
	var rows []struct {
		ProjectID               uint   `gorm:"column:project_id"`
		ProductID               uint   `gorm:"column:product_id"`
		CodeWindowMinutes       int    `gorm:"column:code_window_minutes"`
		ActivationWindowMinutes int    `gorm:"column:activation_window_minutes"`
		WarrantyMinutes         int    `gorm:"column:warranty_minutes"`
		LooseMatch              bool   `gorm:"column:loose_match"`
		RuleType                string `gorm:"column:rule_type"`
		Pattern                 string `gorm:"column:pattern"`
	}
	err := s.dbFor(ctx).Table("projects AS p").
		Select("p.id AS project_id, pp.id AS product_id, pp.code_window_minutes, pp.activation_window_minutes, pp.warranty_minutes, p.loose_match, pmr.rule_type, pmr.pattern").
		Joins(`JOIN project_products AS pp ON pp.project_id = p.id AND pp.type = 'gmail'
			AND pp.id = (SELECT MIN(candidate.id) FROM project_products AS candidate WHERE candidate.project_id = p.id AND candidate.type = 'gmail')`).
		Joins("JOIN project_mail_rules AS pmr ON pmr.project_id = p.id AND pmr.enabled = 1").
		Where("p.status IN ?", []string{"listed", "delisted"}).
		Order("p.id ASC, pmr.id ASC").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list Gmail history project scopes: %w", err)
	}
	scopes := make([]localGmailHistoryScope, 0)
	indexByProject := make(map[uint]int)
	for _, row := range rows {
		index, exists := indexByProject[row.ProjectID]
		if !exists {
			index = len(scopes)
			indexByProject[row.ProjectID] = index
			scopes = append(scopes, localGmailHistoryScope{
				ProjectID: row.ProjectID, ProductID: row.ProductID,
				CodeWindowMinutes: row.CodeWindowMinutes, ActivationWindowMinutes: row.ActivationWindowMinutes,
				WarrantyMinutes: row.WarrantyMinutes, LooseMatch: row.LooseMatch,
			})
		}
		scopes[index].Rules = append(scopes[index].Rules, localGmailHistoryRule{
			Type: strings.ToLower(strings.TrimSpace(row.RuleType)), Pattern: strings.TrimSpace(row.Pattern),
		})
	}
	return scopes, nil
}

func (s *Service) commitLocalGmailHistory(
	ctx context.Context,
	task localGmailHistoryTask,
	scopes []localGmailHistoryScope,
	matches []localGmailHistoryMatch,
) error {
	return s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var root resourceRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ? AND owner_user_id = ?", task.ResourceID, "gmail", task.OwnerUserID).
			Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("lock Gmail history resource root: %w", err)
		}
		var resource localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", task.ResourceID).Take(&resource).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("lock Gmail history resource: %w", err)
		}
		maintenanceRun, err := findGmailMaintenanceRunTx(
			ctx, tx, task.MaintenanceRunID, task.ResourceID, task.ValidationGeneration, gmailMaintenanceHistory,
		)
		if err != nil {
			return err
		}
		if resource.OwnerUserID != task.OwnerUserID || resource.ValidationGeneration != task.ValidationGeneration ||
			resource.CredentialRevision != task.ExpectedCredentialRevision {
			if maintenanceRun != nil {
				return finishGmailMaintenanceRunTx(
					ctx, tx, maintenanceRun.ID, gmailMaintenanceCanceled,
					"Gmail resource changed before history scanning completed.", s.now().UTC(),
				)
			}
			return nil
		}
		if resource.Status != LocalResourceIdentifying && resource.Status != LocalResourceNormal &&
			resource.Status != localResourceRollbackNormal {
			if maintenanceRun != nil {
				return finishGmailMaintenanceRunTx(
					ctx, tx, maintenanceRun.ID, gmailMaintenanceCanceled,
					"Gmail history scanning was canceled before completion.", s.now().UTC(),
				)
			}
			return nil
		}
		if maintenanceRun == nil {
			maintenanceRun, err = ensureGmailMaintenanceRunTx(
				ctx, tx, resource.ID, resource.ValidationGeneration, gmailMaintenanceHistory,
				resource.CredentialRevision, 0, s.now().UTC(),
			)
			if err != nil {
				return err
			}
		}
		currentScopes, err := s.listLocalGmailHistoryScopes(platform.WithGormTx(ctx, tx))
		if err != nil {
			return err
		}
		if !sameLocalGmailHistoryScopes(scopes, currentScopes) {
			return errLocalGmailHistoryScopeChanged
		}
		for _, match := range matches {
			if err := s.importLocalGmailHistoryMatch(ctx, tx, match); err != nil {
				return err
			}
		}
		if resource.Status != LocalResourceIdentifying {
			return finishGmailMaintenanceRunTx(
				ctx, tx, maintenanceRun.ID, gmailMaintenanceSucceeded, "", s.now().UTC(),
			)
		}
		now := s.now().UTC()
		result := tx.Model(&localResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?",
				task.ResourceID, LocalResourceIdentifying, task.ValidationGeneration, task.ExpectedCredentialRevision).
			Updates(map[string]any{
				"status": localResourceRollbackNormal, "validation_failures": 0,
				"last_safe_error": "", "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("complete Gmail history identification: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return nil
		}
		if err := finishGmailMaintenanceRunTx(
			ctx, tx, maintenanceRun.ID, gmailMaintenanceSucceeded, "", now,
		); err != nil {
			return err
		}
		if err := tx.Model(&resourceRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return fmt.Errorf("bump Gmail history resource version: %w", err)
		}
		if s.systemLogs != nil {
			return s.systemLogs.CreateInTx(ctx, tx, &governancedomain.SystemLog{
				Level: "info", Module: "gmail", EventType: "gmail.resource_history_identified",
				RequestID: task.RequestID, BizType: "gmail_resource",
				BizID:   fmt.Sprintf("%d", task.ResourceID),
				Message: "Gmail resource project history identification completed.",
				Detail:  fmt.Sprintf("Matched %d historical Gmail mailbox identities.", len(matches)),
			})
		}
		return nil
	})
}

func (s *Service) importLocalGmailHistoryMatch(ctx context.Context, tx *gorm.DB, match localGmailHistoryMatch) error {
	match.Email = strings.ToLower(strings.TrimSpace(match.Email))
	if match.ResourceID == 0 || match.ProjectID == 0 || match.ProductID == 0 || match.Email == "" ||
		match.EvidenceCount <= 0 || !isGmailMailbox(match.Mailbox) {
		return ErrLocalValidationConflict
	}
	createdAt := match.FirstMatchedAt.UTC()
	releasedAt := match.LastMatchedAt.UTC()
	if createdAt.IsZero() {
		createdAt = s.now().UTC()
	}
	if releasedAt.IsZero() || releasedAt.Before(createdAt) {
		releasedAt = createdAt
	}
	if s.trade == nil {
		return ErrLocalValidationDependency
	}
	return s.trade.ImportHistoricalGmailUsage(platform.WithGormTx(ctx, tx), []tradeapp.HistoricalGmailUsage{{
		ResourceID: match.ResourceID, ProjectID: match.ProjectID, ProductID: match.ProductID,
		Mailbox: match.Mailbox, Email: match.Email,
		CodeWindowMinutes: match.CodeWindowMinutes, ActivationWindowMinutes: match.ActivationWindowMinutes,
		WarrantyMinutes: match.WarrantyMinutes, FirstMatchedAt: createdAt, LastMatchedAt: releasedAt,
		EvidenceCount: match.EvidenceCount,
	}})
}

func sameLocalGmailHistoryScopes(left, right []localGmailHistoryScope) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].ProjectID != right[i].ProjectID || left[i].ProductID != right[i].ProductID ||
			left[i].CodeWindowMinutes != right[i].CodeWindowMinutes ||
			left[i].ActivationWindowMinutes != right[i].ActivationWindowMinutes ||
			left[i].WarrantyMinutes != right[i].WarrantyMinutes || left[i].LooseMatch != right[i].LooseMatch || len(left[i].Rules) != len(right[i].Rules) {
			return false
		}
		for j := range left[i].Rules {
			if left[i].Rules[j] != right[i].Rules[j] {
				return false
			}
		}
	}
	return true
}

func localGmailHistoryRecipient(mainEmail string, recipients []string) (string, string, bool) {
	recipients = normalizeLocalGmailHistoryRecipients(recipients)
	if len(recipients) != 1 {
		return "", "", false
	}
	recipient := recipients[0]
	mainExact, mainPlus, mainDots, ok := localGmailHistoryAliasForms(mainEmail)
	if !ok {
		return "", "", false
	}
	recipientExact, recipientPlus, recipientDots, ok := localGmailHistoryAliasForms(recipient)
	if !ok || recipientDots != mainDots {
		return "", "", false
	}
	mailbox := GmailMailboxMain
	switch {
	case recipientExact == mainExact:
	case recipientExact != recipientPlus:
		mailbox = GmailMailboxPlus
	case recipientPlus != mainPlus:
		mailbox = GmailMailboxDot
	}
	return mailbox, recipient, true
}

func localGmailHistoryAliasForms(value string) (exact string, plusBase string, dotBase string, ok bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	local, host, found := strings.Cut(value, "@")
	if !found || local == "" || strings.Contains(host, "@") || host != "gmail.com" && host != "googlemail.com" {
		return "", "", "", false
	}
	plusLocal := local
	if plus := strings.IndexByte(plusLocal, '+'); plus > 0 {
		plusLocal = plusLocal[:plus]
	}
	exact = local + "@gmail.com"
	plusBase = plusLocal + "@gmail.com"
	dotBase = strings.ReplaceAll(plusLocal, ".", "") + "@gmail.com"
	return exact, plusBase, dotBase, true
}

func localGmailHistoryMatchesScope(message localGmailHistoryMessage, mailbox string, scope localGmailHistoryScope) bool {
	recipientRule := "exact"
	if mailbox == GmailMailboxDot || mailbox == GmailMailboxPlus {
		recipientRule = mailbox
	}
	if !localGmailHistoryHasRecipientRule(scope.Rules, recipientRule) ||
		!localGmailHistoryMatchesRegexRule(scope.Rules, "sender", message.Sender) {
		return false
	}
	if scope.LooseMatch {
		return true
	}
	return localGmailHistoryMatchesRegexRule(scope.Rules, "subject", message.Subject) &&
		localGmailHistoryMatchesRegexRule(scope.Rules, "body", message.Body)
}

func localGmailHistoryHasRecipientRule(rules []localGmailHistoryRule, want string) bool {
	for _, rule := range rules {
		if rule.Type == "recipient" && strings.EqualFold(strings.TrimSpace(rule.Pattern), want) {
			return true
		}
	}
	return false
}

type localGmailHistoryCachedRegex struct{ re *regexp.Regexp }

var localGmailHistoryRegexCache sync.Map

func localGmailHistoryMatchesRegexRule(rules []localGmailHistoryRule, ruleType, value string) bool {
	for _, rule := range rules {
		if rule.Type != ruleType || rule.Pattern == "" {
			continue
		}
		cached, exists := localGmailHistoryRegexCache.Load(rule.Pattern)
		if !exists {
			re, err := regexp.Compile(rule.Pattern)
			item := localGmailHistoryCachedRegex{re: re}
			if err != nil {
				item.re = nil
			}
			cached, _ = localGmailHistoryRegexCache.LoadOrStore(rule.Pattern, item)
		}
		if re := cached.(localGmailHistoryCachedRegex).re; re != nil && re.MatchString(value) {
			return true
		}
	}
	return false
}

func parseLocalGmailHistoryMessage(raw []byte, receivedAt time.Time) localGmailHistoryMessage {
	message := localGmailHistoryMessage{Body: string(raw), ReceivedAt: receivedAt.UTC()}
	parsed, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return message
	}
	decoder := &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		return htmlcharset.NewReaderLabel(charset, input)
	}}
	message.Subject = decodeLocalGmailHistoryHeader(decoder, parsed.Header.Get("Subject"))
	message.Sender = decodeLocalGmailHistoryHeader(decoder, parsed.Header.Get("From"))
	message.Recipients = localGmailHistoryAddressCandidates(parsed.Header.Get("To"))
	if body, bodyErr := readLocalGmailHistoryMIMEBody(
		parsed.Header.Get("Content-Type"), parsed.Header.Get("Content-Transfer-Encoding"), parsed.Body,
	); bodyErr == nil {
		message.Body = body
	}
	return message
}

func normalizeLocalGmailHistoryRecipients(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

var localGmailHistoryAddressPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

func localGmailHistoryAddressCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	result := make([]string, 0)
	if addresses, err := stdmail.ParseAddressList(raw); err == nil {
		for _, address := range addresses {
			result = append(result, address.Address)
		}
		return result
	}
	return localGmailHistoryAddressPattern.FindAllString(raw, -1)
}

func decodeLocalGmailHistoryHeader(decoder *mime.WordDecoder, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

func readLocalGmailHistoryMIMEBody(contentType, transferEncoding string, body io.Reader) (string, error) {
	value, _, _, err := readLocalGmailHistoryMIMEPart(contentType, transferEncoding, body)
	return value, err
}

func readLocalGmailHistoryMIMEPart(contentType, transferEncoding string, body io.Reader) (string, bool, bool, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "multipart/") {
		reader := multipart.NewReader(body, params["boundary"])
		var htmlBody, plainBody string
		var hasHTML, hasPlain bool
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				return "", false, false, nextErr
			}
			if disposition, _, dispositionErr := mime.ParseMediaType(part.Header.Get("Content-Disposition")); dispositionErr == nil && strings.EqualFold(disposition, "attachment") {
				continue
			}
			partBody, isHTML, found, partErr := readLocalGmailHistoryMIMEPart(
				part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part,
			)
			if partErr != nil || !found {
				continue
			}
			if isHTML {
				if !hasHTML {
					htmlBody, hasHTML = partBody, true
				}
			} else if !hasPlain {
				plainBody, hasPlain = partBody, true
			}
		}
		if hasHTML {
			return htmlBody, true, true, nil
		}
		if hasPlain {
			return plainBody, false, true, nil
		}
		return "", false, false, nil
	}
	if mediaType != "text/plain" && mediaType != "text/html" {
		return "", false, false, nil
	}
	reader := body
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		reader = quotedprintable.NewReader(body)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", false, false, err
	}
	if charset := strings.TrimSpace(params["charset"]); charset != "" {
		if decoded, decodeErr := htmlcharset.NewReaderLabel(charset, bytes.NewReader(data)); decodeErr == nil {
			if value, readErr := io.ReadAll(decoded); readErr == nil {
				data = value
			}
		}
	}
	value := strings.ToValidUTF8(string(data), "\uFFFD")
	return value, mediaType == "text/html", value != "", nil
}
